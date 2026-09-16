package aileveling

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type memoryStore struct {
	mu    sync.Mutex
	items map[string]automation.Checkpoint
}

func newMemoryStore() *memoryStore {
	return &memoryStore{items: make(map[string]automation.Checkpoint)}
}

func (s *memoryStore) Create(_ context.Context, checkpoint automation.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[checkpoint.Plan.ID]; ok {
		return automation.ErrConflict
	}
	s.items[checkpoint.Plan.ID] = checkpoint
	return nil
}

func (s *memoryStore) Load(_ context.Context, id string) (automation.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	checkpoint, ok := s.items[id]
	if !ok {
		return automation.Checkpoint{}, automation.ErrNotFound
	}
	return checkpoint, nil
}

func (s *memoryStore) Save(_ context.Context, checkpoint automation.Checkpoint, expected uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.items[checkpoint.Plan.ID]
	if !ok || current.Revision != expected || checkpoint.Revision != expected+1 {
		return automation.ErrConflict
	}
	s.items[checkpoint.Plan.ID] = checkpoint
	return nil
}

type fakeGame struct {
	mu       sync.Mutex
	snapshot aigame.Snapshot
	actions  []aigame.Action
	err      error
	onAction func(aigame.Action)
}

// gateGame models a Web AutomationSession whose ExecuteExpected already
// owns the control-gate dispatch.
type gateGame struct {
	*fakeGame
	gate *aicontrol.Gate
}

func (*gateGame) UsesControlGate() {}

func (g *gateGame) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	state := g.gate.State()
	return g.gate.Dispatch(ctx, state.Generation, state.Mode, func(sendCtx context.Context) error {
		return g.fakeGame.ExecuteExpected(sendCtx, revision, action)
	})
}

func (g *fakeGame) Observe(context.Context) (aigame.Snapshot, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snapshot, nil
}

func (g *fakeGame) ExecuteExpected(_ context.Context, revision uint64, action aigame.Action) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.snapshot.Revision != revision {
		return aigame.ErrStaleRevision
	}
	g.actions = append(g.actions, action)
	if g.onAction != nil {
		g.onAction(action)
	}
	return g.err
}

type fakeNavigator struct {
	navigation Navigation
	calls      int
}

func (n *fakeNavigator) Next(context.Context, aigame.Snapshot, NavigationRequest) (Navigation, error) {
	n.calls++
	return n.navigation, nil
}

func newCoordinator(t *testing.T, game *fakeGame, navigator Navigator) (*Coordinator, *aicontrol.Gate, *memoryStore) {
	t.Helper()
	gate := aicontrol.New()
	if _, _, err := gate.Switch(1, aicontrol.Leveling, "test"); err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()
	coordinator := &Coordinator{Game: game, Navigator: navigator, Store: store, Gate: gate, CharacterID: "char-1", CharacterName: "Hero"}
	t.Cleanup(gate.Close)
	return coordinator, gate, store
}

func worldSnapshot() aigame.Snapshot {
	return aigame.Snapshot{Revision: 1, Connected: true, Phase: aigame.PhaseWorld, Character: "Hero", Position: aigame.Point{Floor: 1, X: 10, Y: 20}, Player: aigame.PlayerSnapshot{HasStatus: true, Level: 1, HP: 100, MaxHP: 100, Gold: 100, RidePet: -1, RidePetKnown: true}}
}

func startCharacter(t *testing.T, c *Coordinator, extra StartRequest) aimcp.TaskReceipt {
	t.Helper()
	request := StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30, NoProgressTimeout: time.Minute}
	if extra.TargetKind != "" || extra.TargetLevel != 0 || extra.TargetID != "" || len(extra.Targets) > 0 {
		request = extra
		if request.MaximumSeconds == 0 {
			request.MaximumSeconds = 30
		}
		if request.NoProgressTimeout == 0 {
			request.NoProgressTimeout = time.Minute
		}
	}
	receipt, err := c.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func battleSnapshot() aigame.Snapshot {
	snapshot := worldSnapshot()
	snapshot.Phase = aigame.PhaseBattle
	snapshot.Battle = aigame.BattleSnapshot{Active: true, MyNo: 0, MyNoKnown: true, CommandReady: true, BPReceived: true, BCReceived: true, Turn: 1,
		Participants: []aigame.BattleParticipant{{BattleID: 0, Player: true, HP: 100, MaxHP: 100}, {BattleID: 10, Player: true, HP: 30, MaxHP: 30}}}
	return snapshot
}

