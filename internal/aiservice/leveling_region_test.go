package aiservice

import (
	"context"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aileveling"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
)

func TestCollectionFindsReachableSpeciesOutsideShadowedSamplePoints(t *testing.T) {
	grid := ainavigation.FloorMap{ID: 12, Width: 7, Height: 3, Tiles: make([]uint16, 21), Objects: make([]uint16, 21)}
	for i := range grid.Tiles {
		grid.Tiles[i] = 1
		grid.Objects[i] = 1
	}
	grid.Tiles[6] = 0
	grid.Tiles[20] = 0
	tiles, err := ainavigation.New(ainavigation.ImageTable{Images: map[uint16]ainavigation.ImageRule{0: {Walkable: 0}, 1: {Walkable: 1}}}, []ainavigation.FloorMap{grid})
	if err != nil {
		t.Fatal(err)
	}
	target := aiknowledge.Rectangle{X: 3, Y: 0, X2: 6, Y2: 2}
	shadow := aiknowledge.Rectangle{X: 3, Y: 0, X2: 5, Y2: 2}
	k := &aiknowledge.Knowledge{
		EnemiesTable: []aiknowledge.Enemy{{ID: 140, TemplateID: 31, PetFlag: 1, Levels: aiknowledge.Range{Min: 1, Max: 2}}},
		Leveling: []aiknowledge.LevelingArea{
			{ID: 28, Floor: 12, Bounds: target, Verified: true, EnemyIDs: []int{140}, Levels: aiknowledge.Range{Min: 1, Max: 2}},
			{ID: 99, Floor: 12, Bounds: shadow, Verified: true, EnemyIDs: []int{900}, Levels: aiknowledge.Range{Min: 1, Max: 2}},
		}, Encounters: []aiknowledge.EncounterArea{
			{ID: 28, Floor: 12, Bounds: target, ZOrder: 1, EncounterProbability: aiknowledge.Range{Min: 1, Max: 5}},
			{ID: 99, Floor: 12, Bounds: shadow, ZOrder: 2, EncounterProbability: aiknowledge.Range{Min: 1, Max: 5}},
		}}
	n := &LevelingNavigator{Knowledge: k, Tiles: tiles}
	s := aigame.Snapshot{Position: aigame.Point{Floor: 12, X: 0, Y: 1}, Player: aigame.PlayerSnapshot{Level: 1}}
	for i := 0; i < 3 && s.Position.X != 6; i++ {
		route, err := n.NextForPets(context.Background(), s, []PetCollectionTarget{{SpeciesID: 31, MinimumLevel: 1, MaximumLevel: 2, Count: 1}})
		if err != nil {
			t.Fatal(err)
		}
		s.Position = route.Destination
	}
	if s.Position.X != 6 || s.Position.Y != 1 {
		t.Fatal("did not reach the only accessible effective target tile", s.Position)
	}
	// Completely shadowing the remaining tile must reject the area, not farm
	// other species forever merely because the source rectangle contains it.
	k.Encounters[1].Bounds = target
	k.Leveling[1].Bounds = target
	if _, err := n.NextForPets(context.Background(), s, []PetCollectionTarget{{SpeciesID: 31, MinimumLevel: 1, MaximumLevel: 2, Count: 1}}); err == nil {
		t.Fatal("selected a species suppressed by higher-priority encounters")
	}
}

func TestLevelingPrefersCurrentFloorOverZeroDistanceRemoteArea(t *testing.T) {
	k := &aiknowledge.Knowledge{Leveling: []aiknowledge.LevelingArea{
		{ID: 2, Floor: 2, Verified: true, EnemyIDs: []int{2}, Levels: aiknowledge.Range{Min: 1, Max: 2}, Bounds: aiknowledge.Rectangle{X: 1, Y: 1, X2: 2, Y2: 2}},
		{ID: 1, Floor: 1, Verified: true, EnemyIDs: []int{1}, Levels: aiknowledge.Range{Min: 1, Max: 1}, Bounds: aiknowledge.Rectangle{X: 5, Y: 1, X2: 6, Y2: 2}},
	}, Warps: []aiknowledge.MapWarp{{Type: "NONE", Time: "NULL", From: aiknowledge.Point{Floor: 1, X: 0, Y: 2}, To: aiknowledge.Point{Floor: 2, X: 0, Y: 1}, Attribute: "NULL"}}}
	n := &LevelingNavigator{Knowledge: k, Tiles: crossMapNavigator{}}
	r, err := n.Next(context.Background(), aigame.Snapshot{Position: aigame.Point{Floor: 1, X: 0, Y: 1}, Player: aigame.PlayerSnapshot{Level: 1}}, aileveling.NavigationRequest{})
	if err != nil || r.WarpDestinationKnown || r.Destination.X != 4 {
		t.Fatal("nearby training lost to artificially zero-distance remote area", r, err)
	}
}
