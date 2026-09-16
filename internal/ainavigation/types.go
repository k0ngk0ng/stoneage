// Package ainavigation provides deterministic, server-compatible navigation
// over StoneAge 2.5 LS2MAP files.
//
// The package only plans movement within one loaded floor.  Map warp edges
// belong to the knowledge/planner layer; this package deliberately does not
// infer a route between floors or pretend that a warp endpoint is reachable.
package ainavigation

import (
	"errors"
	"fmt"
	"strconv"
)

var (
	// ErrFloorNotFound means that the requested floor is not in the loaded
	// LS2MAP directory.
	ErrFloorNotFound = errors.New("ainavigation: floor not found")
	// ErrOutOfBounds means that a point lies outside the server map rectangle.
	ErrOutOfBounds = errors.New("ainavigation: point is outside floor")
	// ErrBlocked means that the start or destination cell is not walkable under
	// MAP_walkAbleFromPoint's ground movement rules.
	ErrBlocked = errors.New("ainavigation: point is blocked")
	// ErrUnreachable means that both endpoints are valid but no legal path
	// connects them on this floor.
	ErrUnreachable = errors.New("ainavigation: destination is unreachable")
	// ErrInvalidMap means that an LS2MAP or mapset record is structurally
	// invalid and cannot be treated as authoritative collision data.
	ErrInvalidMap = errors.New("ainavigation: invalid map data")
)

// Point is a server grid coordinate. Floor is intentionally absent because a
// Route is always scoped to one floor.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// RouteOptions controls optional constraints applied while searching a ground
// route. Blocked is called for candidate destination cells; the route's
// starting point remains usable so callers can plan a way out of a currently
// restricted cell. A nil Blocked function preserves the ordinary collision
// search.
type RouteOptions struct {
	Blocked func(Point) bool
}

func (p Point) String() string {
	return "(" + strconv.Itoa(p.X) + "," + strconv.Itoa(p.Y) + ")"
}

// ImageRule is one row from mapset.txt. Walkable is the raw server mode:
// 0 blocks, 1 delegates to the tile layer, and 2 always allows ground
// movement. Other values are retained and treated as blocked, matching the
// server switch's default branch.
type ImageRule struct {
	Walkable   int  `json:"walkable"`
	HaveHeight bool `json:"have_height"`
}

// ImageTable is the logical image collision table loaded from mapset.txt.
// Images is keyed by the logical image number stored in LS2MAP cells.
type ImageTable struct {
	Images map[uint16]ImageRule `json:"images"`
}

// Cell contains the two logical image numbers used by one map coordinate.
// It is useful for diagnostics; callers should use Walkable for collision.
type Cell struct {
	Tile   uint16 `json:"tile"`
	Object uint16 `json:"object"`
}

// FloorMap is one parsed LS2MAP. Tiles and Objects are row-major, with index
// y*Width+x. The slices are copied while constructing a Navigator and are
// treated as immutable thereafter.
type FloorMap struct {
	ID         int      `json:"id"`
	Width      int      `json:"width"`
	Height     int      `json:"height"`
	ShowString string   `json:"show_string,omitempty"`
	Source     string   `json:"source,omitempty"`
	Tiles      []uint16 `json:"-"`
	Objects    []uint16 `json:"-"`
}

// Route is a legal path within one floor. Points excludes From and includes
// To, so every element corresponds to one direction byte in Directions.
// Directions uses the native 2.5 wire alphabet: a=up, b=up-right,
// c=right, d=down-right, e=down, f=down-left, g=left, h=up-left.
type Route struct {
	Floor      int     `json:"floor"`
	From       Point   `json:"from"`
	To         Point   `json:"to"`
	Points     []Point `json:"points"`
	Directions string  `json:"directions"`
}

// Len returns the number of movement steps in the route.
func (r Route) Len() int { return len(r.Points) }

// Empty reports whether From already equals To. An empty route has no wire
// directions and no destination points.
func (r Route) Empty() bool { return len(r.Points) == 0 }

// Chunks splits the native direction string into packets no larger than max.
// The 2.5 server accepts at most 32 direction characters in CHAR_walk_init;
// callers submitting W packets should normally use max=32. A non-positive
// max returns one unchanged chunk for a non-empty route.
func (r Route) Chunks(max int) []string {
	if r.Directions == "" {
		return []string{}
	}
	if max <= 0 || max >= len(r.Directions) {
		return []string{r.Directions}
	}
	chunks := make([]string, 0, (len(r.Directions)+max-1)/max)
	for start := 0; start < len(r.Directions); start += max {
		end := start + max
		if end > len(r.Directions) {
			end = len(r.Directions)
		}
		chunks = append(chunks, r.Directions[start:end])
	}
	return chunks
}

// Validate checks the structural invariants of a FloorMap before it is
// installed in a Navigator.
func (m FloorMap) Validate() error {
	if m.ID < 0 {
		return fmt.Errorf("%w: negative floor ID %d", ErrInvalidMap, m.ID)
	}
	if m.Width <= 0 || m.Height <= 0 {
		return fmt.Errorf("%w: floor %d has dimensions %dx%d", ErrInvalidMap, m.ID, m.Width, m.Height)
	}
	if m.Width > maxMapDimension || m.Height > maxMapDimension || m.Width > maxMapCells/m.Height {
		return fmt.Errorf("%w: floor %d dimensions %dx%d exceed limits", ErrInvalidMap, m.ID, m.Width, m.Height)
	}
	cells := m.Width * m.Height
	if len(m.Tiles) != cells || len(m.Objects) != cells {
		return fmt.Errorf("%w: floor %d payload has %d/%d cells for %dx%d", ErrInvalidMap, m.ID, len(m.Tiles), len(m.Objects), m.Width, m.Height)
	}
	return nil
}
