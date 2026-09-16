package aiservice

import (
	"context"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
)

// Model the ordinary EN behavior: the first step succeeds, the pending
// walking queue is cleared, and no battle starts. There is no write failure
// to retry, and a four-step request would never reach its intended endpoint.
type interruptedWalkGame struct {
	*crossMapGame
	requests []string
}

func (g *interruptedWalkGame) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if action.Kind == aigame.ActionMove {
		g.requests = append(g.requests, action.Route)
		if len(action.Route) > 1 {
			action.Route = action.Route[:1]
		}
	}
	return g.crossMapGame.ExecuteExpected(ctx, revision, action)
}

func TestMovementConfirmsEveryEncounterTileBeforeContinuing(t *testing.T) {
	for _, path := range []string{"same-floor", "after-warp", "before-warp"} {
		t.Run(path, func(t *testing.T) {
			backend, fake := gameFixture(t)
			fake.snapshot.Position.Floor = 10
			fake.snapshot.Player.MaxHP = fake.snapshot.Player.HP
			game := &interruptedWalkGame{crossMapGame: &crossMapGame{snapshot: fake.snapshot}}
			backend.Session = game
			backend.Knowledge = &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{{
				Floor: 10, Bounds: aiknowledge.Rectangle{X: 0, Y: 0, X2: 5, Y2: 0},
				EncounterProbability: aiknowledge.Range{Min: 1, Max: 5},
				// Deliberately no verified enemy/group data. Queued movement
				// can still be interrupted when an encounter is rejected.
			}}}
			skill := &MovementSkill{Backend: backend, Navigator: crossMapNavigator{}}
			ctx := context.Background()
			o, err := backend.Observe(ctx, backend.Binding)
			if err != nil {
				t.Fatal(err)
			}
			switch path {
			case "same-floor":
				err = skill.Execute(ctx, crossMapAction(o.Revision, 10, 4, 0))
			case "after-warp":
				_, err = skill.moveWithinFloor(ctx, o, 10, ainavigation.Point{X: 4})
			case "before-warp":
				_, _, err = skill.moveToWarpSource(ctx, o, aiplanner.WarpEdge{
					From: aiknowledge.Point{Floor: 10, X: 4}, To: aiknowledge.Point{Floor: 11},
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			if game.snapshot.Position.X != 4 || len(game.requests) != 4 {
				t.Fatalf("position=%+v requests=%v", game.snapshot.Position, game.requests)
			}
			for _, request := range game.requests {
				if request != "c" {
					t.Fatalf("encounter movement was not individually confirmed: %q", request)
				}
			}
		})
	}
}

func TestMovementSegmentChecksCurrentAndUpcomingEncounterCells(t *testing.T) {
	route := ainavigation.Route{Directions: "cccc", Points: []ainavigation.Point{{X: 1}, {X: 2}, {X: 3}, {X: 4}}}
	for _, tc := range []struct {
		name                        string
		floor, x, probability, want int
	}{
		{"current", 10, 0, 1, 1},
		{"upcoming", 10, 3, 1, 1},
		{"zero-probability", 10, 3, 0, 4},
		{"other-floor", 11, 3, 1, 4},
		{"outside-segment", 10, 5, 1, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skill := &MovementSkill{Backend: &GameBackend{Knowledge: &aiknowledge.Knowledge{
				Encounters: []aiknowledge.EncounterArea{{Floor: tc.floor,
					Bounds: aiknowledge.Rectangle{X: tc.x, X2: tc.x}, EncounterProbability: aiknowledge.Range{Max: tc.probability}}},
			}}}
			if got := skill.segmentEnd(aimcp.Observation{Floor: 10}, route, 0); got != tc.want {
				t.Fatalf("segment end=%d want=%d", got, tc.want)
			}
		})
	}
}
