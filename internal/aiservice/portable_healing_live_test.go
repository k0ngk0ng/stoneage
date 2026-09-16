package aiservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aifunding"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aileveling"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// Normal gameplay only: buy meat, encounter/escape, use meat, and relogin.
// Requires the isolated QA funding override; never injects HP or inventory.
func TestLivePortableHealingFreshAI(t *testing.T) {
	if os.Getenv("STONEAGE_PORTABLE_HEALING_LIVE_TEST") != "1" {
		t.Skip("opt in with STONEAGE_PORTABLE_HEALING_LIVE_TEST=1 and the local QA funding override")
	}
	runLivePortableHealingFreshAI(t, false, false, false)
}

func TestLiveMovementItemRecoveryFreshAI(t *testing.T) {
	if os.Getenv("STONEAGE_MOVEMENT_ITEM_RECOVERY_LIVE_TEST") != "1" {
		t.Skip("opt in with STONEAGE_MOVEMENT_ITEM_RECOVERY_LIVE_TEST=1 and the local QA funding override")
	}
	runLivePortableHealingFreshAI(t, true, false, false)
}

func TestLiveGiftSuppliesFreshAI(t *testing.T) {
	if os.Getenv("STONEAGE_GIFT_SUPPLIES_LIVE_TEST") != "1" {
		t.Skip("explicit isolated QA supply test opt-in required")
	}
	runLivePortableHealingFreshAI(t, false, true, false)
}

func TestLiveLevelingItemRecoveryFreshAI(t *testing.T) {
	if os.Getenv("STONEAGE_LEVELING_ITEM_RECOVERY_LIVE_TEST") != "1" {
		t.Skip("explicit isolated QA leveling recovery opt-in required")
	}
	runLivePortableHealingFreshAI(t, false, false, true)
}

