package sacli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
)

// warpAckTimeout bounds the wait for the server's EV acknowledgement. The
// acknowledgement is what proves the warp was accepted; a warp is a side
// effect and is never replayed blindly.
const warpAckTimeout = 6 * time.Second

// warpConfirmTimeout bounds the wait for the destination floor to appear.
const warpConfirmTimeout = 8 * time.Second

// mapEventTypeForTime maps a mapwarp time selector to the native event type
// (server/legacy 2.5 CHAR_EVENT values, internal/aigame/types.go:63-69).
func mapEventTypeForTime(selector string) (int32, error) {
	switch strings.ToUpper(strings.TrimSpace(selector)) {
	case "", "NULL", "ANY":
		return aigame.MapEventWarp, nil
	case "M", "MORNING":
		return aigame.MapEventWarpMorning, nil
	case "A", "NOON":
		return aigame.MapEventWarpNoon, nil
	case "N", "NIGHT":
		return aigame.MapEventWarpNight, nil
	default:
		return 0, fmt.Errorf("mapwarp source has unsupported time selector %q", selector)
	}
}

// warpGraph builds the mapwarp graph from the shared knowledge snapshot.
func (s *Server) warpGraph() (*aiplanner.WarpGraph, error) {
	s.warpOnce.Do(func() {
		knowledge, err := s.knowledge()
		if err != nil {
			s.warpErr = err
			return
		}
		s.warps = aiplanner.NewWarpGraph(knowledge)
	})
	if s.warpErr != nil {
		return nil, s.warpErr
	}
	return s.warps, nil
}

// commandExits lists the warp relations that start on the current floor.
func (s *Server) commandExits(ctx context.Context, request Request) Response {
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := requireWorld(snapshot); err != nil {
		return actionFailure(err)
	}
	graph, err := s.warpGraph()
	if err != nil {
		return failure(KindServer, "%v", err)
	}
	floor := int(snapshot.Position.Floor)
	lines := make([]string, 0, 8)
	// EdgesFrom matches an exact tile, so a floor listing filters Edges().
	for _, edge := range graph.Edges() {
		if edge.From.Floor != floor {
			continue
		}
		label := strings.ToUpper(edge.Time)
		if label == "" || label == "NULL" {
			label = "any"
		}
		lines = append(lines, fmt.Sprintf("(%d,%d) -> floor %d (%d,%d) time=%s",
			edge.From.X, edge.From.Y, edge.To.Floor, edge.To.X, edge.To.Y, label))
	}
	if len(lines) == 0 {
		return Response{OK: true, Text: fmt.Sprintf("floor %d has no recorded exits", floor)}
	}
	return Response{OK: true, Text: fmt.Sprintf("exits on floor %d:\n%s", floor, strings.Join(lines, "\n"))}
}

