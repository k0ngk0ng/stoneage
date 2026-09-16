package aiservice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
)

// Cross-map movement is deliberately conservative. A mapwarp source is first
// reached with W, then the native EV request is issued once for the verified
// map object. The server's EV(sequence,result) and subsequent position sample
// are both required before the route advances.
var (
	// ErrCrossMapUnavailable means that the knowledge snapshot does not prove
	// a legal, unconditional, zero-cost warp route for the requested move.
	ErrCrossMapUnavailable = errors.New("aiservice: cross-map movement unavailable")
	// ErrCrossMapConfirmation means that the server did not confirm the exact
	// destination of a known warp, or reported a position incompatible with
	// the warp that was being confirmed.
	ErrCrossMapConfirmation = errors.New("aiservice: cross-map warp confirmation failed")
	// ErrCrossMapWarpLimit means that a static route exceeded the configured
	// bound on automatic warp transitions.
	ErrCrossMapWarpLimit = errors.New("aiservice: cross-map warp limit exceeded")
)

const (
	// The native map object stores these event values in CHAR_EVENT. Keep the
	// mapping beside the executor so a static warp edge cannot silently turn
	// into an arbitrary EV request.
	mapEventTypeUnconditional int32 = aigame.MapEventWarp
	mapEventTypeMorning       int32 = aigame.MapEventWarpMorning
	mapEventTypeNoon          int32 = aigame.MapEventWarpNoon
	mapEventTypeNight         int32 = aigame.MapEventWarpNight
)

const (
	defaultMaxWarpEdges = 16
	maxWarpEdges        = 64
)

// executeCrossMap executes a complete route whose initial and final floors
// differ.  The planner supplies only static warp relations, so every From
// endpoint is reached with the normal tile navigator before the next edge is
// considered.
func (s *MovementSkill) executeCrossMap(ctx context.Context, observed aimcp.Observation, args movementArguments) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := movementObservationReady(observed); err != nil {
		return err
	}

	graph := s.WarpGraph
	if graph == nil && s.Backend != nil {
		// The backend knowledge is the reviewed source used by the gameplay
		// factory.  Tests and explicitly constructed skills may provide a
		// graph directly so that this executor remains deterministic.
		graph = aiplanner.NewWarpGraph(s.Backend.Knowledge)
	}
	if graph == nil {
		return ErrCrossMapUnavailable
	}

	from := aiknowledge.Point{Floor: observed.Floor, X: observed.X, Y: observed.Y}
	to := aiknowledge.Point{Floor: args.Floor, X: args.X, Y: args.Y}
	edges, err := s.findCrossMapRoute(ctx, graph, from, to)
	if err != nil {
		return err
	}
	if len(edges) == 0 {
		return fmt.Errorf("%w: no reachable warp route", ErrCrossMapUnavailable)
	}

	current := observed
	for _, edge := range edges {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := movementObservationReady(current); err != nil {
			return err
		}
		// A warp changes floors without a W action, so the common movement
		// health boundary cannot see the destination on its own. Check before
		// walking to the source as well: the server may apply the warp as part
		// of the final W packet, and the source may already be current.
		if err := s.checkTravelHealthAt(current, edge.To.Floor, edge.To.X, edge.To.Y); err != nil {
			return err
		}

		var warped bool
		current, warped, err = s.moveToWarpSource(ctx, current, edge)
		if err != nil {
			return err
		}
		if warped {
			if observationPoint(current) != edge.To {
				return warpMismatch(edge, current)
			}
		} else if observationPoint(current) != edge.From {
			return warpMismatch(edge, current)
		}
		// Recheck the authoritative health sample after source movement. This
		// closes the gap if health changed while reaching the source, and also
		// validates a destination reached by an automatic source-tile warp.
		if err := s.checkTravelHealthAt(current, edge.To.Floor, edge.To.X, edge.To.Y); err != nil {
			return err
		}
		if !warped {
			if err := s.triggerWarpEvent(ctx, current, edge); err != nil {
				return err
			}
			confirmationContext, cancel := context.WithTimeout(ctx, s.warpConfirmationTimeout())
			current, err = s.waitWarpConfirmation(confirmationContext, current, edge)
			cancel()
			if err != nil {
				return err
			}
		}
		if observationPoint(current) != edge.To {
			return warpMismatch(edge, current)
		}
		if err := s.checkTravelHealthAt(current, edge.To.Floor, edge.To.X, edge.To.Y); err != nil {
			return err
		}
	}

	if current.Floor != args.Floor {
		return warpMismatch(aiplanner.WarpEdge{From: from, To: to}, current)
	}
	_, err = s.moveWithinFloor(ctx, current, args.Floor, ainavigation.Point{X: args.X, Y: args.Y})
	return err
}

