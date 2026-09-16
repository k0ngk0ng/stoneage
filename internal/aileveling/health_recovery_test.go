package aileveling

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type healthRecoveryFunc func(context.Context) error

func (f healthRecoveryFunc) Heal(ctx context.Context) error { return f(ctx) }

func TestLevelingRecoversBeforeMovingAndPersistsBeforeConsumption(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	game.snapshot.Player.HP = 20
	c, _, store := newCoordinator(t, game, &fakeNavigator{navigation: Navigation{Route: "a", Destination: aigame.Point{Floor: 1, X: 11, Y: 20}, CostKnown: true}})
	receipt := startCharacter(t, c, StartRequest{})
	calls := 0
	c.HealthRecovery = healthRecoveryFunc(func(ctx context.Context) error {
		checkpoint, err := store.Load(ctx, receipt.Handle)
		if err != nil || checkpoint.Phase != "prepared" || len(game.actions) != 0 {
			t.Fatalf("consumption preceded durable fence: %+v %v", checkpoint, err)
		}
		calls++
		game.snapshot.Player.HP += 20
		game.snapshot.Revision++
		return nil
	})
	for i := 0; i < 2; i++ {
		checkpoint, err := c.Tick(context.Background(), receipt.Handle)
		if err != nil || checkpoint.Phase != "ready" || calls != i+1 || len(game.actions) != 0 {
			t.Fatalf("expected one confirmed recovery and no move: %+v %v calls=%d", checkpoint, err, calls)
		}
	}
	if _, err := c.Tick(context.Background(), receipt.Handle); err != nil || calls != 2 || len(game.actions) != 1 || game.actions[0].Kind != aigame.ActionMove {
		t.Fatalf("healthy observation should allow movement: %v actions=%v calls=%d", err, game.actions, calls)
	}
}

func TestLevelingUnconfirmedRecoveryIsNeverRepeated(t *testing.T) {
	for _, kind := range []string{"error_after_use", "no_hp_change", "identity_change", "position_change"} {
		t.Run(kind, func(t *testing.T) {
			game := &fakeGame{snapshot: worldSnapshot()}
			game.snapshot.Player.HP = 20
			c, _, store := newCoordinator(t, game, &fakeNavigator{navigation: Navigation{InArea: true}})
			receipt := startCharacter(t, c, StartRequest{})
			calls := 0
			c.HealthRecovery = healthRecoveryFunc(func(context.Context) error {
				calls++
				if kind != "no_hp_change" {
					game.snapshot.Player.HP = 80
				}
				if kind == "identity_change" {
					game.snapshot.Character = "Other"
				}
				if kind == "position_change" {
					game.snapshot.Position.X++
				}
				if kind == "error_after_use" {
					return errors.New("reply lost after use")
				}
				return nil
			})
			if _, err := c.Tick(context.Background(), receipt.Handle); err == nil {
				t.Fatal("unconfirmed recovery accepted")
			}
			checkpoint, err := store.Load(context.Background(), receipt.Handle)
			if err != nil || checkpoint.Status != automation.Paused || checkpoint.Phase != "prepared" {
				t.Fatalf("lost uncertain state: %+v %v", checkpoint, err)
			}
			if _, err := c.Tick(context.Background(), receipt.Handle); err != nil || calls != 1 || len(game.actions) != 0 {
				t.Fatalf("replayed recovery: calls=%d actions=%v err=%v", calls, game.actions, err)
			}
		})
	}
}

func TestLevelingLowHealthWithoutSuppliesStopsBeforeNavigation(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	game.snapshot.Player.HP = 49
	navigationCalls := 0
	c, _, store := newCoordinator(t, game, NavigatorFunc(func(context.Context, aigame.Snapshot, NavigationRequest) (Navigation, error) {
		navigationCalls++
		return Navigation{InArea: true}, nil
	}))
	receipt := startCharacter(t, c, StartRequest{})
	if _, err := c.Tick(context.Background(), receipt.Handle); !errors.Is(err, ErrNoRecovery) {
		t.Fatalf("expected recovery gap: %v", err)
	}
	checkpoint, _ := store.Load(context.Background(), receipt.Handle)
	if checkpoint.Status != automation.Paused || checkpoint.Phase != "ready" || navigationCalls != 0 {
		t.Fatalf("unsafe continuation: %+v calls=%d", checkpoint, navigationCalls)
	}
}
