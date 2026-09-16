package aigame

import (
	"fmt"
	"strconv"
	"strings"
)

// The preserved 2.5 trade window has two item/gold display positions and one
// pet position on each side.  The display position (0 or 1) is separate from
// the absolute inventory slot carried by an item offer.
const (
	tradeOfferSlots = 2
	tradePetShow    = 3
)

// tradeDirectionOffsets follows CHAR_getCoordinationDir in the preserved
// server. Direction 0 is north and the remaining values proceed clockwise.
var tradeDirectionOffsets = [...]struct{ x, y int32 }{
	{0, -1}, {1, -1}, {1, 0}, {1, 1},
	{0, 1}, {-1, 1}, {-1, 0}, {-1, -1},
}

// TradePhase is the local interpretation of the server's TD handshake.  The
// server has no explicit "accepted" packet: a successful C packet opens the
// window immediately, while C/A packets received later describe the peer's
// confirmation stages.
type TradePhase string

const (
	TradePhaseIdle       TradePhase = "idle"
	TradePhaseRequesting TradePhase = "requesting"
	TradePhaseTrading    TradePhase = "trading"
	TradePhaseLocked     TradePhase = "locked"
	TradePhaseFinalizing TradePhase = "finalizing"
	TradePhaseClosed     TradePhase = "closed"
	TradePhaseUncertain  TradePhase = "uncertain"
)

// TradeOffer is one 2.5 item/gold display offer, or the single pet offer.
// ItemIndex is an absolute inventory slot (5..19), and PetSlot is a pet
// collection slot (0..4).  The array position in TradeSnapshot.OwnOffers or
// PeerOffers is the native item/gold display position (0 or 1).
//
// Name, Graphic and the pet attributes are observations supplied by the peer
// in a T packet.  They are intentionally optional: the local side only knows
// the source index until the server sends the peer's display record.
type TradeOffer struct {
	Kind      string `json:"kind"`
	ItemIndex int32  `json:"item_index,omitempty"`
	Amount    int32  `json:"amount,omitempty"`
	PetSlot   int32  `json:"pet_slot,omitempty"`

	Name           string `json:"name,omitempty"`
	Graphic        int32  `json:"graphic,omitempty"`
	Effect         string `json:"effect,omitempty"`
	Damage         string `json:"damage,omitempty"`
	Level          int32  `json:"level,omitempty"`
	Attack         int32  `json:"attack,omitempty"`
	Defense        int32  `json:"defense,omitempty"`
	Quick          int32  `json:"quick,omitempty"`
	Transmigration int32  `json:"transmigration,omitempty"`
	MaxHP          int32  `json:"max_hp,omitempty"`

	// Submitted is meaningful for the local side only.  A successful socket
	// write cannot be promoted to a server confirmation; Confirmed means a
	// peer offer was observed from the server, not a completed asset transfer.
	Submitted bool `json:"submitted,omitempty"`
	Confirmed bool `json:"confirmed,omitempty"`
}

// TradeSnapshot is the server-bound trade projection.  PeerFD and Epoch are
// protocol/session implementation details and never enter JSON observation.
// OwnLocked and OwnFinal mean that the corresponding local packet was
// submitted; 2.5 emits no echo for the local side, so callers must not treat
// those booleans as server acknowledgements. PeerLocked and PeerFinal are
// based on observed C/A packets.
type TradeSnapshot struct {
	Active  bool       `json:"active"`
	Pending bool       `json:"pending,omitempty"`
	Phase   TradePhase `json:"phase"`

	// PeerID is the visible actor ID used for the request.  The C packet only
	// returns the peer connection fd/name, so it may be zero for an unsolicited
	// or manually injected trade.
	PeerID   int32  `json:"peer_id,omitempty"`
	PeerFD   int32  `json:"-"`
	PeerName string `json:"peer_name,omitempty"`

	OwnOffers  [tradeOfferSlots]TradeOffer `json:"own_offers"`
	PeerOffers [tradeOfferSlots]TradeOffer `json:"peer_offers"`
	OwnPet     *TradeOffer                 `json:"own_pet,omitempty"`
	PeerPet    *TradeOffer                 `json:"peer_pet,omitempty"`

	OwnLocked  bool `json:"own_locked,omitempty"`
	PeerLocked bool `json:"peer_locked,omitempty"`
	OwnFinal   bool `json:"own_final,omitempty"`
	PeerFinal  bool `json:"peer_final,omitempty"`

	// The explicit Submitted names make the uncertainty boundary clear to
	// adapters which do not want to infer it from OwnLocked/OwnFinal.
	OwnLockSubmitted  bool `json:"own_lock_submitted,omitempty"`
	OwnFinalSubmitted bool `json:"own_final_submitted,omitempty"`

	Closed    bool   `json:"closed,omitempty"`
	Uncertain bool   `json:"uncertain,omitempty"`
	Manual    bool   `json:"manual,omitempty"`
	Version   uint64 `json:"version"`
	Epoch     uint64 `json:"-"`
}