func TestStartSupportsMixedTargetsAndAllAny(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	game.snapshot.Pets = []aigame.PetSnapshot{
		{StableID: "pet-a", IdentityKnown: true, Level: 3, HP: 20, MaxHP: 20},
		{StableID: "pet-b", IdentityKnown: true, Level: 1, HP: 20, MaxHP: 20},
	}
	c, _, store := newCoordinator(t, game, &fakeNavigator{navigation: Navigation{InArea: true}})
	targets := []Target{{Kind: "character", ID: "char-1", Level: 2}, {Kind: "pet", ID: "pet-a", Level: 3}, {Kind: "pet", ID: "pet-b", Level: 2}}
	anyReceipt, err := c.Start(context.Background(), StartRequest{Targets: targets, TargetPolicy: "any", MaximumSeconds: 30})
	if err != nil || anyReceipt.Status != aimcp.ReceiptConfirmed {
		t.Fatalf("any target receipt=%+v err=%v", anyReceipt, err)
	}
	// A second run is allowed after the first completed run; all targets are
	// retained in its durable plan and are evaluated together.
	allReceipt, err := c.Start(context.Background(), StartRequest{Targets: targets, TargetPolicy: "all", MaximumSeconds: 30})
	if err != nil || allReceipt.Status != aimcp.ReceiptRunning {
		t.Fatalf("all target receipt=%+v err=%v", allReceipt, err)
	}
	checkpoint, err := store.Load(context.Background(), allReceipt.Handle)
	if err != nil || len(checkpoint.Plan.Targets) != 3 || checkpoint.Plan.TargetPolicy != "all" {
		t.Fatalf("stored mixed targets=%+v err=%v", checkpoint.Plan.Targets, err)
	}
}

func TestStartRejectsUnknownPetIdentity(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	game.snapshot.Pets = []aigame.PetSnapshot{{StableID: "session-slot-0", IdentityKnown: false, Level: 1}}
	c, _, _ := newCoordinator(t, game, &fakeNavigator{navigation: Navigation{InArea: true}})
	if _, err := c.Start(context.Background(), StartRequest{TargetKind: "pet", TargetID: "session-slot-0", TargetLevel: 2, MaximumSeconds: 30}); !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("unknown pet identity error=%v", err)
	}
}

func TestBattleUsesMyNoAndExistingEnemyOnly(t *testing.T) {
	game := &fakeGame{snapshot: battleSnapshot()}
	c, _, _ := newCoordinator(t, game, nil)
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30})
	if _, err := c.Tick(context.Background(), receipt.Handle); err != nil {
		t.Fatal(err)
	}
	game.mu.Lock()
	if len(game.actions) != 1 || game.actions[0].Kind != aigame.ActionBattle || game.actions[0].Command != "H|A" {
		game.mu.Unlock()
		t.Fatalf("battle action=%+v", game.actions)
	}

	// A roster without the local MyNo is not actionable. The controller pauses
	// and sends no fabricated attack target.
	game.snapshot.Battle.Participants = []aigame.BattleParticipant{{BattleID: 10, HP: 30, MaxHP: 30}}
	game.snapshot.Revision++
	game.snapshot.Battle.Turn++
	game.actions = nil
	game.mu.Unlock()
	_, err := c.Tick(context.Background(), receipt.Handle)
	game.mu.Lock()
	if err == nil || len(game.actions) != 0 {
		t.Fatalf("missing local participant err=%v actions=%+v", err, game.actions)
	}
	game.mu.Unlock()
}

func TestLevelingDoesNotNestControlGateForWebSession(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	navigator := &fakeNavigator{navigation: Navigation{Route: "c", Destination: aigame.Point{Floor: 1, X: 11, Y: 20}}}
	c, gate, _ := newCoordinator(t, game, navigator)
	c.Game = &gateGame{fakeGame: game, gate: gate}
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30})
	done := make(chan error, 1)
	go func() {
		_, err := c.Tick(context.Background(), receipt.Handle)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		game.mu.Lock()
		actions := append([]aigame.Action(nil), game.actions...)
		game.mu.Unlock()
		if len(actions) != 1 || actions[0].Kind != aigame.ActionMove {
			t.Fatalf("actions=%+v", actions)
		}
	case <-time.After(time.Second):
		t.Fatal("leveling submission deadlocked on a nested control gate")
	}
}

func TestStaleSubmissionPausesWithoutRetry(t *testing.T) {
	game := &fakeGame{snapshot: battleSnapshot(), err: aigame.ErrStaleRevision}
	c, _, _ := newCoordinator(t, game, nil)
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30})
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err == nil || checkpoint.Status != automation.Paused {
		t.Fatalf("stale tick checkpoint=%+v err=%v", checkpoint, err)
	}
	if _, err = c.Tick(context.Background(), receipt.Handle); err != nil {
		t.Fatal(err)
	}
	game.mu.Lock()
	defer game.mu.Unlock()
	if len(game.actions) != 1 {
		t.Fatalf("stale submission retried actions=%+v", game.actions)
	}
}

