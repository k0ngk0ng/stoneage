package playermanager

import (
	"context"
	"errors"
	"github.com/k0ngk0ng/stoneage/internal/aiinitial"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
	"testing"
)

func TestVerifyInitialRejectsPartialAndDuplicatePets(t *testing.T) {
	plan := aiinitial.Resolved{CharacterLevel: 60, Weights: characterbuild.Weights{Vital: 1}, Pets: []aiinitial.Pet{{TemplateID: 42, Level: 30}, {TemplateID: 42, Level: 30}}}
	attrs := func(level int64) []playerdata.Attribute {
		return []playerdata.Attribute{{Key: "lv", Value: level}, {Key: "nexp", Value: 0}, {Key: "hp", Value: 100}}
	}
	actual := playerdata.Snapshot{Attributes: append(attrs(60), playerdata.Attribute{Key: "vi", Value: 19700}, playerdata.Attribute{Key: "str"}, playerdata.Attribute{Key: "tou"}, playerdata.Attribute{Key: "dx"}, playerdata.Attribute{Key: "skup"}), Possessions: []playerdata.Possession{{Kind: "pet", Location: "inventory", Slot: 0, ID: 42, Attributes: attrs(30)}, {Kind: "pet", Location: "inventory", Slot: 1, ID: 42, Attributes: attrs(30)}}}
	if err := VerifyInitial(actual, plan); err != nil {
		t.Fatal(err)
	}
	plan.Mount = true
	if err := VerifyInitial(actual, plan); err == nil {
		t.Fatal("accepted missing riding evidence")
	}
	slot, ridingLevel := int64(0), int64(30)
	actual.RidePetSlot, actual.LearnRide, actual.GraphicID = &slot, &ridingLevel, 100500
	if err := VerifyInitial(actual, plan); err != nil {
		t.Fatal(err)
	}
	slot = -1
	if err := VerifyInitial(actual, plan); err == nil {
		t.Fatal("accepted walking instead of mounted")
	}
	slot = 0
	plan.Mount = false
	actual.Attributes[3].Value -= 100
	actual.Attributes[4].Value += 100
	if err := VerifyInitial(actual, plan); err == nil {
		t.Fatal("accepted wrong attribute distribution")
	}
	actual.Attributes[3].Value += 100
	actual.Attributes[4].Value -= 100
	actual.Possessions[1].Slot = 0
	if err := VerifyInitial(actual, plan); err == nil {
		t.Fatal("accepted duplicate slot")
	}
	actual.Possessions[1].Slot = 1
	actual.Possessions[1].Attributes = attrs(31)
	if err := VerifyInitial(actual, plan); err == nil {
		t.Fatal("accepted wrong pet level")
	}
	actual.Possessions = actual.Possessions[:1]
	if err := VerifyInitial(actual, plan); err == nil {
		t.Fatal("accepted missing pet")
	}
}

func TestInitializeAIDoesNotRetryUnconfirmedMutation(t *testing.T) {
	plan := aiinitial.Resolved{CharacterLevel: 60, Weights: characterbuild.Weights{Vital: 1}}
	manager, archives, game := newOfflineManager(t, testArchive("lv=1"))
	if _, err := manager.InitializeAI(context.Background(), "alice", 0, plan); err == nil {
		t.Fatal("accepted offline initialization")
	}
	if countGameAction(game, "initialize_ai") != 0 || len(archives.writes) != 0 {
		t.Fatal("offline initialization mutated character")
	}
	game.call = func(request map[string]string) (map[string]string, error) {
		if request["action"] == "snapshot" {
			return map[string]string{"account": "alice", "character": "Hero", "character_slot": "0", "sequence": "42", "revision": "0123456789abcdef"}, nil
		}
		if request["action"] != "initialize_ai" || request["expected_sequence"] != "42" || request["expected_revision"] != "0123456789abcdef" || request["payload"] != "1|60|1|0|0|0|0" {
			t.Fatalf("unexpected native request: %+v", request)
		}
		return map[string]string{}, nil // accepted but missing durable-save evidence
	}
	if _, err := manager.InitializeAI(context.Background(), "alice", 0, plan); !errors.Is(err, playerdata.ErrUnavailable) {
		t.Fatalf("unconfirmed mutation: %v", err)
	}
	if countGameAction(game, "initialize_ai") != 1 || len(archives.writes) != 0 {
		t.Fatal("unconfirmed mutation retried or wrote archive directly")
	}
}

func TestValidateInitialPetTemplateLimitBeforeGameMutation(t *testing.T) {
	manager, _, game := newOfflineManager(t, testArchive("lv=1"))
	manager.Catalog = &gamecatalog.Catalog{Pets: []gamecatalog.Pet{{Entry: gamecatalog.Entry{ID: 42, TemplateID: 42, Name: "pet"}, LimitLevel: 80}}}
	plan := aiinitial.Resolved{CharacterLevel: 60, Weights: characterbuild.Weights{Vital: 1}, Pets: []aiinitial.Pet{{TemplateID: 42, Level: 81}}}
	if err := manager.ValidateInitial(context.Background(), plan); err == nil {
		t.Fatal("accepted pet above template cap")
	}
	plan.Pets[0].Level = 80
	if err := manager.ValidateInitial(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if len(game.calls) != 0 {
		t.Fatal("validation submitted game operation")
	}
}

func TestVerifyAIInitialReadsSavedArchiveWithoutMutation(t *testing.T) {
	plan := aiinitial.Resolved{CharacterLevel: 60, Weights: characterbuild.Weights{Vital: 1}}
	manager, archives, game := newOfflineManager(t, testArchive("lv=60", "nexp=0", "hp=100", "vi=19700", "str=0", "tou=0", "dx=0", "skup=0"))
	actual, err := manager.VerifyAIInitial(context.Background(), "alice", 0, plan)
	if err != nil || actual.Online || actual.Name != "Hero" {
		t.Fatalf("offline verification: %+v %v", actual, err)
	}
	if countGameAction(game, "initialize_ai") != 0 || len(archives.writes) != 0 {
		t.Fatal("verification mutated game or archive")
	}
	plan.CharacterLevel = 59
	if _, err := manager.VerifyAIInitial(context.Background(), "alice", 0, plan); err == nil {
		t.Fatal("mismatched archive accepted")
	}
	if countGameAction(game, "initialize_ai") != 0 || len(archives.writes) != 0 {
		t.Fatal("failed verification retried initialization")
	}
}
