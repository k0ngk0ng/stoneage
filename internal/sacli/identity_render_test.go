package sacli

import (
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func TestObservationIdentitiesAndChatPresentation(t *testing.T) {
	snapshot := aigame.Snapshot{
		Inventory: []aigame.InventoryItem{{Index: 5, Name: "same name", Graphic: 100, TemplateID: 2415, TemplateIDKnown: true}, {Index: 6, Name: "same name", Graphic: 100}},
		Pets:      []aigame.PetSnapshot{{Slot: 0, Name: "pet", StableID: "pet-unique", IdentityKnown: true}, {Slot: 1, Name: "pet", StableID: "stale"}},
		Chat:      []aigame.ChatMessage{{Channel: "P", Text: "Welcome."}, {Channel: "P", SpeakerCharacterID: "internal-id", Text: "Player: hello"}},
	}
	text := renderSnapshot(snapshot, "")
	for _, expected := range []string{`slot=5 "same name" template_id=2415`, `slot=6 "same name" template_id=unknown`, `stable_id="pet-unique"`, `stable_id="unknown"`, "  - Welcome.", "  - Player: hello"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q in %s", expected, text)
		}
	}
	for _, unexpected := range []string{"[P]", "internal-id:", `stable_id="stale"`} {
		if strings.Contains(text, unexpected) {
			t.Fatalf("unexpected %q", unexpected)
		}
	}
}
