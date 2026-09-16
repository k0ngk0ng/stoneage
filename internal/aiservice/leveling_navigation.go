package aiservice

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aileveling"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
)

// LevelingNavigator chooses from actual encounter data and collision maps.
// It returns at most one short ground segment. Cross-map movement is planned
// from the reviewed mapwarp records, but the coordinator must observe the
// resulting floor before the next segment is selected.
type LevelingNavigator struct {
	Knowledge *aiknowledge.Knowledge
	Tiles     TileNavigator

	// WarpGraph and MaxWarpEdges are optional test/operator overrides. When
	// WarpGraph is nil, Knowledge.Warps is the only source of warp edges.
	WarpGraph    *aiplanner.WarpGraph
	MaxWarpEdges int
}

// tileRouteOptionsNavigator is deliberately optional. Movement and existing
// test adapters only need TileNavigator; the real ainavigation.Navigator also
// implements this interface so leveling can ask for a route that avoids
// unsafe encounter cells.
type tileRouteOptionsNavigator interface {
	RouteContextWithOptions(context.Context, int, ainavigation.Point, ainavigation.Point, ainavigation.RouteOptions) (ainavigation.Route, error)
}

var errUnsafeLevelingRoute = errors.New("leveling route enters an unsafe encounter area")

func (n *LevelingNavigator) Next(ctx context.Context, s aigame.Snapshot, q aileveling.NavigationRequest) (aileveling.Navigation, error) {
	if n.Knowledge == nil || n.Tiles == nil {
		return aileveling.Navigation{}, aileveling.ErrNoNavigation
	}
	if err := ctx.Err(); err != nil {
		return aileveling.Navigation{}, err
	}

	type candidate struct {
		area  aiknowledge.LevelingArea
		score int
	}
	level := int(s.Player.Level)
	currentFloor := int(s.Position.Floor)
	candidates := []candidate{}
	for _, a := range n.Knowledge.Areas() {
		if !a.Verified || (q.AreaID != 0 && a.ID != q.AreaID) {
			continue
		}
		// A leveling area must contain at least one known encounter and stay
		// within one level above the observed character. This admits the
		// verified level-1 area whose enemies range from 1 to 2 while keeping
		// high-level areas unavailable to a low-level character.
		if len(a.EnemyIDs) == 0 || a.Levels.Max > level+1 || a.Levels.Min > level {
			continue
		}
		distance := 0
		if a.Floor == currentFloor {
			x := clamp(int(s.Position.X), a.Bounds.X, a.Bounds.X2)
			y := clamp(int(s.Position.Y), a.Bounds.Y, a.Bounds.Y2)
			distance = abs(x-int(s.Position.X)) + abs(y-int(s.Position.Y))
		}
		candidates = append(candidates, candidate{area: a, score: (level-a.Levels.Max)*100 + distance})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score < candidates[j].score })

	from := ainavigation.Point{X: int(s.Position.X), Y: int(s.Position.Y)}
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return aileveling.Navigation{}, err
		}
		a := c.area
		if a.Floor == currentFloor {
			if navigation, ok, err := n.nextOnFloor(ctx, s, a, from); err != nil {
				return aileveling.Navigation{}, err
			} else if ok {
				return navigation, nil
			}
			continue
		}
		if navigation, ok, err := n.nextAcrossFloors(ctx, s, a, from); err != nil {
			return aileveling.Navigation{}, err
		} else if ok {
			return navigation, nil
		}
	}
	return aileveling.Navigation{}, aileveling.ErrNoNavigation
}

func (n *LevelingNavigator) nextOnFloor(ctx context.Context, s aigame.Snapshot, a aiknowledge.LevelingArea, from ainavigation.Point) (aileveling.Navigation, bool, error) {
	points := levelingApproachPoints(a, from)
	for _, to := range points {
		if err := ctx.Err(); err != nil {
			return aileveling.Navigation{}, false, err
		}
		if occupied(s, to) {
			continue
		}
		route, err := n.routeLeveling(ctx, a.Floor, from, to, int(s.Player.Level))
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return aileveling.Navigation{}, false, err
			}
			continue
		}
		if route.Empty() {
			continue
		}
		return shortLevelingNavigation(route, int(s.Position.Floor)), true, nil
	}
	return aileveling.Navigation{}, false, nil
}

