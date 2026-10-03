package ainavigation

import (
	"context"
	"errors"
	"testing"
)

func TestRouteToAnyFindsReachableGoalAndHonorsBlocking(t *testing.T) {
	floor := FloorMap{ID: 1, Width: 5, Height: 3, Tiles: make([]uint16, 15), Objects: make([]uint16, 15)}
	for i := range floor.Tiles {
		floor.Tiles[i] = 1
		floor.Objects[i] = 1
	}
	floor.Tiles[4] = 0
	n, err := New(ImageTable{Images: map[uint16]ImageRule{0: {Walkable: 0}, 1: {Walkable: 1}}}, []FloorMap{floor})
	if err != nil {
		t.Fatal(err)
	}
	goal := func(p Point) bool { return p.X == 4 }
	route, err := n.RouteToAny(context.Background(), 1, Point{X: 0, Y: 0}, goal, RouteOptions{Blocked: func(p Point) bool { return p == (Point{X: 4, Y: 1}) }})
	if err != nil || route.To != (Point{X: 4, Y: 2}) || route.Empty() {
		t.Fatal("did not choose reachable unblocked goal", route, err)
	}
	for _, p := range route.Points {
		if p == (Point{X: 4, Y: 1}) {
			t.Fatal("route entered blocked goal")
		}
	}
	_, err = n.RouteToAny(context.Background(), 1, Point{}, goal, RouteOptions{Blocked: goal})
	if !errors.Is(err, ErrUnreachable) {
		t.Fatal("all blocked goals should be unreachable", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = n.RouteToAny(ctx, 1, Point{}, goal, RouteOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal("ignored cancellation", err)
	}
	if _, err = n.RouteToAny(context.Background(), 1, Point{}, nil, RouteOptions{}); err == nil {
		t.Fatal("nil goal accepted")
	}
}
