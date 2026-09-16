package aigame

import (
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

func TestRideStatusFullAndMaskedUpdates(t *testing.T) {
	state := newGameState(true)
	if state.snapshot.Player.RidePetKnown {
		t.Fatal("zero value claims mounted")
	}
	full := func(slot string) string {
		values := make([]string, 28)
		for i := range values {
			values[i] = "0"
		}
		values[24] = slot
		return "P1|" + strings.Join(values, "|") + "|Hero|Title"
	}
	mask := "P" + namedproto.EncodeInt(134217728)
	for _, packet := range []string{full("0"), mask + "|0"} {
		state.applySystem(packet)
		if !state.snapshot.Player.RidePetKnown || state.snapshot.Player.RidePet != 0 {
			t.Fatalf("slot zero not known: %s", packet)
		}
		state.applySystem("P2|30")
		if !state.snapshot.Player.RidePetKnown || state.snapshot.Player.RidePet != 0 {
			t.Fatal("HP update lost mount")
		}
	}
	for _, packet := range []string{full("-1"), mask + "|-1"} {
		state.applySystem(packet)
		if !state.snapshot.Player.RidePetKnown || state.snapshot.Player.RidePet != -1 {
			t.Fatalf("walking not known: %s", packet)
		}
	}
	for _, packet := range []string{full("-2"), full("5"), full("?"), "P1|10|20", mask, mask + "|", mask + "|?", mask + "|-2", mask + "|5"} {
		state.applySystem(full("0"))
		state.applySystem(packet)
		if state.snapshot.Player.RidePetKnown {
			t.Fatalf("invalid packet retained mount certainty: %s", packet)
		}
	}
	for _, event := range []string{"CharLogin", "CharLogout"} {
		state.applySystem(full("0"))
		applyEventLocked(&state, stringEvent(event, "successful"))
		if state.snapshot.Player.RidePetKnown {
			t.Fatalf("%s retained previous mount", event)
		}
	}
}
