package aiservice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
)

func safeTravelFloor(id, width, height int) ainavigation.FloorMap {
	cells := width * height
	tiles := make([]uint16, cells)
	objects := make([]uint16, cells)
	for i := range tiles {
		tiles[i], objects[i] = 1, 1
	}
	return ainavigation.FloorMap{ID: id, Width: width, Height: height, Tiles: tiles, Objects: objects}
}

func safeTravelTiles(t *testing.T, floors ...ainavigation.FloorMap) *ainavigation.Navigator {
	t.Helper()
	tiles, err := ainavigation.New(ainavigation.ImageTable{Images: map[uint16]ainavigation.ImageRule{
		1: {Walkable: 1},
	}}, floors)
	if err != nil {
		t.Fatal(err)
	}
	return tiles
}

func safeTravelGame(t *testing.T, floor, x, y, level int32) (*GameBackend, *crossMapGame) {
	t.Helper()
	backend, _ := gameFixture(t)
	game := &crossMapGame{snapshot: aigame.Snapshot{
		Account: "account", Character: "character", Revision: 12, Connected: true,
		Phase: aigame.PhaseWorld, Position: aigame.Point{Floor: floor, X: x, Y: y},
		Player: aigame.PlayerSnapshot{HasStatus: true, Level: level, HP: 10, MaxHP: 10, RidePet: -1, RidePetKnown: true},
	}}
	backend.Session = game
	return backend, game
}

func unsafeTravelArea(id, floor, x, y, x2, y2 int, minLevel, maxLevel int) (aiknowledge.EncounterArea, aiknowledge.LevelingArea) {
	bounds := aiknowledge.Rectangle{X: x, Y: y, X2: x2, Y2: y2}
	return aiknowledge.EncounterArea{
			ID: id, Floor: floor, Bounds: bounds, ZOrder: 20,
			EncounterProbability: aiknowledge.Range{Min: 1, Max: 5},
		}, aiknowledge.LevelingArea{
			ID: id, Floor: floor, Bounds: bounds, Verified: true,
			EnemyIDs: []int{900}, Levels: aiknowledge.Range{Min: minLevel, Max: maxLevel},
		}
}

func movementTrace(start ainavigation.Point, actions []aigame.Action) []ainavigation.Point {
	current := start
	trace := make([]ainavigation.Point, 0)
	for _, action := range actions {
		for _, direction := range []byte(action.Route) {
			if direction < 'a' || direction > 'h' {
				continue
			}
			delta := travelDirections[direction-'a']
			current.X += delta[0]
			current.Y += delta[1]
			trace = append(trace, current)
		}
	}
	return trace
}

func containsTravelPoint(trace []ainavigation.Point, want ainavigation.Point) bool {
	for _, point := range trace {
		if point == want {
			return true
		}
	}
	return false
}

func concatMovementRoutes(actions []aigame.Action) string {
	var routes strings.Builder
	for _, action := range actions {
		routes.WriteString(action.Route)
	}
	return routes.String()
}

func TestMovementSafeTravelDetoursAroundHighLevelEncounter(t *testing.T) {
	tiles := safeTravelTiles(t, safeTravelFloor(10, 7, 3))
	danger, area := unsafeTravelArea(99, 10, 1, 1, 5, 1, 4, 6)
	backend, game := safeTravelGame(t, 10, 0, 1, 1)
	backend.Knowledge = &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{danger}, Leveling: []aiknowledge.LevelingArea{area}}
	skill := &MovementSkill{Backend: backend, Navigator: tiles, SafeTravel: true, SegmentTimeout: time.Second}

	if err := skill.Execute(context.Background(), crossMapAction(12, 10, 6, 1)); err != nil {
		t.Fatal(err)
	}

	game.mu.Lock()
	defer game.mu.Unlock()
	if got := game.snapshot.Position; got != (aigame.Point{Floor: 10, X: 6, Y: 1}) {
		t.Fatalf("final position=%+v, want 10:(6,1)", got)
	}
	if got := concatMovementRoutes(game.moves); got == "cccccc" {
		t.Fatalf("safe travel submitted the direct hazardous route %q", got)
	}
	trace := movementTrace(ainavigation.Point{X: 0, Y: 1}, game.moves)
	for _, point := range trace {
		if danger.Bounds.Contains(point.X, point.Y) {
			t.Fatalf("safe travel entered high-level encounter tile %+v, trace=%v", point, trace)
		}
	}
}

