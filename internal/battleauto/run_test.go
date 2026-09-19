package battleauto

import (
	"context"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// stillGame is a session that accepts every submission and never moves the
// character: the server refused the step, and said nothing about it.
type stillGame struct{ snapshot aigame.Snapshot }

func (g stillGame) Observe(context.Context) (aigame.Snapshot, error) { return g.snapshot, nil }

func (g stillGame) ExecuteExpected(context.Context, uint64, aigame.Action) error { return nil }

// The panel reads the loop's state, and "looking for a fight" while the
// character stands still is the report that made a working loop look broken.
// A walk whose every step is refused has to say so.
func TestStalledWalkIsReported(t *testing.T) {
	policy := Policy{SeekEncounters: true}
	var states []State
	runner := Runner{
		Game:         stillGame{snapshot: worldSnapshot()},
		Policy:       policy,
		SeekInterval: time.Nanosecond,
		State:        func(state State) { states = append(states, state) },
	}
	// Run would build these; the test drives tick directly.
	runner.seeker = &seeker{interval: time.Nanosecond}
	runner.live = &live{}
	if len(states) != 0 {
		t.Fatal("no state before the first pass")
	}
	for i := 0; i < 2*seekStalledAfter; i++ {
		if err := runner.tick(context.Background(), policy); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	if len(states) == 0 {
		t.Fatal("the loop reported no state at all")
	}
	last := states[len(states)-1]
	if last.Blocked != reasonWalkStalled {
		t.Fatalf("blocked = %q, want %q", last.Blocked, reasonWalkStalled)
	}
	if !last.Seeking {
		t.Fatal("a stalled walk is still a walk: the loop keeps trying")
	}
}
