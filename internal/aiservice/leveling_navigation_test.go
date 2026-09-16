package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aileveling"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
)

func TestLevelingMovesInsideEncounterArea(t *testing.T) {
	tiles, err := ainavigation.New(ainavigation.ImageTable{Images: map[uint16]ainavigation.ImageRule{0: {Walkable: 1}, 1: {Walkable: 1}}}, []ainavigation.FloorMap{{ID: 12, Width: 3, Height: 3, Tiles: []uint16{1, 1, 1, 1, 1, 1, 1, 1, 1}, Objects: make([]uint16, 9)}})
	if err != nil {
		t.Fatal(err)
	}
	k := &aiknowledge.Knowledge{Leveling: []aiknowledge.LevelingArea{{ID: 1, Floor: 12, Verified: true, EnemyIDs: []int{1}, Levels: aiknowledge.Range{Min: 1, Max: 1}, Bounds: aiknowledge.Rectangle{X: 0, Y: 0, X2: 2, Y2: 2}}}}
	n := &LevelingNavigator{Knowledge: k, Tiles: tiles}
	s := aigame.Snapshot{Position: aigame.Point{Floor: 12, X: 1, Y: 1}, Player: aigame.PlayerSnapshot{Level: 1}}
	r, err := n.Next(context.Background(), s, aileveling.NavigationRequest{})
	if err != nil || r.InArea || r.Route != "c" || r.Destination.X != 2 || r.MaximumCost != 0 {
		t.Fatalf("did not walk to trigger encounters: %+v %v", r, err)
	}
	k.Leveling[0].Verified = false
	if _, err = n.Next(context.Background(), s, aileveling.NavigationRequest{}); err == nil {
		t.Fatal("used unverified area")
	}
	k.Leveling[0].Verified = true
	k.Leveling[0].Levels.Max = 10
	if _, err = n.Next(context.Background(), s, aileveling.NavigationRequest{}); err == nil {
		t.Fatal("auto selected excessive enemy level")
	}
}

func TestLevelingPlansAcrossMapsFromEachAuthoritativeSnapshot(t *testing.T) {
	k := &aiknowledge.Knowledge{
		Leveling: []aiknowledge.LevelingArea{{
			ID: 28, Floor: 100, Verified: true, EnemyIDs: []int{140},
			Levels: aiknowledge.Range{Min: 1, Max: 2},
			Bounds: aiknowledge.Rectangle{X: 11, Y: 567, X2: 155, Y2: 707},
		}},
		Warps: []aiknowledge.MapWarp{
			{Type: "NONE", Time: "NULL", From: aiknowledge.Point{Floor: 1006, X: 10, Y: 20}, To: aiknowledge.Point{Floor: 1000, X: 98, Y: 44}, Attribute: "NULL"},
			{Type: "NONE", Time: "NULL", From: aiknowledge.Point{Floor: 1000, X: 49, Y: 116}, To: aiknowledge.Point{Floor: 100, X: 637, Y: 491}, Attribute: "NULL"},
		},
	}
	n := &LevelingNavigator{Knowledge: k, Tiles: crossMapNavigator{}}
	request := aileveling.NavigationRequest{
		Targets:      []aileveling.Target{{Kind: "character", ID: "char-1", Level: 2}, {Kind: "pet", ID: "pet-1", Level: 2}},
		TargetPolicy: "all",
		AreaID:       28,
		Parameters:   map[string]json.RawMessage{"pet_target": json.RawMessage(`"pet-1"`)},
	}
	originalRequest := request
	for _, test := range []struct {
		snapshot        aigame.Snapshot
		warpKnown       bool
		warpDestination aigame.Point
	}{
		{snapshot: aigame.Snapshot{Position: aigame.Point{Floor: 1006, X: 15, Y: 22}, Player: aigame.PlayerSnapshot{Level: 1}}},
		{snapshot: aigame.Snapshot{Position: aigame.Point{Floor: 1006, X: 11, Y: 21}, Player: aigame.PlayerSnapshot{Level: 1}}, warpKnown: true, warpDestination: aigame.Point{Floor: 1000, X: 98, Y: 44}},
		{snapshot: aigame.Snapshot{Position: aigame.Point{Floor: 1000, X: 98, Y: 44}, Player: aigame.PlayerSnapshot{Level: 1}}},
		{snapshot: aigame.Snapshot{Position: aigame.Point{Floor: 100, X: 637, Y: 491}, Player: aigame.PlayerSnapshot{Level: 1}}},
	} {
		navigation, err := n.Next(context.Background(), test.snapshot, request)
		if err != nil {
			t.Fatalf("snapshot floor %d: %v", test.snapshot.Position.Floor, err)
		}
		if !navigation.Ready || navigation.InArea || navigation.Route == "" || len(navigation.Route) > 4 {
			t.Fatalf("snapshot floor %d returned invalid navigation %+v", test.snapshot.Position.Floor, navigation)
		}
		if navigation.WarpDestinationKnown != test.warpKnown {
			t.Fatalf("snapshot floor %d warp destination known=%v, want %v: %+v", test.snapshot.Position.Floor, navigation.WarpDestinationKnown, test.warpKnown, navigation)
		}
		if test.warpKnown && navigation.WarpDestination != test.warpDestination {
			t.Fatalf("snapshot floor %d warp destination=%+v, want %+v", test.snapshot.Position.Floor, navigation.WarpDestination, test.warpDestination)
		}
		if navigation.Destination.Floor != test.snapshot.Position.Floor {
			t.Fatalf("snapshot floor %d returned stale destination %+v", test.snapshot.Position.Floor, navigation.Destination)
		}
	}
	if !reflect.DeepEqual(request, originalRequest) {
		t.Fatalf("navigation mutated target request: before=%+v after=%+v", originalRequest, request)
	}
}

