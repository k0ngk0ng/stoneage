package aiservice

import (
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestSavepointProjectionPreservesKnownBitsForQuestConditions(t *testing.T) {
	snapshot := aigame.Snapshot{AI: aigame.AIObservation{
		Received: true, SavePointsKnown: true, SavePoints: -2147483647,
	}}
	observed := ProjectObservation(aimcp.Binding{}, snapshot)
	projected := projectAutomationObservation(observed)
	for _, bit := range []string{"savepoint:0", "savepoint:31"} {
		if !(automation.Condition{Kind: "flag_set", ID: bit}).Match(projected) {
			t.Fatalf("server-recorded village bit lost: %s", bit)
		}
	}
	if !(automation.Condition{Kind: "flag_clear", ID: "savepoint:1"}).Match(projected) {
		t.Fatal("known absent village bit did not remain false")
	}
	if projected.OwnProgress["savepoints"] != -2147483647 {
		t.Fatal("raw savepoint bitmask changed")
	}
	snapshot.AI.SavePoints = 0
	projected = projectAutomationObservation(ProjectObservation(aimcp.Binding{}, snapshot))
	if !(automation.Condition{Kind: "flag_clear", ID: "savepoint:0"}).Match(projected) {
		t.Fatal("explicit zero savepoint mask was lost")
	}
}

func TestMissingSavepointObservationCannotSatisfyQuestPrerequisite(t *testing.T) {
	for _, ai := range []aigame.AIObservation{
		{Received: true},                       // Older server: no optional savepoint field.
		{SavePointsKnown: true, SavePoints: 1}, // No validated own-state response.
	} {
		observed := ProjectObservation(aimcp.Binding{}, aigame.Snapshot{AI: ai})
		projected := projectAutomationObservation(observed)
		for _, kind := range []string{"flag_set", "flag_clear"} {
			if (automation.Condition{Kind: kind, ID: "savepoint:0"}).Match(projected) {
				t.Fatalf("unknown savepoint satisfied %s", kind)
			}
		}
		if _, known := observed.OwnProgress["savepoints"]; known {
			t.Fatal("missing savepoint field became known")
		}
	}
}