// commandWarp triggers the warp under the character, or travels to a
// destination across maps.
func (s *Server) commandWarp(ctx context.Context, request Request) Response {
	// Accept: `warp`, `warp <floor>`, `warp <floor> <x> <y>`, plus `--time`.
	section := aiplanner.TimeAny
	numbers := make([]int, 0, 3)
	for index := 0; index < len(request.Args); index++ {
		switch request.Args[index] {
		case "--time":
			if index+1 >= len(request.Args) {
				return failure(KindUsage, "warp: --time requires M, A or N")
			}
			section = aiplanner.TimeSection(strings.ToUpper(request.Args[index+1]))
			index++
		default:
			value, err := parseCoordinate(request.Args[index])
			if err != nil {
				return failure(KindUsage, "warp: %v", err)
			}
			numbers = append(numbers, value)
		}
	}
	switch len(numbers) {
	case 0, 1, 3:
	default:
		return failure(KindUsage, "usage: sactl warp [floor [x y]] [--time M|A|N]")
	}
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	if err := requireWorld(snapshot); err != nil {
		return actionFailure(err)
	}
	graph, err := s.warpGraph()
	if err != nil {
		return failure(KindServer, "%v", err)
	}
	from := aiknowledge.Point{Floor: int(snapshot.Position.Floor), X: int(snapshot.Position.X), Y: int(snapshot.Position.Y)}

	// Without a destination, use the warp the character is standing on.
	if len(numbers) == 0 || len(numbers) == 1 {
		edges, err := graph.EdgesFrom(from, section)
		if err != nil {
			return failure(KindServer, "look up warp: %v", err)
		}
		edge, ok := selectWarpAt(edges, from, numbers)
		if !ok && len(numbers) == 1 {
			// Nothing under the character: use an exit on this floor that
			// leads to the requested floor and walk to it.
			edge, ok = selectWarpToFloor(graph, from, numbers[0])
		}
		if !ok {
			return actionFailure(fmt.Errorf("warp: no recorded exit at (%d,%d) on floor %d; run `sactl exits` to see the exits of this floor, or `sactl goto` to one of them",
				from.X, from.Y, from.Floor))
		}
		after, err := s.useWarp(ctx, edge)
		if err != nil {
			return actionFailure(err)
		}
		return Response{OK: true, Text: fmt.Sprintf("warped to floor %d (%d,%d)\n%s",
			after.Position.Floor, after.Position.X, after.Position.Y, s.renderObservation(after)), Data: replyJSON(after)}
	}

	to := aiknowledge.Point{Floor: numbers[0], X: numbers[1], Y: numbers[2]}
	plan, err := graph.PlanWarp(from, to, section)
	if err != nil {
		return actionFailure(fmt.Errorf("warp: no route from floor %d (%d,%d) to floor %d (%d,%d): %w",
			from.Floor, from.X, from.Y, to.Floor, to.X, to.Y, err))
	}
	if len(plan.Edges) == 0 {
		return actionFailure(fmt.Errorf("warp: no warp route to floor %d (%d,%d)", to.Floor, to.X, to.Y))
	}
	lines := make([]string, 0, len(plan.Edges)+1)
	for index, edge := range plan.Edges {
		after, err := s.useWarp(ctx, edge)
		if err != nil {
			return actionFailure(fmt.Errorf("warp hop %d of %d: %w\n%s", index+1, len(plan.Edges), err, strings.Join(lines, "\n")))
		}
		lines = append(lines, fmt.Sprintf("hop %d: floor %d (%d,%d)", index+1, after.Position.Floor, after.Position.X, after.Position.Y))
	}
	final, err := s.snapshot(ctx)
	if err != nil {
		return sessionFailure(err)
	}
	// The destination may be a different tile on the arrival floor; walk the
	// rest of the way when it is.
	if int(final.Position.Floor) == to.Floor && (int(final.Position.X) != to.X || int(final.Position.Y) != to.Y) {
		if walked, err := s.walkTo(ctx, ainavigation.Point{X: to.X, Y: to.Y}); err == nil {
			final = walked
			lines = append(lines, fmt.Sprintf("walked to (%d,%d)", to.X, to.Y))
		} else {
			lines = append(lines, "arrived on the target floor but could not walk to the exact tile: "+err.Error())
		}
	}
	arrived := int(final.Position.Floor) == to.Floor
	text := fmt.Sprintf("%s\nnow at floor %d (%d,%d)", strings.Join(lines, "\n"), final.Position.Floor, final.Position.X, final.Position.Y)
	if !arrived {
		return Response{OK: false, Kind: KindAction, Text: text, Data: replyJSON(final),
			Error: fmt.Sprintf("stopped on floor %d instead of %d", final.Position.Floor, to.Floor)}
	}
	return Response{OK: true, Text: text, Data: replyJSON(final)}
}

// selectWarpAt picks the warp the character stands on, or the requested floor.
func selectWarpAt(edges []aiplanner.WarpEdge, from aiknowledge.Point, requested []int) (aiplanner.WarpEdge, bool) {
	for _, edge := range edges {
		if edge.From != from {
			continue
		}
		if len(requested) == 1 && edge.To.Floor != requested[0] {
			continue
		}
		return edge, true
	}
	return aiplanner.WarpEdge{}, false
}

