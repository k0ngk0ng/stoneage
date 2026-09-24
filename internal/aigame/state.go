package aigame

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

// gameState is the mutable, connection-local projection behind Snapshot.
// The server remains authoritative: this structure only records packets that
// have actually arrived on the named-protocol stream.
type gameState struct {
	snapshot Snapshot

	characters       []Character
	actors           map[int32]ActorSnapshot
	pets             map[string]PetSnapshot
	inventory        map[int32]InventoryItem
	party            map[int32]PartyMember
	windows          []WindowSnapshot
	activeWindow     *WindowSnapshot
	addressBook      map[int32]AddressBookEntry
	addressBookKnown bool
	// addressBookRevision is a connection-local sequence for valid complete
	// AB tables. It is separate from Snapshot.Revision because ABI updates and
	// unrelated packets must never satisfy a mail:list reconciliation.
	addressBookRevision uint64
	chat                []ChatMessage
	trade               tradeState

	pendingChatIdentity     *chatIdentity
	pendingPersonIdentities []*personIdentity
	personIdentityInvalid   bool
	chatContext             string
	sessionToken            string
	petEpoch                map[int32]uint64
	petActive               map[int32]bool
	statPointsEpoch         uint64
	socialFlagsEpoch        uint64
}

var nextSessionToken uint64

func newSessionToken() string {
	var random [16]byte
	if _, err := cryptorand.Read(random[:]); err == nil {
		return hex.EncodeToString(random[:])
	}
	// crypto/rand is expected on supported systems. Keep a useful fallback
	// for constrained test environments while retaining process-local
	// uniqueness even if the entropy source is unavailable.
	sequence := atomic.AddUint64(&nextSessionToken, 1)
	return fmt.Sprintf("%x-%x", time.Now().UnixNano(), sequence)
}

func newGameState(connected bool) gameState {
	token := newSessionToken()
	return gameState{
		snapshot: Snapshot{
			Phase:        PhaseGreeting,
			Connected:    connected,
			Position:     Point{Floor: -1},
			At:           time.Now(),
			SessionToken: token,
		},
		actors:       make(map[int32]ActorSnapshot),
		pets:         make(map[string]PetSnapshot),
		inventory:    make(map[int32]InventoryItem),
		party:        make(map[int32]PartyMember),
		trade:        newTradeState(),
		addressBook:  make(map[int32]AddressBookEntry),
		sessionToken: token,
		chatContext:  newChatContext(),
		petEpoch:     make(map[int32]uint64),
		petActive:    make(map[int32]bool),
	}
}

func applyEventLocked(state *gameState, event Event) {
	if state == nil {
		return
	}
	pendingIdentity := state.pendingChatIdentity
	state.pendingChatIdentity = nil // Metadata applies only to the immediately following packet.

	if event.Function == "S" && strings.HasPrefix(eventText(event, 0), "AIPERSON|") {
		p := parsePersonIdentity(eventText(event, 0))
		if len(event.Fields) != 1 || p == nil || len(state.pendingPersonIdentities) >= 128 {
			state.pendingPersonIdentities = nil
			state.personIdentityInvalid = true
		}
		if !state.personIdentityInvalid {
			state.pendingPersonIdentities = append(state.pendingPersonIdentities, p)
		}
		return
	}
	people := state.pendingPersonIdentities
	state.pendingPersonIdentities = nil
	state.personIdentityInvalid = false
	defer state.applyPersonIdentities(event, people)
	switch event.Function {
	case "ClientLogin":
		if strings.EqualFold(eventText(event, 0), "ok") {
			state.snapshot.Phase = PhaseAuthenticated
		}
	case "CharList":
		if strings.EqualFold(eventText(event, 0), "successful") {
			state.characters = parseCharacterList(eventText(event, 1))
			state.snapshot.Phase = PhaseCharacterList
		}
	case "CharLogin":
		if strings.EqualFold(eventText(event, 0), "successful") {
			invalidateTradeForLifecycleLocked(state)
			clearAddressBookLocked(state)
			rotateSessionTokenLocked(state)
			state.snapshot.Phase = PhaseWorld
			state.snapshot.AI.PersistentCharacterID = ""
			clear(state.actors)
			clear(state.party)
			state.chatContext = newChatContext()
			state.snapshot.AI.PartyModeKnown = false
			state.snapshot.Battle = BattleSnapshot{}
			state.snapshot.Player.BattlePetSlotKnown = false
			state.snapshot.Player.RidePetKnown = false
			state.snapshot.Player.StatPointsKnown = false
			state.snapshot.Player.SocialFlagsKnown = false
		}
	case "CharLogout":
		if strings.EqualFold(eventText(event, 0), "successful") {
			invalidateTradeForLifecycleLocked(state)
			clearAddressBookLocked(state)
			rotateSessionTokenLocked(state)
			state.snapshot.Phase = PhaseDisconnected
			state.snapshot.Player.RidePetKnown = false
			state.snapshot.AI.PersistentCharacterID = ""
			clear(state.actors)
			clear(state.party)
			state.chatContext = ""
			state.snapshot.AI.PartyModeKnown = false
		}
	case "XYD":
		if len(event.Fields) >= 2 {
			state.snapshot.Position.X = eventInt(event, 0, state.snapshot.Position.X)
			state.snapshot.Position.Y = eventInt(event, 1, state.snapshot.Position.Y)
			state.snapshot.Position.Direction = eventInt(event, 2, state.snapshot.Position.Direction)
			state.hasPosition()
		}
	case "M":
		// M is a sliding map window (floor, x1, y1, x2, y2, payload),
		// not an owner-position echo. It can still establish the floor when
		// an older gateway delivers it before S:C.
		if len(event.Fields) >= 1 && state.snapshot.Position.Floor < 0 {
			state.snapshot.Position.Floor = eventInt(event, 0, -1)
		}
	case "S":
		if strings.HasPrefix(eventText(event, 0), "AICHAT|") {
			state.pendingChatIdentity = parseChatIdentity(eventText(event, 0))
			return
		}
		state.applySystem(eventText(event, 0))
	case "SKUP":
		state.statPointsEpoch++
		state.snapshot.Player.StatPointsKnown = false
		if len(event.Fields) == 1 && event.Fields[0].Kind == FieldInt && event.Fields[0].Int >= 0 {
			state.snapshot.Player.UnspentStatPoints = event.Fields[0].Int
			state.snapshot.Player.StatPointsKnown = true
		}
	case "FS":
		state.socialFlagsEpoch++
		state.snapshot.Player.SocialFlagsKnown = false
		if len(event.Fields) == 1 && event.Fields[0].Kind == FieldInt && event.Fields[0].Int >= 0 && event.Fields[0].Int <= 63 {
			state.snapshot.Player.SocialFlags = event.Fields[0].Int
			state.snapshot.Player.SocialFlagsKnown = true
		}
	case "KS":
		if len(event.Fields) >= 2 && eventInt(event, 1, 0) == 1 {
			slot := eventInt(event, 0, -2)
			if slot >= -1 && slot < 5 {
				state.snapshot.Player.BattlePetSlot = slot
				state.snapshot.Player.BattlePetSlotKnown = true
			}
		}
	case "C":
		state.applyActors(eventText(event, 0))
	case "CA":
		state.applyActions(eventText(event, 0))
	case "CD":
		state.removeActors(eventText(event, 0))
	case "PME":
		state.applyPetEvent(event)
	case "WN":
		state.applyWindow(event)
	case "AB":
		state.applyAddressBookEvent(event)
	case "ABI":
		state.applyAddressBookItem(event)
	case "TK":
		message := chatFromTK(event)
		if pendingIdentity.matches(event) {
			message.SpeakerCharacterID = pendingIdentity.characterID
		}
		state.appendChat(message)
	case "TD":
		applyTradeEventLocked(state, event)
	case "MSG":
		state.appendChat(ChatMessage{At: event.At, Channel: "msg", FromID: eventInt(event, 0, 0), Text: eventText(event, 1), Color: eventInt(event, 2, 0)})
	case "PMSG":
		state.appendChat(ChatMessage{At: event.At, Channel: "pmsg", FromID: eventInt(event, 0, 0), Text: eventText(event, 3), Color: eventInt(event, 4, 0)})
	case "SI":
		state.swapInventory(eventInt(event, 0, -1), eventInt(event, 1, -1))
	case "I":
		state.applyIndexedInventory(eventText(event, 0))
	case "EN":
		if eventInt(event, 0, 0) > 0 {
			invalidateTradeForLifecycleLocked(state)
		}
		state.beginBattle(event)
	case "BC":
		state.applyBattleRoster(eventText(event, 0))
	case "B":
		state.applyBattlePacket(eventText(event, 0))
	case "RS", "RD":
		battle := &state.snapshot.Battle
		battle.Result = eventText(event, 0)
		// Result packets terminate command input before the later EO/BU
		// handshake. Keep the observable readiness bit consistent with the
		// owner-specific predicates above while the battle remains active.
		battle.Movie = true
		battle.CommandReady = false
	case "PR":
		state.snapshot.AI.PartyModeKnown = false
		// PR's response is only a result acknowledgement. Party N packets
		// remain the source of truth for membership.
	case "BU":
		state.endBattle()
	}
}

