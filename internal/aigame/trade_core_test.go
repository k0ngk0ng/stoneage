package aigame

import (
	"bytes"
	"errors"
	"testing"
)

func tradeCoreState() *gameState {
	state := newGameState(true)
	state.snapshot.Phase = PhaseWorld
	state.snapshot.Position = Point{Floor: 1, X: 10, Y: 10, Direction: 0}
	state.snapshot.Character = "me"
	state.snapshot.Player.ID = 1
	state.actors[1] = ActorSnapshot{ID: 1, CharType: 1, X: 10, Y: 10, Name: "me"}
	state.actors[77] = ActorSnapshot{ID: 77, CharType: 1, X: 10, Y: 9, Name: "bob"}
	state.inventory[5] = InventoryItem{Index: 5, Name: "sword"}
	return &state
}

func bindTradeCore(state *gameState) {
	state.trade.snapshot.Active = true
	state.trade.snapshot.Phase = TradePhaseTrading
	state.trade.snapshot.PeerID = 77
	state.trade.snapshot.PeerFD = 77
	state.trade.snapshot.PeerName = "bob"
}

func tradeCoreAction(state *gameState, command string) Action {
	return Action{Kind: ActionTrade, Command: command}
}

func TestTradeRequestUsesOnlyUniqueFrontPlayer(t *testing.T) {
	state := tradeCoreState()
	action := Action{Kind: ActionTrade, Command: "request", TargetID: 77}
	values, function, err := validateTradeActionLocked(state, action)
	if err != nil {
		t.Fatalf("valid front player rejected: %v", err)
	}
	if function != "TD" || len(values) != 1 || string(values[0].text) != "D|D" {
		t.Fatalf("request wire = function %q values %#v", function, values)
	}

	state.actors[78] = ActorSnapshot{ID: 78, CharType: 1, X: 10, Y: 9, Name: "alice"}
	if _, _, err := validateTradeActionLocked(state, action); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("duplicate front players accepted: %v", err)
	}
	delete(state.actors, 78)
	state.actors[77] = ActorSnapshot{ID: 77, CharType: 2, X: 10, Y: 9, Name: "bob"}
	if _, _, err := validateTradeActionLocked(state, action); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("non-player front actor accepted: %v", err)
	}
}

func TestTradeLockUsesConfirmControlPacket(t *testing.T) {
	state := tradeCoreState()
	bindTradeCore(state)
	action := tradeCoreAction(state, "lock")
	values, function, err := validateTradeActionLocked(state, action)
	if err != nil {
		t.Fatalf("lock rejected: %v", err)
	}
	if function != "TD" || len(values) != 1 || string(values[0].text) != "T|77|bob|C|confirm" {
		t.Fatalf("lock wire = function %q values %#v", function, values)
	}
	applyTradeActionLocked(state, action, function)
	if !state.trade.snapshot.OwnLocked || !state.trade.snapshot.OwnLockSubmitted {
		t.Fatalf("successful lock was not recorded: %+v", state.trade.snapshot)
	}
}

func TestTradePeerControlPacketsAdvanceLockStages(t *testing.T) {
	state := tradeCoreState()
	bindTradeCore(state)
	applyTradeEventLocked(state, stringEvent("TD", "T|77|bob|C"))
	if !state.trade.snapshot.PeerLocked || state.trade.snapshot.PeerFinal {
		t.Fatalf("peer C stage = %+v", state.trade.snapshot)
	}
	applyTradeActionLocked(state, Action{Kind: ActionTrade, Command: "lock"}, "TD")
	applyTradeEventLocked(state, stringEvent("TD", "T|77|bob|A"))
	if !state.trade.snapshot.PeerLocked || !state.trade.snapshot.PeerFinal {
		t.Fatalf("peer A stage = %+v", state.trade.snapshot)
	}
}

func TestTradeConfirmationContainsSixOfferGroups(t *testing.T) {
	state := tradeCoreState()
	bindTradeCore(state)
	state.trade.snapshot.OwnLocked = true
	state.trade.snapshot.PeerLocked = true
	state.trade.snapshot.OwnOffers = [tradeOfferSlots]TradeOffer{{Kind: "item", ItemIndex: 5}, {}}
	state.trade.ownItems[0] = state.inventory[5]
	state.trade.snapshot.OwnPet = &TradeOffer{Kind: "pet", PetSlot: 2}
	state.pets["test-pet"] = PetSnapshot{Slot: 2, IdentityKnown: true, Identity: "test-pet"}
	state.trade.ownPetIdentity = "test-pet"
	state.trade.snapshot.PeerOffers = [tradeOfferSlots]TradeOffer{{Kind: "gold", Amount: 33}, {Kind: "item", ItemIndex: 9}}
	values, function, err := validateTradeActionLocked(state, tradeCoreAction(state, "confirm"))
	if err != nil {
		t.Fatalf("confirmation rejected: %v", err)
	}
	want := "T|77|bob|K|I|5|I|-1|P|2|G|33|I|9|P|-1"
	if function != "TD" || len(values) != 1 || string(values[0].text) != want {
		t.Fatalf("confirmation wire = function %q values %#v, want %q", function, values, want)
	}
}

