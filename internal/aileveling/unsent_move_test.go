package aileveling

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// Unlike a failed socket write, a revision rejection cannot send a packet.
type staleMoveGame struct {
	*fakeGame
	rejects int
	newX    int32
}

func (g *staleMoveGame) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if action.Kind == aigame.ActionMove && g.rejects > 0 {
		g.rejects--
		g.mu.Lock()
		g.snapshot.Revision++
		if g.newX != 0 {
			g.snapshot.Position.X = g.newX
		}
		g.mu.Unlock()
		return aigame.ErrStaleRevision
	}
	return g.fakeGame.ExecuteExpected(ctx, revision, action)
}

func TestUnsentMoveReobservesAndReplansWithReleasedReservation(t *testing.T) {
	fake := &fakeGame{snapshot: worldSnapshot()}
	game := &staleMoveGame{fakeGame: fake, rejects: 1, newX: 20}
	planned := []int32{}
	navigator := NavigatorFunc(func(_ context.Context, s aigame.Snapshot, _ NavigationRequest) (Navigation, error) {
		planned = append(planned, s.Position.X)
		return Navigation{Route: "c", Destination: aigame.Point{Floor: s.Position.Floor, X: s.Position.X + 1, Y: s.Position.Y}, CostKnown: true, MaximumCost: 5}, nil
	})
	c, _, _ := newCoordinator(t, fake, navigator)
	c.Game = game
	now := time.Unix(100, 0)
	c.Now = func() time.Time { return now }
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30, MaximumSpend: 5})
	now = now.Add(time.Second)
	checkpoint, err := c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Status != automation.Running || checkpoint.Phase != "ready" || checkpoint.ReservedSpend != 0 || len(fake.actions) != 0 {
		t.Fatalf("unsent move was not safely released: %+v actions=%v err=%v", checkpoint, fake.actions, err)
	}
	if !checkpoint.StepStartedAt.Equal(time.Unix(100, 0)) {
		t.Fatal("unsent rejection extended progress deadline")
	}
	checkpoint, err = c.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "submitted" || checkpoint.ReservedSpend != 5 || len(fake.actions) != 1 || fake.actions[0].X != 20 || len(planned) != 2 || planned[0] != 10 || planned[1] != 20 {
		t.Fatalf("move did not replan from current state: %+v plans=%v actions=%v err=%v", checkpoint, planned, fake.actions, err)
	}
}

func TestUnsentMoveContentionIsBounded(t *testing.T) {
	fake := &fakeGame{snapshot: worldSnapshot()}
	game := &staleMoveGame{fakeGame: fake, rejects: 20}
	c, _, _ := newCoordinator(t, fake, &fakeNavigator{navigation: Navigation{Route: "c", Destination: aigame.Point{Floor: 1, X: 11, Y: 20}, CostKnown: true}})
	c.Game = game
	receipt := startCharacter(t, c, StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30})
	for i := 0; i < maxStaleMoveAttempts; i++ {
		checkpoint, err := c.Tick(context.Background(), receipt.Handle)
		if i < maxStaleMoveAttempts-1 {
			if err != nil || checkpoint.Status != automation.Running {
				t.Fatalf("premature pause: %+v %v", checkpoint, err)
			}
		} else if !errors.Is(err, aigame.ErrStaleRevision) || checkpoint.Status != automation.Paused || checkpoint.Phase != "ready" {
			t.Fatalf("contention did not pause as unsent: %+v %v", checkpoint, err)
		}
	}
	if len(fake.actions) != 0 {
		t.Fatalf("rejected movements were sent: %v", fake.actions)
	}
	before := game.rejects
	if _, err := c.Tick(context.Background(), receipt.Handle); err != nil || game.rejects != before {
		t.Fatal("paused contention retried")
	}
}
