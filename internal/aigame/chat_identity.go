package aigame

import (
	"bytes"
	"encoding/hex"
	"strconv"
	"strings"
)

// A companion S packet binds a loaded persistent identity to the exact next TK.
// Never retain a mapping from reusable object indexes to persistent people.
type chatIdentity struct {
	objectID, color int32
	characterID     string
	message         []byte
}

func parseChatIdentity(value string) *chatIdentity {
	if len(value) > 4200 {
		return nil
	}
	parts := strings.Split(value, "|")
	if len(parts) != 6 || parts[0] != "AICHAT" || parts[1] != "1" || !ValidPersistentCharacterID(parts[4]) {
		return nil
	}
	object, err := strconv.ParseInt(parts[2], 10, 32)
	if err != nil || object < 0 {
		return nil
	}
	color, err := strconv.ParseInt(parts[3], 10, 32)
	if err != nil {
		return nil
	}
	message, err := hex.DecodeString(parts[5])
	if err != nil || len(message) == 0 || len(message) > 2047 {
		return nil
	}
	return &chatIdentity{objectID: int32(object), color: int32(color), characterID: parts[4], message: message}
}

func (identity *chatIdentity) matches(event Event) bool {
	return identity != nil && event.Function == "TK" && len(event.Fields) == 3 &&
		event.Fields[0].Kind == FieldInt && event.Fields[1].Kind == FieldString && event.Fields[2].Kind == FieldInt &&
		event.Fields[0].Int == identity.objectID && event.Fields[2].Int == identity.color &&
		bytes.Equal(event.Fields[1].Text, identity.message)
}
