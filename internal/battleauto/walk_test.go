package battleauto

import (
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func worldSnapshot() aigame.Snapshot {
	return aigame.Snapshot{
		Phase:    aigame.PhaseWorld,
		Position: aigame.Point{Floor: 100, X: 480, Y: 520, Direction: 4},
	}
}

// walker drives the seeker the way the runner does: one pass per tick, with
// the clock advanced far enough that the pace never hides the pattern.
func walker(t *testing.T, passes int) []string {
	t.Helper()
	s := &seeker{interval: DefaultSeekInterval}
	snapshot := worldSnapshot()
	clock := time.Unix(0, 0)
	var pattern []string
	for i := 0; i < passes; i++ {
		clock = clock.Add(DefaultSeekInterval)
		action, ok := s.next(snapshot, clock)
		if !ok {
			t.Fatalf("pass %d: no action while free to walk", i)
		}
		switch action.Kind {
		case aigame.ActionMove:
			if action.X != snapshot.Position.X || action.Y != snapshot.Position.Y {
				t.Fatalf("pass %d: step from (%d,%d), want the observed (%d,%d)",
					i, action.X, action.Y, snapshot.Position.X, snapshot.Position.Y)
			}
			pattern = append(pattern, action.Route)
			// The server moves the character; the loop learns it from the
			// position query that ends each leg.
			snapshot.Position.X += stepDeltaX(action.Route)
			snapshot.Position.Y += stepDeltaY(action.Route)
		case aigame.ActionStatus:
			if action.Command != "c" {
				t.Fatalf("pass %d: status %q, want the position query", i, action.Command)
			}
			pattern = append(pattern, "?")
		default:
			t.Fatalf("pass %d: unexpected action kind %v", i, action.Kind)
		}
	}
	return pattern
}

func stepDeltaX(letter string) int32 {
	switch letter {
	case stepRight:
		return 1
	case stepLeft:
		return -1
	}
	return 0
}

func stepDeltaY(letter string) int32 {
	switch letter {
	case stepDown:
		return 1
	case stepUp:
		return -1
	}
	return 0
}

// The pattern is the whole point: the character has to keep stepping, and it
// has to keep coming back, or a wall or a village edge turns the walk into a
// slow march away from where the player parked the character.
func TestSeekerWalksOutAndBackOnEachAxis(t *testing.T) {
	pattern := walker(t, 4*3)
	want := []string{"c", "c", "?", "g", "g", "?", "e", "e", "?", "a", "a", "?"}
	for i, letter := range want {
		if pattern[i] != letter {
			t.Fatalf("pattern = %v, want %v", pattern, want)
		}
	}
}

func TestSeekerKeepsStepping(t *testing.T) {
	pattern := walker(t, 4*3*3)
	steps := 0
	for _, item := range pattern {
		if item != "?" {
			steps++
		}
	}
	if steps != 24 {
		t.Fatalf("steps = %d, want 24 in %d passes", steps, len(pattern))
	}
}

func TestSeekerPacesItself(t *testing.T) {
	s := &seeker{interval: DefaultSeekInterval}
	snapshot := worldSnapshot()
	clock := time.Unix(0, 0)
	if _, ok := s.next(snapshot, clock); !ok {
		t.Fatal("the first pass must step")
	}
	if _, ok := s.next(snapshot, clock.Add(DefaultSeekInterval/2)); ok {
		t.Fatal("a step inside the interval must wait")
	}
	if _, ok := s.next(snapshot, clock.Add(DefaultSeekInterval)); !ok {
		t.Fatal("a step after the interval must go through")
	}
}

// Walking is only this loop's to do when the character is free. A battle, a
// menu or a dialogue means somebody else owns the character right now.
func TestSeekerLeavesTheCharacterAloneWhenBusy(t *testing.T) {
	clock := time.Unix(0, 0)
	cases := map[string]aigame.Snapshot{
		"battle": func() aigame.Snapshot {
			s := worldSnapshot()
			s.Phase = aigame.PhaseBattle
			s.Battle = aigame.BattleSnapshot{Active: true}
			return s
		}(),
		"battle without phase": func() aigame.Snapshot {
			s := worldSnapshot()
			s.Battle = aigame.BattleSnapshot{Active: true}
			return s
		}(),
		"window": func() aigame.Snapshot {
			s := worldSnapshot()
			s.ActiveWindow = &aigame.WindowSnapshot{}
			return s
		}(),
		"queued windows": func() aigame.Snapshot {
			s := worldSnapshot()
			s.Windows = []aigame.WindowSnapshot{{}}
			return s
		}(),
		"not in the world": func() aigame.Snapshot {
			s := worldSnapshot()
			s.Phase = aigame.PhaseCharacterList
			return s
		}(),
	}
	for name, snapshot := range cases {
		t.Run(name, func(t *testing.T) {
			s := &seeker{interval: DefaultSeekInterval}
			if action, ok := s.next(snapshot, clock); ok {
				t.Fatalf("walked while busy: %+v", action)
			}
		})
	}
}
