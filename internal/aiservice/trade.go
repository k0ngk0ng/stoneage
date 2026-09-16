package aiservice

import (
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

func projectTrade(s aigame.TradeSnapshot) *aimcp.TradeState {
	if s.Version == 0 && !s.Active && !s.Pending && !s.Closed && !s.Uncertain && !s.Manual {
		return nil
	}
	t := &aimcp.TradeState{
		Active: s.Active, Pending: s.Pending, Closed: s.Closed, Uncertain: s.Uncertain,
		Manual: s.Manual, Phase: string(s.Phase), PeerID: s.PeerID, PeerName: s.PeerName,
		OwnLockSubmitted: s.OwnLockSubmitted, OwnFinalSubmitted: s.OwnFinalSubmitted,
		PeerLocked: s.PeerLocked, PeerFinal: s.PeerFinal,
	}
	for i := range s.OwnOffers {
		t.OwnOffers[i] = projectTradeOffer(s.OwnOffers[i])
		t.PeerOffers[i] = projectTradeOffer(s.PeerOffers[i])
	}
	if s.OwnPet != nil {
		p := projectTradeOffer(*s.OwnPet)
		t.OwnPet = &p
	}
	if s.PeerPet != nil {
		p := projectTradeOffer(*s.PeerPet)
		t.PeerPet = &p
	}
	return t
}

func projectTradeOffer(s aigame.TradeOffer) aimcp.TradeOffer {
	return aimcp.TradeOffer{
		Kind: s.Kind, ItemIndex: s.ItemIndex, PetSlot: s.PetSlot, Amount: s.Amount,
		Name: s.Name, Graphic: s.Graphic, Effect: s.Effect, Damage: s.Damage,
		Level: s.Level, Attack: s.Attack, Defense: s.Defense, Quick: s.Quick,
		Transmigration: s.Transmigration, MaxHP: s.MaxHP, Submitted: s.Submitted, Confirmed: s.Confirmed,
	}
}