func TestUnlimitedFundsComesFromCallback(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	navigator := &fakeNavigator{navigation: Navigation{Route: "a", Destination: aigame.Point{Floor: 1, X: 11, Y: 20}, CostKnown: true, MaximumCost: 10}}
	c, _, _ := newCoordinator(t, game, navigator)
	fundingCalls := 0
	c.UnlimitedFunds = func(context.Context) (bool, error) { fundingCalls++; return true, nil }
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30, ReserveGold: 100, MaximumSpend: 10})
	if receipt.Status != aimcp.ReceiptRunning {
		t.Fatalf("unlimited start=%+v", receipt)
	}
	if checkpoint, err := c.Tick(context.Background(), receipt.Handle); err != nil || checkpoint.Status != automation.Running {
		t.Fatalf("unlimited tick checkpoint=%+v err=%v", checkpoint, err)
	}
	game.mu.Lock()
	defer game.mu.Unlock()
	if fundingCalls < 2 || len(game.actions) != 1 || game.actions[0].Kind != aigame.ActionMove {
		t.Fatalf("unlimited funding calls=%d actions=%+v", fundingCalls, game.actions)
	}
}

func TestDeadCharacterPausesWithoutInventingRecovery(t *testing.T) {
	snapshot := worldSnapshot()
	snapshot.Player.HP = 0
	game := &fakeGame{snapshot: snapshot}
	c, _, _ := newCoordinator(t, game, &fakeNavigator{navigation: Navigation{InArea: true}})
	receipt, err := c.Start(context.Background(), StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30})
	if err != nil || receipt.State != string(automation.Paused) || !strings.Contains(receipt.Reason, "疗伤") {
		t.Fatalf("dead start receipt=%+v err=%v", receipt, err)
	}
}

func TestNoProgressTimeoutAndLeaseContext(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	c, _, _ := newCoordinator(t, game, &fakeNavigator{navigation: Navigation{InArea: true}})
	now := time.Unix(100, 0)
	c.Now = func() time.Time { return now }
	lease, cancelLease := context.WithCancel(context.Background())
	defer cancelLease()
	c.Lease = lease
	httpCtx, cancelHTTP := context.WithCancel(context.Background())
	cancelHTTP()
	receipt, err := c.Start(httpCtx, StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30, NoProgressTimeout: time.Second})
	if err != nil || receipt.Status != aimcp.ReceiptRunning {
		t.Fatalf("lease start receipt=%+v err=%v", receipt, err)
	}
	now = now.Add(2 * time.Second)
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Status != automation.Paused || !strings.Contains(checkpoint.Reason, "无进度") {
		t.Fatalf("timeout checkpoint=%+v err=%v", checkpoint, err)
	}
}