func (n *LevelingNavigator) nextAcrossFloors(ctx context.Context, s aigame.Snapshot, a aiknowledge.LevelingArea, from ainavigation.Point) (aileveling.Navigation, bool, error) {
	graph := n.levelingWarpGraph(a.Floor)
	if graph == nil || len(graph.Edges()) == 0 {
		return aileveling.Navigation{}, false, nil
	}
	for _, to := range levelingApproachPoints(a, from) {
		if err := ctx.Err(); err != nil {
			return aileveling.Navigation{}, false, err
		}
		// Validate the requested final tile before traversing the static warp
		// graph. Encounter rectangles often contain decorative or blocked
		// cells at their corners; allowing those candidates into the graph
		// search makes every incoming warp repeatedly explore the whole map.
		// RouteContext validates an identity route's endpoints, so this works
		// with the narrow TileNavigator interface without adding a collision
		// API or guessing from coordinates.
		if _, err := n.routeLeveling(ctx, a.Floor, to, to, int(s.Player.Level)); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return aileveling.Navigation{}, false, err
			}
			continue
		}
		edges, firstRoute, err := n.findSafeCrossMapRoute(ctx, graph,
			aiknowledge.Point{Floor: int(s.Position.Floor), X: from.X, Y: from.Y},
			aiknowledge.Point{Floor: a.Floor, X: to.X, Y: to.Y}, int(s.Player.Level))
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return aileveling.Navigation{}, false, err
			}
			continue
		}
		if len(edges) == 0 {
			continue
		}
		if firstRoute.Empty() {
			// The preceding W segment has already been acknowledged at this
			// exact source tile. A native EV is a separate client request; leave
			// Route empty so the coordinator submits it on this tick and never
			// treats W as an implicit floor transition.
			return aileveling.Navigation{
				Ready:                true,
				Destination:          aigame.Point{Floor: int32(edges[0].From.Floor), X: int32(edges[0].From.X), Y: int32(edges[0].From.Y)},
				WarpDestination:      aigame.Point{Floor: int32(edges[0].To.Floor), X: int32(edges[0].To.X), Y: int32(edges[0].To.Y)},
				WarpDestinationKnown: true,
				WarpEventType:        aigame.MapEventWarp,
				CostKnown:            true,
				Reason:               "verified map event source reached",
			}, true, nil
		}
		navigation := shortLevelingNavigation(firstRoute, int(s.Position.Floor))
		// Only the final packet needed to enter the first verified warp source
		// can transition floors. If the server applies the warp before the next
		// observation, the source tile will never be reported; carry the exact
		// known destination for that packet. Longer routes must be acknowledged
		// at their current-floor intermediate endpoint first.
		if len(firstRoute.Directions) <= 4 {
			navigation.WarpDestination = aigame.Point{Floor: int32(edges[0].To.Floor), X: int32(edges[0].To.X), Y: int32(edges[0].To.Y)}
			navigation.WarpDestinationKnown = true
		}
		return navigation, true, nil
	}
	return aileveling.Navigation{}, false, nil
}

type levelingCrossMapSearchState struct {
	point      aiknowledge.Point
	edges      []aiplanner.WarpEdge
	firstRoute ainavigation.Route
}

// findSafeCrossMapRoute searches static warp edges while proving every ground
// connection. In addition to the path to each source, it checks the exact
// destination point after every warp and the complete ground route from that
// destination to the requested target. This keeps a short first packet from
// hiding a later unsafe encounter area and allows another warp path to win
// when the shortest static path is unsafe.
func (n *LevelingNavigator) findSafeCrossMapRoute(ctx context.Context, graph *aiplanner.WarpGraph, from, to aiknowledge.Point, playerLevel int) ([]aiplanner.WarpEdge, ainavigation.Route, error) {
	if graph == nil {
		return nil, ainavigation.Route{}, ErrCrossMapUnavailable
	}
	byFloor := make(map[int][]aiplanner.WarpEdge)
	for _, edge := range graph.Edges() {
		if !isUnconditionalZeroCostWarp(edge) || !strings.EqualFold(strings.TrimSpace(edge.Time), "NULL") || !validKnowledgePoint(edge.From) || !validKnowledgePoint(edge.To) || edge.From == edge.To {
			continue
		}
		byFloor[edge.From.Floor] = append(byFloor[edge.From.Floor], edge)
	}
	if len(byFloor) == 0 {
		return nil, ainavigation.Route{}, ErrCrossMapUnavailable
	}

	queue := []levelingCrossMapSearchState{{point: from}}
	visited := map[aiknowledge.Point]int{from: 0}
	limit := n.MaxWarpEdges
	if limit <= 0 {
		limit = defaultMaxWarpEdges
	}
	if limit > maxWarpEdges {
		limit = maxWarpEdges
	}
	limitReached := false
	for head := 0; head < len(queue); head++ {
		if err := ctx.Err(); err != nil {
			return nil, ainavigation.Route{}, err
		}
		state := queue[head]
		for _, edge := range byFloor[state.point.Floor] {
			if err := ctx.Err(); err != nil {
				return nil, ainavigation.Route{}, err
			}
			pathLength := len(state.edges) + 1
			if pathLength > limit {
				limitReached = true
				continue
			}
			firstRoute, err := n.routeLeveling(ctx, state.point.Floor,
				ainavigation.Point{X: state.point.X, Y: state.point.Y},
				ainavigation.Point{X: edge.From.X, Y: edge.From.Y}, playerLevel)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, ainavigation.Route{}, err
				}
				continue
			}
			path := append(append([]aiplanner.WarpEdge(nil), state.edges...), edge)
			firstPath := state.firstRoute
			if len(state.edges) == 0 {
				firstPath = firstRoute
			}
			// A warp destination is an authoritative position sample, so it must
			// pass the same effective encounter-row check as a ground endpoint.
			if !n.encounterPointSafe(edge.To.Floor, edge.To.X, edge.To.Y, playerLevel) {
				continue
			}
			if edge.To.Floor == to.Floor {
				if _, err := n.routeLeveling(ctx, edge.To.Floor,
					ainavigation.Point{X: edge.To.X, Y: edge.To.Y},
					ainavigation.Point{X: to.X, Y: to.Y}, playerLevel); err == nil {
					return path, firstPath, nil
				} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, ainavigation.Route{}, err
				}
			}
			if previous, ok := visited[edge.To]; ok && previous <= pathLength {
				continue
			}
			visited[edge.To] = pathLength
			queue = append(queue, levelingCrossMapSearchState{point: edge.To, edges: path, firstRoute: firstPath})
		}
	}
	if limitReached {
		return nil, ainavigation.Route{}, fmt.Errorf("%w: no route within %d warp edges", ErrCrossMapWarpLimit, limit)
	}
	return nil, ainavigation.Route{}, ErrCrossMapUnavailable
}

