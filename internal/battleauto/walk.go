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

// next returns the action that keeps encounters coming, or false when walking
// is not this loop's to do: a battle, a menu or a dialogue all mean the
// character is busy, and a second writer is exactly what the control lease
// exists to prevent.
func (s *seeker) next(snapshot aigame.Snapshot, now time.Time) (aigame.Action, bool) {
	if snapshot.Phase != aigame.PhaseWorld || snapshot.Battle.Active ||
		snapshot.ActiveWindow != nil || len(snapshot.Windows) > 0 {
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