func TestResumeRequiresAuthoritativeProgressBeforeRebinding(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	navigator := &fakeNavigator{navigation: Navigation{Route: "a", Destination: aigame.Point{Floor: 1, X: 11, Y: 20}, CostKnown: true}}
	c, gate, store := newCoordinator(t, game, navigator)
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30})
	if _, err := c.Tick(context.Background(), receipt.Handle); err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Takeover("human"); err != nil {
		t.Fatal(err)
	}
	paused, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || paused.Status != automation.Paused {
		t.Fatalf("stale leveling owner checkpoint=%+v err=%v", paused, err)
	}
	checkpoint, err := store.Load(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Status != automation.Paused || checkpoint.Phase != "submitted" {
		t.Fatalf("paused checkpoint=%+v err=%v", checkpoint, err)
	}

	// A new lease cannot retry the same movement while the authoritative
	// position is unchanged.
	state, lease, err := gate.Switch(gate.State().Generation, aicontrol.Leveling, "resume")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resume(lease, receipt.Handle, state.Generation); !errors.Is(err, ErrUnknownDelivery) {
		t.Fatalf("unchanged movement resume error=%v, want ErrUnknownDelivery", err)
	}
	checkpoint, err = store.Load(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Status != automation.Paused {
		t.Fatalf("uncertain checkpoint changed=%+v err=%v", checkpoint, err)
	}

	// Once the server reports the destination, the same checkpoint can be
	// rebound and the next tick is allowed to choose a new action.
	game.mu.Lock()
	game.snapshot.Position.X = 11
	game.snapshot.Revision++
	game.mu.Unlock()
	state, lease, err = gate.Switch(state.Generation, aicontrol.Paused, "refresh lease")
	if err != nil {
		t.Fatal(err)
	}
	state, lease, err = gate.Switch(state.Generation, aicontrol.Leveling, "resume after server update")
	if err != nil {
		t.Fatal(err)
	}
	if receipt, err := c.Resume(lease, receipt.Handle, state.Generation); err != nil || receipt.Status != aimcp.ReceiptRunning {
		t.Fatalf("confirmed movement resume receipt=%+v err=%v", receipt, err)
	}
}

func TestPendingMoveRequiresDestinationOrBattleEntry(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	navigator := &fakeNavigator{navigation: Navigation{
		Route: "a", Destination: aigame.Point{Floor: 1, X: 11, Y: 20}, CostKnown: true,
	}}
	c, _, _ := newCoordinator(t, game, navigator)
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 4, MaximumSeconds: 30})
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" {
		t.Fatalf("submitted movement checkpoint=%+v err=%v", checkpoint, err)
	}

	// A level/pet update and an intermediate position do not acknowledge the
	// route. The submitted checkpoint must remain fenced against retries.
	game.mu.Lock()
	game.snapshot.Player.Level = 2
	game.snapshot.Pets = []aigame.PetSnapshot{{StableID: "pet-a", IdentityKnown: true, Level: 2}}
	game.snapshot.Position.X = 10
	game.snapshot.Position.Y = 21
	game.snapshot.Revision++
	game.mu.Unlock()
	checkpoint, err = c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" {
		t.Fatalf("unconfirmed movement checkpoint=%+v err=%v", checkpoint, err)
	}
	game.mu.Lock()
	if len(game.actions) != 2 || game.actions[0].Kind != aigame.ActionMove || game.actions[1].Kind != aigame.ActionStatus || game.actions[1].Command != "c" {
		t.Fatalf("unconfirmed movement retried actions=%+v", game.actions)
	}
	game.mu.Unlock()

	// Once the exact destination is observed, the pending route is cleared and
	// the navigator may choose the next step.
	navigator.navigation = Navigation{InArea: true}
	game.mu.Lock()
	game.snapshot.Position.X = 11
	game.snapshot.Position.Y = 20
	game.snapshot.Revision++
	game.mu.Unlock()
	checkpoint, err = c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "ready" {
		t.Fatalf("confirmed movement checkpoint=%+v err=%v", checkpoint, err)
	}
	game.mu.Lock()
	defer game.mu.Unlock()
	if len(game.actions) != 2 || game.actions[0].Kind != aigame.ActionMove || game.actions[1].Kind != aigame.ActionStatus || game.actions[1].Command != "c" {
		t.Fatalf("confirmed movement sent unexpected action=%+v", game.actions)
	}
}

func TestPendingMoveRequiresKnownWarpDestination(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	navigator := &fakeNavigator{navigation: Navigation{
		Route: "a", Destination: aigame.Point{Floor: 1, X: 11, Y: 20},
		WarpDestination: aigame.Point{Floor: 100, X: 2, Y: 3}, WarpDestinationKnown: true,
		CostKnown: true,
	}}
	c, _, _ := newCoordinator(t, game, navigator)
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 4, MaximumSeconds: 30})
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" {
		t.Fatalf("submitted warp movement checkpoint=%+v err=%v", checkpoint, err)
	}

	// Reaching the known source tile confirms the W movement. The coordinator
	// then asks the navigator for a separate EV action; this fixture keeps that
	// next action in-area so it can inspect the pending movement contract.
	navigator.navigation = Navigation{InArea: true}
	game.mu.Lock()
	game.snapshot.Position.X = 11
	game.snapshot.Position.Y = 20
	game.snapshot.Revision++
	game.mu.Unlock()
	checkpoint, err = c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "ready" {
		t.Fatalf("source tile acknowledged movement checkpoint=%+v err=%v", checkpoint, err)
	}

	game.mu.Lock()
	game.snapshot.Phase = aigame.PhaseWorld
	game.snapshot.Position = aigame.Point{Floor: 100, X: 2, Y: 3}
	game.snapshot.Revision++
	game.mu.Unlock()
	checkpoint, err = c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "ready" {
		t.Fatalf("warp destination confirmation checkpoint=%+v err=%v", checkpoint, err)
	}
	game.mu.Lock()
	defer game.mu.Unlock()
	if len(game.actions) != 1 {
		t.Fatalf("warp movement sent unexpected action=%+v", game.actions)
	}
}