type crossMapSearchState struct {
	point aiknowledge.Point
	edges []aiplanner.WarpEdge
}

// staticWalkabilityNavigator exposes the collision query implemented by the
// authoritative map navigator. Cross-map routing must check the requested
// destination through this API before searching warp edges: a route from a
// point to itself is intentionally empty, so using routeBetween(to, to) as
// that check would let a blocked destination pass without consulting the map.
type staticWalkabilityNavigator interface {
	Walkable(floorID, x, y int) bool
}

// findCrossMapRoute searches the known warp endpoints while asking the tile
// navigator to prove every ground connection.  WarpGraph.PlanWarp only links
// endpoints which are exactly equal; the game also permits walking from the
// current tile to the first endpoint, between endpoints on a floor, and from
// the last endpoint to the requested destination.  Those ground connections
// are therefore part of this executor's search rather than inferred from the
// static graph.
func (s *MovementSkill) findCrossMapRoute(ctx context.Context, graph *aiplanner.WarpGraph, from, to aiknowledge.Point) ([]aiplanner.WarpEdge, error) {
	if graph == nil {
		return nil, ErrCrossMapUnavailable
	}
	if err := validateWarpTimeSection(s.WarpTimeSection); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCrossMapUnavailable, err)
	}
	navigator, ok := s.Navigator.(staticWalkabilityNavigator)
	if !ok {
		return nil, fmt.Errorf("%w: tile navigator cannot prove destination %v is walkable", ErrCrossMapUnavailable, to)
	}
	if !navigator.Walkable(to.Floor, to.X, to.Y) {
		return nil, fmt.Errorf("%w: destination %v is not walkable", ErrCrossMapUnavailable, to)
	}
	limit := s.warpEdgeLimit()
	byFloor := make(map[int][]aiplanner.WarpEdge)
	for _, edge := range graph.Edges() {
		if !isUnconditionalZeroCostWarp(edge) || !warpTimeAllowed(edge.Time, s.WarpTimeSection) || !validKnowledgePoint(edge.From) || !validKnowledgePoint(edge.To) || edge.From == edge.To {
			continue
		}
		if s.SafeTravel && !navigator.Walkable(edge.To.Floor, edge.To.X, edge.To.Y) {
			continue
		}
		byFloor[edge.From.Floor] = append(byFloor[edge.From.Floor], edge)
	}
	if len(byFloor) == 0 {
		return nil, ErrCrossMapUnavailable
	}

	queue := []crossMapSearchState{{point: from}}
	visited := map[aiknowledge.Point]int{from: 0}
	limitReached := false
	for head := 0; head < len(queue); head++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		state := queue[head]
		for _, edge := range byFloor[state.point.Floor] {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			pathLength := len(state.edges) + 1
			if pathLength > limit {
				limitReached = true
				continue
			}
			if _, err := s.routeBetween(ctx, state.point, edge.From); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
				continue
			}
			path := append(append([]aiplanner.WarpEdge(nil), state.edges...), edge)
			if edge.To.Floor == to.Floor {
				if _, err := s.routeBetween(ctx, edge.To, to); err == nil {
					return path, nil
				} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil, err
				}
			}
			if previous, ok := visited[edge.To]; ok && previous <= pathLength {
				continue
			}
			visited[edge.To] = pathLength
			queue = append(queue, crossMapSearchState{point: edge.To, edges: path})
		}
	}
	if limitReached {
		return nil, fmt.Errorf("%w: no route within %d warp edges", ErrCrossMapWarpLimit, limit)
	}
	return nil, ErrCrossMapUnavailable
}

