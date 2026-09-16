package aiservice

import (
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestInventoryDisplayNameCannotSatisfyTemplateQuestCondition(t *testing.T) {
	snapshot := aigame.Snapshot{Inventory: []aigame.InventoryItem{
		{Index: 9, Name: "item:2415"},
		{Index: 10, Name: "ordinary-item"},
	}}
	observed := ProjectObservation(aimcp.Binding{}, snapshot)
	if (automation.Condition{Kind: "item_count", ID: "item:2415", Value: 1}).Match(projectAutomationObservation(observed)) {
		t.Fatal("an item display name became evidence of a server template ID")
	}
	if observed.Inventory["ordinary-item"] != 1 {
		t.Fatal("ordinary inventory name counts were lost")
	}
	if len(observed.InventoryItems) != 2 || observed.InventoryItems[0].Name != "item:2415" {
		t.Fatal("structured inventory display names were lost")
	}
}

func TestKnownBackpackTemplatesSatisfyQuestConditions(t *testing.T) {
	snapshot := aigame.Snapshot{
		AI: aigame.AIObservation{Received: true, ItemsKnown: true, Items: []aigame.AIInventoryItem{
			{Slot: 9, TemplateID: 2415}, {Slot: 11, TemplateID: 2415}, {Slot: 12, TemplateID: 2414},
		}},
		Inventory: []aigame.InventoryItem{{Index: 9, Name: "renamed flower"}},
	}
	observed := ProjectObservation(aimcp.Binding{}, snapshot)
	projected := projectAutomationObservation(observed)
	for _, condition := range []automation.Condition{
		{Kind: "item_count", ID: "item:2415", Value: 2},
		{Kind: "item_count", ID: "item:2414", Value: 1},
		{Kind: "flag_set", ID: "inventory:known"},
		{Kind: "backpack_free_slots", Value: 12},
	} {
		if !condition.Match(projected) {
			t.Fatalf("authoritative backpack did not satisfy %+v", condition)
		}
	}
	if observed.OwnProgress["backpack_used_slots"] != 3 {
		t.Fatal("occupied backpack count did not come from the authoritative sample")
	}
	if (automation.Condition{Kind: "backpack_free_slots", Value: 13}).Match(projected) {
		t.Fatal("occupied slots were counted as free quest space")
	}
	snapshot.AI.Items = nil
	observed = ProjectObservation(aimcp.Binding{}, snapshot)
	if !observed.Flags["inventory:known"] || observed.OwnProgress["backpack_used_slots"] != 0 {
		t.Fatal("an explicitly empty backpack became unknown")
	}
	if observed.Inventory["item:2415"] != 0 {
		t.Fatal("removed template remained present through an older display inventory")
	}
	if !(automation.Condition{Kind: "backpack_free_slots", Value: 15}).Match(projectAutomationObservation(observed)) {
		t.Fatal("known empty backpack lost free capacity")
	}
}

func TestUnknownBackpackTemplatesCannotSatisfyQuestConditions(t *testing.T) {
	for _, ai := range []aigame.AIObservation{
		{Received: true, Items: []aigame.AIInventoryItem{{Slot: 9, TemplateID: 2415}}},
		{ItemsKnown: true, Items: []aigame.AIInventoryItem{{Slot: 9, TemplateID: 2415}}},
	} {
		observed := ProjectObservation(aimcp.Binding{}, aigame.Snapshot{AI: ai})
		projected := projectAutomationObservation(observed)
		if (automation.Condition{Kind: "item_count", ID: "item:2415", Value: 1}).Match(projected) ||
			(automation.Condition{Kind: "flag_set", ID: "inventory:known"}).Match(projected) ||
			(automation.Condition{Kind: "backpack_free_slots", Value: 1}).Match(projected) {
			t.Fatal("unvalidated backpack became quest evidence")
		}
		if _, known := observed.OwnProgress["backpack_used_slots"]; known {
			t.Fatal("unknown backpack became a known slot count")
		}
	}
}
