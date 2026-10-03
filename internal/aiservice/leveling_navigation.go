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
	Knowledge     *aiknowledge.Knowledge
	Tiles         TileNavigator
	nonSpawning   map[int]bool
	occupiedFloor int
	occupiedTiles map[ainavigation.Point]bool
	// Scoped to one Next calculation. Never reuse across observations:
	// level, encounter policy and visible occupancy can all change.
	encounterRows map[int][]int
	safeRows      map[int]bool
	routes        map[levelingRouteKey]levelingRouteResult

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

type tileRegionNavigator interface {
	RouteToAny(context.Context, int, ainavigation.Point, func(ainavigation.Point) bool, ainavigation.RouteOptions) (ainavigation.Route, error)
}

var errUnsafeLevelingRoute = errors.New("leveling route enters an unsafe encounter area")

func (n *LevelingNavigator) Next(ctx context.Context, s aigame.Snapshot, q aileveling.NavigationRequest) (aileveling.Navigation, error) {
	return n.next(ctx, s, q, nil)
}

// NextForPets uses the same collision, effective encounter and route checks as
// leveling, but selects areas containing an eligible requested native species.
func (n *LevelingNavigator) NextForPets(ctx context.Context, s aigame.Snapshot, targets []PetCollectionTarget) (aileveling.Navigation, error) {
	if n.Knowledge == nil {
		return aileveling.Navigation{}, aileveling.ErrNoNavigation
	}
	wanted := map[int]bool{}
	for _, enemy := range n.Knowledge.EnemiesTable {
		for _, target := range targets {
			if enemy.TemplateID == int(target.SpeciesID) && enemy.PetFlag != 0 && enemy.Levels.Min <= int(target.MaximumLevel) && enemy.Levels.Max >= int(target.MinimumLevel) {
				wanted[enemy.ID] = true
			}
		}
	}
	return n.next(ctx, s, aileveling.NavigationRequest{}, func(a aiknowledge.LevelingArea) bool {
		for _, id := range a.EnemyIDs {
			if wanted[id] {
				return true
			}
		}
		return false
	})
}

func (n *LevelingNavigator) next(ctx context.Context, s aigame.Snapshot, q aileveling.NavigationRequest, eligible func(aiknowledge.LevelingArea) bool) (aileveling.Navigation, error) {
	if n.Knowledge == nil || n.Tiles == nil {
		return aileveling.Navigation{}, aileveling.ErrNoNavigation
	}
	if err := ctx.Err(); err != nil {
		return aileveling.Navigation{}, err
	}
	// Bind only this calculation to the current server observation. A table
	// reload, missing extension, or mismatch revokes the previous exemption.
	bound := *n
	bound.nonSpawning = n.Knowledge.NonSpawningEncounters(s.AI.EncounterPolicy)
	bound.encounterRows = make(map[int][]int)
	bound.safeRows = make(map[int]bool)
	bound.routes = make(map[levelingRouteKey]levelingRouteResult)
	for index, row := range n.Knowledge.Encounters {
		if row.ZOrder > 0 {
			bound.encounterRows[row.Floor] = append(bound.encounterRows[row.Floor], index)
			bound.safeRows[index] = bound.encounterRowSafe(index, int(s.Player.Level))
		}
	}
	bound.occupiedFloor = int(s.Position.Floor)
	bound.occupiedTiles = map[ainavigation.Point]bool{}
	for _, actor := range s.Actors {
		if actorBlocksMovement(actor.Kind, int(actor.CharType), actor.GraphicKnown, int(actor.Graphic)) && (actor.X != s.Position.X || actor.Y != s.Position.Y) {
			bound.occupiedTiles[ainavigation.Point{X: int(actor.X), Y: int(actor.Y)}] = true
		}
	}
	n = &bound

	type candidate struct {
		area  aiknowledge.LevelingArea
		score int
		hops  int
	}
	level := int(s.Player.Level)
	currentFloor := int(s.Position.Floor)
	floorHops, entrances := n.reachableLevelingFloors(currentFloor)
	candidates := []candidate{}
	for _, a := range n.Knowledge.Areas() {
		if eligible != nil && !eligible(a) {
			continue
		}
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
		hops, reachable := floorHops[a.Floor]
		if !reachable {
			continue
		}
		distance := 0
		if a.Floor == currentFloor {
			x := clamp(int(s.Position.X), a.Bounds.X, a.Bounds.X2)
			y := clamp(int(s.Position.Y), a.Bounds.Y, a.Bounds.Y2)
			distance = abs(x-int(s.Position.X)) + abs(y-int(s.Position.Y))
		} else {
			distance = int(^uint(0) >> 1)
			for _, entrance := range entrances[a.Floor] {
				d := abs(clamp(entrance.X, a.Bounds.X, a.Bounds.X2)-entrance.X) + abs(clamp(entrance.Y, a.Bounds.Y, a.Bounds.Y2)-entrance.Y)
				if d < distance {
					distance = d
				}
			}
		}
		candidates = append(candidates, candidate{area: a, score: (level-a.Levels.Max)*100 + distance, hops: hops})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].hops != candidates[j].hops {
			return candidates[i].hops < candidates[j].hops
		}
		return candidates[i].score < candidates[j].score
	})

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
	if _, ok := n.Tiles.(tileRegionNavigator); ok {
		route, err := n.routeLevelingArea(ctx, a, from, int(s.Player.Level))
		if err == nil {
			return shortLevelingNavigation(route, a.Floor), true, nil
		}
		if ctx.Err() != nil {
			return aileveling.Navigation{}, false, ctx.Err()
		}
		return aileveling.Navigation{}, false, nil
	}
	points := levelingApproachPoints(a, from)
	for _, to := range points {
		if err := ctx.Err(); err != nil {
			return aileveling.Navigation{}, false, err
		}
		if occupied(s, to) {
			continue
		}
		if !n.levelingAreaActiveAt(a, to) {
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
	if _, ok := n.Tiles.(tileRegionNavigator); ok {
		edges, firstRoute, err := n.findSafeCrossMapRouteTo(ctx, graph, aiknowledge.Point{Floor: int(s.Position.Floor), X: from.X, Y: from.Y}, a.Floor, int(s.Player.Level), func(entry ainavigation.Point) error {
			_, err := n.routeLevelingArea(ctx, a, entry, int(s.Player.Level))
			return err
		})
		if err != nil {
			if ctx.Err() != nil {
				return aileveling.Navigation{}, false, ctx.Err()
			}
			return aileveling.Navigation{}, false, nil
		}
		return crossMapLevelingNavigation(edges, firstRoute), true, nil
	}
	for _, to := range levelingApproachPoints(a, from) {
		if err := ctx.Err(); err != nil {
			return aileveling.Navigation{}, false, err
		}
		if !n.levelingAreaActiveAt(a, to) {
			continue
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
		return crossMapLevelingNavigation(edges, firstRoute), true, nil
	}
	return aileveling.Navigation{}, false, nil
}

func crossMapLevelingNavigation(edges []aiplanner.WarpEdge, firstRoute ainavigation.Route) aileveling.Navigation {
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
		}
	}
	navigation := shortLevelingNavigation(firstRoute, edges[0].From.Floor)
	// Only the final packet needed to enter the first verified warp source
	// can transition floors. If the server applies the warp before the next
	// observation, the source tile will never be reported; carry the exact
	// known destination for that packet. Longer routes must be acknowledged
	// at their current-floor intermediate endpoint first.
	if len(firstRoute.Directions) <= 4 {
		navigation.WarpDestination = aigame.Point{Floor: int32(edges[0].To.Floor), X: int32(edges[0].To.X), Y: int32(edges[0].To.Y)}
		navigation.WarpDestinationKnown = true
	}
	return navigation
}

