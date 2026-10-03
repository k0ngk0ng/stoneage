package aiservice

import (
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

func TestEncounterPolicyExemptionHonorsOverlapAndRevocation(t *testing.T) {
	r := aiknowledge.EncounterArea{ID: 21, Floor: 100, Bounds: aiknowledge.Rectangle{Width: 10, Height: 10, X2: 10, Y2: 10},
		EncounterProbability: aiknowledge.Range{Min: 10, Max: 20}, MaxEnemies: 4, ZOrder: 1, EventNow: -1, EventEnd: -1, EnemyGroup: -1,
		GroupIDs: []int{1230, -1, -1, -1, -1, -1, -1, -1, -1, -1}, GroupProbabilities: make([]int, 10)}
	k := &aiknowledge.Knowledge{Encounters: []aiknowledge.EncounterArea{r}, Groups: []aiknowledge.EncounterGroup{{ID: 77}}}
	n := &LevelingNavigator{Knowledge: k}
	if n.encounterPointSafe(100, 5, 5, 40) {
		t.Fatal("unverified missing group allowed")
	}
	token := k.EncounterPolicyToken() + ":0"
	n.nonSpawning = k.NonSpawningEncounters(token)
	if !n.encounterPointSafe(100, 5, 5, 40) {
		t.Fatal("verified abort still blocked")
	}
	skill := &MovementSkill{Backend: &GameBackend{Knowledge: k}, Navigator: safeTravelTiles(t, safeTravelFloor(100, 12, 12))}
	bound, err := skill.travelNavigator(40, token)
	if err != nil {
		t.Fatal(err)
	}
	skill.Navigator = bound
	o := aimcp.Observation{Floor: 100, X: 4, Y: 5, Character: aimcp.Entity{Level: 40}, EncounterPolicy: token}
	if err := skill.checkTravelEncounters(o, aigame.Action{Route: "c"}); err != nil {
		t.Fatal(err)
	}
	o.EncounterPolicy = k.EncounterPolicyToken() + ":-"
	if err := skill.checkTravelEncounters(o, aigame.Action{Route: "c"}); err == nil {
		t.Fatal("cached route ignored revoked exemption")
	}
	// A higher-priority overlapping row with real/unknown enemies wins.
	r.ZOrder = 2 // Duplicate IDs must not transfer exemptions across rows.
	r.GroupIDs = []int{77, -1, -1, -1, -1, -1, -1, -1, -1, -1}
	k.Encounters = append(k.Encounters, r)
	n.nonSpawning = k.NonSpawningEncounters(k.EncounterPolicyToken() + ":0")
	if n.encounterPointSafe(100, 5, 5, 40) {
		t.Fatal("overlapping normal encounter bypassed")
	}
	if len(k.NonSpawningEncounters(token)) != 0 {
		t.Fatal("old table retained exemption")
	}
	projected := projectAutomationObservation(aimcp.Observation{EncounterPolicy: token})
	if projected.EncounterPolicy != token {
		t.Fatal("preview lost policy")
	}
}
