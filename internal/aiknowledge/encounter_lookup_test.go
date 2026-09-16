package aiknowledge

import "testing"

func TestEncounterAtUsesServerPriorityAndSourceOrder(t *testing.T) {
	bounds := Rectangle{X: 10, Y: 20, X2: 12, Y2: 22}
	k := &Knowledge{Encounters: []EncounterArea{
		{ID: 80, Floor: 100, Bounds: bounds, ZOrder: 1},
		{ID: 90, Floor: 100, Bounds: bounds, ZOrder: 3, GroupIDs: []int{7}, GroupProbabilities: []int{100}},
		{ID: 10, Floor: 100, Bounds: bounds, ZOrder: 3},
		{ID: 20, Floor: 200, Bounds: bounds, ZOrder: 9},
	}}
	for _, point := range [][2]int{{10, 20}, {11, 21}, {12, 22}} {
		row, ok := k.EncounterAt(100, point[0], point[1])
		if !ok || row.ID != 90 {
			t.Fatalf("point %v selected %+v, found=%v", point, row, ok)
		}
		row.GroupIDs[0] = 99
		row.GroupProbabilities[0] = 0
	}
	if k.Encounters[1].GroupIDs[0] != 7 || k.Encounters[1].GroupProbabilities[0] != 100 {
		t.Fatal("lookup exposes mutable source slots")
	}
	for _, point := range [][2]int{{9, 20}, {13, 22}, {10, 19}, {12, 23}} {
		if row, ok := k.EncounterAt(100, point[0], point[1]); ok {
			t.Fatalf("outside point %v selected %+v", point, row)
		}
	}
}

func TestEncounterAtIgnoresDisabledRows(t *testing.T) {
	k := &Knowledge{Encounters: []EncounterArea{
		{ID: 1, Floor: 100, Bounds: Rectangle{X2: 10, Y2: 10}, ZOrder: 0},
		{ID: 2, Floor: 100, Bounds: Rectangle{X2: 10, Y2: 10}, ZOrder: -1},
	}}
	if _, ok := k.EncounterAt(100, 5, 5); ok {
		t.Fatal("disabled encounter selected")
	}
	if _, ok := (*Knowledge)(nil).EncounterAt(100, 5, 5); ok {
		t.Fatal("nil knowledge selected an encounter")
	}
}
