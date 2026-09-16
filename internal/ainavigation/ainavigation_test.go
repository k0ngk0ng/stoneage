package ainavigation

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMapsetUsesServerColumnsAndDefaults(t *testing.T) {
	table, err := ParseMapset(strings.NewReader("# comment\n7 1 1 0 0 -1\n8 1 1 1 1 -1\n9\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := table.Images[7]; got.Walkable != 0 || got.HaveHeight {
		t.Fatalf("image 7 = %+v", got)
	}
	if got := table.Images[8]; got.Walkable != 1 || !got.HaveHeight {
		t.Fatalf("image 8 = %+v", got)
	}
	if got := table.Images[9]; got.Walkable != 1 || got.HaveHeight {
		t.Fatalf("image 9 defaults = %+v", got)
	}
}

func TestParseLS2MAPUsesBigEndianAndAcceptsLegacyTrailingBytes(t *testing.T) {
	images := ImageTable{Images: map[uint16]ImageRule{1: {Walkable: 1}, 2: {Walkable: 0}}}
	raw := encodeMap(42, 2, 1, []uint16{1, 2}, []uint16{1, 1})
	raw = append(raw, 0xde, 0xad)
	floor, err := ParseLS2MAP(bytes.NewReader(raw), images)
	if err != nil {
		t.Fatal(err)
	}
	if floor.ID != 42 || floor.Width != 2 || floor.Height != 1 || floor.ShowString != "floor-42" {
		t.Fatalf("floor header = %+v", floor)
	}
	if floor.Tiles[0] != 1 || floor.Tiles[1] != 2 || floor.Objects[0] != 1 || floor.Objects[1] != 1 {
		t.Fatalf("floor cells = tiles %v objects %v", floor.Tiles, floor.Objects)
	}
}

func TestRouteMatchesServerGroundCollisionModes(t *testing.T) {
	images := ImageTable{Images: map[uint16]ImageRule{
		1: {Walkable: 1}, // walkable ground tile
		2: {Walkable: 0}, // solid ground tile
		3: {Walkable: 0}, // solid object
		4: {Walkable: 2}, // object mode 2 always allows
	}}
	// The 5x3 floor contains all three server object modes. Cell (2,0) has
	// object=0, (2,1) has object=1 with a blocked tile, and (2,2) has object=2
	// with a blocked tile; only the last one is walkable among those cells.
	tiles := make([]uint16, 15)
	objects := make([]uint16, 15)
	for i := range tiles {
		tiles[i], objects[i] = 1, 1
	}
	objects[2] = 3
	tiles[7] = 2
	objects[12] = 4
	tiles[12] = 2
	navigator, err := New(images, []FloorMap{{ID: 5, Width: 5, Height: 3, Tiles: tiles, Objects: objects}})
	if err != nil {
		t.Fatal(err)
	}
	if navigator.Walkable(5, 2, 0) || navigator.Walkable(5, 2, 1) || !navigator.Walkable(5, 2, 2) {
		t.Fatal("server collision modes were not reproduced")
	}
	if navigator.Walkable(5, -1, 0) || navigator.Walkable(5, 5, 0) || navigator.Walkable(999, 0, 0) {
		t.Fatal("out-of-floor cells reported walkable")
	}
	route, err := navigator.Route(5, Point{X: 0, Y: 0}, Point{X: 4, Y: 2})
	if err != nil {
		t.Fatal(err)
	}
	if route.Directions == "" || len(route.Directions) != len(route.Points) || route.Points[len(route.Points)-1] != (Point{X: 4, Y: 2}) {
		t.Fatalf("route = %+v", route)
	}
	for i, point := range route.Points {
		if !navigator.Walkable(5, point.X, point.Y) {
			t.Fatalf("route step %d enters blocked cell %v", i, point)
		}
	}
}

func TestRouteRejectsDiagonalCornerCuttingAndUnreachableFloor(t *testing.T) {
	images := ImageTable{Images: map[uint16]ImageRule{1: {Walkable: 1}, 2: {Walkable: 0}}}
	// Two blocked orthogonal neighbours must prevent a diagonal, exactly as
	// CHAR_walk_move checks MAP_walkAble(ox+dx,oy) and MAP_walkAble(ox,oy+dy).
	tiles := []uint16{1, 2, 1, 2, 1, 1, 1, 1, 1}
	objects := make([]uint16, len(tiles))
	for i := range objects {
		objects[i] = 1
	}
	navigator, err := New(images, []FloorMap{{ID: 1, Width: 3, Height: 3, Tiles: tiles, Objects: objects}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := navigator.Route(1, Point{X: 0, Y: 0}, Point{X: 1, Y: 1}); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("corner cut error = %v, want ErrUnreachable", err)
	}
	// A solid column separates two valid cells.
	for index := range tiles {
		tiles[index] = 1
	}
	for _, index := range []int{1, 4, 7} {
		tiles[index] = 2
	}
	if _, err := New(images, []FloorMap{{ID: 2, Width: 3, Height: 3, Tiles: tiles, Objects: objects}}); err != nil {
		t.Fatal(err)
	}
	blockedNavigator, _ := New(images, []FloorMap{{ID: 2, Width: 3, Height: 3, Tiles: tiles, Objects: objects}})
	if _, err := blockedNavigator.Route(2, Point{X: 0, Y: 1}, Point{X: 2, Y: 1}); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("separated floor error = %v, want ErrUnreachable", err)
	}
	if _, err := blockedNavigator.Route(2, Point{X: -1, Y: 0}, Point{X: 0, Y: 0}); !errors.Is(err, ErrOutOfBounds) {
		t.Fatalf("out-of-bounds error = %v, want ErrOutOfBounds", err)
	}
	if _, err := blockedNavigator.Route(2, Point{X: 0, Y: 1}, Point{X: 1, Y: 1}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("blocked destination error = %v, want ErrBlocked", err)
	}
}

func TestRouteUsesNativeDirectionAlphabetAndChunks(t *testing.T) {
	images := ImageTable{Images: map[uint16]ImageRule{1: {Walkable: 1}}}
	mapData := FloorMap{ID: 3, Width: 3, Height: 3, Tiles: make([]uint16, 9), Objects: make([]uint16, 9)}
	for i := range mapData.Tiles {
		mapData.Tiles[i], mapData.Objects[i] = 1, 1
	}
	navigator, err := New(images, []FloorMap{mapData})
	if err != nil {
		t.Fatal(err)
	}
	route, err := navigator.Route(3, Point{X: 1, Y: 1}, Point{X: 0, Y: 0})
	if err != nil || route.Directions != "h" {
		t.Fatalf("up-left route = %q, err=%v", route.Directions, err)
	}
	route, err = navigator.Route(3, Point{X: 0, Y: 0}, Point{X: 2, Y: 2})
	if err != nil || route.Directions != "dd" {
		t.Fatalf("down-right route = %q, err=%v", route.Directions, err)
	}
	if chunks := route.Chunks(1); len(chunks) != 2 || chunks[0] != "d" || chunks[1] != "d" {
		t.Fatalf("chunks = %v", chunks)
	}
	if got, err := navigator.Directions(3, Point{X: 0, Y: 0}, Point{X: 0, Y: 0}); err != nil || got != "" {
		t.Fatalf("identity directions = %q, err=%v", got, err)
	}
}

func TestRouteContextWithOptionsAvoidsBlockedCells(t *testing.T) {
	images := ImageTable{Images: map[uint16]ImageRule{1: {Walkable: 1}}}
	mapData := FloorMap{ID: 6, Width: 7, Height: 3, Tiles: make([]uint16, 21), Objects: make([]uint16, 21)}
	for i := range mapData.Tiles {
		mapData.Tiles[i], mapData.Objects[i] = 1, 1
	}
	navigator, err := New(images, []FloorMap{mapData})
	if err != nil {
		t.Fatal(err)
	}
	blocked := func(point Point) bool {
		return point.Y == 1 && point.X >= 1 && point.X <= 5
	}
	route, err := navigator.RouteContextWithOptions(context.Background(), 6, Point{X: 0, Y: 1}, Point{X: 6, Y: 1}, RouteOptions{Blocked: blocked})
	if err != nil {
		t.Fatal(err)
	}
	if route.Empty() || route.Points[len(route.Points)-1] != (Point{X: 6, Y: 1}) {
		t.Fatalf("blocked-cell route = %+v", route)
	}
	for _, point := range route.Points {
		if blocked(point) {
			t.Fatalf("route entered blocked point %v: %+v", point, route)
		}
	}
}

func TestLoadActualStoneAge25Maps(t *testing.T) {
	dataDir := filepath.Join("..", "..", "runtime", "legacy-server", "gmsv", "data")
	navigator, err := Load(context.Background(), Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	floors := navigator.Floors()
	if len(floors) < 690 || len(floors) > 700 {
		t.Fatalf("loaded %d floors, want checked-in 2.5 map set", len(floors))
	}
	floor, ok := navigator.Floor(1040)
	if !ok || floor.Width <= 0 || floor.Height <= 0 || len(floor.Tiles) != floor.Width*floor.Height {
		t.Fatalf("floor 1040 = %+v, present=%v", floor, ok)
	}
	// Find a real adjacent walkable pair in the loaded map and verify the
	// route encoder against the actual mapset/LS2MAP data rather than a test
	// fixture alone.
	var from, to Point
	found := false
	for y := 0; y < floor.Height && !found; y++ {
		for x := 0; x < floor.Width && !found; x++ {
			if !navigator.Walkable(1040, x, y) {
				continue
			}
			for _, delta := range routeDirections {
				nx, ny := x+delta.dx, y+delta.dy
				if nx >= 0 && ny >= 0 && nx < floor.Width && ny < floor.Height && navigator.Walkable(1040, nx, ny) {
					from, to, found = Point{X: x, Y: y}, Point{X: nx, Y: ny}, true
					break
				}
			}
		}
	}
	if !found {
		t.Fatal("floor 1040 has no adjacent walkable cells")
	}
	route, err := navigator.Route(1040, from, to)
	if err != nil || len(route.Directions) != 1 {
		t.Fatalf("actual map route %v -> %v = %+v, err=%v", from, to, route, err)
	}
}

func TestRouteContextHonoursCancellation(t *testing.T) {
	images := ImageTable{Images: map[uint16]ImageRule{1: {Walkable: 1}}}
	cells := 500 * 500
	mapData := FloorMap{ID: 4, Width: 500, Height: 500, Tiles: make([]uint16, cells), Objects: make([]uint16, cells)}
	for i := range mapData.Tiles {
		mapData.Tiles[i], mapData.Objects[i] = 1, 1
	}
	navigator, err := New(images, []FloorMap{mapData})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := navigator.RouteContext(ctx, 4, Point{X: 0, Y: 0}, Point{X: 499, Y: 499}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled route error = %v", err)
	}
}

func encodeMap(id, width, height int, tiles, objects []uint16) []byte {
	if len(tiles) != width*height || len(objects) != width*height {
		panic(fmt.Sprintf("bad test map dimensions: %d/%d for %dx%d", len(tiles), len(objects), width, height))
	}
	var buffer bytes.Buffer
	buffer.WriteString(ls2MapMagic)
	var value [2]byte
	binary.BigEndian.PutUint16(value[:], uint16(id))
	buffer.Write(value[:])
	label := make([]byte, 32)
	copy(label, fmt.Sprintf("floor-%d", id))
	buffer.Write(label)
	binary.BigEndian.PutUint16(value[:], uint16(width))
	buffer.Write(value[:])
	binary.BigEndian.PutUint16(value[:], uint16(height))
	buffer.Write(value[:])
	for _, cell := range append(append([]uint16(nil), tiles...), objects...) {
		binary.BigEndian.PutUint16(value[:], cell)
		buffer.Write(value[:])
	}
	return buffer.Bytes()
}

func TestLoadRejectsUnknownLS2MAPImage(t *testing.T) {
	temp := t.TempDir()
	mapDir := filepath.Join(temp, "map")
	if err := os.MkdirAll(mapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mapDir, "mapset.txt"), []byte("1 1 1 1 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mapDir, "bad-map"), encodeMap(1, 1, 1, []uint16{2}, []uint16{1}), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(context.Background(), Options{MapDir: mapDir})
	if !errors.Is(err, ErrInvalidMap) {
		t.Fatalf("unknown image load error = %v", err)
	}
}