func (n *LevelingNavigator) routeLevelingArea(ctx context.Context, a aiknowledge.LevelingArea, from ainavigation.Point, level int) (ainavigation.Route, error) {
	return n.Tiles.(tileRegionNavigator).RouteToAny(ctx, a.Floor, from, func(p ainavigation.Point) bool {
		return p != from && a.Bounds.Contains(p.X, p.Y) && n.levelingAreaActiveAt(a, p)
	}, ainavigation.RouteOptions{Blocked: func(p ainavigation.Point) bool {
		return n.pointOccupied(a.Floor, p) || !n.encounterPointSafe(a.Floor, p.X, p.Y, level)
	}})
}

// Encounter rectangles overlap. The server rolls only the effective row at
// the chosen tile; a lower-priority rectangle containing the desired species
// is not sufficient evidence that those pets can actually spawn there.
func (n *LevelingNavigator) levelingAreaActiveAt(a aiknowledge.LevelingArea, point ainavigation.Point) bool {
	if len(n.Knowledge.Encounters) == 0 {
		return true
	} // Narrow synthetic navigation adapters.
	index, ok := n.Knowledge.EncounterIndexAt(a.Floor, point.X, point.Y)
	if !ok || n.nonSpawning[index] || n.Knowledge.Encounters[index].EncounterProbability.Max <= 0 {
		return false
	}
	effective, ok := n.Knowledge.FindEncounterArea(index)
	return ok && effective.ID == a.ID && effective.Floor == a.Floor && effective.Bounds == a.Bounds
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
	return n.findSafeCrossMapRouteTo(ctx, graph, from, to.Floor, playerLevel, func(entry ainavigation.Point) error {
		_, err := n.routeLeveling(ctx, to.Floor, entry, ainavigation.Point{X: to.X, Y: to.Y}, playerLevel)
		return err
	})
}

