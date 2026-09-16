package aimcp

import (
	"encoding/json"
	"testing"
)

func TestStatAllocationValidationAtBothBoundaries(t *testing.T) {
	for _, input := range []struct {
		json  string
		valid bool
	}{
		{`{"kind":"allocate-stat","index":0,"expected_revision":1}`, true},
		{`{"kind":"allocate-stat","index":3,"expected_revision":1}`, true},
		{`{"kind":"allocate-stat","expected_revision":1}`, false},
		{`{"kind":"allocate-stat","index":4,"expected_revision":1}`, false},
		{`{"kind":"allocate-stat","index":0,"value":10,"expected_revision":1}`, false},
	} {
		var params actionParams
		if err := json.Unmarshal([]byte(input.json), &params); err != nil {
			t.Fatal(err)
		}
		action, err := params.action()
		if (err == nil) != input.valid {
			t.Fatalf("%s: %v", input.json, err)
		}
		if input.valid {
			if _, err := validateTypedAction(action); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := validateTypedAction(TypedAction{Kind: "allocate-stat", Index: 4, ExpectedRevision: 1}); err == nil {
		t.Fatal("remote accepted invalid stat index")
	}
}
