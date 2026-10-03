package aiknowledge

import (
	"strings"
	"testing"
)

func policyKnowledge() *Knowledge {
	return &Knowledge{Encounters: []EncounterArea{{ID: 21, Floor: 100,
		Bounds:               Rectangle{X: 2, Y: 3, Width: 8, Height: 7, X2: 10, Y2: 10},
		EncounterProbability: Range{Min: 10, Max: 20}, MaxEnemies: 4, ZOrder: 1,
		EventNow: -1, EventEnd: -1, EnemyGroup: -1,
		GroupIDs:           []int{77, 1230, -1, -1, -1, -1, -1, -1, -1, -1},
		GroupProbabilities: []int{100, 0, -1, -1, -1, -1, -1, -1, -1, -1},
	}}, Groups: []EncounterGroup{{ID: 77}, {ID: 88}}}
}

func TestEncounterPolicyRequiresMatchingNativeTables(t *testing.T) {
	k := policyKnowledge()
	token := k.EncounterPolicyToken()
	if token == "" || !k.NonSpawningEncounters(token + ":0")[0] {
		t.Fatal(token)
	}
	for _, bad := range []string{"", "missing-group-abort-v2:0000000000000000", token + "0:21"} {
		if len(k.NonSpawningEncounters(bad)) != 0 {
			t.Fatal("unverified exemption", bad)
		}
	}
	for _, list := range []string{"", "0,0", "1", "-1", "00", "+0", "0,", " 0"} {
		if len(k.NonSpawningEncounters(token+":"+list)) != 0 {
			t.Fatal("malformed ordinal accepted", list)
		}
	}
	if len(k.NonSpawningEncounters(token+":-")) != 0 {
		t.Fatal("native reload not respected")
	}
	for name, mutate := range map[string]func(*Knowledge){
		"changed rectangle":     func(k *Knowledge) { k.Encounters[0].Bounds.Width++; k.Encounters[0].Bounds.X2++ },
		"changed priority":      func(k *Knowledge) { k.Encounters[0].ZOrder++ },
		"changed group":         func(k *Knowledge) { k.Encounters[0].GroupIDs[1] = 567 },
		"changed probability":   func(k *Knowledge) { k.Encounters[0].GroupProbabilities[1]++ },
		"changed event":         func(k *Knowledge) { k.Encounters[0].EventNow = 3 },
		"extra overlapping row": func(k *Knowledge) { r := k.Encounters[0]; r.ID++; r.ZOrder++; k.Encounters = append(k.Encounters, r) },
		"duplicate row id":      func(k *Knowledge) { k.Encounters = append(k.Encounters, k.Encounters[0]) },
		"incomplete row":        func(k *Knowledge) { k.Encounters[0].GroupIDs = nil },
	} {
		t.Run(name, func(t *testing.T) {
			changed := policyKnowledge()
			mutate(changed)
			if changed.EncounterPolicyToken() == token || len(changed.NonSpawningEncounters(token+":0")) != 0 {
				t.Fatal("stale proof accepted")
			}
		})
	}
}

func TestEncounterBlankEventFieldsMatchNativeAtoi(t *testing.T) {
	fields := strings.Split("21,100,582,399,783,560,1,5,2,40,88,91,89,92,1230,,,,,,50,50,10,10,100,,,,,,,,", ",")
	rows, _, err := parseEncounters([]byte(strings.Join(fields, ",")), "encount.txt", true)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	r := rows[0]
	if r.EventNow != 0 || r.EventEnd != 0 || r.EnemyGroup != 0 || r.GroupIDs[5] != -1 || r.GroupProbabilities[5] != -1 {
		t.Fatal(r)
	}
}

func TestEncounterPolicyDoesNotInventEventOrEnemyExemptions(t *testing.T) {
	k := policyKnowledge()
	k.Encounters[0].GroupIDs[1] = -1
	k.Encounters[0].EnemyGroup = 1230
	k.Encounters[0].EventNow = 5
	// A known group with missing enemies is not the unconditional early abort.
	k.Groups[0].EnemyIDs = []int{99999}
	if len(k.NonSpawningEncounters(k.EncounterPolicyToken()+":-")) != 0 {
		t.Fatal("event/enemy gap marked safe")
	}
}
