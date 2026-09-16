package aiknowledge

import (
	"encoding/json"
	"testing"
)

func TestHealerTaskCannotSupplyRatesOrWindowCommands(t *testing.T) {
	for _, tc := range []struct {
		arguments string
		valid     bool
	}{
		{`{"npc":"village-nurse"}`, true},
		{`{"npc":"village-nurse","hp_rate_milli":0}`, false},
		{`{"npc":"village-nurse","choice":4}`, false},
		{`{"npc":"village-nurse","actor_id":42}`, false},
		{`{"npc":""}`, false},
	} {
		step := TaskStep{Kind: "heal", Action: TaskAction{Skill: "npc.heal", Arguments: json.RawMessage(tc.arguments)}}
		if err := validateAction(step); (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.arguments, err)
		}
	}
}

func TestRecoveryTaskUsesAutomaticReviewedSelection(t *testing.T) {
	for _, args := range []string{`{}`, `{"npc":"arbitrary"}`, `{"hp_rate_milli":0}`} {
		err := validateAction(TaskStep{Kind: "recover", Action: TaskAction{Skill: "npc.recover", Arguments: json.RawMessage(args)}})
		if (err == nil) != (args == `{}`) {
			t.Fatalf("args=%s err=%v", args, err)
		}
	}
}

func TestItemHealingTaskCannotSelectRawSlotsOrTargets(t *testing.T) {
	for _, args := range []string{`{"item":"meat"}`, `{"item":"meat","slot":5}`, `{"item":"meat","target":1}`, `{"item":"meat","hp":999}`} {
		err := validateAction(TaskStep{Kind: "heal_item", Action: TaskAction{Skill: "item.heal", Arguments: json.RawMessage(args)}})
		if (err == nil) != (args == `{"item":"meat"}`) {
			t.Fatalf("args=%s err=%v", args, err)
		}
	}
}

func TestStockTaskCannotSupplyShopOrPrice(t *testing.T) {
	for _, args := range []string{`{"item":"meat","target_count":2}`, `{"item":"meat","target_count":16}`, `{"item":"meat","target_count":0}`, `{"item":"meat","target_count":2,"unit_price":0}`, `{"item":"meat","target_count":2,"npc":"other"}`} {
		err := validateAction(TaskStep{Kind: "stock", Action: TaskAction{Skill: "item.stock", Arguments: json.RawMessage(args)}})
		if (err == nil) != (args == `{"item":"meat","target_count":2}`) {
			t.Fatalf("%s: %v", args, err)
		}
	}
}

func TestStockTaskReservedCapacity(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{
		{`{"item":"meat","target_count":5,"reserve_slots":2}`, true},
		{`{"item":"meat","target_count":5,"reserve_slots":10}`, true},
		{`{"item":"meat","target_count":5,"reserve_slots":11}`, false},
		{`{"item":"meat","target_count":5,"reserve_slots":-1}`, false},
		{`{"item":"meat","target_count":5,"reserve_slots":1.5}`, false},
		{`{"item":"meat","target_count":5,"reserve_slots":null}`, false},
	} {
		err := validateAction(TaskStep{Kind: "stock", Action: TaskAction{Skill: "item.stock", Arguments: json.RawMessage(tc.raw)}})
		if (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.raw, err)
		}
	}
}
