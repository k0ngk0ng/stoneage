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
			s.ActiveWindow = &aigame.WindowSnapshot{Open: true}
			return s
		}(),
		"queued window": func() aigame.Snapshot {
			s := worldSnapshot()
			s.Windows = []aigame.WindowSnapshot{{Open: true}}
			return s
		}(),
		"queued windows": func() aigame.Snapshot {
			s := worldSnapshot()
			s.Windows = []aigame.WindowSnapshot{{Open: true}}
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

// A notice window is what the login announcement is: one button, no decision.
// Answering it is the difference between a loop that walks and one that sits
// behind a message until the player comes back.
func TestNoticeWindowIsAnsweredAndChoicesAreNot(t *testing.T) {
	notice := worldSnapshot()
	notice.ActiveWindow = &aigame.WindowSnapshot{Type: 28, ButtonType: noticeBit, Sequence: 4, ObjectID: 7, Open: true}
	action, ok := noticeAction(notice)
	if !ok {
		t.Fatal("a one-button notice must be answerable")
	}
	if action.Kind != aigame.ActionWindow || action.WindowSequence != 4 || action.WindowObjectID != 7 || action.WindowSelect != noticeBit {
		t.Fatalf("notice answer = %+v", action)
	}
	if action.X != notice.Position.X || action.Y != notice.Position.Y {
		t.Fatalf("the answer must carry the player's own coordinates: %+v", action)
	}

	// 确定 with 取消, or a plain choice window, belongs to the player.
	for name, window := range map[string]aigame.WindowSnapshot{
		"confirm and cancel": {Type: 1, ButtonType: noticeBit | 2, Sequence: 5, ObjectID: 8, Open: true},
		"yes and no":         {Type: 2, ButtonType: 4 | 8, Sequence: 6, ObjectID: 9, Open: true},
		"already answered":   {Type: 28, ButtonType: noticeBit, Sequence: 7, ObjectID: 10, Open: true, Submitted: true},
		"closed":             {Type: 28, ButtonType: noticeBit, Sequence: 8, ObjectID: 11},
	} {
		snapshot := worldSnapshot()
		copy := window
		snapshot.ActiveWindow = &copy
		if action, ok := noticeAction(snapshot); ok {
			t.Fatalf("%s must be left to the player: %+v", name, action)
		}
	}

	// And the walk still refuses to step while any window is open.
	blocked := worldSnapshot()
	blocked.ActiveWindow = &aigame.WindowSnapshot{Type: 28, ButtonType: noticeBit, Open: true}
	if got := (&seeker{}).blocked(blocked); got != "window" {
		t.Fatalf("blocked = %q, want window", got)
	}
}

func TestAnsweredWindowDoesNotBlockTheWalk(t *testing.T) {
	answered := worldSnapshot()
	answered.ActiveWindow = &aigame.WindowSnapshot{Type: 28, ButtonType: noticeBit, Sequence: 4, ObjectID: 7, Open: true, Submitted: true}
	if got := (&seeker{}).blocked(answered); got != "" {
		t.Fatalf("blocked = %q, want the walk to be free", got)
	}
	if _, ok := noticeAction(answered); ok {
		t.Fatal("an answered notice must not be answered twice")
	}
	if _, ok := (&seeker{interval: DefaultSeekInterval}).next(answered, time.Unix(1, 0)); !ok {
		t.Fatal("the walk must resume once the window is answered")
	}
}
