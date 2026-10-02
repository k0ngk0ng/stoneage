package aigame

import (
	"fmt"
	"strings"
	"testing"
)

func TestAIObservationActivePetRefresh(t *testing.T) {
	s := &Session{state: newGameState(false)}
	for _, slot := range []int{0, 4, -1, 2} {
		payload := partyObservationBase + fmt.Sprintf("|pet=2,known-pet,35|active_pet=%d", slot)
		s.applyEvent(stringEvent("S", payload))
		p := s.Snapshot().Player
		if !p.BattlePetSlotKnown || p.BattlePetSlot != int32(slot) {
			t.Fatalf("selection refresh %d: %+v", slot, p)
		}
	}
	before := s.Snapshot().Player
	for _, value := range []string{"", "-2", "5", "1.5", "2147483648", "x", "0|active_pet=1"} {
		payload := partyObservationBase + "|active_pet=" + value
		if _, ok := parseAIObservation(strings.Split(payload, "|")[1:]); ok {
			t.Fatalf("invalid selection accepted: %q", value)
		}
		s.applyEvent(stringEvent("S", payload))
		if got := s.Snapshot().Player; got != before {
			t.Fatalf("malformed response changed player: %+v", got)
		}
	}
	// The older optional-field response cannot undo a current native KS
	// acknowledgement when it still identifies the same owned pet.
	s.applyEvent(stringEvent("S", partyObservationBase+"|pet=2,known-pet,35"))
	if !s.Snapshot().Player.BattlePetSlotKnown || s.Snapshot().Player.BattlePetSlot != 2 || s.Snapshot().AI.BattlePetSlotKnown {
		t.Fatal("legacy response invented or erased selection evidence")
	}
	// A complete refresh removes the old slot before confirming no selection.
	s.applyEvent(stringEvent("S", partyObservationBase+"|active_pet=-1"))
	if p := s.Snapshot().Player; !p.BattlePetSlotKnown || p.BattlePetSlot != -1 {
		t.Fatal("pet removal lost authoritative deselection")
	}
}
