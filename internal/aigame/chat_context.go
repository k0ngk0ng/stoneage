package aigame

import (
	"crypto/rand"
	"encoding/hex"
)

// newChatContext identifies an observation session, never a player. Randomness
// avoids aliasing a previous process's connection counter in durable history.
// If entropy is unavailable, keep attribution unknown instead of inventing a
// reusable identity from an account, name, timestamp or process-local counter.
func newChatContext() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(value[:])
}