func TestTradeNameIsEncodedAsCP936(t *testing.T) {
	state := tradeCoreState()
	bindTradeCore(state)
	state.trade.snapshot.PeerName = "小明"
	want, err := encodeLegacyUTF8("T|77|小明|I|1|5")
	if err != nil {
		t.Fatal(err)
	}
	values, function, err := validateTradeActionLocked(state, Action{Kind: ActionTrade, Command: "offer-item", Index: 0, Value: 5})
	if err != nil {
		t.Fatalf("Chinese peer name rejected: %v", err)
	}
	if function != "TD" || len(values) != 1 || !bytes.Equal(values[0].text, want) {
		t.Fatalf("encoded trade name = %x, want %x", values[0].text, want)
	}
}

func TestTradeOfferChangeAfterLockBecomesUncertain(t *testing.T) {
	state := tradeCoreState()
	bindTradeCore(state)
	applyTradeEventLocked(state, stringEvent("TD", "T|77|bob|I|1|5|sword|effect|5|damage"))
	if !state.trade.snapshot.PeerOffers[0].Confirmed {
		t.Fatalf("peer offer was not marked confirmed: %+v", state.trade.snapshot.PeerOffers[0])
	}
	applyTradeEventLocked(state, stringEvent("TD", "T|77|bob|C"))
	applyTradeEventLocked(state, stringEvent("TD", "T|77|bob|G|1|10"))
	if !state.trade.snapshot.Uncertain || state.trade.snapshot.Phase != TradePhaseUncertain {
		t.Fatalf("post-lock offer change was not fenced: %+v", state.trade.snapshot)
	}
	if _, _, err := validateTradeActionLocked(state, Action{Kind: ActionTrade, Command: "offer-item", Index: 0, Value: 5}); !errors.Is(err, ErrInvalidAction) {
		t.Fatalf("uncertain trade accepted a new offer: %v", err)
	}
}

func TestTradeIncomingPeerAndCancelReopen(t *testing.T) {
	state := tradeCoreState()
	applyTradeEventLocked(state, stringEvent("TD", "C|77|bob|1"))
	if got := state.trade.snapshot; !got.Active || got.Manual || got.PeerID != 77 {
		t.Fatalf("incoming peer: %+v", got)
	}
	applyTradeActionLocked(state, Action{Kind: ActionTrade, Command: "cancel"}, "TD")
	if _, _, err := validateTradeActionLocked(state, Action{Kind: ActionTrade, Command: "lock"}); err == nil {
		t.Fatal("continued after cancel submission")
	}
	applyTradeEventLocked(state, stringEvent("TD", "W|77|bob"))
	a := Action{Kind: ActionTrade, Command: "request", TargetID: 77}
	if _, _, err := validateTradeActionLocked(state, a); err != nil {
		t.Fatalf("cannot reopen after authoritative close: %v", err)
	}
	applyTradeActionLocked(state, a, "TD")
	if got := state.trade.snapshot; !got.Pending || got.Closed || got.Uncertain {
		t.Fatalf("new request: %+v", got)
	}
	applyTradeEventLocked(state, stringEvent("TD", "C|88|different|1"))
	if !state.trade.snapshot.Uncertain {
		t.Fatal("accepted wrong request recipient")
	}
}

func TestTradeUncertainWriteAndOwnInventoryChangeFenceConfirmation(t *testing.T) {
	state := tradeCoreState()
	bindTradeCore(state)
	applyTradeActionLocked(state, Action{Kind: ActionTrade, Command: "offer-item", Index: 0, Value: 5}, "TD")
	state.inventory[5] = InventoryItem{Index: 5, Name: "different"}
	if _, _, err := validateTradeActionLocked(state, Action{Kind: ActionTrade, Command: "lock"}); err == nil {
		t.Fatal("locked a changed inventory slot")
	}
	markTradeWriteUncertainLocked(state)
	if _, _, err := validateTradeActionLocked(state, Action{Kind: ActionTrade, Command: "offer-gold", Value: 1}); err == nil {
		t.Fatal("replayed after uncertain write")
	}
	if _, _, err := validateTradeActionLocked(state, Action{Kind: ActionTrade, Command: "cancel"}); err != nil {
		t.Fatal(err)
	}
}