// routeLeveling proves a complete ground route for leveling. It first uses
// the normal shortest route, then asks an ainavigation.Navigator for a route
// with unsafe encounter cells blocked when the shortest route is rejected.
// The optional interface preserves compatibility with existing TileNavigator
// adapters while enabling a real collision-aware detour where available.
func (n *LevelingNavigator) routeLeveling(ctx context.Context, floor int, from, to ainavigation.Point, playerLevel int) (ainavigation.Route, error) {
	if !n.encounterPointSafe(floor, to.X, to.Y, playerLevel) {
		return ainavigation.Route{}, errUnsafeLevelingRoute
	}
	route, err := n.Tiles.RouteContext(ctx, floor, from, to)
	if err != nil {
		return ainavigation.Route{}, err
	}
	if err := validateLevelingRoute(route, from, to); err != nil {
		return ainavigation.Route{}, err
	}
	if n.routeEncounterSafe(route, floor, len(route.Points), playerLevel) {
		return route, nil
	}

	optionsNavigator, ok := n.Tiles.(tileRouteOptionsNavigator)
	if !ok {
		return ainavigation.Route{}, errUnsafeLevelingRoute
	}
	safeRoute, err := optionsNavigator.RouteContextWithOptions(ctx, floor, from, to, ainavigation.RouteOptions{
		Blocked: func(point ainavigation.Point) bool {
			return !n.encounterPointSafe(floor, point.X, point.Y, playerLevel)
		},
	})
	if err != nil {
		return ainavigation.Route{}, err
	}
	if err := validateLevelingRoute(safeRoute, from, to); err != nil {
		return ainavigation.Route{}, err
	}
	if !n.routeEncounterSafe(safeRoute, floor, len(safeRoute.Points), playerLevel) {
		return ainavigation.Route{}, errUnsafeLevelingRoute
	}
	return safeRoute, nil
}

// encounterPointSafe checks the exact effective encounter row at one point.
// EncounterAt mirrors the server's Z-order lookup, so overlapping source
// rectangles do not get combined into a fictional risk profile. A row whose
// effective probability is zero is safe even when its group data is absent;
// any positive-probability row still requires a verified leveling area.
func (n *LevelingNavigator) encounterPointSafe(floor, x, y, playerLevel int) bool {
	row, ok := n.Knowledge.EncounterAt(floor, x, y)
	if !ok || row.EncounterProbability.Max <= 0 {
		return true
	}
	area, ok := n.Knowledge.FindArea(row.ID)
	return ok && area.Verified && len(area.EnemyIDs) > 0 && area.Levels.Max > 0 && area.Levels.Min <= playerLevel && area.Levels.Max <= playerLevel+1
}

// routeEncounterSafe checks the complete ground route, rather than only the
// packet prefix that will be submitted on the current tick.
func (n *LevelingNavigator) routeEncounterSafe(route ainavigation.Route, floor, steps, playerLevel int) bool {
	if steps <= 0 {
		return true
	}
	if steps > len(route.Points) {
		return false
	}
	for _, point := range route.Points[:steps] {
		if !n.encounterPointSafe(floor, point.X, point.Y, playerLevel) {
			return false
		}
	}
	return true
}

