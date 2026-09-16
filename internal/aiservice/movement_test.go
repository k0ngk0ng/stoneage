package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type oneStepNavigator struct{}

func (oneStepNavigator) RouteContext(context.Context, int, ainavigation.Point, ainavigation.Point) (ainavigation.Route, error) {
	return ainavigation.Route{Directions: "c", Points: []ainavigation.Point{{X: 1, Y: 0}}}, nil
}

type moveGame struct {
	fakeGame
	moves, moveAttempts, status int
	confirm                     bool
	staleMoveOnce               bool
	staleMoveAlways             bool
	staleMoveChangesPosition    bool
	moveError                   error
}

// gateMoveGame models the Web AutomationSession boundary: the session owns
// the Gate.Dispatch call made by ExecuteExpected. The service must therefore
// not wrap this method in a second dispatch.
type gateMoveGame struct {
	*moveGame
	gate *aicontrol.Gate
}

func (*gateMoveGame) UsesControlGate() {}

func (g *gateMoveGame) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	state := g.gate.State()
	return g.gate.Dispatch(ctx, state.Generation, state.Mode, func(sendCtx context.Context) error {
		return g.moveGame.ExecuteExpected(sendCtx, revision, action)
	})
}

func (g *moveGame) ExecuteExpected(_ context.Context, revision uint64, a aigame.Action) error {
	if a.Kind == aigame.ActionMove {
		g.moveAttempts++
		if g.staleMoveAlways || g.staleMoveOnce {
			g.staleMoveOnce = false
			g.snapshot.Revision++
			if g.staleMoveChangesPosition {
				g.snapshot.Position.X++
			}
			return aigame.ErrStaleRevision
		}
	}
	if g.snapshot.Revision != revision {
		return aigame.ErrStaleRevision
	}
	if a.Kind == aigame.ActionMove {
		g.moves++
		if g.moveError != nil {
			return g.moveError
		}
	}
	if a.Kind == aigame.ActionStatus {
		g.status++
		if g.confirm {
			g.snapshot.Position.X = 1
			g.snapshot.Revision++
		}
	}
	return nil
}
func movementFixture(t *testing.T, confirm bool) (*MovementSkill, *moveGame) {
	b, f := gameFixture(t)
	g := &moveGame{fakeGame: *f, confirm: confirm}
	g.snapshot.Position.Floor = 10
	b.Session = g
	return &MovementSkill{Backend: b, Navigator: oneStepNavigator{}, SegmentTimeout: 700 * time.Millisecond}, g
}
func TestMovementWaitsForServerPositionSample(t *testing.T) {
	s, g := movementFixture(t, true)
	err := s.Execute(context.Background(), automation.Action{Skill: "move", ExpectedRevision: 12, Arguments: json.RawMessage(`{"floor":10,"x":1,"y":0}`)})
	if err != nil || g.moves != 1 || g.status != 1 || g.snapshot.Position.X != 1 {
		t.Fatalf("move=%d status=%d err=%v", g.moves, g.status, err)
	}
}
func TestMovementNeverRepeatsWWhenPositionUnknown(t *testing.T) {
	s, g := movementFixture(t, false)
	err := s.Execute(context.Background(), automation.Action{Skill: "move", ExpectedRevision: 12, Arguments: json.RawMessage(`{"floor":10,"x":1,"y":0}`)})
	if err == nil || g.moves != 1 || g.status == 0 {
		t.Fatalf("move=%d status=%d err=%v", g.moves, g.status, err)
	}
}

func TestMovementRetriesOnlyAnUnsentStaleMove(t *testing.T) {
	s, g := movementFixture(t, true)
	g.staleMoveOnce = true
	err := s.Execute(context.Background(), automation.Action{Skill: "move", ExpectedRevision: 12, Arguments: json.RawMessage(`{"floor":10,"x":1,"y":0}`)})
	if err != nil || g.moveAttempts != 2 || g.moves != 1 || g.status != 1 || g.snapshot.Position.X != 1 {
		t.Fatalf("move attempts=%d writes=%d status=%d position=%+v err=%v", g.moveAttempts, g.moves, g.status, g.snapshot.Position, err)
	}
}

func TestMovementDoesNotRetryStaleMoveAfterPositionChanges(t *testing.T) {
	s, g := movementFixture(t, true)
	g.staleMoveOnce = true
	g.staleMoveChangesPosition = true
	err := s.Execute(context.Background(), automation.Action{Skill: "move", ExpectedRevision: 12, Arguments: json.RawMessage(`{"floor":10,"x":1,"y":0}`)})
	if !errors.Is(err, aigame.ErrStaleRevision) || g.moveAttempts != 1 || g.moves != 0 || g.status != 0 {
		t.Fatalf("move attempts=%d writes=%d status=%d err=%v", g.moveAttempts, g.moves, g.status, err)
	}
}

func TestMovementDoesNotRetryUncertainMoveWrite(t *testing.T) {
	s, g := movementFixture(t, true)
	g.moveError = io.ErrUnexpectedEOF
	err := s.Execute(context.Background(), automation.Action{Skill: "move", ExpectedRevision: 12, Arguments: json.RawMessage(`{"floor":10,"x":1,"y":0}`)})
	if !errors.Is(err, io.ErrUnexpectedEOF) || g.moveAttempts != 1 || g.moves != 1 || g.status != 0 {
		t.Fatalf("move attempts=%d writes=%d status=%d err=%v", g.moveAttempts, g.moves, g.status, err)
	}
}

func TestMovementBoundsRepeatedUnsentStaleMoves(t *testing.T) {
	s, g := movementFixture(t, true)
	g.staleMoveAlways = true
	err := s.Execute(context.Background(), automation.Action{Skill: "move", ExpectedRevision: 12, Arguments: json.RawMessage(`{"floor":10,"x":1,"y":0}`)})
	if !errors.Is(err, aigame.ErrStaleRevision) || g.moveAttempts != 2 || g.moves != 0 || g.status != 0 {
		t.Fatalf("move attempts=%d writes=%d status=%d err=%v", g.moveAttempts, g.moves, g.status, err)
	}
}

func TestMovementDoesNotNestControlGateForWebSession(t *testing.T) {
	s, game := movementFixture(t, true)
	s.Backend.Session = &gateMoveGame{moveGame: game, gate: s.Backend.Gate}
	action := automation.Action{Skill: "move", ExpectedRevision: 12, Arguments: json.RawMessage(`{"floor":10,"x":1,"y":0}`)}
	done := make(chan error, 1)
	go func() { done <- s.Execute(context.Background(), action) }()
	select {
	case err := <-done:
		if err != nil || game.moves != 1 || game.status != 1 {
			t.Fatalf("move=%d status=%d err=%v", game.moves, game.status, err)
		}
	case <-time.After(time.Second):
		t.Fatal("movement submission deadlocked on a nested control gate")
	}
}

func TestMovementFencesFormerOwner(t *testing.T) {
	s, g := movementFixture(t, true)
	if _, err := s.Backend.Gate.Takeover("manual"); err != nil {
		t.Fatal(err)
	}
	err := s.Execute(context.Background(), automation.Action{Skill: "move", ExpectedRevision: 12, Arguments: json.RawMessage(`{"floor":10,"x":1,"y":0}`)})
	if err != aicontrol.ErrStale || g.moves != 0 {
		t.Fatalf("late move=%d err=%v", g.moves, err)
	}
}
