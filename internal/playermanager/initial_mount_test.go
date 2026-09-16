package playermanager

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiinitial"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
	"github.com/k0ngk0ng/stoneage/internal/playerbridge"
)

func TestInitialMountPreflightUsesNativeCatalogWithoutMutation(t *testing.T) {
	m, archives, game := newOfflineManager(t, testArchive("lv=1"))
	m.Catalog = &gamecatalog.Catalog{Pets: []gamecatalog.Pet{{Entry: gamecatalog.Entry{TemplateID: 42}, LimitLevel: 80}}}
	plan := aiinitial.Resolved{CharacterLevel: 35, Weights: characterbuild.Weights{Vital: 1}, Pets: []aiinitial.Pet{{TemplateID: 42, Level: 30}}, Mount: true}
	reject := false
	game.call = func(request map[string]string) (map[string]string, error) {
		switch request["action"] {
		case "ai_mount_default":
			return map[string]string{"template_id": "42"}, nil
		case "validate_ai":
			if request["payload"] != "2|35|1|0|0|0|1|1|42:30" || request["value"] != "100000" {
				t.Fatalf("unexpected validation: %+v", request)
			}
			if reject {
				return nil, &playerbridge.Error{Code: "invalid_mount", Message: "private server detail"}
			}
			return map[string]string{"mounted": "1", "mount_graphic_id": "100500"}, nil
		default:
			t.Fatalf("preflight attempted %s", request["action"])
			return nil, nil
		}
	}
	pet, err := m.DefaultAIMount(context.Background())
	if err != nil || pet.TemplateID != 42 || pet.Level != 1 {
		t.Fatalf("default: %+v %v", pet, err)
	}
	if err := m.ValidateInitial(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	reject = true
	if err := m.ValidateInitial(context.Background(), plan); !errors.Is(err, aiinitial.ErrInvalidMount) {
		t.Fatalf("incompatible mount should be classifiable: %v", err)
	}
	if len(archives.writes) != 0 {
		t.Fatal("preflight wrote archive")
	}
}

func TestOnlineSnapshotCarriesReadOnlyRidingState(t *testing.T) {
	fields := map[string]string{"account": "alice", "character": "Hero", "character_slot": "0", "revision": "0123456789abcdef", "character.graphic_id": "100500", "character.ride_pet_slot": "0", "character.learn_ride": "35"}
	snapshot, err := onlineSnapshot(fields, "alice", 0)
	if err != nil || snapshot.RidePetSlot == nil || *snapshot.RidePetSlot != 0 || snapshot.LearnRide == nil || *snapshot.LearnRide != 35 {
		t.Fatalf("riding projection: %+v %v", snapshot, err)
	}
	fields["character.ride_pet_slot"] = "bad"
	if _, err := onlineSnapshot(fields, "alice", 0); err == nil {
		t.Fatal("accepted malformed riding state")
	}
}
