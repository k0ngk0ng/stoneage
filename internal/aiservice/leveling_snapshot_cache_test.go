package aiservice

import (
	"context"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aileveling"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
)

func TestLevelingSearchCachesDoNotOutliveObservation(t *testing.T) {
	tiles, err := ainavigation.New(ainavigation.ImageTable{Images: map[uint16]ainavigation.ImageRule{0: {Walkable: 1}}}, []ainavigation.FloorMap{{ID: 12, Width: 5, Height: 3, Tiles: make([]uint16, 15), Objects: make([]uint16, 15)}})
	if err != nil {
		t.Fatal(err)
	}
	corridor := aiknowledge.Rectangle{X: 2, Y: 0, X2: 2, Y2: 2, Height: 2}
	target := aiknowledge.Rectangle{X: 4, Y: 0, X2: 4, Y2: 2, Height: 2}
	k := &aiknowledge.Knowledge{
		Encounters: []aiknowledge.EncounterArea{
			{ID: 21, Floor: 12, Bounds: corridor, ZOrder: 1, EncounterProbability: aiknowledge.Range{Min: 100, Max: 100}},
			{ID: 22, Floor: 12, Bounds: target, ZOrder: 1, EncounterProbability: aiknowledge.Range{Min: 100, Max: 100}},
		},
		Leveling: []aiknowledge.LevelingArea{
			{ID: 21, Floor: 12, Bounds: corridor, Verified: true, EnemyIDs: []int{9}, Levels: aiknowledge.Range{Min: 8, Max: 9}},
			{ID: 22, Floor: 12, Bounds: target, Verified: true, EnemyIDs: []int{1}, Levels: aiknowledge.Range{Min: 1, Max: 1}},
		},
	}
	for i := range k.Encounters {
		k.Encounters[i].GroupIDs = []int{999, -1, -1, -1, -1, -1, -1, -1, -1, -1}
		k.Encounters[i].GroupProbabilities = make([]int, 10)
	}
	if k.EncounterPolicyToken() == "" {
		t.Fatal("invalid native encounter fixture")
	}
	n := &LevelingNavigator{Knowledge: k, Tiles: tiles}
	s := aigame.Snapshot{Position: aigame.Point{Floor: 12, X: 0, Y: 1}, Player: aigame.PlayerSnapshot{Level: 1}}
	check := func(wantRoute bool) {
		t.Helper()
		_, err := n.Next(context.Background(), s, aileveling.NavigationRequest{AreaID: 22})
		if (err == nil) != wantRoute {
			t.Fatalf("level=%d policy=%q want route=%t err=%v", s.Player.Level, s.AI.EncounterPolicy, wantRoute, err)
		}
	}
	check(false)
	s.AI.EncounterPolicy = k.EncounterPolicyToken() + ":0"
	check(true)
	s.AI.EncounterPolicy = ""
	check(false)
	s.Player.Level = 9
	check(true)
	s.Player.Level = 1
	check(false)
	// Old table exemptions must not survive a source-table change either.
	s.AI.EncounterPolicy = k.EncounterPolicyToken() + ":0"
	k.Encounters[0].ZOrder = 2
	check(false)
}
