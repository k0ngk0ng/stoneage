package ainavigation

import (
	"context"
	"fmt"
	"sort"
)

// Navigator is an immutable collection of parsed floors and the server's
// image collision rules. It is safe for concurrent Route and Walkable calls.
type Navigator struct {
	images ImageTable
	floors map[int]FloorMap
}

// New constructs a Navigator from parsed image rules and floor maps. All
// mutable input maps and slices are copied. Duplicate floor IDs follow the
// legacy loader's last-record-wins behavior; callers which need provenance
// can inspect the selected FloorMap.Source through Floor.
func New(images ImageTable, floors []FloorMap) (*Navigator, error) {
	if len(images.Images) == 0 {
		return nil, fmt.Errorf("%w: image table is empty", ErrInvalidMap)
	}
	imageCopy := ImageTable{Images: make(map[uint16]ImageRule, len(images.Images))}
	for image, rule := range images.Images {
		imageCopy.Images[image] = rule
	}
	floorCopy := make(map[int]FloorMap, len(floors))
	for _, floor := range floors {
		if err := floor.Validate(); err != nil {
			return nil, err
		}
		for index, image := range floor.Tiles {
			if _, ok := imageCopy.Images[image]; !ok {
				return nil, fmt.Errorf("%w: floor %d tile cell %d references unknown image %d", ErrInvalidMap, floor.ID, index, image)
			}
		}
		for index, image := range floor.Objects {
			if _, ok := imageCopy.Images[image]; !ok {
				return nil, fmt.Errorf("%w: floor %d object cell %d references unknown image %d", ErrInvalidMap, floor.ID, index, image)
			}
		}
		floor.Tiles = append([]uint16(nil), floor.Tiles...)
		floor.Objects = append([]uint16(nil), floor.Objects...)
		floorCopy[floor.ID] = floor
	}
	return &Navigator{images: imageCopy, floors: floorCopy}, nil
}

// Floors returns selected floor IDs in ascending order.
func (n *Navigator) Floors() []int {
	if n == nil {
		return nil
	}
	result := make([]int, 0, len(n.floors))
	for id := range n.floors {
		result = append(result, id)
	}
	sort.Ints(result)
	return result
}

// Floor returns a copy of one floor's metadata and cell arrays.
func (n *Navigator) Floor(id int) (FloorMap, bool) {
	if n == nil {
		return FloorMap{}, false
	}
	floor, ok := n.floors[id]
	if !ok {
		return FloorMap{}, false
	}
	floor.Tiles = append([]uint16(nil), floor.Tiles...)
	floor.Objects = append([]uint16(nil), floor.Objects...)
	return floor, true
}

// Cell returns the logical tile/object pair at a coordinate.
func (n *Navigator) Cell(floorID, x, y int) (Cell, error) {
	floor, err := n.getFloor(floorID)
	if err != nil {
		return Cell{}, err
	}
	index, err := floor.index(x, y)
	if err != nil {
		return Cell{}, err
	}
	return Cell{Tile: floor.Tiles[index], Object: floor.Objects[index]}, nil
}

// Walkable reports whether a point is legal for a normal ground character.
// It mirrors MAP_walkAbleFromPoint(ff, fx, fy, FALSE); an absent floor or
// outside point returns false.
func (n *Navigator) Walkable(floorID, x, y int) bool {
	if n == nil {
		return false
	}
	floor, ok := n.floors[floorID]
	if !ok || x < 0 || y < 0 || x >= floor.Width || y >= floor.Height {
		return false
	}
	index := y*floor.Width + x
	tileRule, tileOK := n.images.Images[floor.Tiles[index]]
	objectRule, objectOK := n.images.Images[floor.Objects[index]]
	if !tileOK || !objectOK {
		return false
	}
	switch objectRule.Walkable {
	case 0:
		return false
	case 1:
		return tileRule.Walkable == 1
	case 2:
		return true
	default:
		return false
	}
}

// Route returns a shortest legal ground path on floorID. The breadth-first
// neighbor order is the native wire order a through h, making ties stable and
// ensuring each diagonal obeys the server's two-cardinal-side check.
func (n *Navigator) Route(floorID int, from, to Point) (Route, error) {
	return n.RouteContext(context.Background(), floorID, from, to)
}

// RouteContext is the cancellable form of Route for callers planning across
// a large floor or serving a request with a deadline.
func (n *Navigator) RouteContext(ctx context.Context, floorID int, from, to Point) (Route, error) {
	return n.RouteContextWithOptions(ctx, floorID, from, to, RouteOptions{})
}

// RouteWithOptions is the synchronous form of RouteContextWithOptions.
func (n *Navigator) RouteWithOptions(floorID int, from, to Point, options RouteOptions) (Route, error) {
	return n.RouteContextWithOptions(context.Background(), floorID, from, to, options)
}

