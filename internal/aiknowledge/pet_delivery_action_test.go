package aiknowledge

import (
	"encoding/json"
	"testing"
)

func TestTaskPetDeliveryRequiresExplicitStableIdentities(t *testing.T) {
	for _, tc := range []struct {
		ids   string
		valid bool
	}{
		{`["captured-a","captured-b"]`, true},
		{`[]`, false}, {`null`, false}, {`[0,1]`, false},
		{`["a","a"]`, false}, {`[""]`, false},
		{`["a","b","c","d","e","f"]`, false},
	} {
		step := TaskStep{Kind: "confirm", Action: TaskAction{Skill: "npc.window", Arguments: json.RawMessage(`{"npc":"commission","window_sequence":235,"choice":4,"pet_ids":` + tc.ids + `}`)}}
		if err := validateAction(step); (err == nil) != tc.valid {
			t.Fatalf("ids=%s err=%v", tc.ids, err)
		}
	}
}