// tradeState contains the private binding behind the public observation.
type tradeState struct {
	snapshot          TradeSnapshot
	requestSent       bool
	requestTargetID   int32
	requestTargetName string
	ownItems          [tradeOfferSlots]InventoryItem
	ownPetIdentity    string
}

func newTradeState() tradeState {
	return tradeState{snapshot: TradeSnapshot{Phase: TradePhaseIdle}}
}

func tradeFacingTarget(state *gameState, id int32) bool {
	p := state.snapshot.Position
	if p.Floor < 0 || p.Direction < 0 || p.Direction >= int32(len(tradeDirectionOffsets)) {
		return false
	}
	delta := tradeDirectionOffsets[p.Direction]
	var count int
	var found int32
	for _, actor := range state.actors {
		if actor.CharType == 1 && actor.ID != state.snapshot.Player.ID && actor.X == p.X+delta.x && actor.Y == p.Y+delta.y {
			count++
			found = actor.ID
		}
	}
	return count == 1 && found == id && safeTradeToken(state.actors[id].Name)
}

func tradeAdjacent(p Point, actor ActorSnapshot) bool {
	dx, dy := actor.X-p.X, actor.Y-p.Y
	return p.Floor >= 0 && dx >= -1 && dx <= 1 && dy >= -1 && dy <= 1 && (dx != 0 || dy != 0)
}

func tradeOwnedPet(state *gameState, slot int32) (PetSnapshot, bool) {
	for _, pet := range state.pets {
		if pet.Slot == slot && pet.IdentityKnown {
			return pet, true
		}
	}
	return PetSnapshot{}, false
}

func validateOwnTradeAssets(state *gameState) error {
	trade := &state.trade
	var gold int64
	for i, offer := range trade.snapshot.OwnOffers {
		switch offer.Kind {
		case "item":
			item, ok := state.inventory[offer.ItemIndex]
			if !ok || item != trade.ownItems[i] {
				return fmt.Errorf("%w: offered inventory changed; cancel trade", ErrInvalidAction)
			}
		case "gold":
			gold += int64(offer.Amount)
		}
	}
	if gold > 0 && (!state.snapshot.Player.HasStatus || gold > int64(state.snapshot.Player.Gold)) {
		return fmt.Errorf("%w: offered balance changed; cancel trade", ErrInvalidAction)
	}
	if offer := trade.snapshot.OwnPet; offer != nil {
		pet, ok := tradeOwnedPet(state, offer.PetSlot)
		if !ok || pet.Identity != trade.ownPetIdentity {
			return fmt.Errorf("%w: offered pet identity changed; cancel trade", ErrInvalidAction)
		}
	}
	return nil
}

func markTradeWriteUncertainLocked(state *gameState) { markTradeUncertainLocked(state) }

// A lifecycle boundary invalidates any old fd/offer binding. This is not a
// trade-success acknowledgement and deliberately preserves no reusable peer.
func invalidateTradeForLifecycleLocked(state *gameState) {
	if state == nil {
		return
	}
	old := state.trade.snapshot
	state.trade = newTradeState()
	state.trade.snapshot.Epoch = old.Epoch + 1
	state.trade.snapshot.Version = old.Version
	if old.Active || old.Pending || old.Uncertain || old.Manual {
		state.trade.snapshot.Closed = true
		state.trade.snapshot.Phase = TradePhaseClosed
		state.trade.snapshot.Version++
	}
}

