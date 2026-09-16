package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

type travelPetHealer struct {
	heal func(aigame.PetSnapshot) error
}

func (h travelPetHealer) Heal(context.Context) error {
	return errors.New("unexpected character healing")
}
func (h travelPetHealer) HealPet(_ context.Context, p aigame.PetSnapshot) error { return h.heal(p) }

func TestTaskTravelHealsSelectedPetsBeforeWalking(t *testing.T) {
	for _, role := range []string{"riding", "battle"} {
		for _, uncertain := range []bool{false, true} {
			t.Run(role+map[bool]string{false: "/confirmed", true: "/uncertain"}[uncertain], func(t *testing.T) {
				skill, game := movementFixture(t, true)
				skill.SafeTravel = true
				game.snapshot.Player.MaxHP = game.snapshot.Player.HP
				game.snapshot.Player.Level = 10
				game.snapshot.Player.RidePetKnown, game.snapshot.Player.RidePet = true, -1
				if role == "riding" {
					game.snapshot.Player.RidePet = 3
				} else {
					game.snapshot.Player.BattlePetSlotKnown, game.snapshot.Player.BattlePetSlot = true, 3
				}
				game.snapshot.Pets = []aigame.PetSnapshot{{Slot: 3, StableID: "pet-three", IdentityKnown: true, Name: "Pet", Graphic: 100251, Level: 2, HP: 10, MaxHP: 50}}
				area, leveling := unsafeTravelArea(1, 10, 0, 0, 1, 0, 1, 2)
				skill.Backend.Knowledge = &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{area}, Leveling: []aiknowledge.LevelingArea{leveling}}
				calls := 0
				skill.HealthRecovery = travelPetHealer{heal: func(p aigame.PetSnapshot) error {
					calls++
					if game.moveAttempts != 0 || p.StableID != "pet-three" || p.Slot != 3 {
						t.Fatalf("wrong recovery subject/order: %+v attempts=%d", p, game.moveAttempts)
					}
					game.snapshot.Pets[0].HP = 30
					game.snapshot.Revision++
					if uncertain {
						return errors.New("unknown item use")
					}
					return nil
				}}
				err := skill.Execute(context.Background(), crossMapAction(12, 10, 1, 0))
				if calls != 1 {
					t.Fatalf("healing calls=%d", calls)
				}
				if uncertain {
					if err == nil || game.moveAttempts != 0 {
						t.Fatalf("uncertain recovery resumed: %v attempts=%d", err, game.moveAttempts)
					}
				} else if err != nil || game.moves != 1 {
					t.Fatalf("confirmed recovery: %v moves=%d", err, game.moves)
				}
			})
		}
	}
}

func TestTravelPetReadinessAndRoleProjection(t *testing.T) {
	for _, mode := range []string{"healthy", "injured", "dead", "missing", "unknown-role", "unknown-id", "local-identity", "walking"} {
		t.Run(mode, func(t *testing.T) {
			snapshot := aigame.Snapshot{Player: aigame.PlayerSnapshot{HP: 100, MaxHP: 100, RidePetKnown: true, RidePet: 0}, Pets: []aigame.PetSnapshot{{Slot: 0, StableID: "pet-zero", IdentityKnown: true, HP: 25, MaxHP: 50}}}
			switch mode {
			case "injured":
				snapshot.Pets[0].HP = 24
			case "dead":
				snapshot.Pets[0].HP = 0
			case "missing":
				snapshot.Pets = nil
			case "unknown-role":
				snapshot.Player.RidePetKnown = false
			case "unknown-id":
				snapshot.Pets[0].IdentityKnown = false
				snapshot.Pets[0].HP = 10
			case "local-identity":
				snapshot.Pets[0].IdentityKnown = false
				snapshot.Pets[0].Identity = "local:0:1"
				snapshot.Pets[0].IdentityEpoch = 1
				snapshot.Pets[0].HP = 10
			case "walking":
				snapshot.Player.RidePet = -1
				snapshot.Pets = nil
			}
			o := ProjectObservation(aimcp.Binding{}, snapshot)
			err := (&MovementSkill{SafeTravel: true}).travelHealthError(o, 10, 1, 1)
			if mode == "healthy" || mode == "walking" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if mode == "injured" || mode == "local-identity" {
				var required *travelPetHealingRequired
				if !errors.As(err, &required) || required.slot != 0 || (mode == "injured" && required.id != "pet-zero") || (mode == "local-identity" && (required.id != "" || required.localIdentity != "local:0:1" || required.localEpoch != 1)) {
					t.Fatalf("wrong healing request: %v", err)
				}
			} else if !errors.Is(err, ErrTravelPetStateUnknown) {
				t.Fatalf("unsafe state accepted: %v", err)
			}
		})
	}
}

func TestTravelPetRecoveryRejectsRoleOrIdentityChange(t *testing.T) {
	for _, mode := range []string{"role", "identity"} {
		t.Run(mode, func(t *testing.T) {
			skill, game := movementFixture(t, true)
			game.snapshot.Player.MaxHP = game.snapshot.Player.HP
			game.snapshot.Player.RidePetKnown, game.snapshot.Player.RidePet = true, 3
			game.snapshot.Pets = []aigame.PetSnapshot{{Slot: 3, StableID: "original", IdentityKnown: true, HP: 10, MaxHP: 50}}
			if mode == "role" {
				game.snapshot.Player.RidePet = -1
				game.snapshot.Player.BattlePetSlotKnown, game.snapshot.Player.BattlePetSlot = true, 3
			} else {
				game.snapshot.Pets[0].StableID = "replacement"
			}
			calls := 0
			skill.HealthRecovery = travelPetHealer{heal: func(aigame.PetSnapshot) error { calls++; return nil }}
			err := skill.recoverTravelPet(context.Background(), &travelPetHealingRequired{role: "riding", slot: 3, id: "original"})
			if !errors.Is(err, ErrTravelPetStateUnknown) || calls != 0 {
				t.Fatalf("changed subject healed: err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestPetSelectionDoesNotExportLocalIdentity(t *testing.T) {
	snapshot := aigame.Snapshot{Player: aigame.PlayerSnapshot{RidePetKnown: true, RidePet: 0}, Pets: []aigame.PetSnapshot{{Slot: 0, Identity: "private-local-incarnation", IdentityEpoch: 7}}}
	observed := ProjectObservation(aimcp.Binding{}, snapshot)
	if observed.RidingPet.LocalIdentity != "private-local-incarnation" || observed.RidingPet.Pet == nil || observed.RidingPet.Pet.ID != "" {
		t.Fatalf("incorrect internal continuity: %+v", observed.RidingPet)
	}
	encoded, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-local-incarnation") || strings.Contains(string(encoded), "LocalEpoch") {
		t.Fatalf("local identity exported: %s", encoded)
	}
}
