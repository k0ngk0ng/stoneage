package aiplanner

import (
	"errors"
	"fmt"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

var (
	ErrInvalidTimeSection = errors.New("aiplanner: invalid time section")
	ErrNoWarpPath         = errors.New("aiplanner: no warp-only path")
)

// TimeSection is the server's mapwarp time gate. The original server uses
// M for morning, A for noon, and N for night. E is retained to represent the
// server's evening clock section; mapwarp.txt has no E-specific selector.
type TimeSection string

const (
	TimeUnknown TimeSection = ""
	TimeAny     TimeSection = "ANY"
	TimeMorning TimeSection = "M"
	TimeNoon    TimeSection = "A"
	TimeNight   TimeSection = "N"
	TimeEvening TimeSection = "E"
)

// WarpEdge is one static relation from map/mapwarp.txt. It says nothing
// about walkability between a character and From or after arriving at To.
type WarpEdge struct {
	Type      string                `json:"type"`
	Time      string                `json:"time"`
	From      aiknowledge.Point     `json:"from"`
	To        aiknowledge.Point     `json:"to"`
	Attribute string                `json:"attribute,omitempty"`
	Source    aiknowledge.SourceRef `json:"source"`
}

// WarpGraph contains only mapwarp edges from a knowledge snapshot.
type WarpGraph struct {
	edges []WarpEdge
}

// NewWarpGraph copies the static mapwarp relations from knowledge. A nil
// snapshot produces an empty graph, which can still answer an identity route.
func NewWarpGraph(knowledge *aiknowledge.Knowledge) *WarpGraph {
	if knowledge == nil {
		return &WarpGraph{}
	}
	return NewWarpGraphFromWarps(knowledge.Warps)
}

// NewWarpGraphFromWarps builds a graph from already parsed mapwarp records.
func NewWarpGraphFromWarps(warps []aiknowledge.MapWarp) *WarpGraph {
	graph := &WarpGraph{edges: make([]WarpEdge, 0, len(warps))}
	for _, warp := range warps {
		graph.edges = append(graph.edges, WarpEdge{
			Type: warp.Type, Time: warp.Time, From: warp.From, To: warp.To,
			Attribute: warp.Attribute, Source: warp.Source,
		})
	}
	return graph
}

// Edges returns a copy in source-file order.
func (g *WarpGraph) Edges() []WarpEdge {
	if g == nil {
		return nil
	}
	return append([]WarpEdge(nil), g.edges...)
}

// EdgesFrom returns time-compatible outgoing edges. An unknown current
// section deliberately returns only unconditional NULL edges; callers must
// provide a real section or TimeAny before using time-gated edges.
func (g *WarpGraph) EdgesFrom(from aiknowledge.Point, section TimeSection) ([]WarpEdge, error) {
	section, err := normalizeTimeSection(section)
	if err != nil {
		return nil, err
	}
	result := make([]WarpEdge, 0)
	if g == nil {
		return result, nil
	}
	for _, edge := range g.edges {
		if edge.From == from && edgeAllowedAt(edge.Time, section) {
			result = append(result, edge)
		}
	}
	return result, nil
}

// WarpPlan is a path through static warp edges. TileNavigationAvailable is
// always false: this package intentionally does not parse LS2MAP tiles or
// claim that a character can walk to any edge endpoint.
type WarpPlan struct {
	From                    aiknowledge.Point `json:"from"`
	To                      aiknowledge.Point `json:"to"`
	Section                 TimeSection       `json:"section"`
	Edges                   []WarpEdge        `json:"edges"`
	Complete                bool              `json:"complete"`
	TileNavigationAvailable bool              `json:"tile_navigation_available"`
}

// PlanWarp finds a deterministic breadth-first path using only compatible
// mapwarp edges. The returned plan is complete only in the warp graph; the
// caller remains responsible for tile navigation to each From coordinate.
func (g *WarpGraph) PlanWarp(from, to aiknowledge.Point, section TimeSection) (WarpPlan, error) {
	section, err := normalizeTimeSection(section)
	if err != nil {
		return WarpPlan{}, err
	}
	plan := WarpPlan{From: from, To: to, Section: section, TileNavigationAvailable: false}
	if from == to {
		plan.Complete = true
		plan.Edges = []WarpEdge{}
		return plan, nil
	}
	if g == nil || len(g.edges) == 0 {
		return WarpPlan{}, fmt.Errorf("%w: graph is empty", ErrNoWarpPath)
	}

	type predecessor struct {
		point aiknowledge.Point
		edge  WarpEdge
	}
	queue := []aiknowledge.Point{from}
	visited := map[aiknowledge.Point]bool{from: true}
	previous := make(map[aiknowledge.Point]predecessor)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, edge := range g.edges {
			if edge.From != current || !edgeAllowedAt(edge.Time, section) || visited[edge.To] {
				continue
			}
			visited[edge.To] = true
			previous[edge.To] = predecessor{point: current, edge: edge}
			if edge.To == to {
				path := make([]WarpEdge, 0)
				for point := to; point != from; {
					entry, ok := previous[point]
					if !ok {
						return WarpPlan{}, fmt.Errorf("%w: predecessor chain is incomplete", ErrNoWarpPath)
					}
					path = append(path, entry.edge)
					point = entry.point
				}
				for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
					path[left], path[right] = path[right], path[left]
				}
				plan.Edges = path
				plan.Complete = true
				return plan, nil
			}
			queue = append(queue, edge.To)
		}
	}
	return WarpPlan{}, fmt.Errorf("%w: %v to %v at section %q", ErrNoWarpPath, from, to, section)
}

func normalizeTimeSection(section TimeSection) (TimeSection, error) {
	switch strings.ToUpper(strings.TrimSpace(string(section))) {
	case "", "UNKNOWN":
		return TimeUnknown, nil
	case "ANY", "NULL", "*":
		return TimeAny, nil
	case "M", "MORNING":
		return TimeMorning, nil
	case "A", "NOON":
		return TimeNoon, nil
	case "N", "NIGHT":
		return TimeNight, nil
	case "E", "EVENING":
		return TimeEvening, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidTimeSection, section)
	}
}

func edgeAllowedAt(edgeTime string, section TimeSection) bool {
	switch strings.ToUpper(strings.TrimSpace(edgeTime)) {
	case "", "NULL":
		return true
	case "M":
		return section == TimeMorning || section == TimeAny
	case "A":
		return section == TimeNoon || section == TimeAny
	case "N":
		return section == TimeNight || section == TimeAny
	default:
		return false
	}
}