func (n *LevelingNavigator) findSafeCrossMapRouteTo(ctx context.Context, graph *aiplanner.WarpGraph, from aiknowledge.Point, targetFloor, playerLevel int, finish func(ainavigation.Point) error) ([]aiplanner.WarpEdge, ainavigation.Route, error) {
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
			// A previously reached destination already had its final leg
			// checked. Proving another path to the same point cannot improve
			// this breadth-first search; avoid repeating large tile searches.
			if previous, ok := visited[edge.To]; ok && previous <= pathLength {
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
			if edge.To.Floor == targetFloor {
				if err := finish(ainavigation.Point{X: edge.To.X, Y: edge.To.Y}); err == nil {
					return path, firstPath, nil
				} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, ainavigation.Route{}, err
				}
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
type levelingRouteKey struct {
	floor, level int
	from, to     ainavigation.Point
}

type levelingRouteResult struct {
	route ainavigation.Route
	err   error
}

func (n *LevelingNavigator) routeLeveling(ctx context.Context, floor int, from, to ainavigation.Point, playerLevel int) (ainavigation.Route, error) {
	if err := ctx.Err(); err != nil {
		return ainavigation.Route{}, err
	}
	key := levelingRouteKey{floor: floor, level: playerLevel, from: from, to: to}
	if cached, ok := n.routes[key]; ok {
		return cached.route, cached.err
	}
	route, err := n.computeLevelingRoute(ctx, floor, from, to, playerLevel)
	// Reuse only within this observation. Limit memory even on unusual
	// warp graphs; reaching the cap affects speed, never route validity.
	if n.routes != nil && len(n.routes) < 512 && len(route.Points) <= 4096 && ctx.Err() == nil {
		n.routes[key] = levelingRouteResult{route: route, err: err}
	}
	return route, err
}

func (n *LevelingNavigator) computeLevelingRoute(ctx context.Context, floor int, from, to ainavigation.Point, playerLevel int) (ainavigation.Route, error) {
	if n.pointOccupied(floor, to) || !n.encounterPointSafe(floor, to.X, to.Y, playerLevel) {
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
			return n.pointOccupied(floor, point) || !n.encounterPointSafe(floor, point.X, point.Y, playerLevel)
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
	if n.encounterRows != nil {
		selected := -1
		for _, index := range n.encounterRows[floor] {
			row := n.Knowledge.Encounters[index]
			if row.Bounds.Contains(x, y) && (selected < 0 || row.ZOrder > n.Knowledge.Encounters[selected].ZOrder) {
				selected = index
			}
		}
		return selected < 0 || n.safeRows[selected]
	}
	index, ok := n.Knowledge.EncounterIndexAt(floor, x, y)
	if !ok {
		return true
	}
	return n.encounterRowSafe(index, playerLevel)
}

func (n *LevelingNavigator) encounterRowSafe(index, playerLevel int) bool {
	row := n.Knowledge.Encounters[index]
	if row.EncounterProbability.Max <= 0 {
		return true
	}
	if n.nonSpawning[index] {
		return true
	}
	area, ok := n.Knowledge.FindEncounterArea(index)
	return ok && area.Verified && len(area.EnemyIDs) > 0 && area.Levels.Max > 0 && area.Levels.Min <= playerLevel && area.Levels.Max <= playerLevel+1
}

// routeEncounterSafe checks the complete ground route, rather than only the
// packet prefix that will be submitted on the current tick.
func (n *LevelingNavigator) pointOccupied(floor int, p ainavigation.Point) bool {
	return floor == n.occupiedFloor && n.occupiedTiles[p]
}

func (n *LevelingNavigator) routeEncounterSafe(route ainavigation.Route, floor, steps, playerLevel int) bool {
	if steps <= 0 {
		return true
	}
	if steps > len(route.Points) {
		return false
	}
	for _, point := range route.Points[:steps] {
		if n.pointOccupied(floor, point) || !n.encounterPointSafe(floor, point.X, point.Y, playerLevel) {
			return false
		}
	}
	return true
}

func (n *LevelingNavigator) levelingWarpEdges() []aiplanner.WarpEdge {
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
	return allowed
}

// Rank reachable nearby floors before distant areas. This is only a search
// heuristic: every selected ground segment and warp still needs the complete
// collision, occupancy and encounter checks below.
func (n *LevelingNavigator) reachableLevelingFloors(current int) (map[int]int, map[int][]aiknowledge.Point) {
	byFloor := map[int][]aiplanner.WarpEdge{}
	for _, edge := range n.levelingWarpEdges() {
		byFloor[edge.From.Floor] = append(byFloor[edge.From.Floor], edge)
	}
	hops := map[int]int{current: 0}
	entrances := map[int][]aiknowledge.Point{}
	queue := []int{current}
	for head := 0; head < len(queue); head++ {
		floor := queue[head]
		for _, edge := range byFloor[floor] {
			next := hops[floor] + 1
			previous, seen := hops[edge.To.Floor]
			if !seen {
				hops[edge.To.Floor] = next
				queue = append(queue, edge.To.Floor)
			}
			if !seen || previous == next {
				entrances[edge.To.Floor] = append(entrances[edge.To.Floor], edge.To)
			}
		}
	}
	return hops, entrances
}

func (n *LevelingNavigator) levelingWarpGraph(targetFloor int) *aiplanner.WarpGraph {
	allowed := n.levelingWarpEdges()
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
