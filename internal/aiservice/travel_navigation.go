package aiservice

import (
	"context"
	"errors"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
)

var ErrUnsafeTravelRoute = errors.New("自动赶路未找到适合当前人物等级、且遭遇资料完整的路线")

// travelTileNavigator binds one route calculation to the observed level. It
// shares the leveling encounter policy, including effective overlapping rows,
// and additionally avoids warp source tiles with unsafe arrival points.
type travelTileNavigator struct {
	tiles       TileNavigator
	safety      *LevelingNavigator
	level       int
	warpHazards map[aiknowledge.Point]bool
}

func (s *MovementSkill) travelNavigator(level int) (*travelTileNavigator, error) {
	if s.Backend == nil || s.Backend.Knowledge == nil || s.Navigator == nil {
		return nil, ErrUnsafeTravelRoute
	}
	tiles := s.Navigator
	if bound, ok := tiles.(*travelTileNavigator); ok {
		if bound.level == level && bound.safety.Knowledge == s.Backend.Knowledge {
			return bound, nil
		}
		tiles = bound.tiles
	}
	graph := aiplanner.NewWarpGraph(s.Backend.Knowledge)
	warps := graph.Edges()
	if s.WarpGraph != nil {
		warps = append(warps, s.WarpGraph.Edges()...)
	}
	n := &travelTileNavigator{tiles: tiles, safety: &LevelingNavigator{Knowledge: s.Backend.Knowledge}, level: level, warpHazards: make(map[aiknowledge.Point]bool)}
	for _, edge := range warps {
		if !n.safety.encounterPointSafe(edge.To.Floor, edge.To.X, edge.To.Y, level) {
			n.warpHazards[edge.From] = true
		}
	}
	return n, nil
}

func (n *travelTileNavigator) blocked(floor int, p ainavigation.Point) bool {
	if !n.safety.encounterPointSafe(floor, p.X, p.Y, n.level) {
		return true
	}
	// A server can trigger mapwarps on W, before a separate EV request. Do
	// not let a same-floor detour accidentally enter a dangerous destination.
	return n.warpHazards[aiknowledge.Point{Floor: floor, X: p.X, Y: p.Y}]
}

func (n *travelTileNavigator) Walkable(floor, x, y int) bool {
	tiles, ok := n.tiles.(staticWalkabilityNavigator)
	return ok && !n.blocked(floor, ainavigation.Point{X: x, Y: y}) && tiles.Walkable(floor, x, y)
}

func (n *travelTileNavigator) RouteContext(ctx context.Context, floor int, from, to ainavigation.Point) (ainavigation.Route, error) {
	if err := ctx.Err(); err != nil {
		return ainavigation.Route{}, err
	}
	if n.blocked(floor, to) {
		return ainavigation.Route{}, ErrUnsafeTravelRoute
	}
	route, err := n.tiles.RouteContext(ctx, floor, from, to)
	if err != nil {
		return ainavigation.Route{}, err
	}
	if err := validateLevelingRoute(route, from, to); err != nil {
		return ainavigation.Route{}, err
	}
	if n.safeRoute(floor, from, route) {
		return route, nil
	}
	options, ok := n.tiles.(tileRouteOptionsNavigator)
	if !ok {
		return ainavigation.Route{}, ErrUnsafeTravelRoute
	}
	route, err = options.RouteContextWithOptions(ctx, floor, from, to, ainavigation.RouteOptions{Blocked: func(p ainavigation.Point) bool { return n.blocked(floor, p) }})
	if err != nil {
		if ctx.Err() != nil {
			return ainavigation.Route{}, ctx.Err()
		}
		return ainavigation.Route{}, errors.Join(ErrUnsafeTravelRoute, err)
	}
	if err := validateLevelingRoute(route, from, to); err != nil {
		return ainavigation.Route{}, err
	}
	if !n.safeRoute(floor, from, route) {
		return ainavigation.Route{}, ErrUnsafeTravelRoute
	}
	return route, nil
}

var travelDirections = [...][2]int{{0, -1}, {1, -1}, {1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}}

func (n *travelTileNavigator) safeRoute(floor int, from ainavigation.Point, route ainavigation.Route) bool {
	p := from
	for i, direction := range []byte(route.Directions) {
		if direction < 'a' || direction > 'h' {
			return false
		}
		delta := travelDirections[direction-'a']
		p.X += delta[0]
		p.Y += delta[1]
		if i >= len(route.Points) || p != route.Points[i] || n.blocked(floor, p) {
			return false
		}
	}
	return len(route.Points) == len(route.Directions)
}

// Check the submitted segment again using the latest level, including the
// safe pre-write stale-revision retry. A prior route never grants permission
// to cross stronger encounters after an authoritative state change.
func (s *MovementSkill) checkTravelEncounters(o aimcp.Observation, action aigame.Action) error {
	n, err := s.travelNavigator(o.Character.Level)
	if err != nil {
		return err
	}
	p := ainavigation.Point{X: o.X, Y: o.Y}
	for _, direction := range []byte(action.Route) {
		if direction < 'a' || direction > 'h' {
			return ErrUnsafeTravelRoute
		}
		delta := travelDirections[direction-'a']
		p.X += delta[0]
		p.Y += delta[1]
		if n.blocked(o.Floor, p) {
			return ErrUnsafeTravelRoute
		}
	}
	return nil
}
