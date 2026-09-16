package aiplanner

import (
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

func TestWarpPlanUsesTimeGatedStaticEdgesOnly(t *testing.T) {
	a := aiknowledge.Point{Floor: 1, X: 1, Y: 1}
	b := aiknowledge.Point{Floor: 2, X: 2, Y: 2}
	c := aiknowledge.Point{Floor: 3, X: 3, Y: 3}
	graph := NewWarpGraphFromWarps([]aiknowledge.MapWarp{
		{Type: "NONE", Time: "M", From: a, To: b},
		{Type: "NONE", Time: "NULL", From: b, To: c},
		{Type: "NONE", Time: "N", From: a, To: c},
	})
	plan, err := graph.PlanWarp(a, c, TimeMorning)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Complete || plan.TileNavigationAvailable || len(plan.Edges) != 2 || plan.Edges[0].To != b || plan.Edges[1].To != c {
		t.Fatalf("unexpected morning plan: %#v", plan)
	}
	plan, err = graph.PlanWarp(a, c, TimeNight)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Edges) != 1 || plan.Edges[0].Time != "N" {
		t.Fatalf("night gate was not selected: %#v", plan)
	}
	if _, err := graph.PlanWarp(a, c, TimeEvening); !errors.Is(err, ErrNoWarpPath) {
		t.Fatalf("evening incorrectly used a gated edge: %v", err)
	}
}

func TestWarpUnknownSectionKeepsOnlyUnconditionalEdges(t *testing.T) {
	a := aiknowledge.Point{Floor: 1, X: 1, Y: 1}
	b := aiknowledge.Point{Floor: 2, X: 2, Y: 2}
	graph := NewWarpGraphFromWarps([]aiknowledge.MapWarp{{Time: "M", From: a, To: b}})
	if edges, err := graph.EdgesFrom(a, TimeUnknown); err != nil || len(edges) != 0 {
		t.Fatalf("unknown section exposed gated edge: edges=%#v err=%v", edges, err)
	}
	if _, err := graph.PlanWarp(a, b, TimeUnknown); !errors.Is(err, ErrNoWarpPath) {
		t.Fatalf("unknown section used gated edge: %v", err)
	}
	if _, err := graph.PlanWarp(a, b, TimeSection("dawn")); !errors.Is(err, ErrInvalidTimeSection) {
		t.Fatalf("invalid time selector accepted: %v", err)
	}
}

func TestWarpIdentityPlanNeedsNoNavigation(t *testing.T) {
	p := aiknowledge.Point{Floor: 5, X: 7, Y: 9}
	plan, err := NewWarpGraph(nil).PlanWarp(p, p, TimeUnknown)
	if err != nil || !plan.Complete || plan.TileNavigationAvailable || len(plan.Edges) != 0 {
		t.Fatalf("identity plan: %#v err=%v", plan, err)
	}
}
