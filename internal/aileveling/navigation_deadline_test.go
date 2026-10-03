package aileveling

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type deadlineNavigator struct {
	t     *testing.T
	calls int
}

func (n *deadlineNavigator) Next(ctx context.Context, _ aigame.Snapshot, _ NavigationRequest) (Navigation, error) {
	n.calls++
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > time.Second {
		n.t.Fatal("navigation did not inherit remaining task budget")
	}
	<-ctx.Done()
	return Navigation{}, ctx.Err()
}

func TestNavigationCannotOutliveTaskOrNoProgressBudget(t *testing.T) {
	for _, idle := range []bool{false, true} {
		name := "total"
		if idle {
			name = "no-progress"
		}
		t.Run(name, func(t *testing.T) {
			game := &fakeGame{snapshot: worldSnapshot()}
			navigator := &deadlineNavigator{t: t}
			c, _, store := newCoordinator(t, game, navigator)
			now := time.Now()
			c.Now = func() time.Time { return now }
			request := StartRequest{TargetKind: "character", TargetLevel: 2, MaximumSeconds: 1, NoProgressTimeout: time.Minute}
			if idle {
				request.MaximumSeconds = 30
				request.NoProgressTimeout = time.Second
			}
			receipt := startCharacter(t, c, request)
			now = now.Add(980 * time.Millisecond)
			checkpoint, err := c.Tick(context.Background(), receipt.Handle)
			if !errors.Is(err, context.DeadlineExceeded) || checkpoint.Status != automation.Paused {
				t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
			}
			persisted, err := store.Load(context.Background(), receipt.Handle)
			if err != nil || persisted.Status != automation.Paused || len(game.actions) != 0 || navigator.calls != 1 {
				t.Fatal("deadline did not pause without action", persisted, err)
			}
		})
	}
}