// RouteContextWithOptions is RouteContext with an optional caller-supplied
// blocked-cell predicate. The predicate is evaluated in addition to the
// server collision rules and can therefore express temporary hazards such as
// encounter areas without changing the underlying map data.
func (n *Navigator) RouteContextWithOptions(ctx context.Context, floorID int, from, to Point, options RouteOptions) (Route, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Route{}, err
	}
	floor, err := n.getFloor(floorID)
	if err != nil {
		return Route{}, err
	}
	start, err := floor.index(from.X, from.Y)
	if err != nil {
		return Route{}, fmt.Errorf("%w: start %s: %v", ErrOutOfBounds, from, err)
	}
	goal, err := floor.index(to.X, to.Y)
	if err != nil {
		return Route{}, fmt.Errorf("%w: destination %s: %v", ErrOutOfBounds, to, err)
	}
	if !n.walkableAt(floor, start) {
		return Route{}, fmt.Errorf("%w: start %s", ErrBlocked, from)
	}
	if !n.walkableAt(floor, goal) {
		return Route{}, fmt.Errorf("%w: destination %s", ErrBlocked, to)
	}
	if options.Blocked != nil && options.Blocked(to) {
		return Route{}, fmt.Errorf("%w: destination %s", ErrBlocked, to)
	}
	result := Route{Floor: floorID, From: from, To: to, Points: []Point{}, Directions: ""}
	if start == goal {
		return result, nil
	}
	// -2 means unseen, -1 is the root. A flat integer queue avoids allocating
	// one Go object per cell on the large 800x800/800x1200 floors in the data.
	previous := make([]int, len(floor.Tiles))
	for index := range previous {
		previous[index] = -2
	}
	previous[start] = -1
	direction := make([]byte, len(floor.Tiles))
	queue := make([]int, 1, len(floor.Tiles))
	queue[0] = start
	found := false
	for head := 0; head < len(queue); head++ {
		if head&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return Route{}, err
			}
		}
		current := queue[head]
		if current == goal {
			found = true
			break
		}
		x := current % floor.Width
		y := current / floor.Width
		for dir, delta := range routeDirections {
			nextX, nextY := x+delta.dx, y+delta.dy
			if nextX < 0 || nextY < 0 || nextX >= floor.Width || nextY >= floor.Height {
				continue
			}
			next := nextY*floor.Width + nextX
			if previous[next] != -2 || !n.walkableAt(floor, next) {
				continue
			}
			if options.Blocked != nil && options.Blocked(Point{X: nextX, Y: nextY}) {
				continue
			}
			if delta.dx != 0 && delta.dy != 0 {
				// CHAR_walk_move checks both cardinal neighbors from the
				// current coordinate before accepting a diagonal.
				if !n.Walkable(floor.ID, x+delta.dx, y) || !n.Walkable(floor.ID, x, y+delta.dy) {
					continue
				}
			}
			previous[next] = current
			direction[next] = byte('a' + dir)
			queue = append(queue, next)
		}
	}
	if !found {
		return Route{}, fmt.Errorf("%w: floor %d from %s to %s", ErrUnreachable, floorID, from, to)
	}
	pathPoints := make([]Point, 0)
	pathDirections := make([]byte, 0)
	for current := goal; current != start; current = previous[current] {
		pathPoints = append(pathPoints, Point{X: current % floor.Width, Y: current / floor.Width})
		pathDirections = append(pathDirections, direction[current])
	}
	reversePoints(pathPoints)
	reverseBytes(pathDirections)
	result.Points = pathPoints
	result.Directions = string(pathDirections)
	return result, nil
}

// Directions is a convenience wrapper for callers which only need the W
// packet's native direction string.
func (n *Navigator) Directions(floorID int, from, to Point) (string, error) {
	return n.DirectionsContext(context.Background(), floorID, from, to)
}

// DirectionsContext is the cancellable form of Directions.
func (n *Navigator) DirectionsContext(ctx context.Context, floorID int, from, to Point) (string, error) {
	route, err := n.RouteContext(ctx, floorID, from, to)
	if err != nil {
		return "", err
	}
	return route.Directions, nil
}

// RouteWithoutContext is the convenience form for short synchronous callers.
func (n *Navigator) RouteWithoutContext(floorID int, from, to Point) (Route, error) {
	return n.Route(floorID, from, to)
}

func (n *Navigator) getFloor(id int) (FloorMap, error) {
	if n == nil {
		return FloorMap{}, fmt.Errorf("%w: navigator is nil", ErrFloorNotFound)
	}
	floor, ok := n.floors[id]
	if !ok {
		return FloorMap{}, fmt.Errorf("%w: %d", ErrFloorNotFound, id)
	}
	return floor, nil
}

func (m FloorMap) index(x, y int) (int, error) {
	if x < 0 || y < 0 || x >= m.Width || y >= m.Height {
		return 0, fmt.Errorf("%s outside %dx%d", Point{X: x, Y: y}, m.Width, m.Height)
	}
	return y*m.Width + x, nil
}

func (n *Navigator) walkableAt(floor FloorMap, index int) bool {
	if index < 0 || index >= len(floor.Tiles) || index >= len(floor.Objects) {
		return false
	}
	tileRule, tileOK := n.images.Images[floor.Tiles[index]]
	objectRule, objectOK := n.images.Images[floor.Objects[index]]
	if !tileOK || !objectOK {
		return false
	}
	switch objectRule.Walkable {
	case 0:
		return false
	case 1:
		return tileRule.Walkable == 1
	case 2:
		return true
	default:
		return false
	}
}

type routeDirection struct {
	dx int
	dy int
}

var routeDirections = [...]routeDirection{
	{dx: 0, dy: -1},
	{dx: 1, dy: -1},
	{dx: 1, dy: 0},
	{dx: 1, dy: 1},
	{dx: 0, dy: 1},
	{dx: -1, dy: 1},
	{dx: -1, dy: 0},
	{dx: -1, dy: -1},
}

func reversePoints(points []Point) {
	for left, right := 0, len(points)-1; left < right; left, right = left+1, right-1 {
		points[left], points[right] = points[right], points[left]
	}
}

func reverseBytes(values []byte) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
