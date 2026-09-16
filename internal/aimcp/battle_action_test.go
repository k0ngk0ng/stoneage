package aimcp

import (
	"encoding/json"
	"testing"
)

func TestBattlePetDefaultSurvivesBothMCPValidationBoundaries(t *testing.T) {
	var params actionParams
	if err := json.Unmarshal([]byte(`{"kind":"battle","command":"pet","index":255,"target_id":255,"expected_revision":1}`), &params); err != nil {
		t.Fatal(err)
	}
	action, err := params.action()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateTypedAction(action); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []TypedAction{
		{Kind: "item", Index: 255, TargetID: 255},
		{Kind: "battle", Command: "item", Index: 255, TargetID: 255},
		{Kind: "battle", Command: "pet", Index: 255, TargetID: 10},
		{Kind: "battle", Command: "pet", Index: 20, TargetID: 255},
	} {
		invalid.ExpectedRevision = 1
		encoded, err := json.Marshal(invalid)
		if err != nil {
			t.Fatal(err)
		}
		var decoded actionParams
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, err := decoded.action(); err == nil {
			t.Fatalf("MCP accepted invalid sentinel: %+v", invalid)
		}
		if _, err := validateTypedAction(invalid); err == nil {
			t.Fatalf("remote accepted invalid sentinel: %+v", invalid)
		}
	}
}