func (s *MovementSkill) routeBetween(ctx context.Context, from, to aiknowledge.Point) (ainavigation.Route, error) {
	if from.Floor != to.Floor {
		return ainavigation.Route{}, errors.New("ground route crosses floors")
	}
	if from.X == to.X && from.Y == to.Y {
		return ainavigation.Route{Floor: from.Floor, From: ainavigation.Point{X: from.X, Y: from.Y}, To: ainavigation.Point{X: to.X, Y: to.Y}}, nil
	}
	route, err := s.Navigator.RouteContext(ctx, from.Floor, ainavigation.Point{X: from.X, Y: from.Y}, ainavigation.Point{X: to.X, Y: to.Y})
	if err != nil {
		return ainavigation.Route{}, err
	}
	if len(route.Directions) != len(route.Points) {
		return ainavigation.Route{}, errors.New("invalid tile route")
	}
	if len(route.Points) == 0 || route.Points[len(route.Points)-1] != (ainavigation.Point{X: to.X, Y: to.Y}) {
		return ainavigation.Route{}, errors.New("invalid tile route destination")
	}
	return route, nil
}

func validateWarpTimeSection(section aiplanner.TimeSection) error {
	switch strings.ToUpper(strings.TrimSpace(string(section))) {
	case "", "UNKNOWN", "ANY", "NULL", "*", "M", "MORNING", "A", "NOON", "N", "NIGHT", "E", "EVENING":
		return nil
	default:
		return fmt.Errorf("invalid warp time section %q", section)
	}
}

func warpTimeAllowed(edgeTime string, section aiplanner.TimeSection) bool {
	timeName := strings.ToUpper(strings.TrimSpace(edgeTime))
	sectionName := strings.ToUpper(strings.TrimSpace(string(section)))
	if sectionName == "" || sectionName == "UNKNOWN" {
		sectionName = "UNKNOWN"
	}
	switch timeName {
	case "", "NULL":
		return true
	case "M":
		return sectionName == "M" || sectionName == "MORNING" || sectionName == "ANY" || sectionName == "*"
	case "A":
		return sectionName == "A" || sectionName == "NOON" || sectionName == "ANY" || sectionName == "*"
	case "N":
		return sectionName == "N" || sectionName == "NIGHT" || sectionName == "ANY" || sectionName == "*"
	default:
		return false
	}
}

func isUnconditionalZeroCostWarp(edge aiplanner.WarpEdge) bool {
	// PointType NONE is the only mapwarp type whose execution is known to be
	// an unconditional map event.  FREE and ERROR are deliberately rejected;
	// the attribute is also required to be absent or NULL so an unrecognised
	// charge/condition cannot be treated as free.
	return strings.TrimSpace(edge.Type) == "NONE" &&
		(edge.Attribute == "" || strings.EqualFold(strings.TrimSpace(edge.Attribute), "NULL"))
}

func validKnowledgePoint(point aiknowledge.Point) bool {
	return point.Floor >= 0 && point.X >= 0 && point.Y >= 0
}

func (s *MovementSkill) warpEdgeLimit() int {
	limit := s.MaxWarpEdges
	if limit <= 0 {
		limit = defaultMaxWarpEdges
	}
	if limit > maxWarpEdges {
		limit = maxWarpEdges
	}
	return limit
}

func (s *MovementSkill) warpConfirmationTimeout() time.Duration {
	timeout := s.WarpConfirmationTimeout
	if timeout <= 0 || timeout > 30*time.Second {
		return 5 * time.Second
	}
	return timeout
}

