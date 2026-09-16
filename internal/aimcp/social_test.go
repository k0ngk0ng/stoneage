package aimcp

import (
	"encoding/json"
	"testing"
)

func TestSocialAndTradeActionParameterBoundaries(t *testing.T) {
	for _, tc := range []struct {
		payload string
		valid   bool
	}{
		{`{"kind":"social-setting","command":"trade","value":0,"expected_revision":1}`, true},
		{`{"kind":"social-setting","command":"duel","value":1,"expected_revision":1}`, true},
		{`{"kind":"social-setting","command":"trade","expected_revision":1}`, false},
		{`{"kind":"social-setting","command":"raw","value":63,"expected_revision":1}`, false},
		{`{"kind":"trade","command":"request","target_id":7,"expected_revision":1}`, true},
		{`{"kind":"trade","command":"request","expected_revision":1}`, false},
		{`{"kind":"trade","command":"offer-item","index":0,"value":5,"expected_revision":1}`, true},
		{`{"kind":"trade","command":"offer-item","index":2,"value":5,"expected_revision":1}`, false},
		{`{"kind":"trade","command":"offer-item","index":0,"value":0,"expected_revision":1}`, false},
		{`{"kind":"trade","command":"offer-pet","pet_slot":0,"expected_revision":1}`, true},
		{`{"kind":"trade","command":"offer-gold","index":0,"value":0,"expected_revision":1}`, false},
		{`{"kind":"trade","command":"confirm","expected_revision":1}`, true},
		{`{"kind":"trade","command":"confirm","text":"T|1|someone|K","expected_revision":1}`, false},
	} {
		var params actionParams
		if err := json.Unmarshal([]byte(tc.payload), &params); err != nil {
			t.Fatal(err)
		}
		a, err := params.action()
		if (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.payload, err)
		}
		if tc.valid {
			if _, err := validateTypedAction(a); err != nil {
				t.Fatalf("remote %s: %v", tc.payload, err)
			}
		}
	}
}

func TestTradeObservationValidation(t *testing.T) {
	for _, tc := range []struct {
		trade TradeState
		valid bool
	}{
		{TradeState{Phase: "trading", PeerName: "同伴", PeerPet: &TradeOffer{Kind: "pet", PetSlot: 0, Name: "宠物", Level: 30}}, true},
		{TradeState{PeerName: "bad\x00name"}, false},
		{TradeState{Phase: "invented-success"}, false},
		{TradeState{PeerPet: &TradeOffer{Kind: "pet", PetSlot: 5}}, false},
		{TradeState{PeerOffers: [2]TradeOffer{{Kind: "item", ItemIndex: 20}}}, false},
		{TradeState{PeerOffers: [2]TradeOffer{{Kind: "gold", Amount: -1}}}, false},
	} {
		if got := validTradeObservation(&tc.trade); got != tc.valid {
			t.Fatalf("trade validity %v want %v: %+v", got, tc.valid, tc.trade)
		}
	}
}