// validateTradeActionLocked returns exactly one TD wire string.  It does not
// mutate state. The execution path revalidates and writes under stateMu.
func validateTradeActionLocked(state *gameState, action Action) ([]wireValue, string, error) {
	if state == nil {
		return nil, "", ErrClosed
	}
	if state.snapshot.Phase != PhaseWorld || state.snapshot.Battle.Active {
		return nil, "", fmt.Errorf("%w: trade requires an idle world character", ErrWrongPhase)
	}
	trade := &state.trade
	command := action.Command
	if action.Kind != ActionTrade {
		return nil, "", fmt.Errorf("%w: not a trade action", ErrInvalidAction)
	}

	// Once a human packet or an unexplained peer mutation has invalidated the
	// transaction, cancellation is the only safe agent operation.
	if trade.snapshot.Uncertain || trade.snapshot.Manual {
		if command != "cancel" || !trade.snapshot.Active || trade.snapshot.PeerFD <= 0 {
			return nil, "", fmt.Errorf("%w: uncertain trade only permits cancel", ErrInvalidAction)
		}
		return tradeCloseValues(trade)
	}

	switch command {
	case "request":
		if action.TargetID <= 0 {
			return nil, "", fmt.Errorf("%w: trade request target must be positive", ErrInvalidAction)
		}
		if trade.snapshot.Active || trade.snapshot.Pending || trade.requestSent {
			return nil, "", fmt.Errorf("%w: trade request already has a pending transaction", ErrInvalidAction)
		}
		if len(state.party) != 0 || !tradeFacingTarget(state, action.TargetID) {
			return nil, "", fmt.Errorf("%w: trade requires no party and one visible player in front matching target_id", ErrInvalidAction)
		}
		return []wireValue{{kind: wireString, text: []byte("D|D")}}, "TD", nil

	case "offer-item":
		if err := validateTradeActive(trade); err != nil {
			return nil, "", err
		}
		if trade.snapshot.OwnLocked || trade.snapshot.PeerLocked || action.Index < 0 || action.Index >= tradeOfferSlots || action.Value < 5 || action.Value >= 20 {
			return nil, "", fmt.Errorf("%w: item offer requires an unlocked trade, position 0..1 and backpack slot 5..19", ErrInvalidAction)
		}
		if _, ok := state.inventory[action.Value]; !ok {
			return nil, "", fmt.Errorf("%w: offered item is not observed", ErrInvalidAction)
		}
		for i, offer := range trade.snapshot.OwnOffers {
			if int32(i) != action.Index && offer.Kind == "item" && offer.ItemIndex == action.Value {
				return nil, "", fmt.Errorf("%w: item is already offered", ErrInvalidAction)
			}
		}
		values, err := tradePeerPrefix(trade)
		if err != nil {
			return nil, "", err
		}
		values = append(values, "I", strconv.FormatInt(int64(action.Index+1), 10), strconv.FormatInt(int64(action.Value), 10))
		return tradeStringValue(strings.Join(values, "|"))

	case "offer-gold":
		if err := validateTradeActive(trade); err != nil {
			return nil, "", err
		}
		if trade.snapshot.OwnLocked || trade.snapshot.PeerLocked || action.Index < 0 || action.Index >= tradeOfferSlots || action.Value <= 0 {
			return nil, "", fmt.Errorf("%w: gold offer requires an unlocked trade, position 0..1 and positive amount", ErrInvalidAction)
		}
		total := int64(action.Value)
		for i, offer := range trade.snapshot.OwnOffers {
			if int32(i) != action.Index && offer.Kind == "gold" {
				total += int64(offer.Amount)
			}
		}
		if !state.snapshot.Player.HasStatus || total > int64(state.snapshot.Player.Gold) {
			return nil, "", fmt.Errorf("%w: trade gold exceeds observed balance", ErrInvalidAction)
		}
		values, err := tradePeerPrefix(trade)
		if err != nil {
			return nil, "", err
		}
		values = append(values, "G", strconv.FormatInt(int64(action.Index+1), 10), strconv.FormatInt(int64(action.Value), 10))
		return tradeStringValue(strings.Join(values, "|"))

	case "offer-pet":
		if err := validateTradeActive(trade); err != nil {
			return nil, "", err
		}
		if trade.snapshot.OwnLocked || trade.snapshot.PeerLocked || action.PetSlot < 0 || action.PetSlot >= 5 {
			return nil, "", fmt.Errorf("%w: pet offer requires an unlocked trade and pet slot 0..4", ErrInvalidAction)
		}
		if _, ok := tradeOwnedPet(state, action.PetSlot); !ok {
			return nil, "", fmt.Errorf("%w: offered pet is not observed", ErrInvalidAction)
		}
		values, err := tradePeerPrefix(trade)
		if err != nil {
			return nil, "", err
		}
		values = append(values, "P", strconv.Itoa(tradePetShow), strconv.FormatInt(int64(action.PetSlot), 10))
		return tradeStringValue(strings.Join(values, "|"))

	case "lock":
		if err := validateTradeActive(trade); err != nil {
			return nil, "", err
		}
		if trade.snapshot.OwnLocked || trade.snapshot.OwnLockSubmitted || trade.snapshot.PeerFinal {
			return nil, "", fmt.Errorf("%w: trade lock was already submitted", ErrInvalidAction)
		}
		if err := validateOwnTradeAssets(state); err != nil {
			return nil, "", err
		}
		values, err := tradePeerPrefix(trade)
		if err != nil {
			return nil, "", err
		}
		values = append(values, "C", "confirm")
		return tradeStringValue(strings.Join(values, "|"))

	case "confirm":
		if err := validateTradeActive(trade); err != nil {
			return nil, "", err
		}
		if !trade.snapshot.OwnLocked || !trade.snapshot.PeerLocked || trade.snapshot.OwnFinal || trade.snapshot.OwnFinalSubmitted {
			return nil, "", fmt.Errorf("%w: confirm requires both observed lock stages and one final submission", ErrInvalidAction)
		}
		if err := validateOwnTradeAssets(state); err != nil {
			return nil, "", err
		}
		payload, err := buildTradeConfirmationPayload(trade)
		if err != nil {
			return nil, "", err
		}
		return tradeStringValue(payload)

	case "cancel":
		if !trade.snapshot.Active || trade.snapshot.PeerFD <= 0 {
			return nil, "", fmt.Errorf("%w: no bound trade to cancel", ErrInvalidAction)
		}
		return tradeCloseValues(trade)

	default:
		return nil, "", fmt.Errorf("%w: unsupported trade operation %q", ErrInvalidAction, command)
	}
}