// moveToWarpSource walks to one edge.From. Only the final ground segment is
// allowed to observe edge.To directly, because a compatible server may apply
// the map warp as part of the same movement packet. On the stock GMSV the
// source sample is followed by triggerWarpEvent, and no W packet is sent while
// that event is pending confirmation.
func (s *MovementSkill) moveToWarpSource(ctx context.Context, observed aimcp.Observation, edge aiplanner.WarpEdge) (aimcp.Observation, bool, error) {
	if observed.Floor != edge.From.Floor {
		return observed, false, warpMismatch(edge, observed)
	}
	target := ainavigation.Point{X: edge.From.X, Y: edge.From.Y}
	if observed.X == target.X && observed.Y == target.Y {
		return observed, false, nil
	}
	route, err := s.Navigator.RouteContext(ctx, observed.Floor, ainavigation.Point{X: observed.X, Y: observed.Y}, target)
	if err != nil {
		return observed, false, err
	}
	if len(route.Directions) != len(route.Points) {
		return observed, false, errors.New("invalid tile route")
	}
	if len(route.Points) > 0 && route.Points[len(route.Points)-1] != target {
		return observed, false, fmt.Errorf("invalid tile route destination: got %v, want %v", route.Points[len(route.Points)-1], target)
	}
	if len(route.Directions) == 0 {
		return observed, false, nil
	}

	timeout := s.segmentTimeout()
	for offset := 0; offset < len(route.Directions); {
		if err := ctx.Err(); err != nil {
			return observed, false, err
		}
		if err := movementObservationReady(observed); err != nil {
			return observed, false, err
		}
		// A compatible server may apply this warp with the final W packet.
		// Check the destination immediately before every W so a health change
		// observed while walking to the source cannot bypass the warp guard.
		if err := s.checkTravelHealthAt(observed, edge.To.Floor, edge.To.X, edge.To.Y); err != nil {
			return observed, false, err
		}
		end := s.segmentEnd(observed, route, offset)
		segmentTarget := route.Points[end-1]
		if end == len(route.Directions) {
			segmentContext, cancel := context.WithTimeout(ctx, timeout)
			var submitErr error
			observed, submitErr = s.submitMove(segmentContext, observed, aigame.Move(int32(observed.X), int32(observed.Y), route.Directions[offset:end]), func(latest aimcp.Observation) error {
				return s.checkTravelHealthAt(latest, edge.To.Floor, edge.To.X, edge.To.Y)
			})
			if submitErr != nil {
				cancel()
				return observed, false, submitErr
			}
			var warped bool
			observed, warped, err = s.waitPositionAtWarpSource(segmentContext, edge, segmentTarget)
			cancel()
			if err != nil {
				return observed, false, err
			}
			return observed, warped, nil
		}

		segmentContext, cancel := context.WithTimeout(ctx, timeout)
		var submitErr error
		observed, submitErr = s.submitMove(segmentContext, observed, aigame.Move(int32(observed.X), int32(observed.Y), route.Directions[offset:end]), func(latest aimcp.Observation) error {
			return s.checkTravelHealthAt(latest, edge.To.Floor, edge.To.X, edge.To.Y)
		})
		if submitErr != nil {
			cancel()
			return observed, false, submitErr
		}
		observed, err = s.waitPosition(segmentContext, edge.From.Floor, segmentTarget)
		cancel()
		if err != nil {
			return observed, false, err
		}
		offset = end
	}
	return observed, false, nil
}

// waitPositionAtWarpSource accepts the source tile while the server is
// applying the event, or the exact destination if the event was applied before
// the first status sample.  It never submits another Move packet.
func (s *MovementSkill) waitPositionAtWarpSource(ctx context.Context, edge aiplanner.WarpEdge, sourceTarget ainavigation.Point) (aimcp.Observation, bool, error) {
	ticker := time.NewTicker(movementPollInterval(ctx))
	defer ticker.Stop()
	first := true
	last := aimcp.Observation{}
	for {
		if !first {
			select {
			case <-ctx.Done():
				return last, false, ctx.Err()
			case <-ticker.C:
			}
		}
		first = false
		if err := ctx.Err(); err != nil {
			return last, false, err
		}
		o, err := s.Backend.Observe(ctx, s.Backend.Binding)
		if err != nil {
			return o, false, err
		}
		last = o
		if err := movementObservationReady(o); err != nil {
			return o, false, err
		}
		if observationPoint(o) == edge.To {
			return o, true, nil
		}
		if o.Floor != edge.From.Floor {
			return o, false, warpMismatch(edge, o)
		}
		if o.X == sourceTarget.X && o.Y == sourceTarget.Y {
			return o, false, nil
		}
		// This is a read-only refresh.  The ground movement is intentionally
		// not retried when its completion sample is delayed.
		if err := s.submit(ctx, o.Revision, aigame.Action{Kind: aigame.ActionStatus, Command: "c"}); err != nil && !errors.Is(err, aigame.ErrStaleRevision) {
			return o, false, err
		}
	}
}

