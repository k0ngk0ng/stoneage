package aileveling

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type petHealthRecoveryFunc func(context.Context, aigame.PetSnapshot) error

func (f petHealthRecoveryFunc) Heal(context.Context) error {
	return errors.New("unexpected player recovery")
}
func (f petHealthRecoveryFunc) HealPet(ctx context.Context, pet aigame.PetSnapshot) error {
	return f(ctx, pet)
}

func TestLevelingPetRecoveryConfirmsIdentityAndHealthBeforeMoving(t *testing.T) {
	for _, mode := range []string{"confirmed", "fresh_identity", "replaced", "unknown_identity", "no_hp_gain", "other_slot", "error_after_use", "takeover"} {
		t.Run(mode, func(t *testing.T) {
			game := &fakeGame{snapshot: worldSnapshot()}
			pet := aigame.PetSnapshot{Slot: 3, Name: "Pet", Graphic: 100251, Level: 2, HP: 10, MaxHP: 50, IdentityKnown: true, StableID: "pet-identity"}
			if mode == "fresh_identity" {
				pet.IdentityKnown = false
				pet.StableID = ""
			}
			game.snapshot.Pets = []aigame.PetSnapshot{pet}
			game.snapshot.Player.BattlePetSlotKnown = true
			game.snapshot.Player.BattlePetSlot = 3
			moves := 0
			c, gate, store := newCoordinator(t, game, NavigatorFunc(func(context.Context, aigame.Snapshot, NavigationRequest) (Navigation, error) {
				moves++
				return Navigation{InArea: true}, nil
			}))
			r := startCharacter(t, c, StartRequest{})
			calls := 0
			c.HealthRecovery = petHealthRecoveryFunc(func(ctx context.Context, expected aigame.PetSnapshot) error {
				calls++
				checkpoint, err := store.Load(ctx, r.Handle)
				if err != nil || checkpoint.Phase != "prepared" || expected.Slot != 3 || expected.HP != 10 {
					t.Fatalf("missing consumption fence or wrong pet: %+v %+v %v", checkpoint, expected, err)
				}
				current := &game.snapshot.Pets[0]
				current.HP = 35
				current.IdentityKnown, current.StableID = true, "pet-identity"
				switch mode {
				case "replaced":
					current.StableID = "other-pet"
				case "unknown_identity":
					current.IdentityKnown = false
				case "no_hp_gain":
					current.HP = 10
				case "other_slot":
					game.snapshot.Player.BattlePetSlot = 2
				case "error_after_use":
					return errors.New("item reply lost")
				case "takeover":
					_, _, err := gate.Switch(gate.State().Generation, aicontrol.Manual, "test takeover")
					return err
				}
				return nil
			})
			_, err := c.Tick(context.Background(), r.Handle)
			checkpoint, loadErr := store.Load(context.Background(), r.Handle)
			ok := mode == "confirmed" || mode == "fresh_identity"
			if loadErr != nil || (err == nil) != ok || calls != 1 || moves != 0 {
				t.Fatalf("result: %+v err=%v load=%v calls=%d moves=%d", checkpoint, err, loadErr, calls, moves)
			}
			if ok {
				if checkpoint.Phase != "ready" {
					t.Fatalf("not ready after confirmation: %+v", checkpoint)
				}
				if _, err := c.Tick(context.Background(), r.Handle); err != nil || calls != 1 || moves != 1 {
					t.Fatalf("did not resume from fresh observation: %v calls=%d moves=%d", err, calls, moves)
				}
			} else {
				if checkpoint.Phase != "prepared" || checkpoint.Status != automation.Paused {
					t.Fatalf("uncertain pet recovery lost: %+v", checkpoint)
				}
				_, _ = c.Tick(context.Background(), r.Handle)
				if calls != 1 || moves != 0 {
					t.Fatal("uncertain consumption repeated")
				}
			}
		})
	}
}
