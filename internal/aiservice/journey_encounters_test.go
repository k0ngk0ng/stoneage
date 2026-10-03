package aiservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

type journeyEncounterGame struct {
	*crossMapGame
	stationary bool
}

func (g *journeyEncounterGame) ExecuteExpected(ctx context.Context, rev uint64, a aigame.Action) error {
	if err := g.crossMapGame.ExecuteExpected(ctx, rev, a); err != nil {
		return err
	}
	if a.Kind == aigame.ActionMove {
		g.mu.Lock()
		if g.stationary {
			g.snapshot.Position.X, g.snapshot.Position.Y = a.X, a.Y
		}
		g.snapshot.Phase = aigame.PhaseBattle
		g.snapshot.Battle.Active = true
		g.mu.Unlock()
	}
	return nil
}

func TestJourneyEncounterBudgetDistinguishesProgressFromRepeatedLocation(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		destination, recoveries int
		stationary, limit       bool
	}{
		{"progress beyond eight encounters", 40, 10, false, false},
		{"repeated same location remains bounded", 40, 8, true, true},
		{"total journey remains bounded", 260, 64, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, base := gameFixture(t)
			base.snapshot.Position.Floor = 10
			g := &journeyEncounterGame{crossMapGame: &crossMapGame{snapshot: base.snapshot}, stationary: tc.stationary}
			backend.Session = g
			skill := &MovementSkill{Backend: backend, Navigator: crossMapNavigator{}}
			calls := 0
			skill.BattleRecovery = movementBattleRecoveryFunc(func(ctx context.Context) error {
				calls++
				g.mu.Lock()
				defer g.mu.Unlock()
				g.snapshot.Battle.Active = false
				g.snapshot.Phase = aigame.PhaseWorld
				g.snapshot.Revision++
				return ctx.Err()
			})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			err := skill.Execute(ctx, crossMapAction(12, 10, tc.destination, 0))
			if calls != tc.recoveries || errors.Is(err, ErrTravelBattleLimit) != tc.limit || !tc.limit && err != nil {
				t.Fatalf("recoveries=%d err=%v", calls, err)
			}
			if !tc.limit && (g.snapshot.Position.X != int32(tc.destination) || g.snapshot.Battle.Active) {
				t.Fatal("journey did not arrive in world", g.snapshot.Position, g.snapshot.Battle.Active)
			}
		})
	}
}