func runLivePortableHealingFreshAI(t *testing.T, automaticRecovery, prepareOnly, levelingRecovery bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	const qaBinaryHash = "51f57aa068a72148078d8cf0118fde3f9aafbfa62a0323fe936d29be9b0f2c19"
	binary, err := exec.CommandContext(ctx, "docker", "exec", "stoneage-player-qa-gmsv-1", "sha256sum", "/proc/1/exe").Output()
	if err != nil || len(strings.Fields(string(binary))) != 2 || strings.Fields(string(binary))[0] != qaBinaryHash {
		t.Fatal("running QA binary must match the reviewed funding/observation build")
	}
	f := movementCrossMapLiveProvisionFreshAI(t, ctx, "portable-healing", "portable-healing-live-test")
	dataRoot := filepath.Join(f.RepoRoot, "runtime", "legacy-server", "gmsv", "data")
	effectiveRoot := filepath.Join(f.RepoRoot, "build", "player-integration", "game", "gmsv", "data")
	sources := []string{"npc/genout/shop_m.create", "npc/genout/npcgen.template", "npc/genout/ss_1004_17_13", "itemset.txt"}
	hashes := map[string]string{}
	fingerprint := sha256.New()
	for _, path := range sources {
		hash, err := movementCrossMapLiveCompareFile(dataRoot, effectiveRoot, path)
		if err != nil {
			t.Fatal(err)
		}
		hashes[path] = hash
		raw, err := os.ReadFile(filepath.Join(dataRoot, path))
		if err != nil {
			t.Fatal(err)
		}
		fingerprint.Write([]byte(path + "\x00"))
		fingerprint.Write(raw)
		fingerprint.Write([]byte{0})
	}
	const reviewed = "d6010f2c0f183311f353c801f2b43c022389cd2f50956bcaeddb442dd75b2862"
	if hex.EncodeToString(fingerprint.Sum(nil)) != reviewed {
		t.Fatal("shop source contract changed; review it before purchasing")
	}
	knowledge, err := aiknowledge.LoadDataDir(ctx, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	navigator, err := ainavigation.LoadDataDir(ctx, dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := movementCrossMapLiveVerifyEffectiveData(ctx, f.RepoRoot, navigator); err != nil {
		t.Fatal(err)
	}
	floor, ok := navigator.Floor(1004)
	if !ok {
		t.Fatal("shop floor is missing")
	}
	if _, err := movementCrossMapLiveCompareFile(dataRoot, effectiveRoot, filepath.Join("map", floor.Source)); err != nil {
		t.Fatal(err)
	}
	manager, err := aifunding.NewManager(filepath.Join(f.RepoRoot, "build", "player-integration", "tmp", "ai-funding", "policies"))
	if err != nil {
		t.Fatal(err)
	}
	account, slot := f.Created.Account.Username, f.Created.Binding.CharacterSlot
	t.Cleanup(func() {
		if err := manager.Revoke(account, slot); err != nil {
			t.Error("revoke isolated funding policy:", err)
		}
		path, err := manager.PolicyPath(account, slot)
		if err != nil {
			t.Error(err)
		} else if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) || manager.Allowed(account, slot) {
			t.Error("isolated funding capability was not removed")
		}
	})
	gate := aicontrol.New()
	defer gate.Close()
	state, _, err := gate.Switch(gate.State().Generation, aicontrol.Agent, "isolated funding purchase")
	if err != nil {
		t.Fatal(err)
	}
	binding := aimcp.Binding{AccountID: account, CharacterID: f.Created.Binding.CharacterID,
		CharacterName: f.Created.Binding.CharacterName, Generation: state.Generation}
	backend := &GameBackend{Binding: binding, Gate: gate, Owner: aicontrol.Agent, Session: f.Lease.Session,
		Knowledge: knowledge, OwnStateRefresh: &OwnStateRefresher{},
		Funding: func(context.Context) (bool, error) { return manager.Allowed(account, slot), nil }}
	if automaticRecovery || levelingRecovery {
		diagnostic := &travelDiagnosticSession{GameSession: backend.Session}
		backend.Session = diagnostic
		defer diagnostic.persist(t, f.RepoRoot)
	}
	initial, err := movementCrossMapLiveWaitObservation(ctx, backend, binding, func(o aimcp.Observation) bool {
		return o.Ready && o.Phase == string(aigame.PhaseWorld) && o.Floor == 1006 && o.Flags["inventory:known"]
	})
	if err != nil {
		t.Fatal(err)
	}
	const unitPrice = int64(12)
	quantity := 2
	stockArgs := stockArguments{Item: "small-meat", TargetCount: quantity}
	var prep aiknowledge.TaskStep
	if prepareOnly {
		task, found := knowledge.FindTask("hometown-0-gift-exchange")
		if !found || len(task.Steps) == 0 || task.Status != aiknowledge.TaskUnverified || task.ExecutionVerified {
			t.Fatal("expected an unverified gift task")
		}
		prep = task.Steps[0]
		if prep.Kind != "stock" || json.Unmarshal(prep.Action.Arguments, &stockArgs) != nil || stockArgs.Item != "small-meat" || stockArgs.TargetCount != 5 || stockArgs.ReserveSlots != 2 || prep.MaximumCost != 60 {
			t.Fatal("unexpected gift preparation contract")
		}
		quantity = stockArgs.TargetCount
	}
	if initial.Gold < 0 || quantity < 1 || quantity > 15 || len(initial.Inventory) != 0 {
		t.Fatalf("fresh inventory/balance unsuitable for bounded purchase: gold=%d quantity=%d inventory=%v", initial.Gold, quantity, initial.Inventory)
	}
	cost := int64(quantity) * unitPrice
	t.Logf("fresh visible gold=%d; purchase %d x item 2344 at 12, total=%d", initial.Gold, quantity, cost)
	movement := &MovementSkill{Backend: backend, Navigator: navigator, WarpGraph: aiplanner.NewWarpGraph(knowledge), SegmentTimeout: 5 * time.Second, WarpConfirmationTimeout: 8 * time.Second}
	ledgerPath := filepath.Join(f.RepoRoot, "build", "player-integration", "tmp", "ai-funding", "ledger.log")
	baseline, err := os.ReadFile(ledgerPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read funding ledger baseline:", err)
	}
	if bytes.Contains(baseline, []byte("|"+account+"|")) {
		t.Fatal("fresh account unexpectedly has prior funding charges")
	}
	started := time.Now().Unix()
	if err := manager.Provision(account, slot, 0); err != nil {
		t.Fatal("provision server-owned unlimited funding:", err)
	}
	catalogRoot := filepath.Join(f.RepoRoot, "ai", "catalogs")
	healingItems, err := LoadHealingItemsForData(filepath.Join(catalogRoot, "healing-items-2.5.json"), knowledge.Fingerprint(), filepath.Join(dataRoot, "itemset.txt"))
	if err != nil {
		t.Fatal(err)
	}
	npcs, err := LoadNPCRegistry(filepath.Join(catalogRoot, "shops-2.5.json"), knowledge.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	offers, err := LoadStockItems(filepath.Join(catalogRoot, "stock-items-2.5.json"), knowledge.Fingerprint(), npcs, healingItems)
	if err != nil {
		t.Fatal(err)
	}
	stock := &StockSkill{Backend: backend, Movement: movement, Contracts: offers}
	raw, _ := json.Marshal(stockArgs)
	beforeStock, err := backend.Observe(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := stock.Execute(ctx, automation.Action{Skill: "item.stock", ExpectedRevision: beforeStock.Revision, Arguments: raw, MaximumCost: cost}); err != nil {
		t.Fatal("automatic stock:", err)
	}
	t.Log("stock skill reached shop and confirmed target inventory")
	finalCtx, finalCancel := context.WithTimeout(ctx, 15*time.Second)
	defer finalCancel()
	final, err := movementCrossMapLiveWaitObservation(finalCtx, backend, binding, func(o aimcp.Observation) bool {
		return o.Flags["inventory:known"] && o.Inventory["item:2344"] == quantity
	})
	if err != nil {
		t.Fatal("server did not confirm purchased item count:", err)
	}
	if final.Gold != initial.Gold || !final.UnlimitedFunds {
		t.Fatalf("unexpected funding result: initial=%d final=%d cost=%d unlimited=%v", initial.Gold, final.Gold, cost, final.UnlimitedFunds)
	}
	ledger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal("read QA funding ledger:", err)
	}
	if !bytes.HasPrefix(ledger, baseline) {
		t.Fatal("funding ledger was replaced during the purchase")
	}
	charges := 0
	for _, line := range strings.Split(string(ledger[len(baseline):]), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) != 6 || fields[1] != account || fields[2] != strconv.Itoa(slot) {
			continue
		}
		if fields[0] != "1" || fields[3] != "npc" || fields[4] != strconv.FormatInt(cost, 10) {
			t.Fatal("unexpected charge for isolated account")
		}
		stamp, err := strconv.ParseInt(fields[5], 10, 64)
		if err != nil || stamp < started || stamp > time.Now().Unix()+1 {
			t.Fatal("charge timestamp is outside the purchase interval")
		}
		charges++
	}
	if charges != 1 {
		t.Fatalf("expected exactly one authoritative NPC charge, got %d", charges)
	}
	if prepareOnly {
		checkPrepared := func(o aimcp.Observation) bool {
			projected := projectAutomationObservation(o)
			for _, c := range prep.SuccessConditions {
				if !(automation.Condition{Kind: c.Kind, ID: c.ID, Value: c.Value, X: c.X, Y: c.Y}).Match(projected) {
					return false
				}
			}
			return o.Connected && o.Ready && !o.Battle.Active && o.Character.HP > 0
		}
		if !checkPrepared(final) {
			t.Fatal("purchase did not satisfy task preparation predicates")
		}
		if err := manager.Revoke(account, slot); err != nil {
			t.Fatal(err)
		}
		relogged := reloginGiftCharacter(t, ctx, f, backend, checkPrepared)
		if relogged.Gold != initial.Gold || relogged.UnlimitedFunds {
			t.Fatal("supply state/funding revocation did not persist")
		}
		evidence := map[string]any{"test": t.Name(), "passed": true, "knowledge_fingerprint": knowledge.Fingerprint(), "qa_binary_sha256": qaBinaryHash,
			"source_hashes": hashes, "quantity": quantity, "reserved_slots": stockArgs.ReserveSlots, "free_slots_after_relogin": 15 - relogged.OwnProgress["backpack_used_slots"],
			"cost": cost, "ledger_charges": charges, "task_step_predicates_verified": true, "relogin_verified": true, "funding_revoked": true, "full_quest_verified": false, "model_invoked": false}
		data, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.RepoRoot, "build", "ai", "gift-supplies-live-evidence.json"), append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		t.Log("gift task supplies and reserved capacity confirmed after relogin; full quest remains unverified")
		return
	}
	// Obtain real damage from one bounded journey, stopping after the first
	// successful escape with reduced HP. The sentinel stops movement before
	// it can continue walking with the newly reduced HP.
	injured := errors.New("normal encounter established reduced HP")
	battle := &TravelBattle{Backend: backend}
	movement.BattleRecovery = movementBattleRecoveryFunc(func(battleCtx context.Context) error {
		if err := battle.Escape(battleCtx); err != nil {
			if snapshot, observeErr := backend.Session.Observe(battleCtx); observeErr == nil {
				t.Logf("travel recovery stopped: HP=%d/%d battle_active=%v error=%v", snapshot.Player.HP, snapshot.Player.MaxHP, snapshot.Battle.Active, err)
			}
			return err
		}
		snapshot, err := backend.Session.Observe(battleCtx)
		if err != nil {
			return err
		}
		t.Logf("escaped normal encounter: HP=%d/%d", snapshot.Player.HP, snapshot.Player.MaxHP)
		if snapshot.Player.HasStatus && snapshot.Player.HP > 0 && snapshot.Player.HP < snapshot.Player.MaxHP && (!(automaticRecovery || levelingRecovery) || snapshot.Player.HP < snapshot.Player.MaxHP/2+snapshot.Player.MaxHP%2) {
			return injured
		}
		return nil
	})
	beforeJourney, err := backend.Observe(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	journeyArgs, _ := json.Marshal(movementArguments{Floor: 100, X: 265, Y: 560})
	journeyErr := movement.Execute(ctx, automation.Action{Skill: "move", ExpectedRevision: beforeJourney.Revision, Arguments: journeyArgs})
	if !errors.Is(journeyErr, injured) {
		t.Fatalf("normal journey did not establish controlled injury: %v", journeyErr)
	}
	beforeUse, err := refreshInventoryIdentity(ctx, NewNPCSkill(backend, nil))
	if err != nil {
		t.Fatal(err)
	}
	if beforeUse.Phase != aigame.PhaseWorld || beforeUse.Battle.Active || beforeUse.Player.HP <= 0 || beforeUse.Player.HP >= beforeUse.Player.MaxHP || healingItemCount(beforeUse, 2344) != quantity {
		t.Fatal("invalid naturally injured pre-use state")
	}
	healingItems, err = LoadHealingItemsForData(filepath.Join(f.RepoRoot, "ai", "catalogs", "healing-items-2.5.json"), knowledge.Fingerprint(), filepath.Join(dataRoot, "itemset.txt"))
	if err != nil {
		t.Fatal("load packaged healing catalog:", err)
	}
	healing := &ItemHealingSkill{Backend: backend, Contracts: healingItems}
	observed, err := backend.Observe(ctx, binding)
	if err != nil {
		t.Fatal(err)
	}
	var resumedTarget *ainavigation.Point
	if levelingRecovery {
		if observed.Character.HP >= (observed.Character.MaxHP+1)/2 {
			t.Fatal("leveling recovery requires naturally low HP")
		}
		plans, err := automation.OpenStore(filepath.Join(f.Root, "leveling-recovery.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer plans.Close()
		receipts, err := OpenReceiptStore(filepath.Join(f.Root, "leveling-recovery-receipts.db"))
		if err != nil {
			t.Fatal("open leveling recovery receipts:", err)
		}
		defer receipts.Close()
		builder, err := NewGameplayBuilder(GameplayConfig{Plans: plans, Tiles: navigator, HealingItems: healingItems})
		if err != nil {
			t.Fatal(err)
		}
		trace := &containerLevelingSession{GameSession: backend.Session, actions: map[aigame.ActionKind]int{}}
		built, err := builder(ctx, BackendInput{Binding: binding, Gate: gate, Session: trace, Knowledge: knowledge, Lease: ctx, Funding: backend.Funding, Receipts: receipts})
		if err != nil {
			t.Fatal("build leveling recovery backend:", err)
		}
		defer built.(*GameBackend).Close()
		coordinator := built.(*GameBackend).Tasks.(*GameTasks).Leveling
		receipt, err := coordinator.Start(ctx, aileveling.StartRequest{TargetKind: "character", TargetLevel: observed.Character.Level + 1, MaximumSeconds: 30, Generation: binding.Generation})
		if err != nil {
			t.Fatal(err)
		}
		checkpoint, err := coordinator.Tick(ctx, receipt.Handle)
		if err != nil || checkpoint.Status != automation.Running || checkpoint.Phase != "ready" {
			t.Fatalf("leveling recovery failed: %+v %v", checkpoint, err)
		}
		trace.mu.Lock()
		moves, warps := trace.actions[aigame.ActionMove], trace.actions[aigame.ActionMapEvent]
		trace.mu.Unlock()
		if moves != 0 || warps != 0 {
			t.Fatal("leveling moved before reobserving confirmed recovery")
		}
		if _, err := coordinator.Cancel(ctx, receipt.Handle, "bounded recovery verified"); err != nil {
			t.Fatal(err)
		}
	} else if automaticRecovery {
		// Resume one actual route tile with the production health recovery.
		// The test never calls Heal or ID itself in this mode.
		if observed.Floor != 100 || observed.Character.HP >= (observed.Character.MaxHP+1)/2 {
			t.Fatal("automatic recovery requires naturally low HP on the outdoor floor")
		}
		route, err := navigator.RouteContext(ctx, observed.Floor, ainavigation.Point{X: observed.X, Y: observed.Y}, ainavigation.Point{X: 265, Y: 560})
		if err != nil || len(route.Points) == 0 {
			t.Fatal("no onward route for automatic recovery:", err)
		}
		target := route.Points[0]
		resumedTarget = &target
		movement.HealthRecovery = &TravelItemRecovery{Healing: healing}
		movement.BattleRecovery = battle
		args, _ := json.Marshal(movementArguments{Floor: observed.Floor, X: target.X, Y: target.Y})
		// Route computation can outlive the sampled revision. Observe again
		// after it, without replaying any movement or item-use operation.
		ready, err := backend.Observe(ctx, binding)
		if err != nil || ready.Floor != observed.Floor || ready.X != observed.X || ready.Y != observed.Y {
			t.Fatal("position changed while preparing recovery route:", err)
		}
		if err := movement.Execute(ctx, automation.Action{Skill: "move", ExpectedRevision: ready.Revision, Arguments: args}); err != nil {
			t.Fatal("automatic recovery and resumed movement:", err)
		}
		arrived, err := backend.Observe(ctx, binding)
		if err != nil || arrived.Floor != observed.Floor || arrived.X != target.X || arrived.Y != target.Y {
			t.Fatal("resumed movement lacks authoritative arrival:", err)
		}
	} else {
		// Do not replay the complete skill: a failure can occur after ID was sent.
		if err := healing.Execute(ctx, automation.Action{Skill: "item.heal", ExpectedRevision: observed.Revision, Arguments: json.RawMessage(`{"item":"small-meat"}`)}); err != nil {
			t.Fatal("native meat use:", err)
		}
	}
	afterUse, err := refreshInventoryIdentity(ctx, NewNPCSkill(backend, nil))
	if err != nil {
		t.Fatal(err)
	}
	if healingItemCount(afterUse, 2344) != quantity-1 || afterUse.Player.HP <= beforeUse.Player.HP || afterUse.Player.HP > afterUse.Player.MaxHP {
		t.Fatal("meat use requires both consumption and HP gain")
	}
	t.Logf("native meat consumed: HP %d -> %d/%d, remaining=%d", beforeUse.Player.HP, afterUse.Player.HP, afterUse.Player.MaxHP, quantity-1)
	if err := manager.Revoke(account, slot); err != nil || manager.Allowed(account, slot) {
		t.Fatal("revoke funding after purchase:", err)
	}
	// Re-enter the saved character with the grant revoked. This obtains fresh
	// inventory and gold from the server, rather than relying on a pre-purchase
	// gold field retained in the first session's snapshot.
	f.Lease.Close()
	reloginCtx, reloginCancel := context.WithTimeout(ctx, 20*time.Second)
	defer reloginCancel()
	for {
		lease, err := f.Provider.Open(reloginCtx, f.Profile)
		if err == nil {
			if lease.Binding.CharacterID != f.Created.Binding.CharacterID || lease.Binding.CharacterName != f.Created.Binding.CharacterName {
				lease.Close()
				t.Fatal("relogin changed the isolated character binding")
			}
			f.Lease = lease
			backend.Session = lease.Session
			backend.OwnStateRefresh = &OwnStateRefresher{}
			break
		}
		select {
		case <-reloginCtx.Done():
			t.Fatal("relogin after purchase:", err)
		case <-time.After(250 * time.Millisecond):
		}
	}
	relogged, err := movementCrossMapLiveWaitObservation(reloginCtx, backend, binding, func(o aimcp.Observation) bool {
		return o.Connected && o.Ready && o.Phase == string(aigame.PhaseWorld) && o.Flags["inventory:known"] && o.Inventory["item:2344"] == quantity-1 && o.Character.HP == int(afterUse.Player.HP)
	})
	if err != nil || relogged.Gold != initial.Gold || relogged.UnlimitedFunds {
		t.Fatalf("purchase did not persist after revocation/relogin: gold=%d unlimited=%v err=%v", relogged.Gold, relogged.UnlimitedFunds, err)
	}
	evidence := map[string]any{"test": t.Name(), "status": "passed", "initial_gold": initial.Gold, "final_gold": final.Gold,
		"cost": cost, "item_id": 2344, "quantity": quantity, "ledger_charges": charges, "source_hashes": hashes, "qa_binary_sha256": qaBinaryHash,
		"stock_catalog_verified": true, "stock_skill_verified": true, "healing_catalog_verified": true, "itemset_sha256": healingItems["small-meat"].ItemsetSHA256, "relogged_hp": relogged.Character.HP, "relogged_item_count": relogged.Inventory["item:2344"], "natural_injury_verified": true, "hp_before_use": beforeUse.Player.HP, "hp_after_use": afterUse.Player.HP, "remaining_items": quantity - 1, "use_relogin_verified": true, "funded_purchase_verified": true, "purchase_relogin_verified": true, "funding_revoked": true, "model_invoked": false}
	evidenceName := "portable-healing-live-evidence.json"
	if automaticRecovery {
		evidence["automatic_movement_recovery_verified"] = true
		evidence["resumed_target"] = resumedTarget
		evidenceName = "movement-item-recovery-live-evidence.json"
	}
	if levelingRecovery {
		evidence["automatic_leveling_recovery_verified"] = true
		evidence["full_leveling_target_verified"] = false
		evidenceName = "leveling-item-recovery-live-evidence.json"
	}
	raw, _ = json.MarshalIndent(evidence, "", "  ")
	if err := os.WriteFile(filepath.Join(f.RepoRoot, "build", "ai", evidenceName), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