// selectWarpToFloor picks a warp on the current floor that leads to the
// requested floor, so `warp <floor>` can walk to the exit by itself.
func selectWarpToFloor(graph *aiplanner.WarpGraph, from aiknowledge.Point, targetFloor int) (aiplanner.WarpEdge, bool) {
	for _, edge := range graph.Edges() {
		if edge.From.Floor == from.Floor && edge.To.Floor == targetFloor {
			return edge, true
		}
	}
	return aiplanner.WarpEdge{}, false
}

// useWarp walks to the warp source when needed, then triggers exactly one EV
// and waits for the server's acknowledgement and the destination floor.
func (s *Server) useWarp(ctx context.Context, edge aiplanner.WarpEdge) (aigame.Snapshot, error) {
	snapshot, err := s.snapshot(ctx)
	if err != nil {
		return aigame.Snapshot{}, err
	}
	if int(snapshot.Position.Floor) != edge.From.Floor {
		return aigame.Snapshot{}, fmt.Errorf("warp source (%d,%d) is on floor %d but the character is on floor %d",
			edge.From.X, edge.From.Y, edge.From.Floor, snapshot.Position.Floor)
	}
	if int(snapshot.Position.X) != edge.From.X || int(snapshot.Position.Y) != edge.From.Y {
		snapshot, err = s.walkTo(ctx, ainavigation.Point{X: edge.From.X, Y: edge.From.Y})
		if err != nil {
			return aigame.Snapshot{}, fmt.Errorf("walk to warp source (%d,%d): %w", edge.From.X, edge.From.Y, err)
		}
	}
	event, err := mapEventTypeForTime(edge.Time)
	if err != nil {
		return aigame.Snapshot{}, err
	}
	game, err := s.session(ctx)
	if err != nil {
		return aigame.Snapshot{}, err
	}
	sequence := aigame.NextMapEventSequence()
	if err := s.submit(ctx, snapshot.Revision, aigame.MapEvent(event, sequence, snapshot.Position.X, snapshot.Position.Y, -1)); err != nil {
		return aigame.Snapshot{}, fmt.Errorf("submit map event: %w", err)
	}
	ackContext, cancel := context.WithTimeout(ctx, warpAckTimeout)
	defer cancel()
	ack, err := game.WaitForMapEvent(ackContext, sequence)
	if err != nil {
		return aigame.Snapshot{}, fmt.Errorf("map event %d was submitted but its acknowledgement did not arrive: %w (do not repeat the warp blindly)", sequence, err)
	}
	result := int32(-1)
	if len(ack.Fields) > 1 {
		result = ack.Fields[1].IntValue(-1)
	}
	if result != 1 {
		return aigame.Snapshot{}, fmt.Errorf("server rejected the map event (sequence %d, result %d)", sequence, result)
	}
	return s.confirmFloor(ctx, int32(edge.To.Floor), warpConfirmTimeout)
}

// confirmFloor waits until the observed floor becomes the expected one.
func (s *Server) confirmFloor(ctx context.Context, floor int32, timeout time.Duration) (aigame.Snapshot, error) {
	deadline := time.Now().Add(timeout)
	var last aigame.Snapshot
	for {
		snapshot, err := s.snapshot(ctx)
		if err != nil {
			return aigame.Snapshot{}, err
		}
		last = snapshot
		if snapshot.Position.Floor == floor {
			return snapshot, nil
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("destination floor %d did not arrive within %s (still on floor %d at (%d,%d))",
				floor, timeout, last.Position.Floor, last.Position.X, last.Position.Y)
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// parseCoordinate parses one map coordinate argument.
func parseCoordinate(value string) (int, error) {
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid coordinate %q", value)
	}
	return number, nil
}
