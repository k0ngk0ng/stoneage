package aiservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aifunding"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// This opt-in check uses normal gameplay in the isolated, patched local QA
// server. No teleport, item injection or encounter-table changes are allowed.
// Passing proves the concrete journey, not unattended model task execution.
func TestLiveGiftExchangeFreshAI(t *testing.T) {
	if os.Getenv("STONEAGE_GIFT_EXCHANGE_LIVE_TEST") != "1" {
		t.Skip("set STONEAGE_GIFT_EXCHANGE_LIVE_TEST=1 for a complete local QA gift journey")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Check research readiness before creating a character, starting a gateway
	// or touching QA containers. An opt-in flag does not waive preparation.
	knowledge, err := aiknowledge.LoadDataDir(ctx, filepath.Join(movementCrossMapLiveRepositoryRoot(t), "runtime", "legacy-server", "gmsv", "data"))
	if err != nil {
		t.Fatal(err)
	}
	task, ok := knowledge.FindTask("hometown-0-gift-exchange")
	if !ok || !task.PreparationReviewed || strings.TrimSpace(task.PreparationNotes) == "" {
		t.Fatal("gift journey blocked: guide/version, character/pet minimum levels and route preparation require review; see docs/ai-quest-sources.md")
	}
	const patchedBinary = "51f57aa068a72148078d8cf0118fde3f9aafbfa62a0323fe936d29be9b0f2c19"
	raw, err := exec.CommandContext(ctx, "docker", "exec", "stoneage-player-qa-gmsv-1", "sha256sum", "/proc/1/exe").Output()
	fields := strings.Fields(string(raw))
	if err != nil || len(fields) != 2 || fields[0] != patchedBinary {
		t.Fatal("gift journey requires the reviewed local QA enemy-bounds/funding/observation binary")
	}
	runLiveGiftPickupFreshAI(t, func(ctx context.Context, f *movementCrossMapLiveFreshAI, b *GameBackend, m *MovementSkill) {
		prepareLiveGiftJourney(t, ctx, f, b, m)
	}, func(ctx context.Context, f *movementCrossMapLiveFreshAI, b *GameBackend, m *MovementSkill) {
		continueLiveGiftExchange(t, ctx, f, b, m)
	})
}

func prepareLiveGiftJourney(t *testing.T, ctx context.Context, f *movementCrossMapLiveFreshAI, backend *GameBackend, movement *MovementSkill) {
	t.Helper()
	data := filepath.Join(f.RepoRoot, "runtime", "legacy-server", "gmsv", "data")
	effective := filepath.Join(f.RepoRoot, "build", "player-integration", "game", "gmsv", "data")
	for _, source := range []string{"npc/genout/shop_m.create", "npc/genout/npcgen.template", "npc/genout/ss_1004_17_13", "itemset.txt"} {
		if _, err := movementCrossMapLiveCompareFile(data, effective, source); err != nil {
			t.Fatal(err)
		}
	}
	catalogs := filepath.Join(f.RepoRoot, "ai", "catalogs")
	fingerprint := backend.Knowledge.Fingerprint()
	healing, err := LoadHealingItemsForData(filepath.Join(catalogs, "healing-items-2.5.json"), fingerprint, filepath.Join(data, "itemset.txt"))
	if err != nil {
		t.Fatal(err)
	}
	npcs, err := LoadNPCRegistry(filepath.Join(catalogs, "shops-2.5.json"), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	offers, err := LoadStockItems(filepath.Join(catalogs, "stock-items-2.5.json"), fingerprint, npcs, healing)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := aifunding.NewManager(filepath.Join(f.RepoRoot, "build", "player-integration", "tmp", "ai-funding", "policies"))
	if err != nil {
		t.Fatal(err)
	}
	account, slot := f.Created.Account.Username, f.Created.Binding.CharacterSlot
	t.Cleanup(func() {
		if err := manager.Revoke(account, slot); err != nil {
			t.Error(err)
		}
	})
	if err := manager.Provision(account, slot, 0); err != nil {
		t.Fatal(err)
	}
	backend.Funding = func(context.Context) (bool, error) { return manager.Allowed(account, slot), nil }
	task, ok := backend.Knowledge.FindTask("hometown-0-gift-exchange")
	if !ok || task.Status != aiknowledge.TaskUnverified || task.ExecutionVerified || len(task.Steps) == 0 || task.Steps[0].Kind != "stock" {
		t.Fatal("expected unverified gift task with explicit supplies")
	}
	step := task.Steps[0]
	stock := &StockSkill{Backend: backend, Movement: movement, Contracts: offers}
	o, err := backend.Observe(ctx, backend.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := stock.Execute(ctx, automation.Action{Skill: step.Action.Skill, Arguments: step.Action.Arguments, MaximumCost: step.MaximumCost, ExpectedRevision: o.Revision}); err != nil {
		t.Fatal("gift preparation:", err)
	}
	o, err = backend.Observe(ctx, backend.Binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range step.SuccessConditions {
		if !(automation.Condition{Kind: c.Kind, ID: c.ID, Value: c.Value}).Match(projectAutomationObservation(o)) {
			t.Fatal("gift preparation predicate not confirmed")
		}
	}
	movement.HealthRecovery = &TravelItemRecovery{Healing: &ItemHealingSkill{Backend: backend, Contracts: healing}}
	t.Log("embedded gift preparation confirmed; travel item recovery installed")
}

func continueLiveGiftExchange(t *testing.T, ctx context.Context, fixture *movementCrossMapLiveFreshAI, backend *GameBackend, movement *MovementSkill) {
	t.Helper()
	diagnostic := &travelDiagnosticSession{GameSession: backend.Session}
	backend.Session = diagnostic
	defer diagnostic.persist(t, fixture.RepoRoot)
	stage := "verify-yayoi-contract"
	passed := false
	var last aimcp.Observation
	var battleRecoveries []map[string]any
	sourceHashes := map[string]string{}
	defer func() {
		// Only structured, credential-free game state is recorded. Do not dump
		// sessions, raw startup files, model configuration or account passwords.
		observeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if observed, err := backend.Observe(observeCtx, backend.Binding); err == nil {
			last = observed
		}
		evidence := map[string]any{
			"test": t.Name(), "passed": passed, "stage": stage,
			"battle_recoveries": battleRecoveries,
			"recorded_at":       time.Now().UTC(), "knowledge_digest": backend.Knowledge.Fingerprint(),
			"npc_source_hashes": sourceHashes, "model_invoked": false,
			"position": map[string]int{"floor": last.Floor, "x": last.X, "y": last.Y},
			"phase":    last.Phase, "battle_active": last.Battle.Active,
			"hp": last.Character.HP, "inventory_known": last.Flags["inventory:known"],
			"flower_count": last.Inventory["item:2415"], "shell_count": last.Inventory["item:2414"],
			"meat_count": last.Inventory["item:2344"], "travel_item_recovery_configured": movement.HealthRecovery != nil,
			"end_event_2": last.Flags["end:2"], "revision": last.Revision,
		}
		raw, err := json.MarshalIndent(evidence, "", "  ")
		path := ""
		if err == nil {
			var file *os.File
			file, err = os.CreateTemp(filepath.Join(fixture.RepoRoot, "build", "ai"), "gift-exchange-evidence-*.json")
			if err == nil {
				path = file.Name()
				_, err = file.Write(append(raw, '\n'))
				closeErr := file.Close()
				if err == nil {
					err = closeErr
				}
			}
		}
		if err != nil {
			t.Errorf("persist gift exchange evidence: %v", err)
		}
		t.Logf("complete gift journey stage=%s passed=%v floor=%d position=(%d,%d) battle=%v evidence=%s", stage, passed, last.Floor, last.X, last.Y, last.Battle.Active, path)
	}()

	dataRoot := filepath.Join(fixture.RepoRoot, "runtime", "legacy-server", "gmsv", "data")
	effectiveRoot := strings.TrimSpace(os.Getenv("STONEAGE_MOVEMENT_CROSS_MAP_EFFECTIVE_DATA_DIR"))
	if effectiveRoot == "" {
		effectiveRoot = filepath.Join(fixture.RepoRoot, "build", "player-integration", "game", "gmsv", "data")
	}
	hash := sha256.New()
	for _, source := range []string{"npc/sainasu/event/event02.create", "npc/sainasu/event/event02_1"} {
		digest, err := movementCrossMapLiveCompareFile(dataRoot, effectiveRoot, source)
		if err != nil {
			t.Fatal(err)
		}
		sourceHashes[source] = digest
		raw, err := os.ReadFile(filepath.Join(dataRoot, source))
		if err != nil {
			t.Fatal(err)
		}
		hash.Write([]byte(source + "\x00"))
		hash.Write(raw)
		hash.Write([]byte{0})
	}
	const reviewed = "3274c6b975a36d3454a299c1f9c8d3599074bd581d39426baa9741b7c3cbc85c"
	if hex.EncodeToString(hash.Sum(nil)) != reviewed {
		t.Fatal("Yayoi sources changed; review before running the exchange")
	}
	spec := NPCSpec{
		Alias: "sainasu-yayoi", Name: "弥生", Floor: 2000, X: 55, Y: 92, TalkRange: 1,
		Verified: true, SourceFingerprint: reviewed,
		Windows: []NPCWindowSpec{
			{Type: 0, Sequence: 235, WindowObjectFromActor: true, Choices: map[int]NPCChoice{4: {Button: 4, MaximumCost: 0}}},
			{Type: 0, Sequence: 430, WindowObjectFromActor: true, Choices: map[int]NPCChoice{1: {Button: 1, MaximumCost: 0}}},
		},
	}
	registry, err := NewNPCRegistry([]NPCSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	npc := NewNPCSkill(backend, registry)
	observe := func() aimcp.Observation {
		o, err := backend.Observe(ctx, backend.Binding)
		if err != nil {
			t.Fatal(err)
		}
		last = o
		return o
	}
	submit := func(skill DeterministicSkill, name string, args any) {
		o := observe()
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		if err := skill.Execute(ctx, automation.Action{Skill: name, Arguments: raw, ExpectedRevision: o.Revision}); err != nil {
			t.Fatalf("gift journey %s: %v", stage, err)
		}
	}
	wait := func(predicate func(aimcp.Observation) bool) aimcp.Observation {
		bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		o, err := movementCrossMapLiveWaitObservation(bounded, backend, backend.Binding, predicate)
		if err != nil {
			t.Fatalf("gift journey did not confirm %s: %v", stage, err)
		}
		last = o
		return o
	}
	stage = "travel-to-yayoi"
	recovery := &TravelBattle{Backend: backend}
	movement.BattleRecovery = movementBattleRecoveryFunc(func(ctx context.Context) error {
		err := recovery.Escape(ctx)
		result := map[string]any{"returned_to_world": err == nil}
		if err != nil {
			result["error"] = err.Error()
		}
		if snapshot, observeErr := backend.Session.Observe(ctx); observeErr == nil {
			result["result"] = snapshot.Battle.Result
			result["last_command"] = snapshot.Battle.LastCommand
			result["phase"] = snapshot.Phase
			result["hp"] = snapshot.Player.HP
		}
		battleRecoveries = append(battleRecoveries, result)
		t.Logf("travel battle recovery %d: %v", len(battleRecoveries), result)
		return err
	})
	submit(movement, "move", movementArguments{Floor: 2000, X: 55, Y: 93})
	arrived := wait(func(o aimcp.Observation) bool {
		return o.Connected && o.Ready && !o.Battle.Active && o.Floor == 2000 && o.X == 55 && o.Y == 93
	})
	actorID, matches := -1, 0
	for _, actor := range arrived.Actors {
		if actor.X == spec.X && actor.Y == spec.Y && actor.ID >= 0 &&
			(actor.Name == spec.Name || actor.FreeName == spec.Name || actor.Title == spec.Name) {
			actorID, matches = actor.ID, matches+1
		}
	}
	if matches != 1 {
		t.Fatalf("expected one source-matched Yayoi actor, got %d", matches)
	}
	stage = "talk-to-yayoi"
	submit(npc, "npc.talk", npcTalkArgumentsForLive(spec, actorID))
	window := func(sequence, buttons int) func(aimcp.Observation) bool {
		return func(o aimcp.Observation) bool {
			w := o.ActiveWindow
			return o.Ready && w != nil && w.Open && w.Type == 0 && w.Sequence == sequence && w.ObjectID == actorID && w.ButtonType == buttons
		}
	}
	wait(window(235, giftWindowButtonYesNo))
	stage = "accept-flower"
	submit(npc, "npc.window", npcWindowArgumentsForLive(spec.Alias, 235, giftWindowButtonYes))
	wait(window(430, giftWindowButtonOK))
	stage = "confirm-exchange"
	submit(npc, "npc.window", npcWindowArgumentsForLive(spec.Alias, 430, giftWindowButtonOK))
	complete := func(o aimcp.Observation) bool {
		return o.Connected && o.Ready && o.Phase == string(aigame.PhaseWorld) &&
			o.Flags["inventory:known"] && o.Inventory["item:2414"] == 1 && o.Inventory["item:2415"] == 0 && o.Flags["end:2"] && o.Flags["savepoint:0"]
	}
	wait(complete)
	stage = "relogin-completed-exchange"
	last = reloginGiftCharacter(t, ctx, fixture, backend, complete)
	passed = true
	stage = "completed-and-relogged"
}

type movementBattleRecoveryFunc func(context.Context) error

func (f movementBattleRecoveryFunc) Escape(ctx context.Context) error { return f(ctx) }
