package aiservice

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// Exercises the production builder and background coordinator against the
// isolated QA server. Hometown 1 has a source-verified approach to area 28;
// no scripted navigation, position injection or character-stat changes.
func TestLiveLevelingFreshAI(t *testing.T) {
	if os.Getenv("STONEAGE_LEVELING_LIVE_TEST") != "1" {
		t.Skip("set STONEAGE_LEVELING_LIVE_TEST=1 for real local QA leveling")
	}
	runLiveLevelingFreshAI(t, 2, "leveling-live-evidence.json", false)
}

func TestLiveLevelingContinuousAI(t *testing.T) {
	if os.Getenv("STONEAGE_CONTINUOUS_LEVELING_LIVE_TEST") != "1" {
		t.Skip("set STONEAGE_CONTINUOUS_LEVELING_LIVE_TEST=1 for real local QA continuous leveling")
	}
	runLiveLevelingFreshAI(t, 3, "leveling-continuous-evidence.json", false)
}

func TestLiveLevelingSuppliedAI(t *testing.T) {
	if os.Getenv("STONEAGE_SUPPLIED_LEVELING_LIVE_TEST") != "1" {
		t.Skip("explicit local QA supplied leveling opt-in required")
	}
	runLiveLevelingFreshAI(t, 5, "leveling-supplied-evidence.json", true)
}