func TestMovementSafeTravelChoosesSafeCrossMapWarpAlternative(t *testing.T) {
	tiles := safeTravelTiles(t,
		safeTravelFloor(1, 2, 2),
		safeTravelFloor(2, 2, 2),
		safeTravelFloor(3, 2, 2),
	)
	danger, area := unsafeTravelArea(99, 2, 0, 0, 0, 0, 4, 6)
	unsafe := aiknowledge.Point{Floor: 1, X: 1, Y: 0}
	unsafeDestination := aiknowledge.Point{Floor: 2, X: 0, Y: 0}
	firstFrom := aiknowledge.Point{Floor: 1, X: 0, Y: 1}
	firstTo := aiknowledge.Point{Floor: 2, X: 1, Y: 1}
	secondFrom := aiknowledge.Point{Floor: 2, X: 1, Y: 1}
	secondTo := aiknowledge.Point{Floor: 3, X: 0, Y: 1}
	warps := []aiknowledge.MapWarp{
		{Type: "NONE", Time: "NULL", From: unsafe, To: unsafeDestination, Attribute: "NULL"},
		{Type: "NONE", Time: "NULL", From: firstFrom, To: firstTo, Attribute: "NULL"},
		{Type: "NONE", Time: "NULL", From: secondFrom, To: secondTo, Attribute: "NULL"},
	}
	backend, game := safeTravelGame(t, 1, 0, 0, 1)
	backend.Knowledge = &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{danger}, Leveling: []aiknowledge.LevelingArea{area}, Warps: warps}
	game.warpTargets = map[aiknowledge.Point]aiknowledge.Point{
		unsafe: unsafeDestination, firstFrom: firstTo, secondFrom: secondTo,
	}
	game.warpOnStatus = true
	skill := &MovementSkill{Backend: backend, Navigator: tiles, SafeTravel: true, SegmentTimeout: time.Second, WarpConfirmationTimeout: time.Second}

	if err := skill.Execute(context.Background(), crossMapAction(12, 3, 1, 0)); err != nil {
		t.Fatal(err)
	}

	game.mu.Lock()
	defer game.mu.Unlock()
	if got := game.snapshot.Position; got != (aigame.Point{Floor: 3, X: 1, Y: 0}) {
		t.Fatalf("final position=%+v, want 3:(1,0)", got)
	}
	if len(game.mapEvents) != 2 {
		t.Fatalf("map events=%d, want two safe warps", len(game.mapEvents))
	}
	for _, event := range game.mapEvents {
		if event.X == int32(unsafe.X) && event.Y == int32(unsafe.Y) {
			t.Fatalf("selected hazardous warp source %+v", unsafe)
		}
	}
	if len(game.moves) == 0 || game.moves[0].Route != "e" {
		t.Fatalf("first ground move=%v, want route to the safe alternative source", game.moves)
	}
}

func TestMovementSafeTravelRejectsUnknownOrHighLevelEncounterWithoutWrites(t *testing.T) {
	tiles := safeTravelTiles(t, safeTravelFloor(10, 5, 1))
	for _, tc := range []struct {
		name     string
		withArea bool
		min, max int
	}{
		{name: "unknown", withArea: false},
		{name: "high-level", withArea: true, min: 4, max: 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			danger, area := unsafeTravelArea(99, 10, 1, 0, 3, 0, tc.min, tc.max)
			backend, game := safeTravelGame(t, 10, 0, 0, 1)
			knowledge := &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{danger}}
			if tc.withArea {
				knowledge.Leveling = []aiknowledge.LevelingArea{area}
			}
			backend.Knowledge = knowledge
			skill := &MovementSkill{Backend: backend, Navigator: tiles, SafeTravel: true, SegmentTimeout: time.Second}

			err := skill.Execute(context.Background(), crossMapAction(12, 10, 4, 0))
			if !errors.Is(err, ErrUnsafeTravelRoute) {
				t.Fatalf("err=%v, want ErrUnsafeTravelRoute", err)
			}
			game.mu.Lock()
			defer game.mu.Unlock()
			if len(game.moves) != 0 || len(game.mapEvents) != 0 {
				t.Fatalf("unsafe route wrote moves=%d map_events=%d", len(game.moves), len(game.mapEvents))
			}
			if got := game.snapshot.Position; got != (aigame.Point{Floor: 10, X: 0, Y: 0}) {
				t.Fatalf("position changed after rejected route: %+v", got)
			}
		})
	}
}

