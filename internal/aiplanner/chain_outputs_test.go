package aiplanner

import (
	"encoding/json"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestChainNamespacesCollectionAndDeliveryReceiptsTogether(t *testing.T) {
	p := automation.Plan{Completion: []automation.Condition{{Kind: "step_confirmed", ID: "deliver"}}, Steps: []automation.Step{
		{ID: "collect", Action: automation.Action{Skill: "pet.collect", Arguments: json.RawMessage(`{"targets":[]}`)}, Success: []automation.Condition{{Kind: "pet_collection", ID: "collect", Value: 4}}},
		{ID: "deliver", Preconditions: []automation.Condition{{Kind: "pet_collection", ID: "collect", Value: 4}, {Kind: "item_count", ID: "item:20031", Value: 1}}, Action: automation.Action{Skill: "pet.deliver", Arguments: json.RawMessage(`{"npc":"manager","pet_collection":"collect","choice":1}`)}, Success: []automation.Condition{{Kind: "step_confirmed", ID: "deliver"}}},
	}}
	if err := namespaceStepOutputs(&p, "stage-2/"); err != nil {
		t.Fatal(err)
	}
	var args map[string]any
	if err := json.Unmarshal(p.Steps[1].Action.Arguments, &args); err != nil {
		t.Fatal(err)
	}
	if p.Steps[0].ID != "stage-2/collect" || p.Steps[0].Success[0].ID != "stage-2/collect" || p.Steps[1].Preconditions[0].ID != "stage-2/collect" || args["pet_collection"] != "stage-2/collect" {
		t.Fatal("collection reference disconnected", p, args)
	}
	if p.Completion[0].ID != "stage-2/deliver" || p.Steps[1].Success[0].ID != "stage-2/deliver" {
		t.Fatal("receipt not namespaced")
	}
	if p.Steps[1].Preconditions[1].ID != "item:20031" || args["npc"] != "manager" {
		t.Fatal("non-step identity was renamed")
	}
}
