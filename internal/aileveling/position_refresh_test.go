package aileveling

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestPendingMoveQueriesServerPositionWithoutReplayingMove(t *testing.T) {
	for _, webGate := range []bool{false, true} {
		name := "raw"
		if webGate {
			name = "web"
		}
		t.Run(name, func(t *testing.T) {
			game := &fakeGame{snapshot: worldSnapshot()}
			destination := aigame.Point{Floor: 1, X: 11, Y: 20}
			navigator := &fakeNavigator{navigation: Navigation{Route: "a", Destination: destination, CostKnown: true}}
			c, gate, _ := newCoordinator(t, game, navigator)
			if webGate {
				c.Game = &gateGame{fakeGame: game, gate: gate}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 4, MaximumSeconds: 30})
			// Model native W: writing it does not echo our coordinates.
			first, err := c.Tick(ctx, receipt.Handle)
			if err != nil || first.Phase != "submitted" {
				t.Fatalf("move: %+v %v", first, err)
			}
			game.onAction = func(action aigame.Action) {
				if action.Kind == aigame.ActionStatus && action.Command == "c" {
					game.snapshot.Position = destination
					game.snapshot.Revision++
				}
			}
			queried, err := c.Tick(ctx, receipt.Handle)
			if err != nil || queried.Phase != "submitted" || !queried.StepStartedAt.Equal(first.StepStartedAt) {
				t.Fatalf("query must not acknowledge movement or reset deadline: %+v %v", queried, err)
			}
			navigator.navigation = Navigation{InArea: true}
			confirmed, err := c.Tick(ctx, receipt.Handle)
			if err != nil || confirmed.Phase != "ready" {
				t.Fatalf("observed position: %+v %v", confirmed, err)
			}
			if len(game.actions) != 2 || game.actions[0].Kind != aigame.ActionMove || game.actions[1].Kind != aigame.ActionStatus || game.actions[1].Command != "c" {
				t.Fatalf("expected one move and one coordinate query: %+v", game.actions)
			}
		})
	}
}

func TestPendingMovePositionQueryFailureKeepsMovementUnconfirmed(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "failed"
		if stale {
			name = "stale"
		}
		t.Run(name, func(t *testing.T) {
			game := &fakeGame{snapshot: worldSnapshot()}
			c, _, _ := newCoordinator(t, game, &fakeNavigator{navigation: Navigation{Route: "a", Destination: aigame.Point{Floor: 1, X: 11, Y: 20}, CostKnown: true}})
			receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 4, MaximumSeconds: 30})
			first, err := c.Tick(context.Background(), receipt.Handle)
			if err != nil {
				t.Fatal(err)
			}
			game.err = errors.New("query connection failed")
			if stale {
				game.err = aigame.ErrStaleRevision
			}
			checkpoint, err := c.Tick(context.Background(), receipt.Handle)
			if checkpoint.Phase != "submitted" || !checkpoint.StepStartedAt.Equal(first.StepStartedAt) {
				t.Fatalf("lost pending movement: %+v", checkpoint)
			}
			if stale {
				if err != nil || checkpoint.Status != automation.Running {
					t.Fatalf("stale query must allow fresh observation: %+v %v", checkpoint, err)
				}
				game.err = nil
				if _, err := c.Tick(context.Background(), receipt.Handle); err != nil {
					t.Fatal(err)
				}
			} else if err == nil || checkpoint.Status != automation.Paused {
				t.Fatalf("query failure must pause: %+v %v", checkpoint, err)
			}
			moves := 0
			for _, action := range game.actions {
				if action.Kind == aigame.ActionMove {
					moves++
				}
			}
			if moves != 1 {
				t.Fatalf("movement was replayed: %+v", game.actions)
			}
		})
	}
}
