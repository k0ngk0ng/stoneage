package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aileveling"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aiservice"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type recoveryCheckpointStore struct {
	mu          sync.Mutex
	checkpoints map[string]automation.Checkpoint
}

type delayedRecoveryStore struct {
	*recoveryCheckpointStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *delayedRecoveryStore) Load(ctx context.Context, handle string) (automation.Checkpoint, error) {
	checkpoint, err := s.recoveryCheckpointStore.Load(ctx, handle)
	if err != nil || checkpoint.Status != automation.Running {
		return checkpoint, err
	}
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return checkpoint, ctx.Err()
	}
	return checkpoint, nil
}

func (s *recoveryCheckpointStore) Create(_ context.Context, checkpoint automation.Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checkpoints == nil {
		s.checkpoints = make(map[string]automation.Checkpoint)
	}
	if _, exists := s.checkpoints[checkpoint.Plan.ID]; exists {
		return automation.ErrConflict
	}
	s.checkpoints[checkpoint.Plan.ID] = checkpoint
	return nil
}

func (s *recoveryCheckpointStore) Load(_ context.Context, handle string) (automation.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	checkpoint, ok := s.checkpoints[handle]
	if !ok {
		return automation.Checkpoint{}, automation.ErrNotFound
	}
	return checkpoint, nil
}

func (s *recoveryCheckpointStore) Save(_ context.Context, checkpoint automation.Checkpoint, expected uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.checkpoints[checkpoint.Plan.ID]
	if !ok || current.Revision != expected {
		return automation.ErrConflict
	}
	s.checkpoints[checkpoint.Plan.ID] = checkpoint
	return nil
}

func recoveryLevelPlan(handle, characterID string, target int) automation.Plan {
	return automation.Plan{
		ID: handle, CharacterID: characterID, Mode: "leveling", KnowledgeRevision: "recovery-test",
		Title: "recovery test", Targets: []automation.Target{{Kind: "character", ID: characterID, Level: target}}, TargetPolicy: "all",
		MaximumSeconds: 60, MaximumDeaths: 0,
		Steps: []automation.Step{{ID: "encounter", Action: automation.Action{Skill: "leveling.encounter", Arguments: json.RawMessage(`{}`)}, Success: []automation.Condition{{Kind: "not_battle"}}, TimeoutSeconds: 60, MaximumCost: 0, CostKnown: true}},
	}
}