func validateTradeActive(trade *tradeState) error {
	if trade == nil || !trade.snapshot.Active || trade.snapshot.Closed || trade.snapshot.PeerFD <= 0 || trade.snapshot.PeerName == "" {
		return fmt.Errorf("%w: no active server-bound trade", ErrInvalidAction)
	}
	return nil
}

func tradePeerPrefix(trade *tradeState) ([]string, error) {
	if err := validateTradeActive(trade); err != nil {
		return nil, err
	}
	if !safeTradeToken(trade.snapshot.PeerName) {
		return nil, fmt.Errorf("%w: bound peer name cannot be represented in TD", ErrInvalidAction)
	}
	return []string{"T", strconv.FormatInt(int64(trade.snapshot.PeerFD), 10), trade.snapshot.PeerName}, nil
}

func tradeCloseValues(trade *tradeState) ([]wireValue, string, error) {
	if err := validateTradeActive(trade); err != nil {
		return nil, "", err
	}
	values, err := tradePeerPrefix(trade)
	if err != nil {
		return nil, "", err
	}
	values[0] = "W"
	return tradeStringValue(strings.Join(values, "|"))
}

func tradeStringValue(value string) ([]wireValue, string, error) {
	encoded, err := encodeLegacyUTF8(value)
	if err != nil {
		return nil, "", fmt.Errorf("%w: trade text: %v", ErrTextEncoding, err)
	}
	return []wireValue{{kind: wireString, text: encoded}}, "TD", nil
}

// applyTradeActionLocked records only a successfully written local action.
// It never turns a socket write into a server confirmation. Called under the
// same lock as the socket write, before any incoming reply is applied.
func applyTradeActionLocked(state *gameState, action Action, function string) {
	if state == nil || action.Kind != ActionTrade || function != "TD" {
		return
	}
	trade := &state.trade

	if action.Command == "request" {
		if trade.snapshot.Active || trade.requestSent {
			return
		}
		trade.requestSent = true
		trade.requestTargetID = action.TargetID
		trade.requestTargetName = state.actors[action.TargetID].Name
		trade.snapshot = TradeSnapshot{Epoch: trade.snapshot.Epoch + 1, Version: trade.snapshot.Version}
		trade.snapshot.Pending = true
		trade.snapshot.PeerID = action.TargetID
		trade.snapshot.Phase = TradePhaseRequesting
		trade.snapshot.Version++
		return
	}

	if action.Command == "cancel" {
		// W is only a submission marker. The authoritative close transition is
		// applied by the subsequent TD W event.
		trade.snapshot.Uncertain = true
		trade.snapshot.Phase = TradePhaseUncertain
		trade.snapshot.Version++
		return
	}
	if !trade.snapshot.Active || trade.snapshot.Closed || trade.snapshot.Uncertain || trade.snapshot.Manual {
		return
	}

	switch action.Command {
	case "offer-item":
		offer := TradeOffer{Kind: "item", ItemIndex: action.Value, Submitted: true}
		item := state.inventory[action.Value]
		trade.ownItems[action.Index] = item
		offer.Name, offer.Graphic, offer.Effect = item.Name, item.Graphic, item.Memo
		if !sameTradeOffer(trade.snapshot.OwnOffers[action.Index], offer) {
			trade.snapshot.OwnOffers[action.Index] = offer
			trade.resetTradeConfirmations()
			trade.snapshot.Version++
		}
	case "offer-gold":
		offer := TradeOffer{Kind: "gold", Amount: action.Value, Submitted: true}
		if !sameTradeOffer(trade.snapshot.OwnOffers[action.Index], offer) {
			trade.snapshot.OwnOffers[action.Index] = offer
			trade.resetTradeConfirmations()
			trade.snapshot.Version++
		}
	case "offer-pet":
		offer := &TradeOffer{Kind: "pet", PetSlot: action.PetSlot, Submitted: true}
		pet, _ := tradeOwnedPet(state, action.PetSlot)
		trade.ownPetIdentity = pet.Identity
		offer.Name, offer.Graphic, offer.Level = pet.Name, pet.Graphic, pet.Level
		offer.Attack, offer.Defense, offer.Quick, offer.MaxHP = pet.Attack, pet.Defense, pet.Quick, pet.MaxHP
		if !sameTradeOfferPtr(trade.snapshot.OwnPet, offer) {
			trade.snapshot.OwnPet = offer
			trade.resetTradeConfirmations()
			trade.snapshot.Version++
		}
	case "lock":
		trade.snapshot.OwnLocked = true
		trade.snapshot.OwnLockSubmitted = true
		trade.snapshot.Version++
	case "confirm":
		trade.snapshot.OwnFinal = true
		trade.snapshot.OwnFinalSubmitted = true
		trade.snapshot.Version++
	}
	trade.recomputePhase()
}

