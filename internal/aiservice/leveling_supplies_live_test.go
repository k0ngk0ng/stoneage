package aiservice

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aifunding"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func configureSuppliedLeveling(t *testing.T, ctx context.Context, f *movementCrossMapLiveFreshAI, knowledge *aiknowledge.Knowledge, config *GameplayConfig) func(context.Context) (bool, error) {
	t.Helper()
	data := filepath.Join(f.RepoRoot, "runtime", "legacy-server", "gmsv", "data")
	effective := movementCrossMapLiveEffectiveRoot(f.RepoRoot)
	for _, name := range []string{"npc/genout/shop_m.create", "npc/genout/npcgen.template", "npc/genout/ss_2004_17_13", "itemset.txt", "map/mapwarp.txt"} {
		if _, err := movementCrossMapLiveCompareFile(data, effective, name); err != nil {
			t.Fatal(err)
		}
	}
	catalog := filepath.Join(f.RepoRoot, "ai", "catalogs")
	var err error
	config.HealingItems, err = LoadHealingItemsForData(filepath.Join(catalog, "healing-items-2.5.json"), knowledge.Fingerprint(), filepath.Join(data, "itemset.txt"))
	if err != nil {
		t.Fatal(err)
	}
	config.NPCs, err = LoadNPCRegistry(filepath.Join(catalog, "shops-2.5.json"), knowledge.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	config.StockItems, err = LoadStockItems(filepath.Join(catalog, "stock-items-2.5.json"), knowledge.Fingerprint(), config.NPCs, config.HealingItems)
	if err != nil {
		t.Fatal(err)
	}
	offer, ok := config.StockItems["hometown-1-small-meat"]
	if !ok || offer.NPC.Floor != 2004 || offer.NPC.X != 17 || offer.NPC.Y != 13 || offer.TemplateID != 2344 || offer.UnitPrice != 12 || offer.ShopIndex != 1 {
		t.Fatal("unexpected hometown 1 shop contract")
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
		path, err := manager.PolicyPath(account, slot)
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) || manager.Allowed(account, slot) {
			t.Error("funding policy not removed")
		}
	})
	if err := manager.Provision(account, slot, 0); err != nil {
		t.Fatal(err)
	}
	return func(context.Context) (bool, error) { return manager.Allowed(account, slot), nil }
}

func prepareSuppliedLeveling(t *testing.T, ctx context.Context, backend *GameBackend, offers map[string]StockContract) {
	t.Helper()
	game := backend.Tasks.(*GameTasks).Quests.Engine.Game.(*AutomationGame)
	stock := game.Skills.(SkillSet)["item.stock"].(*StockSkill)
	before, err := backend.Observe(ctx, backend.Binding)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(stockArguments{Item: "hometown-1-small-meat", TargetCount: 5, ReserveSlots: 2})
	if err := stock.Execute(ctx, automation.Action{Skill: "item.stock", Arguments: args, ExpectedRevision: before.Revision, MaximumCost: 60}); err != nil {
		t.Fatal("hometown 1 supplies:", err)
	}
	after, err := backend.Observe(ctx, backend.Binding)
	if err != nil || after.Inventory["item:2344"] != 5 || after.Gold != before.Gold || !after.UnlimitedFunds || after.Floor != offers["hometown-1-small-meat"].NPC.Floor {
		t.Fatalf("unconfirmed hometown 1 purchase: count=%d gold=%d/%d unlimited=%v floor=%d err=%v", after.Inventory["item:2344"], before.Gold, after.Gold, after.UnlimitedFunds, after.Floor, err)
	}
	t.Log("hometown 1 shop confirmed five recovery items under the QA funding capability")
}
