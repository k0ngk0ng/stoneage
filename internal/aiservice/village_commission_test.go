package aiservice

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
)

// Source contracts are checked independently from completion: a matching
// source never enables an unverified task outside this test's compiler copy.
func TestVillageCommissionContractsMatchNativeBranches(t *testing.T) {
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
	nav, err := ainavigation.LoadDataDir(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		slug           string
		floor, voucher int
		predicates     string
		budget, reward int64
	}{
		{"samgil", 1000, 20001, "PET>0-113*2,PET>0-114*2", 136, 400},
		{"marinas", 2000, 20031, "PET>0-1*1,PET>0-3*1,PET>0-212*1,PET>0-213*1", 146, 500},
		{"jaja", 3000, 20061, "PET>0-31*2,PET>0-32*2", 136, 400},
		{"karutana", 4000, 20091, "PET>0-101*2,PET>0-104*2", 136, 400},
	} {
		t.Run(v.slug, func(t *testing.T) {
			id := v.slug + "-pet-commission-a"
			task, ok := k.FindTask(id)
			if !ok || !task.EvidenceVerified || !task.PreparationReviewed {
				t.Fatal("unreviewed static inputs", task)
			}
			copyKnowledge := *k
			copyKnowledge.TaskDefinitions = append([]aiknowledge.TaskDefinition(nil), k.TaskDefinitions...)
			for i := range copyKnowledge.TaskDefinitions {
				if copyKnowledge.TaskDefinitions[i].ID == id {
					copyKnowledge.TaskDefinitions[i].Status = aiknowledge.TaskVerified
					copyKnowledge.TaskDefinitions[i].ExecutionVerified = true
				}
			}
			plan, err := aiplanner.New(&copyKnowledge).BuildTask(ctx, id, aiplanner.TaskOptions{CharacterID: "fixture:0"})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Steps) != 7 || plan.Budget.MaximumSpend != v.budget {
				t.Fatal(plan)
			}
			offer := stock[id]
			if offer.TemplateID != int32(v.voucher) || offer.NPC.Floor != v.floor+9 || offer.UnitPrice != v.budget-96 {
				t.Fatal(offer)
			}
			if !nav.Walkable(offer.NPC.Floor, offer.X, offer.Y) {
				t.Fatal("voucher approach blocked")
			}
			var supply struct {
				Item string `json:"item"`
			}
			if err := json.Unmarshal(plan.Steps[1].Action.Arguments, &supply); err != nil {
				t.Fatal(err)
			}
			meat := stock[supply.Item]
			if meat.TemplateID != 2344 || meat.NPC.Floor != v.floor+4 || !nav.Walkable(meat.NPC.Floor, meat.X, meat.Y) {
				t.Fatal("invalid local supplies", meat)
			}
			// A walkable tile behind a shop counter is not an approach tile:
			// prove it is reachable from a real entrance as well.
			accessible := false
			for _, warp := range k.Warps {
				if warp.To.Floor == meat.NPC.Floor {
					_, err := nav.RouteContext(ctx, meat.NPC.Floor, ainavigation.Point{X: warp.To.X, Y: warp.To.Y}, ainavigation.Point{X: meat.X, Y: meat.Y})
					accessible = accessible || err == nil
				}
			}
			if !accessible {
				t.Fatal("supplies approach isolated from shop entrances", meat)
			}
			manager, ok := npcs.Lookup(v.slug + "-commission-manager")
			if !ok {
				t.Fatal("missing manager")
			}
			var args npcWindowArguments
			if err := json.Unmarshal(plan.Steps[6].Action.Arguments, &args); err != nil {
				t.Fatal(err)
			}
			window, err := resolveNPCWindow(manager, args.WindowSequence)
			if err != nil {
				t.Fatal(err)
			}
			choice, err := resolveNPCChoice(window.Choices, args.Choice)
			if err != nil {
				t.Fatal(err)
			}
			delivery := choice.PetDelivery
			if delivery == nil || strings.Join(delivery.Predicates, ",") != v.predicates || delivery.RequiredItems[int32(v.voucher)] != 1 || len(delivery.ForbiddenItems) != 11 {
				t.Fatal("wrong protected delivery", delivery)
			}
			matched := false
			for _, file := range k.NPC.Files {
				if file.Path != fmt.Sprintf("npc/extra/event/M_%d", v.floor) || file.Events == nil {
					continue
				}
				for _, rule := range file.Events.Rules {
					item, _ := rule.Field("DelItem")
					if item != fmt.Sprintf("%d*1", v.voucher) {
						continue
					}
					pets, _ := rule.Field("DelPet")
					gold, _ := rule.Field("GetStone")
					thanks, _ := rule.Field("ThanksMsg")
					if pets != v.predicates || gold != fmt.Sprint(v.reward) || delivery.GoldReward != v.reward || delivery.AcceptText != thanks {
						t.Fatal("contract differs from native branch", rule)
					}
					matched = true
				}
			}
			if !matched {
				t.Fatal("native branch missing")
			}
			a := plan.Steps[2].Action
			argsCollect, err := collectionArguments(a)
			if err != nil {
				t.Fatal(err)
			}
			total := 0
			for _, target := range argsCollect.Targets {
				total += target.Count
			}
			if total != 4 {
				t.Fatal("collection amount differs from delivery")
			}
			// Captured from the native 2.5 loaders, not reconstructed from
			// client-side missing groups. Changed data must be reverified.
			const policy = "missing-group-abort-v1:3ba13cb10133bd88:0,1,2,3,4,17,24,25,29,30,35,49,53,60,110,113,137,140,160,177,178,185,187,208,210,211,230,231,232,233,249,287,288,289,293,302,312,330,332,333,334,350,353,354,357,397,431,465,468,533,534,571,572,573,574,594,604,664"
			if !strings.HasPrefix(policy, k.EncounterPolicyToken()+":") {
				t.Fatal("native encounter fixture differs from loaded tables")
			}
			level := int32(0)
			for _, c := range task.Preconditions {
				if c.Kind == "character_level" {
					level = int32(c.Value)
				}
			}
			snapshot := aigame.Snapshot{Connected: true, Phase: aigame.PhaseWorld,
				Position: aigame.Point{Floor: int32(meat.NPC.Floor), X: int32(meat.X), Y: int32(meat.Y)},
				Player:   aigame.PlayerSnapshot{HasStatus: true, Level: level, HP: 100, MaxHP: 100},
				AI:       aigame.AIObservation{Received: true, EncounterPolicy: policy}}
			navigator := &LevelingNavigator{Knowledge: k, Tiles: nav}
			for _, target := range argsCollect.Targets {
				if _, err := navigator.NextForPets(ctx, snapshot, []PetCollectionTarget{target}); err != nil {
					t.Fatalf("declared minimum level %d cannot reach species %d: %v", level, target.SpeciesID, err)
				}
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