func runLiveLevelingFreshAI(t *testing.T, targetLevel int, evidenceName string, supplied bool) {
	t.Helper()
	maximumSeconds := 180
	if supplied {
		// Real low-level experience progression exceeded the original four
		// minutes. Keep target 5 and recovery requirements; observe long enough
		// to exercise production restocking without modifying character stats.
		maximumSeconds = 1200
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(maximumSeconds)*time.Second+2*time.Minute)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "docker", "exec", "stoneage-player-qa-gmsv-1", "sha256sum", "/proc/1/exe").Output()
	fields := strings.Fields(string(raw))
	if err != nil || len(fields) != 2 || fields[0] != "51f57aa068a72148078d8cf0118fde3f9aafbfa62a0323fe936d29be9b0f2c19" {
		t.Fatal("leveling requires the reviewed local QA server binary")
	}
	f := movementCrossMapLiveProvisionFreshAIHometown(t, ctx, "leveling", "aiservice-leveling-live-test", 1)
	data := filepath.Join(f.RepoRoot, "runtime", "legacy-server", "gmsv", "data")
	knowledge, err := aiknowledge.LoadDataDir(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	tiles, err := ainavigation.LoadDataDir(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := movementCrossMapLiveVerifyEffectiveData(ctx, f.RepoRoot, tiles); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{2006, 2000, 2004, 100} {
		floor, ok := tiles.Floor(id)
		if !ok {
			t.Fatalf("required hometown 1 map %d missing", id)
		}
		if _, err := movementCrossMapLiveCompareFile(data, filepath.Join(f.RepoRoot, "build", "player-integration", "game", "gmsv", "data"), filepath.Join("map", floor.Source)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"enemybase.txt", "enemy.txt", "group.txt", "encount.txt", "petskill.txt"} {
		if _, err := movementCrossMapLiveCompareFile(data, filepath.Join(f.RepoRoot, "build", "player-integration", "game", "gmsv", "data"), name); err != nil {
			t.Fatal(err)
		}
	}
	plans, err := automation.OpenStore(filepath.Join(f.Root, "leveling.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer plans.Close()
	receipts, err := OpenReceiptStore(filepath.Join(f.Root, "receipts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer receipts.Close()
	gate := aicontrol.New()
	defer gate.Close()
	state, lease, err := gate.Switch(gate.State().Generation, aicontrol.Agent, "real QA leveling")
	if err != nil {
		t.Fatal(err)
	}
	binding := aimcp.Binding{AccountID: f.Created.Account.Username, CharacterID: f.Created.Binding.CharacterID, CharacterName: f.Created.Binding.CharacterName, Generation: state.Generation}
	config := GameplayConfig{Plans: plans, Tiles: tiles}
	var funding func(context.Context) (bool, error)
	if supplied {
		funding = configureSuppliedLeveling(t, ctx, f, knowledge, &config)
	}
	builder, err := NewGameplayBuilder(config)
	if err != nil {
		t.Fatal(err)
	}
	protocolTrace := &containerLevelingSession{GameSession: f.Lease.Session, actions: map[aigame.ActionKind]int{}}
	backend, err := builder(ctx, BackendInput{Binding: binding, Gate: gate, Session: protocolTrace, Knowledge: knowledge, Receipts: receipts, Lease: lease, Funding: funding})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.(*GameBackend).Close()
	if supplied {
		prepareSuppliedLeveling(t, ctx, backend.(*GameBackend), config.StockItems)
	}
	parameters := map[string]json.RawMessage{"area_id": json.RawMessage("28")}
	if supplied {
		parameters["supply_item"] = json.RawMessage(`"hometown-1-small-meat"`)
		parameters["supply_target_count"] = json.RawMessage(`10`)
	}
	receipt, err := backend.StartLeveling(ctx, binding, aimcp.LevelingRequest{TargetKind: "character", TargetLevel: targetLevel, TargetPolicy: "all", MaximumSeconds: maximumSeconds, MaximumDeaths: 0, Parameters: parameters})
	if err != nil {
		t.Fatal(err)
	}
	type sample struct {
		Position             aigame.Point      `json:"position"`
		Level                int32             `json:"level"`
		EXP                  int32             `json:"exp"`
		MaxEXP               int32             `json:"max_exp"`
		HP                   int32             `json:"hp"`
		MaxHP                int32             `json:"max_hp"`
		Items                int               `json:"healing_items"`
		ItemsKnown           bool              `json:"items_known"`
		ItemSubmissions      int               `json:"item_submissions"`
		StockSubmissions     int               `json:"stock_submissions"`
		Battle               bool              `json:"battle"`
		Turn                 int               `json:"turn"`
		Phase                string            `json:"phase"`
		Status               automation.Status `json:"status"`
		Reason               string            `json:"reason,omitempty"`
		PetAttackSubmissions int               `json:"pet_attack_submissions"`
		BattlePetHP          int32             `json:"battle_pet_hp"`
		BattlePetMaxHP       int32             `json:"battle_pet_max_hp"`
		BattlePetEXP         int32             `json:"battle_pet_exp"`
		BattlePetMaxEXP      int32             `json:"battle_pet_max_exp"`
	}
	samples := []sample{}
	petAttacksAtLevelTwo := -1
	defer func() {
		protocolTrace.mu.Lock()
		commands := append([]string(nil), protocolTrace.battleCommands...)
		itemUses := append([][2]int32(nil), protocolTrace.itemUses...)
		itemTemplates := append([]int32(nil), protocolTrace.itemTemplates...)
		stockPurchases := append([]string(nil), protocolTrace.stockPurchases...)
		protocolTrace.mu.Unlock()
		last, _ := protocolTrace.Observe(ctx)
		encoded, marshalErr := json.MarshalIndent(map[string]any{"test": t.Name(), "passed": !t.Failed(), "target_level": targetLevel, "maximum_seconds": maximumSeconds, "supplied": supplied, "knowledge": knowledge.Fingerprint(), "initial_position": f.Initial.Position, "initial_level": f.Initial.Player.Level, "samples": samples, "battle_commands": commands, "stock_purchases": stockPurchases, "item_uses_slot_target": itemUses, "item_use_templates": itemTemplates, "selected_pet_slot": last.Player.BattlePetSlot, "selected_pet_known": last.Player.BattlePetSlotKnown, "pets": last.Pets, "inventory": last.AI.Items, "inventory_known": last.AI.ItemsKnown, "battle_roster": last.Battle.Participants}, "", "  ")
		if marshalErr != nil {
			t.Error(marshalErr)
			return
		}
		path := filepath.Join(f.RepoRoot, "build", "ai", evidenceName)
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Error(err)
		}
		t.Logf("credential-free leveling evidence: %s", path)
	}()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("real leveling timed out")
		case <-ticker.C:
		}
		checkpoint, err := plans.Load(ctx, receipt.Handle)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := f.Lease.Session.Observe(ctx)
		if err != nil {
			t.Fatal(err)
		}
		current := sample{Position: snapshot.Position, Level: snapshot.Player.Level, HP: snapshot.Player.HP, Battle: snapshot.Battle.Active, Turn: int(snapshot.Battle.Turn), Phase: checkpoint.Phase, Status: checkpoint.Status, Reason: checkpoint.Reason}
		protocolTrace.mu.Lock()
		current.PetAttackSubmissions = protocolTrace.petAttacks
		current.ItemSubmissions = protocolTrace.actions[aigame.ActionItem]
		current.StockSubmissions = len(protocolTrace.stockPurchases)
		protocolTrace.mu.Unlock()
		current.MaxHP = snapshot.Player.MaxHP
		current.EXP, current.MaxEXP = snapshot.Player.EXP, snapshot.Player.MaxEXP
		current.ItemsKnown = snapshot.AI.Received && snapshot.AI.ItemsKnown
		current.Items = healingItemCount(snapshot, 2344)
		if snapshot.Player.BattlePetSlotKnown {
			for _, pet := range snapshot.Pets {
				if pet.Slot == snapshot.Player.BattlePetSlot {
					current.BattlePetHP, current.BattlePetMaxHP = pet.HP, pet.MaxHP
					current.BattlePetEXP, current.BattlePetMaxEXP = pet.EXP, pet.MaxEXP
				}
			}
		}
		if snapshot.Player.Level >= 2 && petAttacksAtLevelTwo < 0 {
			petAttacksAtLevelTwo = current.PetAttackSubmissions
		}
		if len(samples) == 0 || samples[len(samples)-1] != current {
			samples = append(samples, current)
			t.Logf("leveling %+v", current)
		}
		if checkpoint.Status == automation.Completed {
			if int(snapshot.Player.Level) < targetLevel || snapshot.Battle.Active || checkpoint.Confirmation == nil {
				t.Fatal("completion lacks real level-up/world evidence")
			}
			if supplied && current.ItemSubmissions == 0 {
				t.Fatal("leveling completed without exercising recovery; no recovery proof")
			}
			if supplied && current.StockSubmissions < 2 {
				t.Fatal("leveling completed without automatic restocking after initial purchase")
			}
			if supplied {
				visitedField, restockIndex := false, -1
				for i, observed := range samples {
					if observed.StockSubmissions < 2 && observed.Position.Floor == 100 {
						visitedField = true
					}
					if visitedField && observed.StockSubmissions >= 2 && observed.Position.Floor == 2004 &&
						!observed.Battle && observed.ItemsKnown && observed.Items == 10 {
						restockIndex = i
						break
					}
				}
				if restockIndex < 0 {
					t.Fatal("no observed field-to-shop restock with confirmed inventory")
				}
				continued := false
				for _, observed := range samples[restockIndex+1:] {
					if observed.Position.Floor == 100 && observed.Level > samples[restockIndex].Level &&
						observed.PetAttackSubmissions > samples[restockIndex].PetAttackSubmissions {
						continued = true
						break
					}
				}
				if !continued {
					t.Fatal("no observed combat and level gain after returning from restock")
				}
			}
			if targetLevel > 2 && (petAttacksAtLevelTwo < 0 || current.PetAttackSubmissions <= petAttacksAtLevelTwo) {
				t.Fatal("continuous leveling lacks pet attack submissions after reaching level 2")
			}
			return
		}
		if checkpoint.Status != automation.Running {
			t.Fatalf("real leveling stopped: %+v", current)
		}
	}
}