func TestLevelingRejectsUnreachableCrossMapRoute(t *testing.T) {
	k := &aiknowledge.Knowledge{
		Leveling: []aiknowledge.LevelingArea{{
			ID: 28, Floor: 100, Verified: true, EnemyIDs: []int{140},
			Levels: aiknowledge.Range{Min: 1, Max: 2}, Bounds: aiknowledge.Rectangle{X: 11, Y: 567, X2: 155, Y2: 707},
		}},
		Warps: []aiknowledge.MapWarp{{Type: "NONE", Time: "NULL", From: aiknowledge.Point{Floor: 1, X: 1, Y: 1}, To: aiknowledge.Point{Floor: 100, X: 2, Y: 2}, Attribute: "NULL"}},
	}
	n := &LevelingNavigator{Knowledge: k, Tiles: rejectingLevelingTiles{}}
	_, err := n.Next(context.Background(), aigame.Snapshot{Position: aigame.Point{Floor: 1, X: 0, Y: 0}, Player: aigame.PlayerSnapshot{Level: 1}}, aileveling.NavigationRequest{AreaID: 28})
	if !errors.Is(err, aileveling.ErrNoNavigation) {
		t.Fatalf("unreachable route error=%v, want ErrNoNavigation", err)
	}
}

func TestLevelingRejectsSegmentThroughHigherLevelEncounter(t *testing.T) {
	unsafeBounds := aiknowledge.Rectangle{X: 11, Y: 0, X2: 14, Y2: 0}
	k := &aiknowledge.Knowledge{
		Leveling: []aiknowledge.LevelingArea{
			{ID: 28, Floor: 100, Verified: true, EnemyIDs: []int{140}, Levels: aiknowledge.Range{Min: 1, Max: 2}, Bounds: aiknowledge.Rectangle{X: 15, Y: 0, X2: 16, Y2: 0}},
			{ID: 99, Floor: 100, Verified: true, EnemyIDs: []int{900}, Levels: aiknowledge.Range{Min: 4, Max: 6}, Bounds: unsafeBounds},
		},
		Encounters: []aiknowledge.EncounterArea{
			{ID: 99, Floor: 100, Bounds: unsafeBounds, EncounterProbability: aiknowledge.Range{Min: 1, Max: 5}, ZOrder: 20},
			// The effective row is selected by the server's highest-Z rule;
			// checking every overlapping row would incorrectly classify this
			// segment as safe because of the lower-level row.
			{ID: 28, Floor: 100, Bounds: unsafeBounds, EncounterProbability: aiknowledge.Range{Min: 1, Max: 5}, ZOrder: 1},
		},
	}
	n := &LevelingNavigator{Knowledge: k, Tiles: crossMapNavigator{}}
	_, err := n.Next(context.Background(), aigame.Snapshot{Position: aigame.Point{Floor: 100, X: 10, Y: 0}, Player: aigame.PlayerSnapshot{Level: 1}}, aileveling.NavigationRequest{AreaID: 28})
	if !errors.Is(err, aileveling.ErrNoNavigation) {
		t.Fatalf("unsafe encounter route error=%v, want ErrNoNavigation", err)
	}
}

