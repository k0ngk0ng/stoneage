package aigame

import (
	"bytes"
	"encoding/hex"
	"strconv"
	"strings"
)

// Metadata binds one visible/party record to the next native packet. It is
// never a long-lived object-index lookup: those indexes can be reused.
type personIdentity struct {
	kind        string
	objectID    int32
	characterID string
	record      []byte
}

func parsePersonIdentity(raw string) *personIdentity {
	if len(raw) > 4200 {
		return nil
	}
	fields := strings.Split(raw, "|")
	if len(fields) != 6 || fields[0] != "AIPERSON" || fields[1] != "1" || (fields[2] != "C" && fields[2] != "N") || !ValidPersistentCharacterID(fields[4]) {
		return nil
	}
	object, err := strconv.ParseInt(fields[3], 10, 32)
	if err != nil || object < 0 || strconv.FormatInt(object, 10) != fields[3] {
		return nil
	}
	record, err := hex.DecodeString(fields[5])
	if err != nil || len(record) == 0 || len(record) > 2047 {
		return nil
	}
	return &personIdentity{kind: fields[2], objectID: int32(object), characterID: fields[4], record: record}
}

func matchingPersonIdentity(pending []*personIdentity, kind string, object int32, record string) string {
	var identity string
	matches := 0
	for _, p := range pending {
		if p.kind == kind && p.objectID == object && bytes.Equal(p.record, []byte(record)) {
			matches++
			identity = p.characterID
		}
	}
	// Duplicate/conflicting metadata is not attribution evidence.
	if matches != 1 {
		return ""
	}
	return identity
}

func (state *gameState) applyPersonIdentities(event Event, pending []*personIdentity) {
	if len(event.Fields) != 1 || event.Fields[0].Kind != FieldString {
		return
	}
	raw := eventText(event, 0)
	switch event.Function {
	case "C":
		counts := map[int32]int{}
		for _, record := range strings.Split(raw, ",") {
			if actor, ok := parseActorRecord(record); ok {
				counts[actor.ID]++
			}
		}
		for _, record := range strings.Split(raw, ",") {
			actor, ok := parseActorRecord(record)
			if !ok || actor.Kind != "character" || actor.CharType != 1 || counts[actor.ID] != 1 {
				continue
			}
			actor.PersistentCharacterID = matchingPersonIdentity(pending, "C", actor.ID, record)
			state.actors[actor.ID] = actor
		}
	case "S":
		parts := strings.Split(raw, "|")
		if len(parts) < 2 || !strings.HasPrefix(parts[0], "N") {
			return
		}
		slot := base62Int(strings.TrimPrefix(parts[0], "N"), -1)
		member, ok := state.party[slot]
		if !ok {
			return
		}
		member.PersistentCharacterID = matchingPersonIdentity(pending, "N", member.ID, raw)
		state.party[slot] = member
	}
}
