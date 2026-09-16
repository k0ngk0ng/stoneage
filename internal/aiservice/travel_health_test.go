package aiservice

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

func TestTravelHealthChecksEveryMovementPath(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		hp, maxHP, encounterX, probability int
		blocked                            bool
	}{
		{"one-hp-current-area", 1, 29, 0, 1, true},
		{"low-hp-next-area", 14, 29, 1, 1, true},
		{"rounded-half", 15, 29, 1, 1, false},
		{"unknown-health", 10, 0, 1, 1, true},
		{"invalid-health", 30, 29, 1, 1, true},
		{"town-to-healer", 1, 29, 5, 1, false},
		{"disabled-encounters", 1, 29, 1, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			skill, game := movementFixture(t, true)
			game.snapshot.Player.HP, game.snapshot.Player.MaxHP = int32(tc.hp), int32(tc.maxHP)
			skill.Backend.Knowledge = &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{{Floor: 10,
				Bounds: aiknowledge.Rectangle{X: tc.encounterX, X2: tc.encounterX}, EncounterProbability: aiknowledge.Range{Max: tc.probability}}}}
			err := skill.Execute(context.Background(), crossMapAction(12, 10, 1, 0))
			if tc.blocked {
				if !errors.Is(err, ErrTravelHealingRequired) || game.moveAttempts != 0 {
					t.Fatalf("err=%v move attempts=%d", err, game.moveAttempts)
				}
			} else if err != nil || game.moves != 1 {
				t.Fatalf("err=%v moves=%d", err, game.moves)
			}
		})
	}
}

type healthChangeOnRetry struct{ *moveGame }

func (g *healthChangeOnRetry) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if action.Kind == aigame.ActionMove {
		g.snapshot.Player.HP = 1
	}
	return g.moveGame.ExecuteExpected(ctx, revision, action)
}

func TestTravelHealthRechecksPreWriteRevisionRetry(t *testing.T) {
	skill, game := movementFixture(t, true)
	game.snapshot.Player.MaxHP = 10
	game.staleMoveOnce = true
	skill.Backend.Session = &healthChangeOnRetry{game}
	skill.Backend.Knowledge = &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{{Floor: 10,
		Bounds: aiknowledge.Rectangle{X: 1, X2: 1}, EncounterProbability: aiknowledge.Range{Max: 1}}}}
	err := skill.Execute(context.Background(), crossMapAction(12, 10, 1, 0))
	if !errors.Is(err, ErrTravelHealingRequired) || game.moveAttempts != 1 || game.moves != 0 {
		t.Fatalf("err=%v attempts=%d writes=%d", err, game.moveAttempts, game.moves)
	}
}

func TestTravelHealthStopsAfterEscapeBeforeResumingWalk(t *testing.T) {
	skill, game := movementFixture(t, true)
	game.snapshot.Player.MaxHP = 10
	game.snapshot.Phase = aigame.PhaseBattle
	game.snapshot.Battle.Active = true
	skill.Backend.Knowledge = &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{{Floor: 10,
		Bounds: aiknowledge.Rectangle{X: 0, X2: 1}, EncounterProbability: aiknowledge.Range{Max: 1}}}}
	recoveries := 0
	skill.BattleRecovery = movementBattleRecoveryFunc(func(context.Context) error {
		recoveries++
		game.snapshot.Phase = aigame.PhaseWorld
		game.snapshot.Battle.Active = false
		game.snapshot.Player.HP = 1
		game.snapshot.Revision++
		return nil
	})
	err := skill.Execute(context.Background(), crossMapAction(12, 10, 1, 0))
	if !errors.Is(err, ErrTravelHealingRequired) || recoveries != 1 || game.moveAttempts != 0 {
		t.Fatalf("err=%v recoveries=%d move attempts=%d", err, recoveries, game.moveAttempts)
	}
}
