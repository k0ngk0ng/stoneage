package aiservice

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
)

type staleWarpSession struct {
	*crossMapGame
	mode     string
	attempts int
}

func (s *staleWarpSession) ExecuteExpected(ctx context.Context, revision uint64, a aigame.Action) error {
	if a.Kind == aigame.ActionMapEvent {
		s.attempts++
		if s.mode == "uncertain" {
			return errors.New("write outcome unknown")
		}
		if s.attempts == 1 || s.mode == "always" {
			s.snapshot.Revision++
			if s.mode == "moved" {
				s.snapshot.Position.X++
			}
			return aigame.ErrStaleRevision
		}
	}
	return s.crossMapGame.ExecuteExpected(ctx, revision, a)
}

func TestWarpEventRetriesOnlyConfirmedUnsentRevisionRejections(t *testing.T) {
	for _, mode := range []string{"once", "always", "moved", "uncertain"} {
		t.Run(mode, func(t *testing.T) {
			_, fixture := gameFixture(t)
			game := &crossMapGame{snapshot: fixture.snapshot}
			move := newCrossMapSkill(t, game, nil)
			session := &staleWarpSession{crossMapGame: game, mode: mode}
			move.Backend.Session = session
			before, err := move.Backend.Observe(context.Background(), move.Backend.Binding)
			if err != nil {
				t.Fatal(err)
			}
			edge := aiplanner.WarpEdge{From: observationPoint(before), To: aiknowledge.Point{Floor: 9, X: 1, Y: 1}, Time: "NULL"}
			err = move.triggerWarpEvent(context.Background(), before, edge)
			wantAttempts, wantWrites := 1, 0
			if mode == "once" {
				wantAttempts, wantWrites = 2, 1
			}
			if mode == "always" {
				wantAttempts = 3
			}
			if (err == nil) != (mode == "once") || session.attempts != wantAttempts || len(game.mapEvents) != wantWrites {
				t.Fatalf("mode=%s err=%v attempts=%d writes=%d", mode, err, session.attempts, len(game.mapEvents))
			}
		})
	}
}
