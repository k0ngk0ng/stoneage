package aiservice

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aileveling"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestGameplayBuilderWiresAuthoritativeTargetStop(t *testing.T) {
	b, f := gameFixture(t)
	f.snapshot.Player.Level = 5
	store, err := automation.OpenStore(filepath.Join(t.TempDir(), "plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	builder, err := NewGameplayBuilder(GameplayConfig{Plans: store, Tiles: oneStepNavigator{}})
	if err != nil {
		t.Fatal(err)
	}
	state, lease, err := b.Gate.Switch(b.Binding.Generation, aicontrol.Agent, "new lease")
	if err != nil {
		t.Fatal(err)
	}
	b.Binding.Generation = state.Generation
	backend, err := builder(context.Background(), BackendInput{Binding: b.Binding, Gate: b.Gate, Session: f, Knowledge: &aiknowledge.Knowledge{Digest: "data"}, Receipts: b.Receipts, Lease: lease, Funding: func(context.Context) (bool, error) { return true, nil }})
	if err != nil {
		t.Fatal(err)
	}
	game := backend.(*GameBackend)
	defer game.Close()
	automationGame := game.Tasks.(*GameTasks).Quests.Engine.Game.(*AutomationGame)
	if !automationGame.Skills.(SkillSet)["move"].(*MovementSkill).SafeTravel {
		t.Fatal("shared task movement did not enable encounter-aware travel")
	}
	if recovery := game.Tasks.(*GameTasks).Leveling.HealthRecovery; recovery != nil {
		t.Fatalf("leveling recovery wired without healing item contract: %T", recovery)
	}
	r, err := backend.StartLeveling(context.Background(), b.Binding, aimcp.LevelingRequest{TargetKind: "character", TargetLevel: 5, TargetPolicy: "all"})
	if err != nil || r.Status != aimcp.ReceiptConfirmed || len(r.Evidence) == 0 || f.writes != 0 {
		t.Fatalf("target stop not connected: %+v writes=%d err=%v", r, f.writes, err)
	}
	o, err := backend.Observe(context.Background(), b.Binding)
	if err != nil || !o.UnlimitedFunds {
		t.Fatalf("funding projection disconnected: %+v %v", o, err)
	}
	if _, err := backend.StartTask(context.Background(), b.Binding, aimcp.TaskRequest{TaskID: "unverified-quest"}); err == nil {
		t.Fatal("invented a missing quest")
	}
}

func TestGameplayBuilderLoadsRecoveryFromNPCHealerMetadata(t *testing.T) {
	recovery, _ := recoveryFixture(t)
	b, f := gameFixture(t)
	store, err := automation.OpenStore(filepath.Join(t.TempDir(), "healer-plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	spec := verifiedNPCSpec()
	spec.Healer = &HealerRates{PaidFromLevel: 10, HPRateMilli: 500}
	registry, err := NewNPCRegistry([]NPCSpec{spec})
	if err != nil {
		t.Fatal(err)
	}
	builder, err := NewGameplayBuilder(GameplayConfig{Plans: store, Tiles: recovery.Tiles, NPCs: registry})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := builder(context.Background(), BackendInput{Binding: b.Binding, Gate: b.Gate, Session: f, Knowledge: &aiknowledge.Knowledge{Digest: "data"}, Receipts: b.Receipts, Lease: context.Background()})
	if err != nil {
		t.Fatal(err)
	}
	game := backend.(*GameBackend)
	defer game.Close()
	automationGame := game.Tasks.(*GameTasks).Quests.Engine.Game.(*AutomationGame)
	if err := automationGame.ValidateSkill(context.Background(), automation.Action{Skill: "npc.recover", Arguments: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	heal := automationGame.Skills.(SkillSet)["npc.heal"].(*HealerSkill)
	if heal.Contracts["trainer"].HPRateMilli != 500 {
		t.Fatal("catalog rates not wired")
	}
}

func TestGameplayBuilderLoadsHealingItemsAndFreezesCatalog(t *testing.T) {
	const digest = "732242dadf14674ad2dfdfcada6224a86edbde4973a6a9c7df3989fd79f5fbba"
	items, err := LoadHealingItems(filepath.Join("..", "..", "ai", "catalogs", "healing-items-2.5.json"), digest)
	if err != nil {
		t.Fatal(err)
	}
	b, f := gameFixture(t)
	store, err := automation.OpenStore(filepath.Join(t.TempDir(), "healing-plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	builder, err := NewGameplayBuilder(GameplayConfig{Plans: store, Tiles: oneStepNavigator{}, HealingItems: items})
	if err != nil {
		t.Fatal(err)
	}
	// The caller cannot change the authority after builder construction.
	items["small-meat"] = HealingItemContract{TemplateID: 4}
	input := BackendInput{Binding: b.Binding, Gate: b.Gate, Session: f, Knowledge: &aiknowledge.Knowledge{Digest: digest}, Receipts: b.Receipts, Lease: context.Background()}
	value, err := builder(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	game := value.(*GameBackend)
	defer game.Close()
	automationGame := game.Tasks.(*GameTasks).Quests.Engine.Game.(*AutomationGame)
	if err := automationGame.ValidateSkill(context.Background(), automation.Action{Skill: "item.heal", Arguments: json.RawMessage(`{"item":"small-meat"}`)}); err != nil {
		t.Fatal(err)
	}
	healing := automationGame.Skills.(SkillSet)["item.heal"].(*ItemHealingSkill)
	move := automationGame.Skills.(SkillSet)["move"].(*MovementSkill)
	recovery, ok := move.HealthRecovery.(*TravelItemRecovery)
	if !ok || recovery.Healing != healing {
		t.Fatal("travel recovery not connected to loaded catalog")
	}
	leveling := game.Tasks.(*GameTasks).Leveling
	levelingRecovery, ok := leveling.HealthRecovery.(*TravelItemRecovery)
	if !ok || levelingRecovery != recovery || levelingRecovery.Healing != healing {
		t.Fatalf("leveling recovery not connected to travel recovery: %T %p/%p", leveling.HealthRecovery, levelingRecovery, recovery)
	}
	if _, ok := leveling.HealthRecovery.(aileveling.PetHealthRecovery); !ok {
		t.Fatal("reviewed item recovery does not support battle-pet recovery")
	}
	if healing.Contracts["small-meat"].TemplateID != 2344 {
		t.Fatal("catalog changed after builder creation")
	}
	input.Knowledge = &aiknowledge.Knowledge{Digest: "different-data"}
	if value, err := builder(context.Background(), input); err == nil || value != nil {
		t.Fatal("enabled healing under different game data")
	}
}

func TestGameplayBuilderWiresReviewedStock(t *testing.T) {
	stock, session, action := stockFixture(t)
	b := stock.Backend
	store, err := automation.OpenStore(filepath.Join(t.TempDir(), "stock-plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	builder, err := NewGameplayBuilder(GameplayConfig{Plans: store, Tiles: oneStepNavigator{}, StockItems: stock.Contracts})
	if err != nil {
		t.Fatal(err)
	}
	stock.Contracts["meat"] = StockContract{}
	input := BackendInput{Binding: b.Binding, Gate: b.Gate, Session: session, Knowledge: b.Knowledge, Receipts: b.Receipts, Lease: context.Background()}
	value, err := builder(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	game := value.(*GameBackend)
	defer game.Close()
	automationGame := game.Tasks.(*GameTasks).Quests.Engine.Game.(*AutomationGame)
	if err := automationGame.ValidateSkill(context.Background(), action); err != nil {
		t.Fatal(err)
	}
	handler := automationGame.Skills.(SkillSet)["item.stock"].(*StockSkill)
	if handler.Movement == nil || handler.Contracts["meat"].UnitPrice != 12 {
		t.Fatal("stock contract or navigation not wired")
	}
	knowledge, err := game.QueryKnowledge(context.Background(), game.Binding, aimcp.KnowledgeQuery{Kind: "rule", ID: "supply:meat"})
	if err != nil || len(knowledge.Entries) != 1 || !knowledge.Entries[0].Verified {
		t.Fatalf("reviewed supply discovery missing: %+v %v", knowledge, err)
	}
	var offer StockOfferSummary
	if err := json.Unmarshal(knowledge.Entries[0].Data, &offer); err != nil || offer.Alias != "meat" || offer.UnitPrice != 12 || offer.TemplateID != 2344 {
		t.Fatalf("supply discovery differs from execution catalog: %+v %v", offer, err)
	}
	input.Knowledge = &aiknowledge.Knowledge{Digest: "other"}
	if value, err := builder(context.Background(), input); err == nil || value != nil {
		t.Fatal("accepted stock under different data")
	}
}