func TestMovementSafeTravelAllowsZeroProbabilityEncounterRow(t *testing.T) {
	tiles := safeTravelTiles(t, safeTravelFloor(10, 5, 1))
	backend, game := safeTravelGame(t, 10, 0, 0, 1)
	backend.Knowledge = &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{{
		ID: 1230, Floor: 10, Bounds: aiknowledge.Rectangle{X: 1, Y: 0, X2: 3, Y2: 0}, ZOrder: 20,
		EncounterProbability: aiknowledge.Range{Min: 0, Max: 0},
	}}}
	skill := &MovementSkill{Backend: backend, Navigator: tiles, SafeTravel: true, SegmentTimeout: time.Second}

	if err := skill.Execute(context.Background(), crossMapAction(12, 10, 4, 0)); err != nil {
		t.Fatal(err)
	}
	game.mu.Lock()
	defer game.mu.Unlock()
	if got := game.snapshot.Position; got != (aigame.Point{Floor: 10, X: 4, Y: 0}) {
		t.Fatalf("final position=%+v, want 10:(4,0)", got)
	}
	if got := concatMovementRoutes(game.moves); got != "cccc" {
		t.Fatalf("zero-probability row changed route to %q, want direct cccc", got)
	}
}

type staleTravelLevelSession struct {
	*crossMapGame
	lowered bool
}

func (s *staleTravelLevelSession) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if action.Kind == aigame.ActionMove && !s.lowered {
		s.crossMapGame.mu.Lock()
		s.snapshot.Revision++
		s.snapshot.Player.Level = 1
		s.crossMapGame.mu.Unlock()
		s.lowered = true
		return aigame.ErrStaleRevision
	}
	return s.crossMapGame.ExecuteExpected(ctx, revision, action)
}

func TestMovementSafeTravelRechecksLoweredLevelBeforeStaleRetry(t *testing.T) {
	tiles := safeTravelTiles(t, safeTravelFloor(10, 5, 1))
	danger, area := unsafeTravelArea(99, 10, 1, 0, 3, 0, 4, 6)
	backend, game := safeTravelGame(t, 10, 0, 0, 5)
	backend.Knowledge = &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{danger}, Leveling: []aiknowledge.LevelingArea{area}}
	session := &staleTravelLevelSession{crossMapGame: game}
	backend.Session = session
	skill := &MovementSkill{Backend: backend, Navigator: tiles, SafeTravel: true}
	observed, err := backend.Observe(context.Background(), backend.Binding)
	if err != nil {
		t.Fatal(err)
	}
	_, err = skill.submitMove(context.Background(), observed, aigame.Move(0, 0, "cccc"))
	if !errors.Is(err, ErrUnsafeTravelRoute) {
		t.Fatalf("err=%v, want ErrUnsafeTravelRoute after level drop", err)
	}
	game.mu.Lock()
	defer game.mu.Unlock()
	if session.lowered == false || len(game.moves) != 0 {
		t.Fatalf("stale retry state lowered=%v moves=%d, want one rejected attempt and no W", session.lowered, len(game.moves))
	}
	if got := game.snapshot.Position; got != (aigame.Point{Floor: 10, X: 0, Y: 0}) {
		t.Fatalf("position changed after lowered-level rejection: %+v", got)
	}
}

func TestMovementSafeTravelAvoidsAutomaticWarpSource(t *testing.T) {
	tiles := safeTravelTiles(t, safeTravelFloor(10, 5, 3), safeTravelFloor(11, 1, 1))
	danger, area := unsafeTravelArea(99, 11, 0, 0, 0, 0, 4, 6)
	warpFrom := aiknowledge.Point{Floor: 10, X: 2, Y: 1}
	warpTo := aiknowledge.Point{Floor: 11, X: 0, Y: 0}
	warp := aiknowledge.MapWarp{Type: "NONE", Time: "NULL", From: warpFrom, To: warpTo, Attribute: "NULL"}
	backend, game := safeTravelGame(t, 10, 0, 1, 1)
	backend.Knowledge = &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{danger}, Leveling: []aiknowledge.LevelingArea{area}, Warps: []aiknowledge.MapWarp{warp}}
	game.warpTargets = map[aiknowledge.Point]aiknowledge.Point{warpFrom: warpTo}
	game.warpOnMove = true
	skill := &MovementSkill{Backend: backend, Navigator: tiles, SafeTravel: true, SegmentTimeout: time.Second}

	if err := skill.Execute(context.Background(), crossMapAction(12, 10, 4, 1)); err != nil {
		t.Fatal(err)
	}

	game.mu.Lock()
	defer game.mu.Unlock()
	if got := game.snapshot.Position; got != (aigame.Point{Floor: 10, X: 4, Y: 1}) {
		t.Fatalf("final position=%+v, want 10:(4,1) without automatic warp", got)
	}
	trace := movementTrace(ainavigation.Point{X: 0, Y: 1}, game.moves)
	if containsTravelPoint(trace, ainavigation.Point{X: warpFrom.X, Y: warpFrom.Y}) {
		t.Fatalf("safe travel entered automatic warp source %+v, trace=%v", warpFrom, trace)
	}
}