// applyTradeEventLocked consumes the single string carried by a server TD
// event. Every event after the opening C must match both the bound fd and the
// bound server name; a mismatched packet cannot rewrite the active trade.
func applyTradeEventLocked(state *gameState, event Event) {
	if state == nil || event.Function != "TD" || len(event.Fields) == 0 {
		return
	}
	message := eventText(event, 0)
	parts := strings.Split(message, "|")
	if len(parts) == 0 {
		return
	}
	kind := strings.ToUpper(strings.TrimSpace(parts[0]))
	if kind != "C" && kind != "T" && kind != "W" {
		return
	}

	trade := &state.trade
	if kind == "C" {
		applyTradeOpenEventLocked(state, parts)
		return
	}
	if len(parts) < 3 {
		markTradeUncertainLocked(state, "TD peer envelope is incomplete")
		return
	}
	fd, name, ok := parseTradePeerEnvelope(parts)
	if !ok {
		markTradeUncertainLocked(state, "TD peer envelope is invalid")
		return
	}
	if !trade.snapshot.Active || trade.snapshot.PeerFD != fd || trade.snapshot.PeerName != name {
		// A late packet from a previous fd/name pair is evidence of a stale
		// stream, but must never be allowed to mutate the current trade.
		if trade.snapshot.Active {
			markTradeUncertainLocked(state, "TD peer identity changed")
		}
		return
	}

	switch kind {
	case "W":
		closeTradeLocked(trade)
		return
	case "T":
		if len(parts) < 4 {
			markTradeUncertainLocked(state, "TD offer kind is missing")
			return
		}
		applyTradeOfferEventLocked(state, parts)
	}
}

func applyTradeOpenEventLocked(state *gameState, parts []string) {
	trade := &state.trade
	if len(parts) < 4 {
		if trade.snapshot.Active {
			markTradeUncertainLocked(state, "TD C envelope is incomplete")
		}
		return
	}
	fd, name, ok := parseTradePeerEnvelope(parts)
	if !ok {
		if trade.snapshot.Active {
			markTradeUncertainLocked(state, "TD C peer envelope is invalid")
		}
		return
	}
	result, err := strconv.ParseInt(strings.TrimSpace(parts[3]), 10, 32)
	if err != nil || (result != 0 && result != 1) {
		if trade.snapshot.Active {
			markTradeUncertainLocked(state, "TD C result is invalid")
		}
		return
	}
	if result == 0 {
		// 2.5 normally speaks failure through chat and does not send C|...|0;
		// retaining a closed/request-failed marker is safer than leaving a
		// request permanently pending if a gateway emits the optional packet.
		if trade.snapshot.Active && (trade.snapshot.PeerFD != fd || trade.snapshot.PeerName != name) {
			return
		}
		if !trade.snapshot.Active && (!trade.requestSent || trade.requestTargetName != name) {
			return
		}
		closeTradeLocked(trade)
		trade.snapshot.PeerFD = fd
		trade.snapshot.PeerName = name
		return
	}

	if trade.snapshot.Active {
		if trade.snapshot.PeerFD != fd || trade.snapshot.PeerName != name {
			markTradeUncertainLocked(state, "TD C opened a different peer")
		}
		return
	}
	manual := trade.snapshot.Manual || trade.snapshot.Uncertain
	targetID := trade.requestTargetID
	if trade.requestSent {
		if trade.requestTargetName != name {
			manual = true
		}
	} else {
		var matches int
		for _, actor := range state.actors {
			if actor.CharType == 1 && actor.ID != state.snapshot.Player.ID && actor.Name == name && tradeAdjacent(state.snapshot.Position, actor) {
				matches++
				targetID = actor.ID
			}
		}
		if matches != 1 {
			manual = true
			targetID = 0
		}
	}

	startTradeLocked(trade, fd, name, targetID, manual)
}

