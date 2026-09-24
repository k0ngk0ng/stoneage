package aiservice

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

func TestTradeProjectionSeparatesSubmissionAndServerObservation(t *testing.T) {
	s := aigame.Snapshot{Trade: aigame.TradeSnapshot{Version: 1, Active: true,
		PeerFD: 123456, Epoch: 987654, PeerName: "Peer", OwnLockSubmitted: true,
		PeerPet: &aigame.TradeOffer{Kind: "pet", Name: "Pet", Level: 25, Confirmed: true},
	}}
	o := ProjectObservation(aimcp.Binding{CharacterID: "hero"}, s)
	if o.Trade == nil || !o.Trade.OwnLockSubmitted || o.Trade.PeerLocked || o.Trade.PeerPet.Level != 25 {
		t.Fatalf("trade projection: %+v", o.Trade)
	}
	b, err := json.Marshal(o.Trade)
	if err != nil || strings.Contains(string(b), "123456") || strings.Contains(string(b), "987654") {
		t.Fatalf("private protocol values leaked: %s %v", b, err)
	}
	o.Trade.PeerPet.Name = "changed"
	if s.Trade.PeerPet.Name != "Pet" {
		t.Fatal("projection aliases session state")
	}
}