func newRecoveryExecutor(t *testing.T, store *recoveryCheckpointStore) (*AutomationExecutor, *aiservice.ReceiptStore) {
	t.Helper()
	receipts, err := aiservice.OpenReceiptStore(t.TempDir() + "/receipts.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receipts.Close() })
	executor, err := NewAutomationExecutor(AutomationExecutorConfig{Knowledge: &aiknowledge.Knowledge{Digest: "recovery-test"}, Tiles: flatAutomationTiles{}, Plans: store, Receipts: receipts, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return executor, receipts
}

func newRecoveryWebSession(t *testing.T, serverID string, level int32) (*tcpSession, net.Conn, aigame.Snapshot) {
	t.Helper()
	left, right := net.Pipe()
	session := newTCPSession("recovery-session", left, 64*1024)
	session.serverID = serverID
	t.Cleanup(func() {
		session.close()
		_ = right.Close()
	})
	seedWebAutomationState(t, session, level, 1000)
	// Keep the selected character's canonical slot in the authoritative
	// character list so recovery cannot fall back to account:name.
	session.applyAuthoritativePacket(webServerPacket(t, 6, "CharList", "successful", `AutomationHero|0\z0\z1\z10\z100\z20\z30\z4\z0\z50\z50\z50\z50\z0\zAutomationHero\zhome`))
	session.applyAuthoritativePacket(webServerPacket(t, 7, "CharLogin", "successful", ""))
	snapshot, err := session.observeAuthoritative(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return session, right, snapshot
}

func recoveryRunningHandle(t *testing.T, executor *AutomationExecutor, store *recoveryCheckpointStore, session *tcpSession, snapshot aigame.Snapshot, handleID string, phase string, target int) *webAutomationHandle {
	t.Helper()
	identity, ok := automationIdentityFromSnapshot(snapshot, session.serverLineID())
	if !ok {
		t.Fatalf("test identity unavailable: %+v", snapshot)
	}
	checkpoint := automation.Checkpoint{Plan: recoveryLevelPlan(handleID, identity.CharacterID, target), Revision: 1, Status: automation.Running, Phase: phase, StartedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.Create(context.Background(), checkpoint); err != nil {
		t.Fatal(err)
	}
	state, lease, err := session.gate.Switch(1, aicontrol.Leveling, "recovery test")
	if err != nil {
		t.Fatal(err)
	}
	return &webAutomationHandle{executor: executor, session: &AutomationSession{ID: session.id, session: session, mode: aicontrol.Leveling, generation: state.Generation}, binding: aimcp.Binding{AccountID: identity.AccountID, CharacterID: identity.CharacterID, CharacterName: identity.CharacterName, Generation: state.Generation}, identity: identity, config: AutomationConfig{Targets: []AutomationTarget{{Kind: "character", Level: 99}}, TargetPolicy: "all", MaximumSeconds: 60}, mode: aicontrol.Leveling, generation: state.Generation, ctx: lease, plans: store, taskHandle: handleID, poll: time.Millisecond}
}

func TestAutomationDetachPersistsPausedRecoveryAndScopesByServer(t *testing.T) {
	store := &recoveryCheckpointStore{}
	executor, _ := newRecoveryExecutor(t, store)
	tcp, _, snapshot := newRecoveryWebSession(t, "line-a", 1)
	handle := recoveryRunningHandle(t, executor, store, tcp, snapshot, "level-detach", "ready", 99)
	tcp.gate.Close()
	if err := handle.Detach(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A watcher which read its receipt before disconnect must not remove the
	// offer when its callback arrives after the durable handoff.
	handle.finishReceipt(aimcp.TaskReceipt{Status: aimcp.ReceiptFailed})
	sent := false
	err := handle.session.Dispatch(context.Background(), handle.generation, aicontrol.Leveling, func(context.Context) error { sent = true; return nil })
	if err == nil || sent {
		t.Fatalf("detached lease dispatched: sent=%v err=%v", sent, err)
	}
	checkpoint, err := store.Load(context.Background(), "level-detach")
	if err != nil || checkpoint.Status != automation.Paused || checkpoint.Phase != "ready" {
		t.Fatalf("detach checkpoint=%+v err=%v", checkpoint, err)
	}
	offer, err := executor.Recovery(context.Background(), &AutomationSession{ID: tcp.id, session: tcp})
	if err != nil || offer == nil || offer.Handle != "level-detach" {
		t.Fatalf("offer=%+v err=%v", offer, err)
	}
	other, _, otherSnapshot := newRecoveryWebSession(t, "line-b", 1)
	if otherSnapshot.Character != snapshot.Character {
		t.Fatalf("test sessions differ: %+v %+v", snapshot, otherSnapshot)
	}
	if offer, err := executor.Recovery(context.Background(), &AutomationSession{ID: other.id, session: other}); err != nil || offer != nil {
		t.Fatalf("cross-line offer=%+v err=%v", offer, err)
	}
}

func TestAutomationRecoverCompletedCheckpointUsesFreshBackendAndConsumesOffer(t *testing.T) {
	store := &recoveryCheckpointStore{}
	executor, _ := newRecoveryExecutor(t, store)
	oldTCP, _, oldSnapshot := newRecoveryWebSession(t, "line-a", 1)
	old := recoveryRunningHandle(t, executor, store, oldTCP, oldSnapshot, "level-complete", "ready", 1)
	fresh, err := executor.buildWebAutomationHandle(old.ctx, old.session, AutomationStartRequest{Mode: old.mode, Generation: old.generation, Config: old.config}, old.binding)
	if err != nil {
		t.Fatal(err)
	}
	old.backend = fresh.backend
	oldTCP.gate.Close()
	if err := old.Detach(context.Background()); err != nil {
		t.Fatal(err)
	}
	newTCP, _, _ := newRecoveryWebSession(t, "line-a", 1)
	state, lease, err := newTCP.gate.Switch(1, aicontrol.Leveling, "recover")
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := executor.Recover(lease, &AutomationSession{ID: newTCP.id, session: newTCP, mode: aicontrol.Leveling, generation: state.Generation}, "level-complete")
	if err != nil {
		t.Fatal(err)
	}
	value, ok := recovered.(*webAutomationHandle)
	if !ok || value == nil {
		t.Fatalf("unexpected recovered handle %T", recovered)
	}
	if value.backend == nil || value.terminalReceipt == nil {
		t.Fatalf("recovered handle=%T backend=%v terminal=%v", recovered, value.backend != nil, value.terminalReceipt != nil)
	}
	if value.backend == old.backend {
		t.Fatal("recovery reused the detached backend")
	}
	checkpoint, err := store.Load(context.Background(), "level-complete")
	if err != nil || checkpoint.Status != automation.Completed {
		t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
	}
	if checkpoint.Plan.Targets[0].Level != 1 || len(store.checkpoints) != 1 {
		t.Fatal("recovery replaced the original plan with current selectors")
	}
	if offer, err := executor.Recovery(context.Background(), &AutomationSession{ID: newTCP.id, session: newTCP}); err != nil || offer != nil {
		t.Fatalf("consumed offer=%+v err=%v", offer, err)
	}
	value.Activate()
	if newTCP.gate.State().Mode != aicontrol.Manual {
		t.Fatalf("terminal recovery did not release gate: %+v", newTCP.gate.State())
	}
}

func TestAutomationRecoverUnknownSubmittedKeepsOfferAndDoesNotReplay(t *testing.T) {
	store := &recoveryCheckpointStore{}
	executor, _ := newRecoveryExecutor(t, store)
	oldTCP, _, oldSnapshot := newRecoveryWebSession(t, "line-a", 1)
	handle := recoveryRunningHandle(t, executor, store, oldTCP, oldSnapshot, "level-unknown", "submitted", 99)
	oldTCP.gate.Close()
	if err := handle.Detach(context.Background()); err != nil {
		t.Fatal(err)
	}
	newTCP, peer, _ := newRecoveryWebSession(t, "line-a", 1)
	state, lease, err := newTCP.gate.Switch(1, aicontrol.Leveling, "recover")
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Recover(lease, &AutomationSession{ID: newTCP.id, session: newTCP, mode: aicontrol.Leveling, generation: state.Generation}, "level-unknown")
	if !errors.Is(err, aileveling.ErrUnknownDelivery) {
		t.Fatalf("unknown delivery err=%v", err)
	}
	checkpoint, err := store.Load(context.Background(), "level-unknown")
	if err != nil || checkpoint.Status != automation.Paused || checkpoint.Phase != "submitted" {
		t.Fatalf("unknown checkpoint=%+v err=%v", checkpoint, err)
	}
	if offer, err := executor.Recovery(context.Background(), &AutomationSession{ID: newTCP.id, session: newTCP}); err != nil || offer == nil {
		t.Fatalf("unknown offer lost: %+v err=%v", offer, err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var packet [1]byte
	n, readErr := peer.Read(packet[:])
	var timeout net.Error
	if n != 0 || !errors.As(readErr, &timeout) || !timeout.Timeout() {
		t.Fatalf("unknown recovery wrote to game socket: n=%d err=%v", n, readErr)
	}
}

func TestAutomationRecoveryClaimIsVisibleAndLateDetachCannotReplaceIt(t *testing.T) {
	store := &recoveryCheckpointStore{}
	executor, _ := newRecoveryExecutor(t, store)
	tcp, _, snapshot := newRecoveryWebSession(t, "line-a", 1)
	handle := recoveryRunningHandle(t, executor, store, tcp, snapshot, "level-claimed", "ready", 99)
	tcp.gate.Close()
	if err := handle.Detach(context.Background()); err != nil {
		t.Fatal(err)
	}
	executor.recoverMu.Lock()
	record := executor.recoveries["level-claimed"]
	if record == nil {
		executor.recoverMu.Unlock()
		t.Fatal("missing recovery record")
	}
	record.claimed = true
	executor.recoverMu.Unlock()
	if offer, err := executor.Recovery(context.Background(), &AutomationSession{ID: tcp.id, session: tcp}); !errors.Is(err, errAutomationRecoveryBusy) || offer != nil {
		t.Fatalf("claimed offer=%+v err=%v", offer, err)
	}
	identity, ok := automationIdentityFromSnapshot(snapshot, tcp.serverLineID())
	if !ok {
		t.Fatal("identity unavailable")
	}
	executor.registerRecovery(&automationRecoveryRecord{identity: identity, handle: "late-detach", mode: aicontrol.Leveling, reason: "late", config: AutomationConfig{}, detachedAt: time.Now().UTC()})
	executor.recoverMu.Lock()
	defer executor.recoverMu.Unlock()
	if executor.recoveries["level-claimed"] == nil || executor.recoveries["late-detach"] != nil {
		t.Fatalf("claimed record was replaced: %+v", executor.recoveries)
	}
}

func TestAutomationDetachReservesIdentityBeforeDurableHandoff(t *testing.T) {
	baseStore := &recoveryCheckpointStore{}
	store := &delayedRecoveryStore{recoveryCheckpointStore: baseStore, started: make(chan struct{}), release: make(chan struct{})}
	executor, _ := newRecoveryExecutor(t, store.recoveryCheckpointStore)
	// The executor must use the delayed wrapper so its first checkpoint read
	// remains in the handoff window while the reconnect checks the identity.
	executor.config.Plans = store
	oldTCP, _, oldSnapshot := newRecoveryWebSession(t, "line-a", 1)
	handle := recoveryRunningHandle(t, executor, baseStore, oldTCP, oldSnapshot, "level-handoff", "ready", 99)
	handle.plans = store
	oldTCP.gate.Close()
	detachDone := make(chan error, 1)
	go func() { detachDone <- handle.Detach(context.Background()) }()

	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("detach did not enter the durable handoff window")
	}

	newTCP, _, _ := newRecoveryWebSession(t, "line-a", 1)
	offer, err := executor.Recovery(context.Background(), &AutomationSession{ID: newTCP.id, session: newTCP})
	if !errors.Is(err, errAutomationRecoveryBusy) || offer != nil {
		t.Fatalf("handoff recovery was not busy: offer=%+v err=%v", offer, err)
	}
	state, lease, err := newTCP.gate.Switch(1, aicontrol.Leveling, "start during handoff")
	if err != nil {
		t.Fatal(err)
	}
	startRequest := levelingAutomationRequest(state.Generation, 99, 0)
	startRequest.SessionID = newTCP.id
	_, err = executor.Start(lease, &AutomationSession{ID: newTCP.id, session: newTCP, mode: aicontrol.Leveling, generation: state.Generation}, startRequest)
	if !errors.Is(err, errAutomationRecoveryBusy) {
		t.Fatalf("direct Start bypassed handoff marker: %v", err)
	}
	close(store.release)
	if err := <-detachDone; err != nil {
		t.Fatal(err)
	}
	offer, err = executor.Recovery(context.Background(), &AutomationSession{ID: newTCP.id, session: newTCP})
	if err != nil || offer == nil || offer.Handle != "level-handoff" {
		t.Fatalf("published recovery=%+v err=%v", offer, err)
	}
}