func (state *gameState) hasPosition() {
	// Keep this as a method so callers do not need a second mutable boolean.
	// Floor may be unknown while XYD is in flight, but X/Y are still useful.
}

func eventText(event Event, index int) string {
	if index < 0 || index >= len(event.Fields) {
		return ""
	}
	field := event.Fields[index]
	if field.Kind == FieldString && field.Raw != "" {
		return decodeLegacyBytes(field.Text)
	}
	return field.String()
}

func eventInt(event Event, index int, fallback int32) int32 {
	if index < 0 || index >= len(event.Fields) {
		return fallback
	}
	field := event.Fields[index]
	if field.Kind == FieldInt {
		return field.Int
	}
	if field.Raw != "" {
		return base62Int(field.Raw, fallback)
	}
	if field.Text != nil {
		return base62Int(string(field.Text), fallback)
	}
	return fallback
}

func legacyText(value string) string {
	// namedproto.DecodeString has already removed the outer protocol escape.
	// Character files use this second escape layer for pipes, commas and
	// newlines.
	return strings.NewReplacer(`\y`, `\`, `\z`, `|`, `\n`, "\n", `\c`, ",").Replace(value)
}

func parseCharacterList(value string) []Character {
	rawText := strings.TrimSpace(value)
	if rawText == "" {
		return nil
	}
	// SAAC has emitted both "name|0\\z<option>" records and whitespace
	// separated records. The option payload itself uses \\z for pipes, so a
	// header is the only reliable boundary for the compact form.
	var starts []struct {
		at        int
		name      string
		bodyStart int
	}
	// Keep the leading "0\\z" in each option: it is the character-list
	// dataPlace field, not a record delimiter. This mirrors the Web parser's
	// `name|(?=0\\z)` boundary rule.
	for _, match := range regexp.MustCompile(`([^\s|]+)\|(0\\z)`).FindAllStringSubmatchIndex(rawText, -1) {
		if len(match) < 6 {
			continue
		}
		name := rawText[match[2]:match[3]]
		starts = append(starts, struct {
			at        int
			name      string
			bodyStart int
		}{at: match[0], name: name, bodyStart: match[4]})
	}
	if len(starts) > 0 {
		result := make([]Character, 0, len(starts))
		for index, start := range starts {
			end := len(rawText)
			if index+1 < len(starts) {
				end = starts[index+1].at
			}
			option := strings.Trim(rawText[start.bodyStart:end], "| \t\r\n")
			result = append(result, Character{Slot: index, Name: legacyText(start.name), Option: legacyText(option)})
		}
		return result
	}
	result := make([]Character, 0, 2)
	for _, record := range strings.Fields(legacyText(rawText)) {
		separator := strings.IndexByte(record, '|')
		name, option := record, ""
		if separator >= 0 {
			name, option = record[:separator], record[separator+1:]
		}
		if name != "" {
			result = append(result, Character{Slot: len(result), Name: legacyText(name), Option: legacyText(option)})
		}
		if len(result) == 2 {
			break
		}
	}
	return result
}

func (state *gameState) applySystem(value string) {
	// Split the native five-field skill records before unescaping their
	// display strings; an escaped pipe must not shift subsequent skill slots.
	if len(value) >= 3 && value[0] == 'W' && value[2] == '|' {
		state.applyPetSkills(value)
		return
	}
	// S("AI") is a server extension whose values use the same pipe envelope
	// but may contain escaped pipes inside a pet identity. Parse it before the
	// normal legacyText pass so an escaped "\\z" remains part of its field.
	if strings.HasPrefix(value, "AI|") {
		state.applyAIObservation(strings.Split(value, "|"))
		return
	}
	parts := splitPipe(legacyText(value))
	if len(parts) == 0 || parts[0] == "" {
		return
	}
	kind := parts[0]
	switch kind[0] {
	case 'C':
		floor := base62Int(strings.TrimPrefix(kind, "C"), state.snapshot.Position.Floor)
		if floor != state.snapshot.Position.Floor {
			invalidateTradeForLifecycleLocked(state)
			// C/CA actor records have no floor field. A server-confirmed
			// map change invalidates the old visible objects before the new
			// floor's actor records arrive.
			clear(state.actors)
		}
		state.snapshot.Position.Floor = floor
		if len(parts) > 1 {
			// max width/height are useful only to the map client; retain the
			// authoritative owner coordinate when supplied by S:C.
			if len(parts) > 3 {
				state.snapshot.Position.X = base62Int(parts[3], state.snapshot.Position.X)
			}
			if len(parts) > 4 {
				state.snapshot.Position.Y = base62Int(parts[4], state.snapshot.Position.Y)
			}
		}
		return
	case 'D':
		state.snapshot.Player.ID = base62Int(strings.TrimPrefix(kind, "D"), state.snapshot.Player.ID)
		return
	case 'P':
		state.applyPlayerStatus(parts)
		return
	case 'M':
		state.snapshot.Player.HP = base62Int(strings.TrimPrefix(kind, "M"), state.snapshot.Player.HP)
		if len(parts) > 1 {
			state.snapshot.Player.MP = base62Int(parts[1], state.snapshot.Player.MP)
		}
		if len(parts) > 2 {
			state.snapshot.Player.EXP = base62Int(parts[2], state.snapshot.Player.EXP)
		}
		state.snapshot.Player.HasStatus = true
		return
	case 'K':
		state.applyPetStatus(parts)
		return
	case 'N':
		state.applyPartyStatus(parts)
		return
	case 'I':
		inventoryParts := append([]string{parts[0][1:]}, parts[1:]...)
		state.applyUnindexedInventory(strings.Join(inventoryParts, "|"))
		return
	}
}

func (state *gameState) applyPetSkills(value string) {
	if value[1] < '0' || value[1] > '4' {
		return
	}
	slot := int32(value[1] - '0')
	pet, exists := state.petForSlot(slot)
	if !exists {
		return
	}
	values := strings.Split(value[3:], "|")
	skills := []PetSkillSnapshot{}
	for offset := 0; offset+3 < len(values) && offset/5 < 7; offset += 5 {
		if values[offset+3] == "" {
			continue
		}
		id, idErr := strconv.ParseInt(values[offset], 10, 32)
		field, fieldErr := strconv.ParseInt(values[offset+1], 10, 32)
		target, targetErr := strconv.ParseInt(values[offset+2], 10, 32)
		if idErr != nil || fieldErr != nil || targetErr != nil || id < 0 || field < 0 || target < 0 {
			continue
		}
		skill := PetSkillSnapshot{Index: int32(offset / 5), ID: int32(id), Field: int32(field), Target: int32(target), Name: legacyText(values[offset+3])}
		if skill.Target >= 100 {
			skill.DeadTarget = true
			skill.Target %= 100
		}
		if offset+4 < len(values) {
			skill.Memo = legacyText(values[offset+4])
		}
		skills = append(skills, skill)
	}
	pet.Skills = skills
	state.upsertPet(slot, pet)
}

// applyAIObservation applies one complete, validated S("AI") response. The
// response is character-scoped by the server; this client only retains the
// opaque character index and the values explicitly returned by that response.
// A malformed response is ignored as a whole so a partial extension packet
// cannot replace an otherwise valid observation.
func (state *gameState) applyAIObservation(parts []string) {
	if state == nil || len(parts) < 1 || parts[0] != "AI" {
		return
	}
	observation, ok := parseAIObservation(parts[1:])
	if !ok {
		return
	}
	state.snapshot.AI = observation
	if observation.StatPointsKnown {
		state.statPointsEpoch++
		state.snapshot.Player.UnspentStatPoints = observation.StatPoints
		state.snapshot.Player.StatPointsKnown = true
	}
	state.mergeAIObservation(observation)
	// applyEvent increments Revision immediately after applyEventLocked. Use
	// the revision that will be visible with this packet, including when the
	// parser is exercised directly by package tests.
	state.snapshot.AIObservationRevision = state.snapshot.Revision + 1
}

// mergeAIObservation folds the complete, character-scoped own-state response
// into the consumable owned-pet projection. AI.Pets remains the raw latest
// response; K status packets must never be allowed to rewrite it. The merged
// state is deliberately conservative: a changed or unknown stable ID starts a
// fresh local identity and drops the previous pet's mutable details.
func (state *gameState) mergeAIObservation(observation AIObservation) {
	if state == nil || !observation.Received {
		return
	}
	incoming := make(map[int32]PetSnapshot, len(observation.Pets))
	for _, pet := range observation.Pets {
		if pet.Slot >= 0 && pet.Slot < 5 {
			incoming[pet.Slot] = pet
		}
	}
	for slot := int32(0); slot < 5; slot++ {
		current, exists := state.petForSlot(slot)
		pet, present := incoming[slot]
		if !present {
			// A complete AI response enumerates every owned slot. Remove an
			// omitted slot from the consumable projection, while retaining the
			// raw AI response above for diagnostics and provenance.
			if exists || state.petActive[slot] {
				state.clearPet(slot)
			}
			continue
		}

		known := pet.IdentityKnown && strings.TrimSpace(pet.StableID) != ""
		if !known {
			if exists && !current.IdentityKnown {
				// The same unidentified slot can still receive a fresh level
				// from AI without losing K's current HP/skills projection.
				current.Level = pet.Level
				state.upsertPet(slot, current)
			} else {
				// An unknown response cannot safely confirm a prior stable
				// identity. Start a new identity and discard old details.
				state.replacePet(slot, PetSnapshot{Slot: slot, Level: pet.Level, Alive: true})
			}
			continue
		}

		if exists && current.IdentityKnown && current.StableID != pet.StableID {
			// A slot is reusable. A changed stable ID proves this is a new
			// pet, so do not carry HP, skills, names or other mutable fields.
			state.replacePet(slot, PetSnapshot{Slot: slot, StableID: pet.StableID,
				IdentityKnown: true, Level: pet.Level, Alive: true})
			continue
		}
		if exists {
			// K may have supplied mutable details while the stable identity
			// was unavailable. Keep those details and enrich the same local
			// identity once AI confirms the stable ID.
			current.StableID = pet.StableID
			current.IdentityKnown = true
			current.Level = pet.Level
			state.updatePet(slot, current)
			continue
		}
		state.replacePet(slot, PetSnapshot{Slot: slot, StableID: pet.StableID,
			IdentityKnown: true, Level: pet.Level, Alive: true})
	}
}

func parseAIObservation(fields []string) (AIObservation, bool) {
	var observation AIObservation
	seen := make(map[string]bool, len(fields))
	seenEnd, seenNow := false, false
	seenRide, seenVersion, seenChara := false, false, false
	petBySlot := make(map[int32]PetSnapshot, 5)
	for _, field := range fields {
		separator := strings.IndexByte(field, '=')
		if separator <= 0 || separator == len(field)-1 {
			return AIObservation{}, false
		}
		key, value := field[:separator], field[separator+1:]
		if key != "pet" && seen[key] {
			return AIObservation{}, false
		}
		if key != "pet" {
			seen[key] = true
		}
		switch key {
		case "character_id":
			if !ValidPersistentCharacterID(value) {
				return AIObservation{}, false
			}
			observation.PersistentCharacterID = value
		case "request":
			if observation.RequestID != "" || !validAIRequestID(value) {
				return AIObservation{}, false
			}
			observation.RequestID = value
		case "stat_points":
			parsed, ok := parseSignedDecimal(value)
			if !ok || parsed < 0 {
				return AIObservation{}, false
			}
			observation.StatPoints, observation.StatPointsKnown = parsed, true
		case "party_mode":
			parsed, ok := parseSignedDecimal(value)
			if !ok || parsed < 0 || parsed > 2 {
				return AIObservation{}, false
			}
			observation.PartyMode, observation.PartyModeKnown = parsed, true
		case "v":
			parsed, ok := parseSignedDecimal(value)
			if !ok || parsed != 1 {
				return AIObservation{}, false
			}
			observation.Version = parsed
			seenVersion = true
		case "chara":
			parsed, ok := parseSignedDecimal(value)
			if !ok || parsed < 0 {
				return AIObservation{}, false
			}
			observation.CharacterIndex = parsed
			seenChara = true
		case "pet":
			pet, ok := parseAIObservationPet(value)
			if !ok || pet.Slot < 0 || pet.Slot >= 5 {
				return AIObservation{}, false
			}
			if _, exists := petBySlot[pet.Slot]; exists {
				return AIObservation{}, false
			}
			petBySlot[pet.Slot] = pet
		case "end":
			values, ok := parseAIObservationEvents(value)
			if !ok {
				return AIObservation{}, false
			}
			observation.EndEvents = values
			seenEnd = true
		case "now":
			values, ok := parseAIObservationEvents(value)
			if !ok {
				return AIObservation{}, false
			}
			observation.NowEvents = values
			seenNow = true
		case "ride":
			parsed, ok := parseSignedDecimal(value)
			if !ok || parsed < 0 {
				return AIObservation{}, false
			}
			observation.LearnRide = parsed
			seenRide = true
		case "sp":
			parsed, ok := parseSignedDecimal(value)
			if !ok {
				return AIObservation{}, false
			}
			observation.SavePoints = parsed
			observation.SavePointsKnown = true
		case "items":
			items, ok := parseAIObservationItems(value)
			if !ok {
				return AIObservation{}, false
			}
			observation.Items = items
			observation.ItemsKnown = true
		default:
			// Unknown fields are ignored after the key/value envelope has been
			// validated, allowing a newer server to add read-only metadata.
		}
	}
	if !seenVersion || !seenChara || !seenEnd || !seenNow || !seenRide {
		return AIObservation{}, false
	}
	observation.Pets = make([]PetSnapshot, 0, len(petBySlot))
	for slot, pet := range petBySlot {
		pet.Slot = slot
		observation.Pets = append(observation.Pets, pet)
	}
	sort.Slice(observation.Pets, func(i, j int) bool { return observation.Pets[i].Slot < observation.Pets[j].Slot })
	observation.Received = true
	return observation, true
}

// The legacy character record reserves the first five absolute inventory
// slots for equipment. The AI observation reports only the backpack range,
// matching CHAR_STARTITEMARRAY..CHAR_MAXITEMHAVE in the server source.
const (
	aiObservationItemSlotStart int32 = 5
	aiObservationItemSlotEnd   int32 = 20
)

func parseAIObservationItems(value string) ([]AIInventoryItem, bool) {
	if value == "none" {
		return []AIInventoryItem{}, true
	}
	if value == "" {
		return nil, false
	}
	records := strings.Split(value, ";")
	items := make([]AIInventoryItem, 0, len(records))
	seenSlots := make(map[int32]bool, len(records))
	for _, record := range records {
		parts := strings.Split(record, ",")
		if len(parts) != 2 {
			return nil, false
		}
		slot, ok := parseSignedDecimal(parts[0])
		if !ok || slot < aiObservationItemSlotStart || slot >= aiObservationItemSlotEnd {
			return nil, false
		}
		templateID, ok := parseSignedDecimal(parts[1])
		if !ok || templateID <= 0 {
			return nil, false
		}
		if seenSlots[slot] {
			return nil, false
		}
		seenSlots[slot] = true
		items = append(items, AIInventoryItem{Slot: slot, TemplateID: templateID})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Slot < items[j].Slot })
	return items, true
}

func parseAIObservationPet(value string) (PetSnapshot, bool) {
	parts := strings.Split(value, ",")
	if len(parts) != 3 {
		return PetSnapshot{}, false
	}
	slot, ok := parseSignedDecimal(parts[0])
	if !ok || slot < 0 || slot >= 5 {
		return PetSnapshot{}, false
	}
	level, ok := parseSignedDecimal(parts[2])
	if !ok || level < 0 {
		return PetSnapshot{}, false
	}
	identity := legacyText(parts[1])
	if identity == "" {
		return PetSnapshot{}, false
	}
	if identity == "unknown" {
		return PetSnapshot{Slot: slot, Level: level, Alive: true}, true
	}
	return PetSnapshot{Slot: slot, StableID: identity, Identity: identity, IdentityKnown: true,
		IdentityEpoch: 1, Level: level, Alive: true}, true
}

func parseAIObservationEvents(value string) ([AIObservationEventGroups]int32, bool) {
	var result [AIObservationEventGroups]int32
	parts := strings.Split(value, ",")
	if len(parts) != AIObservationEventGroups {
		return result, false
	}
	for index, part := range parts {
		parsed, ok := parseSignedDecimal(part)
		if !ok || parsed < 0 {
			return [AIObservationEventGroups]int32{}, false
		}
		result[index] = parsed
	}
	return result, true
}

func parseSignedDecimal(value string) (int32, bool) {
	if !isSignedDecimal(value) {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(parsed), true
}

func (state *gameState) applyPlayerStatus(parts []string) {
	if len(parts) == 0 {
		return
	}
	previousLevel := state.snapshot.Player.Level
	defer func() {
		if state.snapshot.Player.Level != previousLevel {
			// Level gains change the server's available stat points. Never
			// reuse an earlier SKUP/S:AI count until it is observed again.
			state.statPointsEpoch++
			state.snapshot.Player.StatPointsKnown = false
		}
	}()
	marker := strings.TrimPrefix(parts[0], "P")
	if marker == "1" {
		values := parts[1:]
		numericCount := len(values)
		for index := 27; index < len(values); index++ {
			if !isSignedDecimal(values[index]) {
				numericCount = index
				break
			}
		}
		if numericCount > 28 {
			numericCount = 28
		}
		set := func(index int, target *int32) {
			if index < numericCount {
				*target = base62Int(values[index], *target)
			}
		}
		set(0, &state.snapshot.Player.HP)
		set(1, &state.snapshot.Player.MaxHP)
		set(2, &state.snapshot.Player.MP)
		set(3, &state.snapshot.Player.MaxMP)
		set(4, &state.snapshot.Player.Vital)
		set(5, &state.snapshot.Player.Strength)
		set(6, &state.snapshot.Player.Toughness)
		set(7, &state.snapshot.Player.Dexterity)
		set(8, &state.snapshot.Player.EXP)
		set(9, &state.snapshot.Player.MaxEXP)
		set(10, &state.snapshot.Player.Level)
		set(11, &state.snapshot.Player.Attack)
		set(12, &state.snapshot.Player.Defense)
		set(13, &state.snapshot.Player.Quick)
		set(14, &state.snapshot.Player.Charm)
		set(15, &state.snapshot.Player.Luck)
		set(16, &state.snapshot.Player.Earth)
		set(17, &state.snapshot.Player.Water)
		set(18, &state.snapshot.Player.Fire)
		set(19, &state.snapshot.Player.Wind)
		set(20, &state.snapshot.Player.Gold)
		set(21, &state.snapshot.Player.TitleNo)
		set(22, &state.snapshot.Player.DP)
		set(23, &state.snapshot.Player.Transmigration)
		state.snapshot.Player.RidePetKnown = false
		if 24 < numericCount {
			state.applyRidePet(values[24])
		}
		set(26, &state.snapshot.Player.BaseImage)
		nameIndex := numericCount
		if nameIndex < len(values) {
			state.snapshot.Player.Name = legacyText(values[nameIndex])
		}
		if nameIndex+1 < len(values) {
			state.snapshot.Player.Title = legacyText(values[nameIndex+1])
		}
		state.snapshot.Player.HasStatus = true
		return
	}
	mask := wireBase62Int(marker, 0)
	values := parts[1:]
	position := 0
	takeInt := func(target *int32) {
		if position < len(values) {
			*target = base62Int(values[position], *target)
		}
		position++
	}
	takeText := func(target *string) {
		if position < len(values) {
			*target = legacyText(values[position])
		}
		position++
	}
	fields := []struct {
		bit  int32
		setI func(*int32)
		setS func(*string)
	}{
		{2, func(v *int32) { state.snapshot.Player.HP = *v }, nil},
		{4, func(v *int32) { state.snapshot.Player.MaxHP = *v }, nil},
		{8, func(v *int32) { state.snapshot.Player.MP = *v }, nil},
		{16, func(v *int32) { state.snapshot.Player.MaxMP = *v }, nil},
		{32, func(v *int32) { state.snapshot.Player.Vital = *v }, nil},
		{64, func(v *int32) { state.snapshot.Player.Strength = *v }, nil},
		{128, func(v *int32) { state.snapshot.Player.Toughness = *v }, nil},
		{256, func(v *int32) { state.snapshot.Player.Dexterity = *v }, nil},
		{512, func(v *int32) { state.snapshot.Player.EXP = *v }, nil},
		{1024, func(v *int32) { state.snapshot.Player.MaxEXP = *v }, nil},
		{2048, func(v *int32) { state.snapshot.Player.Level = *v }, nil},
		{4096, func(v *int32) { state.snapshot.Player.Attack = *v }, nil},
		{8192, func(v *int32) { state.snapshot.Player.Defense = *v }, nil},
		{16384, func(v *int32) { state.snapshot.Player.Quick = *v }, nil},
		{32768, func(v *int32) { state.snapshot.Player.Charm = *v }, nil},
		{65536, func(v *int32) { state.snapshot.Player.Luck = *v }, nil},
		{131072, func(v *int32) { state.snapshot.Player.Earth = *v }, nil},
		{262144, func(v *int32) { state.snapshot.Player.Water = *v }, nil},
		{524288, func(v *int32) { state.snapshot.Player.Fire = *v }, nil},
		{1048576, func(v *int32) { state.snapshot.Player.Wind = *v }, nil},
		{2097152, func(v *int32) { state.snapshot.Player.Gold = *v }, nil},
		{4194304, func(v *int32) { state.snapshot.Player.TitleNo = *v }, nil},
		{8388608, func(v *int32) { state.snapshot.Player.DP = *v }, nil},
		{16777216, func(v *int32) { state.snapshot.Player.Transmigration = *v }, nil},
		{33554432, nil, func(v *string) { state.snapshot.Player.Name = *v }},
		{67108864, nil, func(v *string) { state.snapshot.Player.Title = *v }},
		{134217728, func(_ *int32) {
			state.snapshot.Player.RidePetKnown = false
			if position > 0 && position <= len(values) {
				state.applyRidePet(values[position-1])
			}
		}, nil},
		{536870912, func(v *int32) { state.snapshot.Player.BaseImage = *v }, nil},
	}
	if mask == 1 {
		for _, field := range fields {
			if field.setI != nil {
				var value int32
				if position < len(values) {
					value = base62Int(values[position], 0)
				}
				position++
				field.setI(&value)
			} else {
				var value string
				if position < len(values) {
					value = legacyText(values[position])
				}
				position++
				field.setS(&value)
			}
		}
	} else {
		for _, field := range fields {
			if mask&field.bit == 0 {
				continue
			}
			if field.setI != nil {
				var value int32
				takeInt(&value)
				field.setI(&value)
			} else {
				var value string
				takeText(&value)
				field.setS(&value)
			}
		}
	}
	state.snapshot.Player.HasStatus = true
}

// A zero-valued struct is not evidence that slot zero is mounted. Only a
// valid native P field (including -1 for walking) establishes this role.
func (state *gameState) applyRidePet(raw string) {
	slot := base62Int(raw, -2)
	state.snapshot.Player.RidePetKnown = slot >= -1 && slot < 5
	if state.snapshot.Player.RidePetKnown {
		state.snapshot.Player.RidePet = slot
	}
}

func (state *gameState) applyPetStatus(parts []string) {
	if len(parts) == 0 {
		return
	}
	slot := base62Int(strings.TrimPrefix(parts[0], "K"), 0)
	if slot < 0 || slot >= 5 {
		return
	}
	if len(parts) < 2 || wireBase62Int(parts[1], 0) == 0 {
		state.clearPet(slot)
		return
	}
	mask := wireBase62Int(parts[1], 0)
	current, exists := state.petForSlot(slot)
	if mask == 1 || !exists {
		// A complete K record has no stable pet ID. Even when the slot was
		// occupied, it may have been replaced; discard the old identity and
		// mutable details until a fresh S("AI") response confirms continuity.
		current = PetSnapshot{Slot: slot}
	}
	if mask == 1 {
		values := parts[2:]
		set := func(index int, target *int32) {
			if index < len(values) {
				*target = base62Int(values[index], *target)
			}
		}
		set(0, &current.Graphic)
		set(1, &current.HP)
		set(2, &current.MaxHP)
		set(3, &current.MP)
		set(4, &current.MaxMP)
		set(5, &current.EXP)
		set(6, &current.MaxEXP)
		set(7, &current.Level)
		set(8, &current.Attack)
		set(9, &current.Defense)
		set(10, &current.Quick)
		if len(values) > 16 {
			current.Slot = base62Int(values[16], slot)
		}
		if len(values) > 19 {
			current.Name = legacyText(values[19])
		}
		if len(values) > 20 {
			current.FreeName = legacyText(values[20])
		}
	} else {
		values := parts[2:]
		position := 0
		takeInt := func(target *int32) {
			if position < len(values) {
				*target = base62Int(values[position], *target)
			}
			position++
		}
		takeText := func(target *string) {
			if position < len(values) {
				*target = legacyText(values[position])
			}
			position++
		}
		fields := []struct {
			bit  int32
			setI func(*int32)
			setS func(*string)
		}{
			{2, func(v *int32) { current.Graphic = *v }, nil},
			{4, func(v *int32) { current.HP = *v }, nil},
			{8, func(v *int32) { current.MaxHP = *v }, nil},
			{16, func(v *int32) { current.MP = *v }, nil},
			{32, func(v *int32) { current.MaxMP = *v }, nil},
			{64, func(v *int32) { current.EXP = *v }, nil},
			{128, func(v *int32) { current.MaxEXP = *v }, nil},
			{256, func(v *int32) { current.Level = *v }, nil},
			{512, func(v *int32) { current.Attack = *v }, nil},
			{1024, func(v *int32) { current.Defense = *v }, nil},
			{2048, func(v *int32) { current.Quick = *v }, nil},
			{8192, func(v *int32) { current.Slot = *v }, nil},
			{524288, nil, func(v *string) { current.Name = *v }},
			{1048576, nil, func(v *string) { current.FreeName = *v }},
		}
		for _, field := range fields {
			if mask&field.bit == 0 {
				continue
			}
			if field.setI != nil {
				var value int32
				takeInt(&value)
				field.setI(&value)
			} else {
				var value string
				takeText(&value)
				field.setS(&value)
			}
		}
	}
	current.Slot = slot
	current.Alive = current.HP > 0
	if mask == 1 {
		current.Identity = ""
		current.IdentityEpoch = 0
		current.IdentityKnown = false
		current.StableID = ""
		state.replacePet(slot, current)
	} else {
		state.upsertPet(slot, current)
	}
}

func (state *gameState) applyPartyStatus(parts []string) {
	// N rows describe members, not CHAR_WORKPARTYMODE. Any update makes the
	// earlier own-state mode uncertain until another S:AI response arrives.
	state.snapshot.AI.PartyModeKnown = false
	if len(parts) == 0 {
		return
	}
	slot := base62Int(strings.TrimPrefix(parts[0], "N"), 0)
	if slot < 0 || slot >= 5 {
		return
	}
	if len(parts) < 2 || base62Int(parts[1], 0) == 0 {
		delete(state.party, slot)
		return
	}
	member := state.party[slot]
	member.PersistentCharacterID = ""
	member.Slot = slot
	mask := wireBase62Int(parts[1], 0)
	if mask == 1 {
		// Native full N rows carry all fields, despite the mask being only 1.
		member = PartyMember{Slot: slot}
		mask = 2 | 4 | 8 | 16 | 32 | 64
	}
	values := parts[2:]
	// Party display slots are reusable. An object replacement cannot inherit
	// the previous member's name or health from an incremental N update.
	if mask&2 != 0 && len(values) > 0 && base62Int(values[0], -1) != member.ID {
		member = PartyMember{Slot: slot}
	}
	position := 0
	take := func() string {
		if position >= len(values) {
			position++
			return ""
		}
		value := values[position]
		position++
		return value
	}
	applyInt := func(bit int32, target *int32) {
		if mask&bit != 0 {
			*target = base62Int(take(), *target)
		}
	}
	applyInt(2, &member.ID)
	applyInt(4, &member.Level)
	applyInt(8, &member.MaxHP)
	applyInt(16, &member.HP)
	applyInt(32, &member.MP)
	if mask&64 != 0 {
		member.Name = legacyText(take())
	}
	state.party[slot] = member
}

func (state *gameState) applyUnindexedInventory(value string) {
	tokens := splitPipe(legacyText(value))
	for offset := 0; offset+9 <= len(tokens); offset += 9 {
		index := int32(offset / 9)
		state.setInventoryRecord(index, tokens[offset:offset+9])
	}
}

func (state *gameState) applyIndexedInventory(value string) {
	tokens := splitPipe(legacyText(value))
	for offset := 0; offset+10 <= len(tokens); offset += 10 {
		index := base62Int(tokens[offset], -1)
		if index < 0 || index >= 20 {
			continue
		}
		state.setInventoryRecord(index, tokens[offset+1:offset+10])
	}
}

func (state *gameState) setInventoryRecord(index int32, fields []string) {
	if index < 0 || index >= 20 || len(fields) < 9 {
		return
	}
	state.invalidateAIInventory()
	if fields[0] == "" {
		delete(state.inventory, index)
		return
	}
	target := base62Int(fields[6], 0)
	state.inventory[index] = InventoryItem{
		Index: index, Name: legacyText(fields[0]), Name2: legacyText(fields[1]), Color: base62Int(fields[2], 0),
		Memo: legacyText(fields[3]), Graphic: base62Int(fields[4], 0), Field: base62Int(fields[5], 0),
		Target: target % 100, DeadTarget: target >= 100, Level: base62Int(fields[7], 0), Send: base62Int(fields[8], 0),
	}
}

func (state *gameState) applyActors(value string) {
	for _, record := range strings.Split(value, ",") {
		actor, ok := parseActorRecord(record)
		if !ok {
			continue
		}
		state.actors[actor.ID] = actor
		if actor.CharType == 3 {
			state.upsertVisiblePet(actor)
		}
		if actor.Name != "" && actor.Name == state.snapshot.Character {
			state.snapshot.Player.ID = actor.ID
			state.snapshot.Position.X = actor.X
			state.snapshot.Position.Y = actor.Y
			state.snapshot.Position.Direction = actor.Direction
		}
	}
}

func parseActorRecord(record string) (ActorSnapshot, bool) {
	parts := strings.Split(record, "|")
	if len(parts) == 0 || record == "" {
		return ActorSnapshot{}, false
	}
	if len(parts) >= 16 && isSignedDecimal(parts[0]) {
		charType, err := strconv.ParseInt(parts[0], 10, 32)
		if err != nil || charType < 0 || charType >= 128 {
			return ActorSnapshot{}, false
		}
		graphic := base62Int(parts[5], 0)
		if graphic == 9999 {
			return ActorSnapshot{}, false
		}
		wireDirection := base62Int(parts[4], 0)
		return ActorSnapshot{
			ID: wireBase62Int(parts[1], 0), Kind: "character", CharType: int32(charType),
			X: base62Int(parts[2], 0), Y: base62Int(parts[3], 0), Direction: wireDirection,
			Graphic: graphic, Level: base62Int(parts[6], 0), Name: legacyText(parts[8]), FreeName: legacyText(parts[9]),
			PetName: legacyText(parts[14]), PetLevel: base62Int(parts[15], 0),
		}, true
	}
	if len(parts) >= 6 {
		return ActorSnapshot{ID: wireBase62Int(parts[0], 0), Kind: "item", CharType: 2, X: base62Int(parts[1], 0), Y: base62Int(parts[2], 0), Graphic: base62Int(parts[3], 0), ItemName: legacyText(parts[5]), Name: legacyText(parts[5])}, true
	}
	if len(parts) >= 4 {
		money := base62Int(parts[3], 0)
		return ActorSnapshot{ID: wireBase62Int(parts[0], 0), Kind: "money", CharType: 3, X: base62Int(parts[1], 0), Y: base62Int(parts[2], 0), Money: money}, true
	}
	return ActorSnapshot{}, false
}

func (state *gameState) applyActions(value string) {
	for _, record := range strings.Split(value, ",") {
		parts := strings.Split(record, "|")
		if len(parts) < 5 {
			continue
		}
		id := wireBase62Int(parts[0], -1)
		if id < 0 {
			continue
		}
		actor := state.actors[id]
		actor.ID = id
		if actor.Kind == "" {
			actor.Kind = "character"
		}
		actor.X = base62Int(parts[1], actor.X)
		actor.Y = base62Int(parts[2], actor.Y)
		actor.Action = base62Int(parts[3], actor.Action)
		actor.Direction = base62Int(parts[4], actor.Direction)
		state.actors[id] = actor
		if actor.Name != "" && actor.Name == state.snapshot.Character {
			state.snapshot.Player.ID = id
			state.snapshot.Position.X = actor.X
			state.snapshot.Position.Y = actor.Y
			state.snapshot.Position.Direction = actor.Direction
		}
	}
}

func (state *gameState) removeActors(value string) {
	for _, token := range strings.Split(value, ",") {
		if token == "" {
			continue
		}
		id := wireBase62Int(token, -1)
		if id == -1 {
			break
		}
		delete(state.actors, id)
		state.removeVisiblePet(id)
	}
}

func (state *gameState) applyPetEvent(event Event) {
	id := eventInt(event, 0, 0)
	pet := PetSnapshot{ID: id, Slot: eventInt(event, 6, -1), Graphic: eventInt(event, 1, 0),
		HP: 0, MaxHP: 0, Alive: true}
	if len(event.Fields) >= 8 {
		raw := legacyText(eventText(event, 7))
		for _, pair := range strings.FieldsFunc(raw, func(r rune) bool { return r == '|' || r == '\\' }) {
			kv := strings.SplitN(pair, ":", 2)
			if len(kv) != 2 {
				continue
			}
			switch kv[0] {
			case "name":
				pet.Name = legacyText(kv[1])
			case "ownt":
				pet.FreeName = legacyText(kv[1])
			case "level":
				pet.Level = base62Int(kv[1], pet.Level)
			case "hp":
				pet.HP = base62Int(kv[1], pet.HP)
			case "maxhp":
				pet.MaxHP = base62Int(kv[1], pet.MaxHP)
			}
		}
	}
	pet.Alive = pet.HP > 0 || pet.MaxHP == 0
	if pet.Slot >= 0 && pet.Slot < 5 {
		state.upsertPet(pet.Slot, pet)
	}
	wireDirection := eventInt(event, 4, 0)
	state.actors[id] = ActorSnapshot{ID: id, Kind: "pet", CharType: 3, X: eventInt(event, 2, 0), Y: eventInt(event, 3, 0), Direction: wireDirection, Graphic: pet.Graphic, PetName: pet.Name, PetLevel: pet.Level, Action: eventInt(event, 5, 0)}
}

func (state *gameState) applyWindow(event Event) {
	if len(event.Fields) < 5 {
		return
	}
	window := WindowSnapshot{Type: eventInt(event, 0, 0), ButtonType: eventInt(event, 1, 0), Sequence: eventInt(event, 2, 0), ObjectID: eventInt(event, 3, 0), Data: legacyText(eventText(event, 4)), Open: true}
	// Match the Web client's inert post-login notification filter. It is not
	// a dialog and must neither block walking nor replace a real active window.
	if window.Type == 28 && strings.TrimSpace(window.Data) == "" && window.Sequence < 0 && window.ObjectID < 0 {
		return
	}
	state.windows = append(state.windows, window)
	if len(state.windows) > 32 {
		state.windows = state.windows[len(state.windows)-32:]
	}
	state.activeWindow = &window
}

func chatFromTK(event Event) ChatMessage {
	raw := legacyText(eventText(event, 1))
	channel := "P"
	text := raw
	if separator := strings.IndexByte(raw, '|'); separator >= 0 {
		channel, text = raw[:separator], raw[separator+1:]
	}
	return ChatMessage{At: event.At, Channel: channel, FromID: eventInt(event, 0, 0), Color: eventInt(event, 2, 0), Text: strings.TrimSpace(text)}
}

func (state *gameState) appendChat(message ChatMessage) {
	if message.Text == "" {
		return
	}
	message.ContextID = state.chatContext
	state.chat = append(state.chat, message)
	if len(state.chat) > 64 {
		state.chat = state.chat[len(state.chat)-64:]
	}
}

func (state *gameState) swapInventory(from, to int32) {
	if from < 0 || from >= 20 || to < 0 || to >= 20 {
		return
	}
	state.invalidateAIInventory()
	left, right := state.inventory[from], state.inventory[to]
	if left.Name == "" {
		delete(state.inventory, from)
	} else {
		left.Index = to
		state.inventory[to] = left
	}
	if right.Name == "" {
		delete(state.inventory, to)
	} else {
		right.Index = from
		state.inventory[from] = right
	}
}

// Native inventory records identify display attributes, not item templates.
// A changed slot cannot inherit a template ID from an older S("AI") sample.
// Keep unrelated own-state evidence, but require a new complete inventory
// response before selecting an item by template again.
func (state *gameState) invalidateAIInventory() {
	state.snapshot.AI.ItemsKnown = false
	state.snapshot.AI.Items = nil
}

func (state *gameState) beginBattle(event Event) {
	typeValue := eventInt(event, 0, 0)
	field := eventInt(event, 1, 0)
	if typeValue <= 0 {
		state.endBattle()
		return
	}
	state.snapshot.Phase = PhaseBattle
	state.snapshot.Battle = BattleSnapshot{Active: true, Type: typeValue, Field: field}
}

func (state *gameState) applyBattlePacket(value string) {
	if value == "" {
		return
	}
	parts := strings.Split(value, "|")
	marker := strings.ToUpper(parts[0])
	if (state.snapshot.Battle.Ended || state.snapshot.Battle.Result == "escaped") && marker != "BU" {
		return
	}
	switch marker {
	case "BVS":
		// Optional battle display values do not start a movie or change command readiness.
		return
	case "BC":
		state.applyBattleRoster(value)
	case "BP":
		values := nonEmpty(parts[1:])
		// BP is also the movie envelope. BE/e0/f1 are all valid hex
		// strings, so a three-token escape movie is not enough to identify
		// the menu packet: its first field must be a native owner slot.
		if len(values) == 3 && len(values[0]) <= 2 && allHex(values) && battleNumber(values[0]) >= 0 && battleNumber(values[0]) <= 20 {
			battle := &state.snapshot.Battle
			battle.Active = true
			battle.MyNo = battleNumber(values[0])
			battle.MyNoKnown = true
			battle.BPFlags = battleNumber(values[1])
			battle.MyMP = battleNumber(values[2])
			battle.BPReceived = true
			battle.PlayerSubmitted = false
			battle.PetSubmitted = false
			battle.LastCommand = ""
			battle.Turn++
			battle.Movie = false
			battle.updateCommandReadiness()
			return
		}
		battle := &state.snapshot.Battle
		battle.Active = true
		battle.Movie = true
		battle.CommandReady = false
		battle.observeEscapeMovie(parts)
		return
	case "BA":
		battle := &state.snapshot.Battle
		battle.AnimationFlags = battleNumber(fieldAt(parts, 1))
		if len(parts) > 2 {
			battle.Turn = battleNumber(parts[2])
		}
		battle.BAReceived = true
		return
	case "BU":
		state.endBattle()
		return
	default:
		battle := &state.snapshot.Battle
		battle.Active = true
		battle.Movie = true
		battle.CommandReady = false
		battle.observeEscapeMovie(parts)
		return
	}
}

func (state *gameState) applyBattleRoster(value string) {
	if state.snapshot.Battle.Ended || state.snapshot.Battle.Result == "escaped" {
		return
	}
	parts := strings.Split(value, "|")
	if len(parts) == 0 {
		return
	}
	if strings.EqualFold(parts[0], "BC") {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return
	}
	battle := &state.snapshot.Battle
	battle.Active = true
	battle.FieldAttack = battleNumber(parts[0])
	body := parts[1:]
	if len(body) > 0 && body[len(body)-1] == "" {
		body = body[:len(body)-1]
	}
	width := 13
	if len(body) > 0 && len(body)%13 != 0 && len(body)%8 == 0 {
		width = 8
	}
	participants := make([]BattleParticipant, 0, len(body)/width)
	for offset := 0; offset+7 < len(body) && len(participants) < 20; offset += width {
		item := BattleParticipant{
			BattleID: battleNumber(body[offset]), Name: legacyText(body[offset+1]), Title: legacyText(body[offset+2]),
			Graphic: battleNumber(body[offset+3]), Level: battleNumber(body[offset+4]), HP: battleNumber(body[offset+5]),
			MaxHP: battleNumber(body[offset+6]), Flags: battleNumber(body[offset+7]),
		}
		item.Player = item.Flags&(1<<2) != 0
		item.Dead = item.Flags&(1<<1) != 0
		if width == 13 && offset+12 < len(body) {
			item.RideFlag = battleSignedNumber(body[offset+8])
			item.PetName = legacyText(body[offset+9])
			item.PetLevel = battleNumber(body[offset+10])
			item.PetHP = battleNumber(body[offset+11])
			item.PetMaxHP = battleNumber(body[offset+12])
		}
		participants = append(participants, item)
	}
	battle.Participants = participants
	battle.BCReceived = true
	if !battle.MyNoKnown {
		for _, item := range participants {
			if item.Player && item.Name == state.snapshot.Character {
				battle.MyNo = item.BattleID
				battle.MyNoKnown = true
				break
			}
		}
	}
	battle.updateCommandReadiness()
}

func (state *gameState) endBattle() {
	battle := &state.snapshot.Battle
	battle.Active = false
	battle.Ended = true
	battle.CommandReady = false
	battle.Movie = false
	state.snapshot.Phase = PhaseWorld
}

func (state *gameState) upsertPet(slot int32, pet PetSnapshot) {
	state.storePet(slot, pet, true, true)
}

// updatePet applies an already-resolved identity change while retaining the
// local continuity key. It is reserved for AI reconciliation; ordinary K/PME
// updates use upsertPet so they cannot accidentally erase a stable ID.
func (state *gameState) updatePet(slot int32, pet PetSnapshot) {
	state.storePet(slot, pet, true, false)
}

// replacePet starts a fresh local identity for an owned slot. It is used for
// complete K records and AI observations which cannot prove continuity.
func (state *gameState) replacePet(slot int32, pet PetSnapshot) {
	state.storePet(slot, pet, false, false)
}

func (state *gameState) storePet(slot int32, pet PetSnapshot, preserveIdentity bool, preserveStableID bool) {
	if slot < 0 || slot >= 5 {
		return
	}
	current, exists := state.petForSlot(slot)
	if !preserveIdentity || !state.petActive[slot] || !exists || current.Identity == "" {
		state.petEpoch[slot]++
		pet.IdentityEpoch = state.petEpoch[slot]
		pet.Identity = fmt.Sprintf("session:%s:pet:%d:%d", state.sessionToken, slot, pet.IdentityEpoch)
		state.petActive[slot] = true
	} else {
		pet.Identity = current.Identity
		pet.IdentityEpoch = current.IdentityEpoch
		if preserveStableID {
			pet.IdentityKnown = current.IdentityKnown
			pet.StableID = current.StableID
		}
	}
	pet.Slot = slot
	if pet.MaxHP > 0 {
		pet.Alive = pet.HP > 0
	}
	for key, value := range state.pets {
		if value.Slot == slot && key != pet.Identity {
			delete(state.pets, key)
		}
	}
	state.pets[pet.Identity] = pet
}

func (state *gameState) clearPet(slot int32) {
	if slot < 0 || slot >= 5 {
		return
	}
	state.petActive[slot] = false
	if state.snapshot.Player.BattlePetSlotKnown && state.snapshot.Player.BattlePetSlot == slot {
		state.snapshot.Player.BattlePetSlotKnown = false
	}
	for key, pet := range state.pets {
		if pet.Slot == slot {
			delete(state.pets, key)
		}
	}
}

func (state *gameState) petForSlot(slot int32) (PetSnapshot, bool) {
	for _, pet := range state.pets {
		if pet.Slot == slot {
			return pet, true
		}
	}
	return PetSnapshot{}, false
}

func (state *gameState) upsertVisiblePet(actor ActorSnapshot) {
	pet := PetSnapshot{ID: actor.ID, Slot: -1, Name: actor.Name, FreeName: actor.FreeName, Graphic: actor.Graphic, Level: actor.Level, Alive: true}
	if actor.PetName != "" {
		pet.Name = actor.PetName
	}
	identity := fmt.Sprintf("session:%s:actor:%d", state.sessionToken, actor.ID)
	if previous, ok := state.pets[identity]; ok {
		pet.Identity = previous.Identity
		pet.IdentityEpoch = previous.IdentityEpoch
	} else {
		pet.Identity = identity
		pet.IdentityEpoch = 1
	}
	pet.IdentityKnown = false
	pet.StableID = ""
	state.pets[identity] = pet
}

func (state *gameState) removeVisiblePet(id int32) {
	for key, pet := range state.pets {
		if pet.Slot < 0 && pet.ID == id {
			delete(state.pets, key)
		}
	}
}

func isSignedDecimal(value string) bool {
	if value == "" {
		return false
	}
	if value[0] == '-' || value[0] == '+' {
		value = value[1:]
	}
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func battleNumber(value string) int32 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if parsed, err := strconv.ParseInt(value, 16, 32); err == nil {
		return int32(parsed)
	}
	return base62Int(value, 0)
}

func wireBase62Int(value string, fallback int32) int32 {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := namedproto.DecodeInt(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func battleSignedNumber(value string) int32 {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "-") {
		return -battleNumber(strings.TrimPrefix(value, "-"))
	}
	parsed, err := strconv.ParseUint(value, 16, 32)
	if err == nil {
		return int32(uint32(parsed))
	}
	return battleNumber(value)
}

func allHex(values []string) bool {
	for _, value := range values {
		if value == "" {
			return false
		}
		for _, character := range value {
			if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
				return false
			}
		}
	}
	return true
}

func nonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func fieldAt(values []string, index int) string {
	if index < 0 || index >= len(values) {
		return ""
	}
	return values[index]
}

func sortPets(values []PetSnapshot) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].Slot != values[j].Slot {
			return values[i].Slot < values[j].Slot
		}
		return values[i].Identity < values[j].Identity
	})
}

func sortInventory(values []InventoryItem) {
	sort.Slice(values, func(i, j int) bool { return values[i].Index < values[j].Index })
}

func sortActors(values []ActorSnapshot) {
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
}

func sortParty(values []PartyMember) {
	sort.Slice(values, func(i, j int) bool { return values[i].Slot < values[j].Slot })
}