// triggerWarpEvent emits exactly one native EV after the server has confirmed
// the verified source tile. The legacy GMSV does not auto-dispatch mapwarp
// events after W; lssproto_EV_recv invokes EVENT_main and returns EV(seq,rc).
// A missing or negative acknowledgement is terminal for this route: EV is a
// side effect and must never be blindly replayed.
func (s *MovementSkill) triggerWarpEvent(ctx context.Context, observed aimcp.Observation, edge aiplanner.WarpEdge) error {
	acker, ok := s.Backend.Session.(interface {
		WaitForMapEvent(context.Context, int32) (aigame.Event, error)
	})
	if !ok {
		return fmt.Errorf("%w: game session cannot correlate EV acknowledgement", ErrCrossMapConfirmation)
	}
	if observationPoint(observed) != edge.From {
		return warpMismatch(edge, observed)
	}
	event, err := mapEventType(edge)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrCrossMapUnavailable, err)
	}
	sequence := aigame.NextMapEventSequence()
	for attempt := 0; ; attempt++ {
		err := s.submit(ctx, observed.Revision, aigame.MapEvent(event, sequence, int32(observed.X), int32(observed.Y), -1))
		if err == nil {
			break
		}
		// ErrStaleRevision is a pre-write rejection, so this sequence has
		// not been sent. Bound fresh-state attempts; all other errors and
		// every acknowledgement failure below remain non-replayable.
		if !errors.Is(err, aigame.ErrStaleRevision) || attempt >= 2 {
			return err
		}
		observed, err = s.Backend.Observe(ctx, s.Backend.Binding)
		if err != nil {
			return err
		}
		if err := movementObservationReady(observed); err != nil {
			return err
		}
		if observationPoint(observed) != edge.From {
			return warpMismatch(edge, observed)
		}
		if err := s.checkTravelHealthAt(observed, edge.To.Floor, edge.To.X, edge.To.Y); err != nil {
			return err
		}
	}
	ackContext, cancel := context.WithTimeout(ctx, s.warpConfirmationTimeout())
	ack, err := acker.WaitForMapEvent(ackContext, sequence)
	cancel()
	if err != nil {
		return fmt.Errorf("%w: EV sequence %d acknowledgement: %v", ErrCrossMapConfirmation, sequence, err)
	}
	if ack.Function != "EV" || len(ack.Fields) < 2 || ack.Fields[0].IntValue(0) != sequence {
		return fmt.Errorf("%w: malformed EV acknowledgement for sequence %d", ErrCrossMapConfirmation, sequence)
	}
	result := ack.Fields[1].IntValue(-1)
	if result != 1 {
		return fmt.Errorf("%w: EV sequence %d rejected with result %d", ErrCrossMapConfirmation, sequence, result)
	}
	return nil
}

// mapEventType is the same mapping used by MAPPOINT_setMapWarpFrom when it
// creates the native map object: NULL -> CHAR_EVENT_WARP, M/A/N -> the
// corresponding time-gated CHAR_EVENT value. Unknown selectors are rejected
// before any packet is written.
func mapEventType(edge aiplanner.WarpEdge) (int32, error) {
	switch strings.ToUpper(strings.TrimSpace(edge.Time)) {
	case "", "NULL":
		return mapEventTypeUnconditional, nil
	case "M", "MORNING":
		return mapEventTypeMorning, nil
	case "A", "NOON":
		return mapEventTypeNoon, nil
	case "N", "NIGHT":
		return mapEventTypeNight, nil
	default:
		return 0, fmt.Errorf("mapwarp source has unsupported event time %q", edge.Time)
	}
}

