package aiservice

import (
	"context"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
)

func TestMovementDetoursAroundVisibleNPC(t *testing.T) {
	for _, safe := range []bool{false, true} {
		backend, game := safeTravelGame(t, 10, 0, 1, 1)
		backend.Knowledge = &aiknowledge.Knowledge{}
		game.snapshot.Actors = []aigame.ActorSnapshot{{ID: 7, Kind: "character", X: 2, Y: 1}}
		skill := &MovementSkill{Backend: backend, Navigator: safeTravelTiles(t, safeTravelFloor(10, 5, 3)), SafeTravel: safe, SegmentTimeout: time.Second}
		if err := skill.Execute(context.Background(), crossMapAction(12, 10, 4, 1)); err != nil {
			t.Fatal(err)
		}
		for _, p := range movementTrace(ainavigation.Point{X: 0, Y: 1}, game.moves) {
			if p == (ainavigation.Point{X: 2, Y: 1}) {
				t.Fatal("walked through NPC")
			}
		}
		if game.snapshot.Position.X != 4 || game.snapshot.Position.Y != 1 {
			t.Fatal(game.snapshot.Position)
		}
	}
}

func TestMovementNPCDetourRetainsEncounterSafety(t *testing.T) {
	backend, game := safeTravelGame(t, 10, 0, 1, 1)
	game.snapshot.Actors = []aigame.ActorSnapshot{{ID: 7, Kind: "character", X: 2, Y: 1}}
	row1, area1 := unsafeTravelArea(1, 10, 0, 0, 4, 0, 80, 90)
	row2, area2 := unsafeTravelArea(2, 10, 0, 2, 4, 2, 80, 90)
	backend.Knowledge = &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{row1, row2}, Leveling: []aiknowledge.LevelingArea{area1, area2}}
	skill := &MovementSkill{Backend: backend, Navigator: safeTravelTiles(t, safeTravelFloor(10, 5, 3)), SafeTravel: true}
	if err := skill.Execute(context.Background(), crossMapAction(12, 10, 4, 1)); err == nil {
		t.Fatal("detoured into dangerous area")
	}
	if len(game.moves) != 0 {
		t.Fatal("unsafe movement sent")
	}
}

func TestMovementOccupiedDestinationDoesNotSend(t *testing.T) {
	backend, game := safeTravelGame(t, 10, 0, 0, 1)
	game.snapshot.Actors = []aigame.ActorSnapshot{{ID: 7, Kind: "character", X: 2, Y: 0}}
	skill := &MovementSkill{Backend: backend, Navigator: safeTravelTiles(t, safeTravelFloor(10, 3, 2))}
	if err := skill.Execute(context.Background(), crossMapAction(12, 10, 2, 0)); err == nil {
		t.Fatal("occupied goal accepted")
	}
	if len(game.moves) != 0 {
		t.Fatal("movement sent")
	}
}

func TestMovementPassesDefeatedNPCEnemyOnly(t *testing.T) {
	for _, tc := range []struct {
		name        string
		kind        int32
		graphic     int32
		known, pass bool
	}{
		{"defeated guard", 20, 0, true, true},
		{"revived guard", 20, 100000, true, false},
		{"unknown guard graphic", 20, 0, false, false},
		{"other invisible NPC", 12, 0, true, false},
		{"invisible player", 1, 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, game := safeTravelGame(t, 10, 0, 0, 1)
			game.snapshot.Actors = []aigame.ActorSnapshot{{ID: 7, Kind: "character", CharType: tc.kind, Graphic: tc.graphic, GraphicKnown: tc.known, X: 1, Y: 0}}
			skill := &MovementSkill{Backend: backend, Navigator: safeTravelTiles(t, safeTravelFloor(10, 3, 1))}
			err := skill.Execute(context.Background(), crossMapAction(12, 10, 2, 0))
			if tc.pass {
				if err != nil || game.snapshot.Position.X != 2 {
					t.Fatal(err, game.snapshot.Position)
				}
			} else if err == nil || len(game.moves) != 0 {
				t.Fatal("blocked actor was traversed", err, game.moves)
			}
		})
	}
}
