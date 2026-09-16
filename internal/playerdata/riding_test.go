package playerdata

import "testing"

func TestSnapshotRidingObservationsAreNotEditable(t *testing.T) {
	doc, err := ParseSave([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := doc.Snapshot()
	if err != nil || snapshot.RidePetSlot != nil || snapshot.LearnRide != nil {
		t.Fatalf("unknown riding: %+v %v", snapshot, err)
	}
	for key, value := range map[string]int64{"ridepet": 0, "learnride": 30} {
		if err := doc.Character.SetInteger(key, value); err != nil {
			t.Fatal(err)
		}
	}
	data, err := doc.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := ParseSave(data)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = reloaded.Snapshot()
	if err != nil || snapshot.RidePetSlot == nil || *snapshot.RidePetSlot != 0 || snapshot.LearnRide == nil || *snapshot.LearnRide != 30 {
		t.Fatalf("saved riding: %+v %v", snapshot, err)
	}
	for _, attr := range Definitions("character") {
		if attr.Key == "ridepet" || attr.Key == "learnride" {
			t.Fatal("riding became directly editable")
		}
	}
}