// waitWarpConfirmation waits for one exact authoritative destination sample.
// Before the destination arrives the only accepted position is edge.From;
// every other floor/coordinate is a confirmation mismatch.
func (s *MovementSkill) waitWarpConfirmation(ctx context.Context, observed aimcp.Observation, edge aiplanner.WarpEdge) (aimcp.Observation, error) {
	ticker := time.NewTicker(movementPollInterval(ctx))
	defer ticker.Stop()
	first := true
	for {
		if !first {
			select {
			case <-ctx.Done():
				return observed, ctx.Err()
			case <-ticker.C:
			}
		}
		first = false
		if err := ctx.Err(); err != nil {
			return observed, err
		}
		o, err := s.Backend.Observe(ctx, s.Backend.Binding)
		if err != nil {
			return o, err
		}
		observed = o
		if !o.Connected || !o.Ready || o.Battle.Active {
			return o, errors.New("movement interrupted before warp confirmation")
		}
		if observationPoint(o) == edge.To {
			return o, nil
		}
		if observationPoint(o) != edge.From {
			return o, warpMismatch(edge, o)
		}
		// Requesting a fresh status is safe to repeat.  A stale revision means
		// a server event raced the refresh; the next Observe decides whether
		// that event was the expected warp.
		if err := s.submit(ctx, o.Revision, aigame.Action{Kind: aigame.ActionStatus, Command: "c"}); err != nil && !errors.Is(err, aigame.ErrStaleRevision) {
			return o, err
		}
	}
}

func movementPollInterval(ctx context.Context) time.Duration {
	const normal = 200 * time.Millisecond
	interval := normal
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 && remaining/2 < interval {
			interval = remaining / 2
		}
	}
	if interval < time.Millisecond {
		return time.Millisecond
	}
	return interval
}

// moveWithinFloor is the existing segmented ground movement used after the
// final warp.  It returns the latest observation so callers can keep the
// revision fence current between segments.
func (s *MovementSkill) moveWithinFloor(ctx context.Context, observed aimcp.Observation, floor int, target ainavigation.Point) (aimcp.Observation, error) {
	if observed.Floor != floor {
		return observed, warpMismatch(aiplanner.WarpEdge{From: observationPoint(observed), To: aiknowledge.Point{Floor: floor, X: target.X, Y: target.Y}}, observed)
	}
	if observed.X == target.X && observed.Y == target.Y {
		return observed, nil
	}
	route, err := s.Navigator.RouteContext(ctx, floor, ainavigation.Point{X: observed.X, Y: observed.Y}, target)
	if err != nil {
		return observed, err
	}
	if len(route.Directions) != len(route.Points) {
		return observed, errors.New("invalid tile route")
	}
	if len(route.Points) > 0 && route.Points[len(route.Points)-1] != target {
		return observed, fmt.Errorf("invalid tile route destination: got %v, want %v", route.Points[len(route.Points)-1], target)
	}
	timeout := s.segmentTimeout()
	for offset := 0; offset < len(route.Directions); {
		if err := ctx.Err(); err != nil {
			return observed, err
		}
		if err := movementObservationReady(observed); err != nil {
			return observed, err
		}
		end := s.segmentEnd(observed, route, offset)
		segmentTarget := route.Points[end-1]
		var submitErr error
		observed, submitErr = s.submitMove(ctx, observed, aigame.Move(int32(observed.X), int32(observed.Y), route.Directions[offset:end]))
		if submitErr != nil {
			return observed, submitErr
		}
		segmentContext, cancel := context.WithTimeout(ctx, timeout)
		observed, err = s.waitPosition(segmentContext, floor, segmentTarget)
		cancel()
		if err != nil {
			return observed, err
		}
		offset = end
	}
	return observed, nil
}

func (s *MovementSkill) segmentTimeout() time.Duration {
	timeout := s.SegmentTimeout
	if timeout <= 0 || timeout > 30*time.Second {
		return 5 * time.Second
	}
	return timeout
}

func movementObservationReady(o aimcp.Observation) error {
	if !o.Connected || !o.Ready {
		return errors.New("movement interrupted by game state")
	}
	if o.Battle.Active {
		return ErrMovementBattle
	}
	return nil
}

func observationPoint(o aimcp.Observation) aiknowledge.Point {
	return aiknowledge.Point{Floor: o.Floor, X: o.X, Y: o.Y}
}

func warpMismatch(edge aiplanner.WarpEdge, observed aimcp.Observation) error {
	return fmt.Errorf("%w: expected warp %v -> %v, observed floor=%d (%d,%d)", ErrCrossMapConfirmation, edge.From, edge.To, observed.Floor, observed.X, observed.Y)
}
