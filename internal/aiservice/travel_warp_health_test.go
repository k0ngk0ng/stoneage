package aiservice

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

func warpDestinationEncounterKnowledge(floor, x, y int) *aiknowledge.Knowledge {
	return &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{{
		Floor:                floor,
		Bounds:               aiknowledge.Rectangle{X: x, Y: y, X2: x, Y2: y},
		EncounterProbability: aiknowledge.Range{Max: 1},
	}}}
}

func TestWarpDestinationHealthIsRecheckedBeforeStaleMoveRetry(t *testing.T) {
	skill, game := movementFixture(t, true)
	game.snapshot.Player.MaxHP = 10
	game.staleMoveOnce = true
	skill.Backend.Session = &healthChangeOnRetry{game}
	skill.Backend.Knowledge = warpDestinationEncounterKnowledge(11, 0, 0)
	ctx := context.Background()
	observed, err := skill.Backend.Observe(ctx, skill.Backend.Binding)
	if err != nil {
		t.Fatal(err)
	}
	_, err = skill.submitMove(ctx, observed, aigame.Move(0, 0, "c"), func(latest aimcp.Observation) error {
		return skill.checkTravelHealthAt(latest, 11, 0, 0)
	})
	if !errors.Is(err, ErrTravelHealingRequired) || game.moveAttempts != 1 || game.moves != 0 {
		t.Fatalf("err=%v attempts=%d writes=%d", err, game.moveAttempts, game.moves)
	}
}

func TestMovementCrossMapBlocksLowHealthAtWarpDestinationWithoutW(t *testing.T) {
	from := aiknowledge.Point{Floor: 1, X: 1, Y: 0}
	to := aiknowledge.Point{Floor: 2, X: 0, Y: 0}
	game := &crossMapGame{
		snapshot: aigame.Snapshot{
			Account: "account", Character: "character", Revision: 12, Connected: true,
			Phase: aigame.PhaseWorld, Position: aigame.Point{Floor: int32(from.Floor), X: int32(from.X), Y: int32(from.Y)},
			Player: aigame.PlayerSnapshot{HasStatus: true, HP: 4, MaxHP: 10},
		},
		warpTargets:  map[aiknowledge.Point]aiknowledge.Point{from: to},
		warpOnStatus: true,
	}
	skill := newCrossMapSkill(t, game, []aiknowledge.MapWarp{{Type: "NONE", Time: "NULL", From: from, To: to, Attribute: "NULL"}})
	skill.Backend.Knowledge = warpDestinationEncounterKnowledge(to.Floor, to.X, to.Y)

	err := skill.Execute(context.Background(), crossMapAction(12, to.Floor, to.X, to.Y))

	game.mu.Lock()
	defer game.mu.Unlock()
	if !errors.Is(err, ErrTravelHealingRequired) {
		t.Fatalf("err=%v, want ErrTravelHealingRequired", err)
	}
	if len(game.moves) != 0 || len(game.mapEvents) != 0 || game.statuses != 0 {
		t.Fatalf("moves=%d map_events=%d statuses=%d, want no writes before unsafe warp", len(game.moves), len(game.mapEvents), game.statuses)
	}
	if got := game.snapshot.Position; got != (aigame.Point{Floor: int32(from.Floor), X: int32(from.X), Y: int32(from.Y)}) {
		t.Fatalf("position=%+v, want unchanged warp source", got)
	}
}

func TestMovementCrossMapAllowsHalfHealthAtWarpDestinationWithoutW(t *testing.T) {
	from := aiknowledge.Point{Floor: 1, X: 1, Y: 0}
	to := aiknowledge.Point{Floor: 2, X: 0, Y: 0}
	game := &crossMapGame{
		snapshot: aigame.Snapshot{
			Account: "account", Character: "character", Revision: 12, Connected: true,
			Phase: aigame.PhaseWorld, Position: aigame.Point{Floor: int32(from.Floor), X: int32(from.X), Y: int32(from.Y)},
			Player: aigame.PlayerSnapshot{HasStatus: true, HP: 5, MaxHP: 10},
		},
		warpTargets:  map[aiknowledge.Point]aiknowledge.Point{from: to},
		warpOnStatus: true,
	}
	skill := newCrossMapSkill(t, game, []aiknowledge.MapWarp{{Type: "NONE", Time: "NULL", From: from, To: to, Attribute: "NULL"}})
	skill.Backend.Knowledge = warpDestinationEncounterKnowledge(to.Floor, to.X, to.Y)

	err := skill.Execute(context.Background(), crossMapAction(12, to.Floor, to.X, to.Y))

	game.mu.Lock()
	defer game.mu.Unlock()
	if err != nil {
		t.Fatalf("err=%v, want successful warp at the half-health threshold", err)
	}
	if len(game.moves) != 0 || len(game.mapEvents) != 1 || game.statuses != 1 {
		t.Fatalf("moves=%d map_events=%d statuses=%d, want one EV and one confirmation status without W", len(game.moves), len(game.mapEvents), game.statuses)
	}
	if got := game.snapshot.Position; got != (aigame.Point{Floor: int32(to.Floor), X: int32(to.X), Y: int32(to.Y)}) {
		t.Fatalf("position=%+v, want warp destination", got)
	}
}
