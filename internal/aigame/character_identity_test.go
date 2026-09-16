package aigame

import (
	"strings"
	"testing"
)

const testPersistentCharacterID = "pc1_0123456789abcdef0123456789abcdef"

func TestPersistentCharacterIdentityObservationValidation(t *testing.T) {
	for _, suffix := range []string{"", "|character_id=" + testPersistentCharacterID} {
		o, ok := parseAIObservation(strings.Split(partyObservationBase+suffix, "|")[1:])
		if !ok || (o.PersistentCharacterID != "") != (suffix != "") {
			t.Fatalf("suffix=%q observation=%+v valid=%v", suffix, o, ok)
		}
	}
	for _, suffix := range []string{"|character_id=", "|character_id=account:0", "|character_id=17", "|character_id=pc1_0123456789abcdef0123456789abcdeF", "|character_id=" + testPersistentCharacterID + "x", "|character_id=" + testPersistentCharacterID + "|character_id=" + testPersistentCharacterID} {
		if _, ok := parseAIObservation(strings.Split(partyObservationBase+suffix, "|")[1:]); ok {
			t.Fatalf("invalid identity accepted: %q", suffix)
		}
	}
}

func TestLoadedCharacterIdentityExpiresAtLifecycleBoundaries(t *testing.T) {
	for name, event := range map[string]Event{
		"login":              stringEvent("CharLogin", "successful"),
		"logout":             stringEvent("CharLogout", "successful"),
		"missing-on-refresh": stringEvent("S", partyObservationBase),
	} {
		t.Run(name, func(t *testing.T) {
			state := newGameState(true)
			applyEventLocked(&state, stringEvent("S", partyObservationBase+"|character_id="+testPersistentCharacterID))
			if state.snapshot.AI.PersistentCharacterID != testPersistentCharacterID {
				t.Fatal("validated identity missing")
			}
			applyEventLocked(&state, event)
			if state.snapshot.AI.PersistentCharacterID != "" {
				t.Fatal("previous character identity survived lifecycle change")
			}
		})
	}
}
