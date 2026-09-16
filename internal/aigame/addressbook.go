package aigame

import (
	"strconv"
	"strings"
)

const (
	maxAddressBookEntries = 80
	addressBookNewFields  = 8
	addressBookOldFields  = 7
)

// clearAddressBookLocked discards all slots on character/session lifecycle
// changes. Address-book indices are reusable and must never survive a relogin.
func clearAddressBookLocked(state *gameState) {
	if state == nil {
		return
	}
	state.addressBook = make(map[int32]AddressBookEntry)
	state.addressBookKnown = false
	state.addressBookRevision = 0
	state.snapshot.AddressBookRevision = 0
}

// rotateSessionTokenLocked fences receipts across character lifecycle
// transitions on a reused TCP connection. A new character may reuse the same
// address-book revision numbers, so it must receive a fresh private token.
func rotateSessionTokenLocked(state *gameState) {
	if state == nil {
		return
	}
	state.sessionToken = newSessionToken()
	state.snapshot.SessionToken = state.sessionToken
}

// invalidateAddressBookLocked discards the cached table without rewinding
// the complete-table sequence. A malformed AB cannot prove a new mail:list,
// and preserving the sequence avoids an ABA match after a later valid frame.
func invalidateAddressBookLocked(state *gameState) {
	if state == nil {
		return
	}
	state.addressBook = make(map[int32]AddressBookEntry)
	state.addressBookKnown = false
}

// applyAddressBookEvent consumes the full AB table.  2.5 has both a legacy
// seven-field format and newer eight-field format (with a trailing reserved
// field); identify the format from the complete token count rather than
// guessing from an individual entry.
func (state *gameState) applyAddressBookEvent(event Event) {
	if state == nil || len(event.Fields) != 1 || event.Fields[0].Kind != FieldString {
		invalidateAddressBookLocked(state)
		return
	}
	// Split the table before applying the legacy escape layer. A character
	// name may contain an escaped pipe (\z), which must remain part of that
	// field instead of becoming a table delimiter.
	entries, ok := parseAddressBookTable(eventText(event, 0))
	if !ok {
		invalidateAddressBookLocked(state)
		return
	}
	state.addressBook = make(map[int32]AddressBookEntry, len(entries))
	for _, entry := range entries {
		state.addressBook[entry.Index] = entry
	}
	state.addressBookKnown = true
	state.addressBookRevision++
	state.snapshot.AddressBookRevision = state.addressBookRevision
}

// applyAddressBookItem consumes one ABI update. Incremental updates are only
// trusted after a complete AB table has been observed; otherwise a partial
// response must not authorize a mail action against an unknown slot.
func (state *gameState) applyAddressBookItem(event Event) {
	if state == nil || len(event.Fields) != 2 || event.Fields[0].Kind != FieldInt || event.Fields[1].Kind != FieldString {
		return
	}
	if !state.addressBookKnown {
		// ABI is an incremental update. Until a complete AB frame has arrived,
		// accepting it could authorize a send against an unknown/reused slot.
		return
	}
	index := eventInt(event, 0, -1)
	if index < 0 || index >= maxAddressBookEntries {
		return
	}
	entry, ok := parseAddressBookEntry(index, eventText(event, 1))
	if !ok {
		return
	}
	if state.addressBook == nil {
		state.addressBook = make(map[int32]AddressBookEntry)
	}
	state.addressBook[index] = entry
}

func parseAddressBookTable(value string) ([]AddressBookEntry, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return []AddressBookEntry{}, true
	}
	tokens := strings.Split(value, "|")
	// Try the legacy seven-field format first. The new format has a trailing
	// reserved field, but an all-empty frame is structurally ambiguous; the
	// legacy interpretation retains the correct 80-slot shape in that case.
	for _, candidate := range []int{addressBookOldFields, addressBookNewFields} {
		if len(tokens)%candidate != 0 {
			continue
		}
		if entries, ok := parseAddressBookTokens(tokens, candidate); ok {
			return entries, true
		}
	}
	// The native sender calls dchop after appending each slot. Accept one
	// trailing empty token as well so gateways which preserve the final
	// separator are compatible; remove only that one token, since trailing
	// empty slots are meaningful and must not be trimmed wholesale. This is
	// deliberately after the exact-token pass so an old-format table whose
	// final slot is empty is not mistaken for a shorter new-format table.
	if len(tokens) > 0 && tokens[len(tokens)-1] == "" {
		for _, candidate := range []int{addressBookOldFields, addressBookNewFields} {
			if (len(tokens)-1)%candidate == 0 {
				trimmed := append([]string(nil), tokens[:len(tokens)-1]...)
				if entries, ok := parseAddressBookTokens(trimmed, candidate); ok {
					return entries, true
				}
			}
		}
	}
	return nil, false
}

