package aigame

import (
	"strings"
	"testing"
)

func TestEncounterPolicyObservationCompatibility(t *testing.T) {
	s := &Session{}
	token := "missing-group-abort-v1:0123456789abcdef:21"
	s.applyEvent(stringEvent("S", partyObservationBase+"|encounter_policy="+token))
	if s.state.snapshot.AI.EncounterPolicy != token {
		t.Fatal(s.state.snapshot.AI)
	}
	s.applyEvent(stringEvent("S", partyObservationBase))
	if s.state.snapshot.AI.EncounterPolicy != "" {
		t.Fatal("old response retained capability")
	}
	for _, suffix := range []string{"|encounter_policy=bad token", "|encounter_policy=" + strings.Repeat("a", 16384), "|encounter_policy=x|encounter_policy=y"} {
		if _, ok := parseAIObservation(strings.Split(partyObservationBase+suffix, "|")[1:]); ok {
			t.Fatal("invalid field accepted")
		}
	}
}