func parseTradePeerEnvelope(parts []string) (int32, string, bool) {
	if len(parts) < 3 {
		return 0, "", false
	}
	fd, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 32)
	if err != nil || fd <= 0 || fd > 2147483647 {
		return 0, "", false
	}
	name := legacyText(parts[2])
	if !safeTradeToken(name) {
		return 0, "", false
	}
	return int32(fd), name, true
}

func safeTradeToken(value string) bool {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "|\\") {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func startTradeLocked(trade *tradeState, fd int32, name string, peerID int32, manual bool) {
	if trade == nil {
		return
	}
	trade.snapshot = TradeSnapshot{
		Active:     true,
		Phase:      TradePhaseTrading,
		PeerID:     peerID,
		PeerFD:     fd,
		PeerName:   name,
		OwnOffers:  [tradeOfferSlots]TradeOffer{},
		PeerOffers: [tradeOfferSlots]TradeOffer{},
		Manual:     manual,
		Uncertain:  manual,
		Epoch:      trade.snapshot.Epoch + 1,
		Version:    trade.snapshot.Version + 1,
	}
	trade.requestSent = true
	trade.requestTargetID = peerID
	trade.recomputePhase()
}

func closeTradeLocked(trade *tradeState) {
	if trade == nil {
		return
	}
	trade.snapshot.Active = false
	trade.snapshot.Pending = false
	trade.snapshot.Closed = true
	trade.snapshot.Manual = false
	trade.snapshot.Uncertain = false
	trade.snapshot.Phase = TradePhaseClosed
	trade.requestSent = false
	trade.requestTargetID = 0
	trade.requestTargetName = ""
	trade.snapshot.Version++
}

// markTradeUncertainLocked is the fail-closed boundary for malformed,
// mismatched, manually injected, or reordered trade packets. A caller can
// still send W/cancel while the peer remains bound, allowing the server to
// cleanly release its CHAR_TRADE state.
func markTradeUncertainLocked(state *gameState, reasons ...string) {
	if state == nil {
		return
	}
	trade := &state.trade
	trade.snapshot.Uncertain = true
	trade.snapshot.Active = !trade.snapshot.Closed && trade.snapshot.PeerFD > 0 && trade.snapshot.PeerName != ""
	trade.snapshot.Pending = false
	trade.snapshot.Phase = TradePhaseUncertain
	trade.snapshot.Version++
}

// invalidateTradeForManualPacket is called by the Web observer before it
// applies a TD packet that did not originate from the agent action path.
// Keeping the active peer allows a cancel packet to be sent, while Manual and
// Uncertain prevent the automation layer from offering or confirming in a
// human-owned window.
func invalidateTradeForManualPacket(state *gameState) {
	if state == nil {
		return
	}
	trade := &state.trade
	trade.snapshot.Manual = true
	trade.snapshot.Uncertain = true
	if trade.snapshot.Active {
		trade.snapshot.Phase = TradePhaseUncertain
	}
	trade.snapshot.Version++
}

func (state *gameState) invalidateTradeForManualPacket() {
	invalidateTradeForManualPacket(state)
}

func (trade *tradeState) resetTradeConfirmations() {
	if trade == nil {
		return
	}
	trade.snapshot.OwnLocked = false
	trade.snapshot.PeerLocked = false
	trade.snapshot.OwnFinal = false
	trade.snapshot.PeerFinal = false
	trade.snapshot.OwnLockSubmitted = false
	trade.snapshot.OwnFinalSubmitted = false
	trade.recomputePhase()
}

func (trade *tradeState) recomputePhase() {
	if trade == nil {
		return
	}
	if trade.snapshot.Uncertain || trade.snapshot.Manual {
		trade.snapshot.Phase = TradePhaseUncertain
		return
	}
	if trade.snapshot.Closed {
		trade.snapshot.Phase = TradePhaseClosed
		return
	}
	if !trade.snapshot.Active {
		if trade.snapshot.Pending {
			trade.snapshot.Phase = TradePhaseRequesting
		} else {
			trade.snapshot.Phase = TradePhaseIdle
		}
		return
	}
	if trade.snapshot.OwnFinal || trade.snapshot.PeerFinal {
		trade.snapshot.Phase = TradePhaseFinalizing
	} else if trade.snapshot.OwnLocked || trade.snapshot.PeerLocked {
		trade.snapshot.Phase = TradePhaseLocked
	} else {
		trade.snapshot.Phase = TradePhaseTrading
	}
}

func applyTradeOfferEventLocked(state *gameState, parts []string) {
	trade := &state.trade
	if trade.snapshot.Uncertain || trade.snapshot.Manual {
		return
	}
	if len(parts) == 4 && (parts[3] == "C" || parts[3] == "A") {
		if parts[3] == "A" && !trade.snapshot.OwnLockSubmitted {
			markTradeUncertainLocked(state)
			return
		}
		changed := !trade.snapshot.PeerLocked || (parts[3] == "A" && !trade.snapshot.PeerFinal)
		trade.snapshot.PeerLocked = true
		if parts[3] == "A" {
			trade.snapshot.PeerFinal = true
		}
		if changed {
			trade.snapshot.Version++
		}
		trade.recomputePhase()
		return
	}
	offer, displaySlot, ok := parseTradePeerOffer(parts)
	if !ok {
		markTradeUncertainLocked(state, "TD offer is invalid")
		return
	}
	offer.Confirmed = true
	if offer.Kind == "pet" {
		if sameTradeOfferPtr(trade.snapshot.PeerPet, &offer) {
			return
		}
		hadConfirmation := trade.snapshot.OwnLocked || trade.snapshot.PeerLocked || trade.snapshot.OwnFinal || trade.snapshot.PeerFinal || trade.snapshot.OwnLockSubmitted || trade.snapshot.OwnFinalSubmitted
		trade.snapshot.PeerPet = &offer
		if hadConfirmation {
			trade.resetTradeConfirmations()
			markTradeUncertainLocked(state, "peer pet offer changed after confirmation")
		} else {
			trade.snapshot.Version++
		}
		return
	}
	old := trade.snapshot.PeerOffers[displaySlot]
	if sameTradeOffer(old, offer) {
		return
	}
	hadConfirmation := trade.snapshot.OwnLocked || trade.snapshot.PeerLocked || trade.snapshot.OwnFinal || trade.snapshot.PeerFinal || trade.snapshot.OwnLockSubmitted || trade.snapshot.OwnFinalSubmitted
	trade.snapshot.PeerOffers[displaySlot] = offer
	if hadConfirmation {
		trade.resetTradeConfirmations()
		markTradeUncertainLocked(state, "peer offer changed after confirmation")
	} else {
		trade.snapshot.Version++
	}
}

func parseTradePeerOffer(parts []string) (TradeOffer, int, bool) {
	if len(parts) < 6 {
		return TradeOffer{}, 0, false
	}
	kind := strings.ToUpper(strings.TrimSpace(parts[3]))
	show, ok := parseTradeInt(parts[4])
	if !ok {
		return TradeOffer{}, 0, false
	}
	switch kind {
	case "I":
		if show < 1 || show > tradeOfferSlots || len(parts) < 10 {
			return TradeOffer{}, 0, false
		}
		itemIndex, ok := parseTradeInt(parts[8])
		if !ok || itemIndex < 5 || itemIndex >= 20 {
			return TradeOffer{}, 0, false
		}
		graphic, _ := parseTradeInt(parts[5])
		return TradeOffer{Kind: "item", ItemIndex: itemIndex, Graphic: graphic, Name: legacyText(parts[6]), Effect: legacyText(parts[7]), Damage: legacyText(parts[9])}, int(show - 1), true

	case "G":
		if show < 1 || show > tradeOfferSlots {
			return TradeOffer{}, 0, false
		}
		amount, ok := parseTradeInt(parts[5])
		if !ok || amount < 0 {
			return TradeOffer{}, 0, false
		}
		return TradeOffer{Kind: "gold", Amount: amount}, int(show - 1), true

	case "P":
		if show != tradePetShow || len(parts) < 14 {
			return TradeOffer{}, 0, false
		}
		petSlot, ok := parseTradeInt(parts[11])
		if !ok || petSlot < 0 || petSlot >= 5 {
			return TradeOffer{}, 0, false
		}
		graphic, _ := parseTradeInt(parts[5])
		level, _ := parseTradeInt(parts[7])
		attack, _ := parseTradeInt(parts[8])
		defense, _ := parseTradeInt(parts[9])
		quick, _ := parseTradeInt(parts[10])
		transmigration, _ := parseTradeInt(parts[12])
		maxHP, _ := parseTradeInt(parts[13])
		return TradeOffer{Kind: "pet", PetSlot: petSlot, Graphic: graphic, Name: legacyText(parts[6]), Level: level, Attack: attack, Defense: defense, Quick: quick, Transmigration: transmigration, MaxHP: maxHP}, 0, true
	default:
		return TradeOffer{}, 0, false
	}
}

func parseTradeInt(value string) (int32, bool) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(parsed), true
}

