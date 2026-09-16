package aigame

import (
	"encoding/json"
	"os"
	"testing"
)

// Feed production-C harness output through the ordinary Go event path.
func TestNativePersonIdentityFixture(t *testing.T) {
	path := os.Getenv("STONEAGE_NATIVE_PERSON_FIXTURE")
	if path == "" {
		t.Skip("offline native harness fixture not requested")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Function     string   `json:"function"`
			Data         string   `json:"data"`
			Metadata     []string `json:"metadata"`
			ObjectID     int32    `json:"object_id"`
			PersistentID string   `json:"persistent_character_id"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) < 2 {
		t.Fatal("missing C/party fixture cases")
	}
	covered := map[string]bool{}
	for _, tc := range fixture.Cases {
		state := newGameState(true)
		if !ValidPersistentCharacterID(tc.PersistentID) || len(tc.Metadata) == 0 {
			t.Fatal("missing native metadata")
		}
		for _, metadata := range tc.Metadata {
			applyEventLocked(&state, stringEvent("S", metadata))
		}
		applyEventLocked(&state, stringEvent(tc.Function, tc.Data))
		matched := false
		if tc.Function == "C" {
			matched = state.actors[tc.ObjectID].PersistentCharacterID == tc.PersistentID
		}
		if tc.Function == "S" {
			for _, member := range state.party {
				if member.ID == tc.ObjectID && member.PersistentCharacterID == tc.PersistentID {
					matched = true
				}
			}
		}
		if !matched {
			t.Fatalf("native fixture did not attribute: %+v", tc)
		}
		covered[tc.Function] = true
	}
	if !covered["C"] || !covered["S"] {
		t.Fatal("native fixture must cover visible and party records")
	}
}