func parseAddressBookTokens(tokens []string, stride int) ([]AddressBookEntry, bool) {
	if stride != addressBookOldFields && stride != addressBookNewFields {
		return nil, false
	}
	if len(tokens) == 0 || len(tokens)%stride != 0 || len(tokens)/stride > maxAddressBookEntries {
		return nil, false
	}
	entries := make([]AddressBookEntry, 0, len(tokens)/stride)
	for index := 0; index < len(tokens)/stride; index++ {
		fields := tokens[index*stride : (index+1)*stride]
		entry, ok := parseAddressBookFields(int32(index), fields)
		if !ok {
			return nil, false
		}
		entries = append(entries, entry)
	}
	return entries, true
}

func parseAddressBookEntry(index int32, value string) (AddressBookEntry, bool) {
	tokens := strings.Split(strings.TrimSpace(value), "|")
	if len(tokens) > 0 && tokens[len(tokens)-1] == "" {
		// ABI payloads in the native server retain their trailing delimiter.
		// Remove one delimiter only; an empty slot still has meaningful empty
		// fields and must not be collapsed.
		trimmed := tokens[:len(tokens)-1]
		for _, candidate := range []int{addressBookOldFields, addressBookNewFields} {
			if len(trimmed) == candidate {
				return parseAddressBookFields(index, trimmed)
			}
		}
	}
	for _, candidate := range []int{addressBookOldFields, addressBookNewFields} {
		if len(tokens) == candidate {
			return parseAddressBookFields(index, tokens)
		}
	}
	return AddressBookEntry{}, false
}

func parseAddressBookFields(index int32, fields []string) (AddressBookEntry, bool) {
	if len(fields) < addressBookOldFields {
		return AddressBookEntry{}, false
	}
	// Empty slots are encoded as a run of delimiters. Treat the complete
	// empty record as valid without trying to parse its numeric fields.
	empty := true
	for _, field := range fields {
		if field != "" {
			empty = false
			break
		}
	}
	if empty {
		return AddressBookEntry{Index: index}, true
	}
	if fields[0] == "" {
		return AddressBookEntry{}, false
	}
	use, ok := parseAddressBookNumber(fields[0])
	if !ok {
		return AddressBookEntry{}, false
	}
	level, levelOK := parseAddressBookNumber(fields[2])
	duel, duelOK := parseAddressBookNumber(fields[3])
	online, onlineOK := parseAddressBookNumber(fields[4])
	graphic, graphicOK := parseAddressBookNumber(fields[5])
	transmigration, transOK := parseAddressBookNumber(fields[6])
	for _, valid := range []bool{levelOK, duelOK, onlineOK, graphicOK, transOK} {
		if !valid {
			return AddressBookEntry{}, false
		}
	}
	if use < 0 || use > 1 || level < 0 || duel < 0 || online < 0 || graphic < 0 || transmigration < 0 {
		return AddressBookEntry{}, false
	}
	name := legacyText(fields[1])
	if len([]byte(name)) > maxChatBytes {
		return AddressBookEntry{}, false
	}
	if len(fields) >= addressBookNewFields && fields[7] != "" {
		reserved, reservedOK := parseAddressBookNumber(fields[7])
		if !reservedOK || reserved != 0 {
			return AddressBookEntry{}, false
		}
	}
	return AddressBookEntry{Index: index, Use: use != 0, Online: online != 0,
		Level: level, DuelPoint: duel, Graphic: graphic, Name: name,
		Transmigration: transmigration}, true
}

func parseAddressBookNumber(value string) (int32, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	if err != nil || parsed < 0 {
		return 0, false
	}
	return int32(parsed), true
}

func (state *gameState) addressBookEntry(index int32) (AddressBookEntry, bool) {
	if state == nil || !state.addressBookKnown || index < 0 || index >= maxAddressBookEntries {
		return AddressBookEntry{}, false
	}
	entry, ok := state.addressBook[index]
	if !ok || !entry.Use {
		return AddressBookEntry{}, false
	}
	return entry, true
}
