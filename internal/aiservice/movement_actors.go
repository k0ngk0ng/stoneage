package aiservice

import (
	"context"
	"errors"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
)

var ErrMovementOccupied = errors.New("movement route is occupied by a visible character")

// Only a proven failed detour supplies the blocker and the reachable tile
// immediately before it. Unknown movement outcomes never construct this.
type movementBlockage struct {
	floor          int
	tile, approach ainavigation.Point
	cause          error
}

func (e *movementBlockage) Error() string   { return errors.Join(ErrMovementOccupied, e.cause).Error() }
func (e *movementBlockage) Unwrap() []error { return []error{ErrMovementOccupied, e.cause} }

func actorBlocksMovement(kind string, charType int, graphicKnown bool, graphic int) bool {
	return (kind == "character" || charType == 1) && !(charType == 20 && graphicKnown && graphic == 0)
}

func movementOccupancy(o aimcp.Observation) map[ainavigation.Point]bool {
	blocked := map[ainavigation.Point]bool{}
	for _, actor := range o.Actors {
		// NPCEnemy dieact=0 sends graphic 0 and becomes ALTERRATIVE
		// (passable) until revival. Require a known graphic and the native
		// NPCEnemy type; unknown or other invisible actors remain obstacles.
		graphic := 0
		if actor.Graphic != nil {
			graphic = *actor.Graphic
		}
		if actorBlocksMovement(actor.Kind, actor.CharType, actor.Graphic != nil, graphic) && (actor.X != o.X || actor.Y != o.Y) {
			blocked[ainavigation.Point{X: actor.X, Y: actor.Y}] = true
		}
	}
	return blocked
}

// Replan before writing when a newly visible NPC/player occupies the next
// segment. Static map collision cannot account for live characters. This is
// not a retry of an unacknowledged W packet.
func (s *MovementSkill) avoidOccupiedSegment(ctx context.Context, o aimcp.Observation, route ainavigation.Route, offset int, target ainavigation.Point) (ainavigation.Route, int, error) {
	blocked := movementOccupancy(o)
	previous := ainavigation.Point{X: o.X, Y: o.Y}
	for _, p := range route.Points[offset:min(offset+4, len(route.Points))] {
		if !blocked[p] {
			previous = p
			continue
		}
		nav, ok := s.Navigator.(tileRouteOptionsNavigator)
		if !ok {
			return route, offset, ErrMovementOccupied
		}
		from := ainavigation.Point{X: o.X, Y: o.Y}
		replanned, err := nav.RouteContextWithOptions(ctx, o.Floor, from, target, ainavigation.RouteOptions{Blocked: func(p ainavigation.Point) bool { return blocked[p] }})
		if err != nil {
			if errors.Is(err, ainavigation.ErrUnreachable) || errors.Is(err, ainavigation.ErrBlocked) {
				return route, offset, &movementBlockage{floor: o.Floor, tile: p, approach: previous, cause: err}
			}
			return route, offset, errors.Join(ErrMovementOccupied, err)
		}
		if err := validateLevelingRoute(replanned, from, target); err != nil {
			return route, offset, err
		}
		for _, p := range replanned.Points {
			if blocked[p] {
				return route, offset, ErrMovementOccupied
			}
		}
		return replanned, 0, nil
	}
	return route, offset, nil
}

func checkMovementOccupancy(o aimcp.Observation, a aigame.Action) error {
	blocked := movementOccupancy(o)
	p := ainavigation.Point{X: o.X, Y: o.Y}
	for _, d := range []byte(a.Route) {
		if d < 'a' || d > 'h' {
			return ErrMovementOccupied
		}
		delta := travelDirections[d-'a']
		p.X += delta[0]
		p.Y += delta[1]
		if blocked[p] {
			return ErrMovementOccupied
		}
	}
	return nil
}