// buildTradeConfirmationPayload emits the complete six-group 2.5 K vector:
// own item 1, own item 2, own pet, peer item 1, peer item 2, peer pet.  Empty
// item groups are I|-1 and the empty pet groups are P|-1.  The peer fd/name
// come from the already bound C packet, never from Action fields.
func buildTradeConfirmationPayload(trade *tradeState) (string, error) {
	if trade == nil {
		return "", ErrClosed
	}
	if err := validateTradeActive(trade); err != nil {
		return "", err
	}
	if trade.snapshot.Uncertain || trade.snapshot.Manual {
		return "", fmt.Errorf("%w: uncertain trade cannot be confirmed", ErrInvalidAction)
	}
	values, err := tradePeerPrefix(trade)
	if err != nil {
		return "", err
	}
	groups := make([]string, 0, 12)
	seenOwn := make(map[int32]bool, tradeOfferSlots)
	seenPeer := make(map[int32]bool, tradeOfferSlots)
	for index := 0; index < tradeOfferSlots; index++ {
		group, itemIndex, ok := tradeOfferWire(trade.snapshot.OwnOffers[index], false)
		if !ok {
			return "", fmt.Errorf("%w: invalid own trade offer %d", ErrInvalidAction, index)
		}
		if itemIndex >= 0 {
			if seenOwn[itemIndex] {
				return "", fmt.Errorf("%w: own item offer is repeated", ErrInvalidAction)
			}
			seenOwn[itemIndex] = true
		}
		groups = append(groups, group)
	}
	petGroup, _, ok := tradeOfferWirePtr(trade.snapshot.OwnPet, true)
	if !ok {
		return "", fmt.Errorf("%w: invalid own pet offer", ErrInvalidAction)
	}
	groups = append(groups, petGroup)
	for index := 0; index < tradeOfferSlots; index++ {
		group, itemIndex, ok := tradeOfferWire(trade.snapshot.PeerOffers[index], false)
		if !ok {
			return "", fmt.Errorf("%w: invalid peer trade offer %d", ErrInvalidAction, index)
		}
		if itemIndex >= 0 {
			if seenPeer[itemIndex] {
				return "", fmt.Errorf("%w: peer item offer is repeated", ErrInvalidAction)
			}
			seenPeer[itemIndex] = true
		}
		groups = append(groups, group)
	}
	peerPetGroup, _, ok := tradeOfferWirePtr(trade.snapshot.PeerPet, true)
	if !ok {
		return "", fmt.Errorf("%w: invalid peer pet offer", ErrInvalidAction)
	}
	groups = append(groups, peerPetGroup)
	values = append(values, "K")
	values = append(values, groups...)
	return strings.Join(values, "|"), nil
}

