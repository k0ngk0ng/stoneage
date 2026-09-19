package battleauto

import (
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// The native 2.5 walk alphabet, one straight step each: a=up, c=right,
// e=down, g=left. The diagonals exist, but a person looking for a fight walks
// straight, and two steps per direction keeps the character in a small box.
const (
	stepUp    = "a"
	stepRight = "c"
	stepDown  = "e"
	stepLeft  = "g"
)

var seekLegs = [4]string{stepRight, stepLeft, stepDown, stepUp}

// DefaultSeekInterval paces the steps. The server rolls the encounter on the
// movement itself, so this is the encounter rate as much as the walking speed;
// a person walks about this fast.
const DefaultSeekInterval = 450 * time.Millisecond

// seekLeg is how many steps go one way before the direction turns back.
const seekLeg = 2

// seeker walks a short back-and-forth pattern so the character keeps rolling
// encounters without wandering off. It only ever steps: two out, two back, then
// the next axis, and it asks the server where it is at every turn because
// nothing echoes the client's own walk -- the next leg has to start from a
// coordinate the server confirmed, or the steps are computed from a tile the
// character left two moves ago.
type seeker struct {
	interval time.Duration
	last     time.Time
	leg      int
	step     int
}

// Blocked reports what is stopping the walk, or "" when nothing is. A loop
// that silently refuses to walk looks exactly like a broken one, and the
// player watching the panel has no way to tell the difference.
func (s *seeker) blocked(snapshot aigame.Snapshot) string {
	switch {
	case snapshot.Phase != aigame.PhaseWorld:
		return "phase"
	case snapshot.Battle.Active:
		return "battle"
	case unansweredWindow(snapshot):
		return "window"
	default:
		return ""
	}
}

// unansweredWindow reports a window nobody has answered yet. An answered window
// stays in the projection until the server replaces or closes it, and treating
// that as "still blocked" would park the walk behind a message that is already
// on its way out.
func unansweredWindow(snapshot aigame.Snapshot) bool {
	if window := snapshot.ActiveWindow; window != nil && window.Open && !window.Submitted {
		return true
	}
	for _, window := range snapshot.Windows {
		if window.Open && !window.Submitted {
			return true
		}
	}
	return false
}

// next returns the action that keeps encounters coming, or false when walking
// is not this loop's to do: a battle, a menu or a dialogue all mean the
// character is busy, and a second writer is exactly what the control lease
// exists to prevent.
func (s *seeker) next(snapshot aigame.Snapshot, now time.Time) (aigame.Action, bool) {
	if s.blocked(snapshot) != "" {
		return aigame.Action{}, false
	}
	if s.interval <= 0 {
		s.interval = DefaultSeekInterval
	}
	if !s.last.IsZero() && now.Sub(s.last) < s.interval {
		return aigame.Action{}, false
	}
	s.last = now

	if s.step >= seekLeg {
		s.step = 0
		s.leg = (s.leg + 1) % len(seekLegs)
		return positionQuery(), true
	}
	s.step++
	// The 2.5 walk carries the origin tile, and the server only accepts it
	// while it is still adjacent to the character, so the step is submitted
	// from the last position the server confirmed.
	return aigame.Move(snapshot.Position.X, snapshot.Position.Y, seekLegs[s.leg]), true
}

// positionQuery is the read-only status request that makes the walking
// executor ask where it actually is.
func positionQuery() aigame.Action {
	return aigame.Action{Kind: aigame.ActionStatus, Command: "c"}
}

// noticeBit is 确定, the only button a notice offers: the wire's other bits are
// 取消, 是, 否 and the page turns, every one of which is a choice.
const noticeBit int32 = 1

// noticeAction answers a window whose only button is 确定 -- the login
// announcement and the like. A character left alone otherwise sits behind that
// message forever and the walk never starts. Anything offering a real choice is
// left for the player: picking for them is not this loop's job.
func noticeAction(snapshot aigame.Snapshot) (aigame.Action, bool) {
	window := snapshot.ActiveWindow
	if window == nil || !window.Open || window.Submitted || window.ButtonType != noticeBit {
		return aigame.Action{}, false
	}
	return aigame.Window(snapshot.Position.X, snapshot.Position.Y,
		window.Sequence, window.ObjectID, noticeBit, ""), true
}
