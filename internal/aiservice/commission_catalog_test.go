package aiservice

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestCommissionCatalogCompilesWithSharedGameplay(t *testing.T) {
	data := filepath.Join("..", "..", "runtime", "legacy-server", "gmsv", "data")
	if _, err := os.Stat(filepath.Join(data, "enemybase.txt")); os.IsNotExist(err) {
		t.Skip("native data absent")
	}
	ctx := context.Background()
	k, err := aiknowledge.LoadDataDir(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join("..", "..", "ai", "catalogs")
	npcs, err := LoadNPCRegistry(filepath.Join(base, "quests-2.5.json"), k.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	healing, err := LoadHealingItemsForData(filepath.Join(base, "healing-items-2.5.json"), k.Fingerprint(), filepath.Join(data, "itemset.txt"))
	if err != nil {
		t.Fatal(err)
	}
	stock, err := LoadStockItemsForData(filepath.Join(base, "quest-stock-items-2.5.json"), k.Fingerprint(), npcs, healing, filepath.Join(data, "itemset.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := healing["marinas-pet-voucher-a"]; ok {
		t.Fatal("quest voucher became a healing item")
	}
	if stock["marinas-pet-commission-a"].TemplateID != 20031 {
		t.Fatal("missing voucher offer")
	}
	tiles, err := ainavigation.LoadDataDir(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	store, err := automation.OpenStore(filepath.Join(t.TempDir(), "plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	b, f := gameFixture(t)
	builder, err := NewGameplayBuilder(GameplayConfig{Plans: store, Tiles: tiles, NPCs: npcs, HealingItems: healing, StockItems: stock})
	if err != nil {
		t.Fatal(err)
	}
	built, err := builder(ctx, BackendInput{Binding: b.Binding, Gate: b.Gate, Session: f, Knowledge: k, Receipts: b.Receipts, Lease: ctx})
	if err != nil {
		t.Fatal(err)
	}
	backend := built.(*GameBackend)
	defer backend.Close()
	quests := backend.Tasks.(*GameTasks).Quests
	plan, err := quests.Builder.Task(ctx, aimcp.TaskRequest{TaskID: "marinas-pet-commission-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 7 || plan.Budget.MaximumSpend != 146 {
		t.Fatalf("plan=%+v", plan)
	}
	for _, step := range plan.Steps {
		a := step.Action
		a.MaximumCost = step.MaximumCost
		if err := quests.Engine.Game.(*AutomationGame).ValidateSkill(ctx, a); err != nil {
			t.Fatalf("%s: %v", step.ID, err)
		}
	}
	// Same compiled guards are consumed by Web preview and by CLI execution.
	observed := automation.Observation{CharacterID: b.Binding.CharacterID, Connected: true, Ready: true, Character: automation.Entity{HP: 35, Level: 1}, Gold: 10000, Flags: map[string]bool{"pets:known": true, "inventory:known": true, "party:solo": true, "gold_limit:known": true}, OwnProgress: map[string]int{"backpack_used_slots": 0, "gold_limit": 1000000}, Inventory: map[string]int{}, Pets: []automation.Entity{{ID: "original", SpeciesID: 112, SpeciesIDKnown: true}}}
	pre, err := automation.EvaluatePreflight(ctx, plan, observed, nil)
	if err != nil || !pre.Ready {
		t.Fatalf("preflight=%+v err=%v", pre, err)
	}
	for _, edit := range []func(*automation.Observation){
		func(o *automation.Observation) { o.Gold = 999500 },
		func(o *automation.Observation) { o.Pets = append(o.Pets, automation.Entity{ID: "second"}) },
		func(o *automation.Observation) {
			o.Pets = []automation.Entity{{ID: "original", SpeciesID: 1, SpeciesIDKnown: true}}
		},
		func(o *automation.Observation) { o.Inventory = map[string]int{"item:20032": 1} },
	} {
		o := observed
		edit(&o)
		pre, err = automation.EvaluatePreflight(ctx, plan, o, nil)
		if err != nil || pre.Ready {
			t.Fatalf("unsafe preflight=%+v err=%v", pre, err)
		}
	}
	chain, err := aiplanner.New(k).BuildChain(ctx, "marinas-pet-commission-a", aiplanner.TaskOptions{CharacterID: b.Binding.CharacterID})
	if err != nil {
		t.Fatal(err)
	}
	if err := chain.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPetCapacityProjectionRejectsStaleIdentities(t *testing.T) {
	s := aigame.Snapshot{Connected: true, Phase: aigame.PhaseWorld, Player: aigame.PlayerSnapshot{HasStatus: true}, AI: aigame.AIObservation{Received: true}}
	if !ProjectObservation(aimcp.Binding{}, s).Flags["pets:known"] {
		t.Fatal("complete empty observation rejected")
	}
	p := aigame.PetSnapshot{Slot: 0, StableID: "owned", IdentityKnown: true}
	s.Pets = []aigame.PetSnapshot{p}
	if ProjectObservation(aimcp.Binding{}, s).Flags["pets:known"] {
		t.Fatal("stale empty AI observation accepted")
	}
	s.AI.Pets = []aigame.PetSnapshot{p}
	if !ProjectObservation(aimcp.Binding{}, s).Flags["pets:known"] {
		t.Fatal("confirmed identity rejected")
	}
	s.Pets[0].IdentityKnown = false
	if ProjectObservation(aimcp.Binding{}, s).Flags["pets:known"] {
		t.Fatal("unconfirmed replacement accepted")
	}
}
