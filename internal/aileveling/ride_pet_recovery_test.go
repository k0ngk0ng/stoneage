package aileveling

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestLevelingRecoversMountedPetBeforeNavigation(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	game.snapshot.Player.RidePet = 3
	game.snapshot.Pets = []aigame.PetSnapshot{{
		Slot: 3, StableID: "ride-pet", IdentityKnown: true, Name: "RidePet", FreeName: "ride",
		Graphic: 100251, Level: 2, HP: 10, MaxHP: 50,
	}}
	navigationCalls := 0
	c, _, store := newCoordinator(t, game, NavigatorFunc(func(context.Context, aigame.Snapshot, NavigationRequest) (Navigation, error) {
		navigationCalls++
		return Navigation{InArea: true}, nil
	}))
	receipt := startCharacter(t, c, StartRequest{})
	var calls int
	c.HealthRecovery = petHealthRecoveryFunc(func(ctx context.Context, expected aigame.PetSnapshot) error {
		calls++
		checkpoint, err := store.Load(ctx, receipt.Handle)
		if err != nil || checkpoint.Phase != "prepared" {
			t.Fatalf("recovery was not fenced: %+v %v", checkpoint, err)
		}
		if expected.Slot != 3 || expected.StableID != "ride-pet" || expected.HP != 10 {
			t.Fatalf("wrong mounted pet: %+v", expected)
		}
		game.snapshot.Pets[0].HP = 35
		return nil
	})
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "ready" || calls != 1 || navigationCalls != 0 || len(game.actions) != 0 {
		t.Fatalf("mounted recovery result: %+v err=%v calls=%d navigation=%d actions=%v", checkpoint, err, calls, navigationCalls, game.actions)
	}
	if _, err := c.Tick(context.Background(), receipt.Handle); err != nil || calls != 1 || navigationCalls != 1 {
		t.Fatalf("healthy mounted pet did not resume: err=%v calls=%d navigation=%d", err, calls, navigationCalls)
	}
}

func TestLevelingPausesWhenMountedPetStateIsUnsafe(t *testing.T) {
	tests := map[string]func(*aigame.Snapshot){
		"unknown": func(snapshot *aigame.Snapshot) {
			snapshot.Player.RidePetKnown = false
		},
		"invalid_slot": func(snapshot *aigame.Snapshot) {
			snapshot.Player.RidePet = 5
		},
		"missing": func(snapshot *aigame.Snapshot) {
			snapshot.Player.RidePet = 3
		},
		"dead": func(snapshot *aigame.Snapshot) {
			snapshot.Player.RidePet = 3
			snapshot.Pets = []aigame.PetSnapshot{{Slot: 3, HP: 0, MaxHP: 50}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			game := &fakeGame{snapshot: worldSnapshot()}
			game.snapshot.Pets = []aigame.PetSnapshot{{Slot: 0, HP: 50, MaxHP: 50}}
			mutate(&game.snapshot)
			navigationCalls := 0
			c, _, store := newCoordinator(t, game, NavigatorFunc(func(context.Context, aigame.Snapshot, NavigationRequest) (Navigation, error) {
				navigationCalls++
				return Navigation{InArea: true}, nil
			}))
			receipt := startCharacter(t, c, StartRequest{})
			if _, err := c.Tick(context.Background(), receipt.Handle); !errors.Is(err, ErrNoRecovery) {
				t.Fatalf("unsafe mounted state error=%v", err)
			}
			checkpoint, err := store.Load(context.Background(), receipt.Handle)
			if err != nil || checkpoint.Status != automation.Paused || navigationCalls != 0 || len(game.actions) != 0 {
				t.Fatalf("unsafe mounted state continued: %+v err=%v navigation=%d actions=%v", checkpoint, err, navigationCalls, game.actions)
			}
		})
	}
}

func TestLevelingRecoveryRejectsMountedOrBattlePetRoleSwitch(t *testing.T) {
	tests := map[string]func(*aigame.Snapshot){
		"mounted": func(snapshot *aigame.Snapshot) {
			snapshot.Player.RidePet = 2
		},
		"battle": func(snapshot *aigame.Snapshot) {
			snapshot.Player.BattlePetSlotKnown = true
			snapshot.Player.BattlePetSlot = 2
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			game := &fakeGame{snapshot: worldSnapshot()}
			game.snapshot.Player.HP = 20
			game.snapshot.Player.BattlePetSlotKnown = true
			game.snapshot.Player.BattlePetSlot = 1
			c, _, store := newCoordinator(t, game, NavigatorFunc(func(context.Context, aigame.Snapshot, NavigationRequest) (Navigation, error) {
				return Navigation{InArea: true}, nil
			}))
			receipt := startCharacter(t, c, StartRequest{})
			c.HealthRecovery = healthRecoveryFunc(func(context.Context) error {
				game.snapshot.Player.HP = 80
				mutate(&game.snapshot)
				return nil
			})
			if _, err := c.Tick(context.Background(), receipt.Handle); !errors.Is(err, ErrNoRecovery) {
				t.Fatalf("role switch accepted: %v", err)
			}
			checkpoint, err := store.Load(context.Background(), receipt.Handle)
			if err != nil || checkpoint.Status != automation.Paused || checkpoint.Phase != "prepared" {
				t.Fatalf("role switch was not retained as uncertain: %+v err=%v", checkpoint, err)
			}
		})
	}
}
