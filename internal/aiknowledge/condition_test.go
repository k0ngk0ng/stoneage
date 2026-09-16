package aiknowledge

import "testing"

func TestItemAbsentConditionValidationAndAutomationCompatibility(t *testing.T) {
	for _, condition := range []MachineCondition{
		{Kind: "item_absent", ID: "item:2417"},
		{Kind: "item_absent", ID: "item:2417", Value: 0},
	} {
		if err := condition.Validate(); err != nil {
			t.Fatalf("valid item_absent condition rejected: %+v: %v", condition, err)
		}
		if !condition.AutomationCompatible() {
			t.Fatalf("item_absent condition was not marked automation-compatible: %+v", condition)
		}
	}
	for _, condition := range []MachineCondition{
		{Kind: "item_absent"},
		{Kind: "item_absent", ID: "item:2417", Value: 1},
		{Kind: "item_absent", ID: "item:2417", Value: -1},
	} {
		if err := condition.Validate(); err == nil {
			t.Fatalf("invalid item_absent condition accepted: %+v", condition)
		}
	}
}
