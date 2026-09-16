package aimcp

import (
	"encoding/json"
	"testing"
)

func TestMailActionParametersAndRemoteValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		valid   bool
	}{
		{name: "list", payload: `{"kind":"mail","command":"list","expected_revision":1}`, valid: true},
		{name: "add", payload: `{"kind":"mail","command":"add","x":4,"y":5,"expected_revision":1}`, valid: true},
		{name: "send", payload: `{"kind":"mail","command":"send","index":3,"text":"hello","color":2,"expected_revision":1}`, valid: true},
		{name: "missing recipient", payload: `{"kind":"mail","command":"send","text":"hello","expected_revision":1}`, valid: false},
		{name: "missing body", payload: `{"kind":"mail","command":"send","index":3,"expected_revision":1}`, valid: false},
		{name: "out of range", payload: `{"kind":"mail","command":"send","index":80,"text":"hello","expected_revision":1}`, valid: false},
		{name: "bad command", payload: `{"kind":"mail","command":"delete","expected_revision":1}`, valid: false},
		{name: "list recipient", payload: `{"kind":"mail","command":"list","index":0,"expected_revision":1}`, valid: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var params actionParams
			if err := json.Unmarshal([]byte(tc.payload), &params); err != nil {
				t.Fatal(err)
			}
			action, err := params.action()
			if (err == nil) != tc.valid {
				t.Fatalf("MCP validation = %v, want %v: %v", err, tc.valid, action)
			}
			if tc.valid {
				if _, err := validateTypedAction(action); err != nil {
					t.Fatalf("remote validation rejected %s: %v", tc.name, err)
				}
			}
		})
	}
}

func TestMailActionSchemaAndObservationValidation(t *testing.T) {
	schema := actionSchema()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("action schema properties have type %T", schema["properties"])
	}
	kind, ok := properties["kind"].(map[string]any)
	if !ok {
		t.Fatal("kind schema is missing")
	}
	enum, ok := kind["enum"].([]string)
	if !ok {
		t.Fatalf("kind enum has type %T", kind["enum"])
	}
	found := false
	for _, value := range enum {
		if value == "mail" {
			found = true
		}
	}
	if !found {
		t.Fatal("game_action schema does not advertise mail")
	}

	binding := Binding{CharacterID: "char", Generation: 1}
	valid := Observation{CharacterID: "char", AddressBookKnown: true, AddressBook: []AddressBookEntry{{Index: 3, Use: true, Name: "Alice"}}}
	if err := validateObservation(valid, binding); err != nil {
		t.Fatalf("valid address book rejected: %v", err)
	}
	invalid := valid
	invalid.AddressBook = []AddressBookEntry{{Index: 3}, {Index: 3}}
	if err := validateObservation(invalid, binding); err == nil {
		t.Fatal("duplicate address-book slots accepted")
	}
	invalid = valid
	invalid.AddressBook = []AddressBookEntry{{Index: 80}}
	if err := validateObservation(invalid, binding); err == nil {
		t.Fatal("out-of-range address-book slot accepted")
	}
}