func tradeOfferWire(offer TradeOffer, pet bool) (string, int32, bool) {
	if offer.Kind == "" {
		if pet {
			return "P|-1", -1, true
		}
		return "I|-1", -1, true
	}
	switch strings.ToLower(strings.TrimSpace(offer.Kind)) {
	case "item", "i":
		if pet || offer.ItemIndex < 5 || offer.ItemIndex >= 20 {
			return "", -1, false
		}
		return "I|" + strconv.FormatInt(int64(offer.ItemIndex), 10), offer.ItemIndex, true
	case "gold", "g":
		if pet || offer.Amount < 0 {
			return "", -1, false
		}
		return "G|" + strconv.FormatInt(int64(offer.Amount), 10), -1, true
	case "pet", "p":
		if !pet || offer.PetSlot < 0 || offer.PetSlot >= 5 {
			return "", -1, false
		}
		return "P|" + strconv.FormatInt(int64(offer.PetSlot), 10), -1, true
	default:
		return "", -1, false
	}
}

func tradeOfferWirePtr(offer *TradeOffer, pet bool) (string, int32, bool) {
	if offer == nil {
		if pet {
			return "P|-1", -1, true
		}
		return "I|-1", -1, true
	}
	return tradeOfferWire(*offer, pet)
}

func sameTradeOffer(left, right TradeOffer) bool {
	return left == right
}

func sameTradeOfferPtr(left, right *TradeOffer) bool {
	if left == nil || right == nil {
		return left == right
	}
	return sameTradeOffer(*left, *right)
}

// cloneTradeSnapshot deep-copies the pointer-valued pet offers. Arrays and
// scalar fields are value-copied by assignment.
func cloneTradeSnapshot(snapshot TradeSnapshot) TradeSnapshot {
	if snapshot.OwnPet != nil {
		pet := *snapshot.OwnPet
		snapshot.OwnPet = &pet
	}
	if snapshot.PeerPet != nil {
		pet := *snapshot.PeerPet
		snapshot.PeerPet = &pet
	}
	return snapshot
}