func TestLevelingRejectsDangerAfterFirstPacketPrefix(t *testing.T) {
	dangerBounds := aiknowledge.Rectangle{X: 15, Y: 0, X2: 15, Y2: 0}
	targetBounds := aiknowledge.Rectangle{X: 16, Y: 0, X2: 16, Y2: 0}
	k := &aiknowledge.Knowledge{
		Leveling: []aiknowledge.LevelingArea{
			{ID: 28, Floor: 100, Verified: true, EnemyIDs: []int{140}, Levels: aiknowledge.Range{Min: 1, Max: 2}, Bounds: targetBounds},
			{ID: 99, Floor: 100, Verified: true, EnemyIDs: []int{900}, Levels: aiknowledge.Range{Min: 4, Max: 6}, Bounds: dangerBounds},
		},
		Encounters: []aiknowledge.EncounterArea{{ID: 99, Floor: 100, Bounds: dangerBounds, EncounterProbability: aiknowledge.Range{Min: 1, Max: 5}, ZOrder: 20}},
	}
	n := &LevelingNavigator{Knowledge: k, Tiles: crossMapNavigator{}}
	route, err := n.Tiles.RouteContext(context.Background(), 100, ainavigation.Point{X: 10, Y: 0}, ainavigation.Point{X: 16, Y: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !n.routeEncounterSafe(route, 100, 4, 1) || n.routeEncounterSafe(route, 100, len(route.Points), 1) {
		t.Fatalf("route safety did not distinguish submitted prefix from full route: prefix=%v full=%v route=%+v", n.routeEncounterSafe(route, 100, 4, 1), n.routeEncounterSafe(route, 100, len(route.Points), 1), route)
	}
	if _, err := n.Next(context.Background(), aigame.Snapshot{Position: aigame.Point{Floor: 100, X: 10, Y: 0}, Player: aigame.PlayerSnapshot{Level: 1}}, aileveling.NavigationRequest{AreaID: 28}); !errors.Is(err, aileveling.ErrNoNavigation) {
		t.Fatalf("danger after first packet error=%v, want ErrNoNavigation", err)
	}
}

func TestLevelingUsesSafeDetourWhenAvailable(t *testing.T) {
	images := ainavigation.ImageTable{Images: map[uint16]ainavigation.ImageRule{1: {Walkable: 1}}}
	mapData := ainavigation.FloorMap{ID: 100, Width: 7, Height: 3, Tiles: make([]uint16, 21), Objects: make([]uint16, 21)}
	for i := range mapData.Tiles {
		mapData.Tiles[i], mapData.Objects[i] = 1, 1
	}
	tiles, err := ainavigation.New(images, []ainavigation.FloorMap{mapData})
	if err != nil {
		t.Fatal(err)
	}
	dangerBounds := aiknowledge.Rectangle{X: 1, Y: 1, X2: 5, Y2: 1}
	targetBounds := aiknowledge.Rectangle{X: 6, Y: 1, X2: 6, Y2: 1}
	k := &aiknowledge.Knowledge{
		Leveling: []aiknowledge.LevelingArea{
			{ID: 28, Floor: 100, Verified: true, EnemyIDs: []int{140}, Levels: aiknowledge.Range{Min: 1, Max: 2}, Bounds: targetBounds},
			{ID: 99, Floor: 100, Verified: true, EnemyIDs: []int{900}, Levels: aiknowledge.Range{Min: 4, Max: 6}, Bounds: dangerBounds},
		},
		Encounters: []aiknowledge.EncounterArea{{ID: 99, Floor: 100, Bounds: dangerBounds, EncounterProbability: aiknowledge.Range{Min: 1, Max: 5}, ZOrder: 20}},
	}
	n := &LevelingNavigator{Knowledge: k, Tiles: tiles}
	route, err := n.routeLeveling(context.Background(), 100, ainavigation.Point{X: 0, Y: 1}, ainavigation.Point{X: 6, Y: 1}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if route.Directions == "cccccc" || route.Empty() {
		t.Fatalf("selected direct or empty route instead of a safe detour: %+v", route)
	}
	if !n.routeEncounterSafe(route, 100, len(route.Points), 1) {
		t.Fatalf("selected route enters an unsafe encounter: %+v", route)
	}
	navigation, err := n.Next(context.Background(), aigame.Snapshot{Position: aigame.Point{Floor: 100, X: 0, Y: 1}, Player: aigame.PlayerSnapshot{Level: 1}}, aileveling.NavigationRequest{AreaID: 28})
	if err != nil || navigation.Route == "cccc" {
		t.Fatalf("leveling navigation did not use safe prefix: %+v, err=%v", navigation, err)
	}
}

func TestLevelingAllowsZeroProbabilityAreaWithoutEnemies(t *testing.T) {
	zeroBounds := aiknowledge.Rectangle{X: 1, Y: 0, X2: 4, Y2: 0}
	k := &aiknowledge.Knowledge{
		Leveling: []aiknowledge.LevelingArea{{ID: 28, Floor: 100, Verified: true, EnemyIDs: []int{140}, Levels: aiknowledge.Range{Min: 1, Max: 2}, Bounds: aiknowledge.Rectangle{X: 5, Y: 0, X2: 5, Y2: 0}}},
		// This row intentionally has no derived leveling area or enemy IDs.
		Encounters: []aiknowledge.EncounterArea{{ID: 1230, Floor: 100, Bounds: zeroBounds, EncounterProbability: aiknowledge.Range{Min: 0, Max: 0}, ZOrder: 20}},
	}
	n := &LevelingNavigator{Knowledge: k, Tiles: crossMapNavigator{}}
	navigation, err := n.Next(context.Background(), aigame.Snapshot{Position: aigame.Point{Floor: 100, X: 0, Y: 0}, Player: aigame.PlayerSnapshot{Level: 1}}, aileveling.NavigationRequest{AreaID: 28})
	if err != nil || !navigation.Ready || navigation.Route == "" {
		t.Fatalf("zero-probability area blocked navigation: %+v, err=%v", navigation, err)
	}
}

func TestLevelingRejectsUnsafeWarpDestination(t *testing.T) {
	targetBounds := aiknowledge.Rectangle{X: 5, Y: 0, X2: 5, Y2: 0}
	dangerBounds := aiknowledge.Rectangle{X: 2, Y: 0, X2: 4, Y2: 0}
	k := &aiknowledge.Knowledge{
		Leveling: []aiknowledge.LevelingArea{
			{ID: 28, Floor: 100, Verified: true, EnemyIDs: []int{140}, Levels: aiknowledge.Range{Min: 1, Max: 2}, Bounds: targetBounds},
			{ID: 99, Floor: 100, Verified: true, EnemyIDs: []int{900}, Levels: aiknowledge.Range{Min: 4, Max: 6}, Bounds: dangerBounds},
		},
		Encounters: []aiknowledge.EncounterArea{{ID: 99, Floor: 100, Bounds: dangerBounds, EncounterProbability: aiknowledge.Range{Min: 1, Max: 5}, ZOrder: 20}},
		Warps:      []aiknowledge.MapWarp{{Type: "NONE", Time: "NULL", From: aiknowledge.Point{Floor: 1, X: 1, Y: 0}, To: aiknowledge.Point{Floor: 100, X: 2, Y: 0}, Attribute: "NULL"}},
	}
	n := &LevelingNavigator{Knowledge: k, Tiles: crossMapNavigator{}}
	_, err := n.Next(context.Background(), aigame.Snapshot{Position: aigame.Point{Floor: 1, X: 0, Y: 0}, Player: aigame.PlayerSnapshot{Level: 1}}, aileveling.NavigationRequest{AreaID: 28})
	if !errors.Is(err, aileveling.ErrNoNavigation) {
		t.Fatalf("unsafe warp destination error=%v, want ErrNoNavigation", err)
	}
}

func TestLevelingRejectsCancelledContext(t *testing.T) {
	k := &aiknowledge.Knowledge{Leveling: []aiknowledge.LevelingArea{{
		ID: 28, Floor: 100, Verified: true, EnemyIDs: []int{140}, Levels: aiknowledge.Range{Min: 1, Max: 2}, Bounds: aiknowledge.Rectangle{X: 11, Y: 567, X2: 155, Y2: 707},
	}}}
	n := &LevelingNavigator{Knowledge: k, Tiles: crossMapNavigator{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := n.Next(ctx, aigame.Snapshot{Position: aigame.Point{Floor: 100, X: 20, Y: 580}, Player: aigame.PlayerSnapshot{Level: 1}}, aileveling.NavigationRequest{AreaID: 28})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled navigation error=%v", err)
	}
}

func TestLevelingRejectsPaidUnknownAndTimeGatedWarps(t *testing.T) {
	for _, warp := range []aiknowledge.MapWarp{
		{Type: "FREE", Time: "NULL", Attribute: "NULL"},
		{Type: "NONE", Time: "NULL", Attribute: "500"},
		{Type: "NONE", Time: "M", Attribute: "NULL"},
		{Type: "NONE", Time: "", Attribute: "NULL"},
		{Type: "NONE", Time: "BOGUS", Attribute: "NULL"},
		{Type: "UNKNOWN", Time: "NULL", Attribute: "NULL"},
	} {
		warp.From = aiknowledge.Point{Floor: 1, X: 1, Y: 1}
		warp.To = aiknowledge.Point{Floor: 100, X: 2, Y: 2}
		t.Run(warp.Type+"/"+warp.Time+"/"+warp.Attribute, func(t *testing.T) {
			k := &aiknowledge.Knowledge{
				Leveling: []aiknowledge.LevelingArea{{
					ID: 28, Floor: 100, Verified: true, EnemyIDs: []int{140}, Levels: aiknowledge.Range{Min: 1, Max: 2}, Bounds: aiknowledge.Rectangle{X: 11, Y: 567, X2: 155, Y2: 707},
				}},
				Warps: []aiknowledge.MapWarp{warp},
			}
			n := &LevelingNavigator{Knowledge: k, Tiles: crossMapNavigator{}}
			_, err := n.Next(context.Background(), aigame.Snapshot{Position: aigame.Point{Floor: 1, X: 0, Y: 0}, Player: aigame.PlayerSnapshot{Level: 1}}, aileveling.NavigationRequest{AreaID: 28})
			if !errors.Is(err, aileveling.ErrNoNavigation) {
				t.Fatalf("warp %+v error=%v, want ErrNoNavigation", warp, err)
			}
		})
	}
}

type rejectingLevelingTiles struct{}

func (rejectingLevelingTiles) RouteContext(ctx context.Context, _ int, _, _ ainavigation.Point) (ainavigation.Route, error) {
	if err := ctx.Err(); err != nil {
		return ainavigation.Route{}, err
	}
	return ainavigation.Route{}, ainavigation.ErrUnreachable
}
