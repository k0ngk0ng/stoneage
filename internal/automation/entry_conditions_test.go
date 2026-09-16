package automation

import (
	"context"
	"testing"
)

type changingEntryGame struct {
	*fakeGame
	calls int
}

func (g *changingEntryGame) Observe(ctx context.Context) (Observation, error) {
	g.calls++
	if g.calls == 2 {
		g.observation.Flags["prerequisite_done"] = false
	}
	return g.fakeGame.Observe(ctx)
}

func TestStartRechecksPrerequisiteCompletionAfterPreflight(t *testing.T) {
	e, g, p := fixture(t)
	g.observation.Flags["prerequisite_done"] = true
	p.Preconditions = []Condition{{Kind: "flag_set", ID: "prerequisite_done"}}
	e.Game = &changingEntryGame{fakeGame: g}
	if _, err := e.Start(context.Background(), p); err == nil {
		t.Fatal("started with a prerequisite that disappeared after preflight")
	}
	if len(g.actions) != 0 {
		t.Fatal("entry validation submitted an action")
	}
}