func (n *LevelingNavigator) levelingWarpGraph(targetFloor int) *aiplanner.WarpGraph {
	var source []aiplanner.WarpEdge
	if n.WarpGraph != nil {
		source = n.WarpGraph.Edges()
	} else {
		for _, warp := range n.Knowledge.Warps {
			source = append(source, aiplanner.WarpEdge{Type: warp.Type, Time: warp.Time, From: warp.From, To: warp.To, Attribute: warp.Attribute, Source: warp.Source})
		}
	}
	// Observation has no time-of-day field. Unknown time therefore means that
	// only an explicitly unconditional NULL warp is safe to select.
	allowed := make([]aiplanner.WarpEdge, 0, len(source))
	for _, edge := range source {
		if isUnconditionalZeroCostWarp(edge) && strings.EqualFold(strings.TrimSpace(edge.Time), "NULL") && validKnowledgePoint(edge.From) && validKnowledgePoint(edge.To) && edge.From != edge.To {
			allowed = append(allowed, edge)
		}
	}
	if len(allowed) == 0 {
		return nil
	}
	// Discard floors that cannot reach the selected area floor in the static
	// graph. This keeps a real map with many unrelated exits from triggering
	// an unbounded number of expensive tile-route probes.
	canReach := map[int]bool{targetFloor: true}
	changed := true
	for changed {
		changed = false
		for _, edge := range allowed {
			if canReach[edge.To.Floor] && !canReach[edge.From.Floor] {
				canReach[edge.From.Floor] = true
				changed = true
			}
		}
	}
	warps := make([]aiknowledge.MapWarp, 0, len(allowed))
	for _, edge := range allowed {
		if canReach[edge.From.Floor] && canReach[edge.To.Floor] {
			warps = append(warps, aiknowledge.MapWarp{Type: edge.Type, Time: edge.Time, From: edge.From, To: edge.To, Attribute: edge.Attribute, Source: edge.Source})
		}
	}
	if len(warps) == 0 {
		return nil
	}
	return aiplanner.NewWarpGraphFromWarps(warps)
}

func levelingApproachPoints(a aiknowledge.LevelingArea, from ainavigation.Point) []ainavigation.Point {
	points := make([]ainavigation.Point, 0, 6)
	if a.Bounds.Contains(from.X, from.Y) {
		for _, delta := range []ainavigation.Point{{X: 1}, {Y: 1}, {X: -1}, {Y: -1}, {X: 1, Y: 1}, {X: -1, Y: 1}, {X: -1, Y: -1}, {X: 1, Y: -1}} {
			point := ainavigation.Point{X: from.X + delta.X, Y: from.Y + delta.Y}
			if a.Bounds.Contains(point.X, point.Y) {
				points = append(points, point)
			}
		}
		return points
	}
	points = append(points,
		ainavigation.Point{X: clamp(from.X, a.Bounds.X, a.Bounds.X2), Y: clamp(from.Y, a.Bounds.Y, a.Bounds.Y2)},
		ainavigation.Point{X: (a.Bounds.X + a.Bounds.X2) / 2, Y: (a.Bounds.Y + a.Bounds.Y2) / 2})
	for _, x := range []int{a.Bounds.X, a.Bounds.X2} {
		for _, y := range []int{a.Bounds.Y, a.Bounds.Y2} {
			points = append(points, ainavigation.Point{X: x, Y: y})
		}
	}
	return points
}

func validateLevelingRoute(route ainavigation.Route, from, to ainavigation.Point) error {
	if len(route.Directions) != len(route.Points) {
		return errors.New("navigation returned invalid route")
	}
	if from == to {
		if !route.Empty() {
			return errors.New("navigation returned invalid identity route")
		}
		return nil
	}
	if route.Empty() || route.Points[len(route.Points)-1] != to {
		return errors.New("navigation returned invalid route destination")
	}
	return nil
}

func shortLevelingNavigation(route ainavigation.Route, floor int) aileveling.Navigation {
	length := len(route.Directions)
	if length > 4 {
		length = 4
	}
	end := route.Points[length-1]
	return aileveling.Navigation{Ready: true, Route: route.Directions[:length], Destination: aigame.Point{Floor: int32(floor), X: int32(end.X), Y: int32(end.Y)}, CostKnown: true}
}

func occupied(s aigame.Snapshot, p ainavigation.Point) bool {
	for _, a := range s.Actors {
		if a.ID != s.Player.ID && a.X == int32(p.X) && a.Y == int32(p.Y) {
			return true
		}
	}
	return false
}

func clamp(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

var _ aileveling.Navigator = (*LevelingNavigator)(nil)