func TestPendingActionDeadlineIsNotExtendedByUnrelatedProgress(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	navigator := &fakeNavigator{navigation: Navigation{
		Route: "a", Destination: aigame.Point{Floor: 1, X: 12, Y: 20}, CostKnown: true,
	}}
	c, _, _ := newCoordinator(t, game, navigator)
	now := time.Unix(100, 0)
	c.Now = func() time.Time { return now }
	receipt := startCharacter(t, c, StartRequest{
		TargetKind: "character", TargetLevel: 4, MaximumSeconds: 30,
		NoProgressTimeout: time.Second,
	})
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" {
		t.Fatalf("submitted movement checkpoint=%+v err=%v", checkpoint, err)
	}
	started := checkpoint.StepStartedAt

	// Unrelated progress updates the observation stamp but keeps the action's
	// original deadline fixed.
	now = now.Add(500 * time.Millisecond)
	game.mu.Lock()
	game.snapshot.Player.Level++
	game.snapshot.Position.Y++
	game.snapshot.Revision++
	game.mu.Unlock()
	checkpoint, err = c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" || !checkpoint.StepStartedAt.Equal(started) {
		t.Fatalf("unconfirmed progress reset deadline checkpoint=%+v err=%v", checkpoint, err)
	}

	// The fixed deadline now expires even though another unrelated update
	// arrived after the first one; the route is still never retried.
	now = now.Add(600 * time.Millisecond)
	game.mu.Lock()
	game.snapshot.Pets = []aigame.PetSnapshot{{StableID: "pet-a", IdentityKnown: true, Level: 2}}
	game.snapshot.Position.Y++
	game.snapshot.Revision++
	game.mu.Unlock()
	checkpoint, err = c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Status != automation.Paused || !strings.Contains(checkpoint.Reason, "无进度") {
		t.Fatalf("pending deadline checkpoint=%+v err=%v", checkpoint, err)
	}
	game.mu.Lock()
	defer game.mu.Unlock()
	if len(game.actions) != 2 || game.actions[0].Kind != aigame.ActionMove || game.actions[1].Kind != aigame.ActionStatus || game.actions[1].Command != "c" {
		t.Fatalf("pending deadline retried action=%+v", game.actions)
	}
}

func TestPendingBattleIgnoresUnrelatedProgress(t *testing.T) {
	game := &fakeGame{snapshot: battleSnapshot()}
	c, _, _ := newCoordinator(t, game, nil)
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 4, MaximumSeconds: 30})
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" {
		t.Fatalf("submitted battle checkpoint=%+v err=%v", checkpoint, err)
	}

	// A status update that leaves the same battle turn unresolved cannot
	// authorize another attack packet.
	game.mu.Lock()
	game.snapshot.Player.Level++
	game.snapshot.Revision++
	game.mu.Unlock()
	checkpoint, err = c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" {
		t.Fatalf("unconfirmed battle checkpoint=%+v err=%v", checkpoint, err)
	}
	game.mu.Lock()
	defer game.mu.Unlock()
	if len(game.actions) != 1 {
		t.Fatalf("unconfirmed battle retried actions=%+v", game.actions)
	}
}

func TestPendingBattleEndWaitsForLeavingBattle(t *testing.T) {
	snapshot := battleSnapshot()
	snapshot.Battle.Result = "win"
	snapshot.Battle.Participants[1].HP = 0
	game := &fakeGame{snapshot: snapshot}
	navigator := &fakeNavigator{navigation: Navigation{InArea: true}}
	c, _, _ := newCoordinator(t, game, navigator)
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 4, MaximumSeconds: 30})
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" {
		t.Fatalf("submitted end battle checkpoint=%+v err=%v", checkpoint, err)
	}

	// The terminal result and a changed turn are still reported inside the
	// battle. EO remains pending and must not be sent a second time.
	game.mu.Lock()
	game.snapshot.Battle.Turn++
	game.snapshot.Revision++
	game.mu.Unlock()
	checkpoint, err = c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" {
		t.Fatalf("active end battle checkpoint=%+v err=%v", checkpoint, err)
	}
	game.mu.Lock()
	if len(game.actions) != 1 {
		t.Fatalf("active end battle retried actions=%+v", game.actions)
	}
	game.snapshot.Phase = aigame.PhaseWorld
	game.snapshot.Battle.Active = false
	game.snapshot.Revision++
	game.mu.Unlock()

	checkpoint, err = c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "ready" {
		t.Fatalf("left end battle checkpoint=%+v err=%v", checkpoint, err)
	}
	game.mu.Lock()
	defer game.mu.Unlock()
	if len(game.actions) != 1 {
		t.Fatalf("left end battle sent unexpected action=%+v", game.actions)
	}
}
