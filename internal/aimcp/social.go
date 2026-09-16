package aimcp

func validTradeObservation(t *TradeState) bool {
	if t == nil {
		return true
	}
	if !validText(t.PeerName, maxTextBytes) || t.PeerID < 0 {
		return false
	}
	switch t.Phase {
	case "", "idle", "requesting", "trading", "locked", "finalizing", "closed", "uncertain":
	default:
		return false
	}
	validOffer := func(o TradeOffer, pet bool) bool {
		if !validText(o.Name, maxTextBytes) || !validDisplayText(o.Effect, maxTextBytes) || !validDisplayText(o.Damage, maxTextBytes) {
			return false
		}
		switch o.Kind {
		case "":
			return true
		case "item":
			return !pet && o.ItemIndex >= 5 && o.ItemIndex < 20
		case "gold":
			return !pet && o.Amount >= 0
		case "pet":
			return pet && o.PetSlot >= 0 && o.PetSlot < 5
		default:
			return false
		}
	}
	for i := range t.OwnOffers {
		if !validOffer(t.OwnOffers[i], false) || !validOffer(t.PeerOffers[i], false) {
			return false
		}
	}
	return (t.OwnPet == nil || validOffer(*t.OwnPet, true)) && (t.PeerPet == nil || validOffer(*t.PeerPet, true))
}

func validSocialSetting(command string, value int32) bool {
	if value != 0 && value != 1 {
		return false
	}
	switch command {
	case "party", "duel", "party-chat", "trade-card", "trade":
		return true
	default:
		return false
	}
}

func validTradeAction(a TypedAction) bool {
	// Connection IDs, names and raw TD payloads never come from model input.
	if a.Text != "" || a.Route != "" || a.Value2 != 0 {
		return false
	}
	switch a.Command {
	case "request":
		return a.TargetID > 0
	case "offer-item":
		return a.Index >= 0 && a.Index <= 1 && a.Value >= 5 && a.Value < 20
	case "offer-gold":
		return a.Index >= 0 && a.Index <= 1 && a.Value > 0
	case "offer-pet":
		return a.PetSlot >= 0 && a.PetSlot < 5
	case "lock", "confirm", "cancel":
		return true
	default:
		return false
	}
}
