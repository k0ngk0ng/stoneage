package main

import (
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

// automationIdentity is the complete binding used when a paused automation
// run is offered to a newly authenticated Web session.  AccountID alone is
// insufficient: an account may have several character slots, and a gateway
// may expose the same account on more than one game line.
//
// Keep this type server-side.  In particular, ServerID is the configured
// stable line identifier, never the resolved TCP address.
type automationIdentity struct {
	PersistentCharacterID string `json:"persistent_character_id,omitempty"`
	AccountID             string `json:"account_id"`
	CharacterID           string `json:"character_id"`
	CharacterName         string `json:"character_name"`
	ServerID              string `json:"server_id"`
}

func (identity automationIdentity) normalized() automationIdentity {
	identity.AccountID = strings.TrimSpace(identity.AccountID)
	identity.CharacterID = strings.TrimSpace(identity.CharacterID)
	identity.CharacterName = strings.TrimSpace(identity.CharacterName)
	identity.ServerID = strings.TrimSpace(identity.ServerID)
	return identity
}

// valid requires every identity component.  A missing component must never
// degrade to a weaker comparison during reconnect, since that would allow an
// account or character from another slot/line to claim a durable handle.
func (identity automationIdentity) valid() bool {
	identity = identity.normalized()
	return identity.AccountID != "" && identity.CharacterID != "" && identity.CharacterName != "" && identity.ServerID != ""
}

// canonicalCharacterID enforces the Web bridge's account:slot form.  A
// slotless account:name fallback is useful for ordinary observation on older
// servers, but it is not strong enough to authorize a reconnect recovery.
func (identity automationIdentity) canonicalCharacterID() bool {
	identity = identity.normalized()
	prefix := identity.AccountID + ":"
	if identity.AccountID == "" || !strings.HasPrefix(identity.CharacterID, prefix) {
		return false
	}
	slotText := strings.TrimPrefix(identity.CharacterID, prefix)
	if slotText == "" {
		return false
	}
	slot, err := strconv.Atoi(slotText)
	return err == nil && slot >= 0 && strconv.Itoa(slot) == slotText
}

// matches compares every binding component, including the loaded persistent
// ID when available. A replacement in the same account slot is a different role.
// CharacterID includes the canonical account:slot identity when available;
// callers must not replace it with an account-only or display-name fallback.
func (identity automationIdentity) matches(other automationIdentity) bool {
	identity = identity.normalized()
	other = other.normalized()
	return identity.valid() && other.valid() && identity.canonicalCharacterID() && other.canonicalCharacterID() && identity == other
}

// automationIdentityFromBinding combines the executor's authoritative
// binding with the observed character name and the session's configured game
// line.  It refuses incomplete bindings rather than creating a recoverable
// record which could later be matched by a weaker key.
func automationIdentityFromBinding(binding aimcp.Binding, snapshot aigame.Snapshot, serverID string) (automationIdentity, bool) {
	identity := automationIdentity{
		PersistentCharacterID: observedPersistentIdentity(snapshot),
		AccountID:             binding.AccountID,
		CharacterID:           binding.CharacterID,
		CharacterName:         binding.CharacterName,
		ServerID:              serverID,
	}
	if identity.CharacterName == "" {
		identity.CharacterName = snapshot.Character
	}
	identity = identity.normalized()
	observed, ok := automationIdentityFromSnapshot(snapshot, serverID)
	if !ok || !identity.matches(observed) {
		return automationIdentity{}, false
	}
	return identity, true
}

// automationIdentityFromSnapshot derives the canonical slot identity from a
// fresh server observation.  The binding remains the preferred source for
// CharacterID, because it was fixed when the run started; this helper exists
// for recovery authorization where only a reconnected session is available.
func automationIdentityFromSnapshot(snapshot aigame.Snapshot, serverID string) (automationIdentity, bool) {
	account := strings.TrimSpace(snapshot.Account)
	character := strings.TrimSpace(snapshot.Character)
	if account == "" || character == "" {
		return automationIdentity{}, false
	}
	characterID := ""
	matches := 0
	for _, candidate := range snapshot.Characters {
		if strings.TrimSpace(candidate.Name) == character {
			matches++
			if candidate.Slot >= 0 {
				characterID = account + ":" + strconv.Itoa(candidate.Slot)
			}
		}
	}
	if characterID == "" || matches != 1 {
		// Do not manufacture a slotless recoverable identity.  A character
		// name is not a sufficient substitute for CharacterID during reconnect.
		return automationIdentity{}, false
	}
	identity := automationIdentity{PersistentCharacterID: observedPersistentIdentity(snapshot), AccountID: account, CharacterID: characterID, CharacterName: character, ServerID: serverID}.normalized()
	return identity, identity.valid() && identity.canonicalCharacterID()
}

func observedPersistentIdentity(snapshot aigame.Snapshot) string {
	if snapshot.Connected && snapshot.AI.Received && aigame.ValidPersistentCharacterID(snapshot.AI.PersistentCharacterID) {
		return snapshot.AI.PersistentCharacterID
	}
	return ""
}

// A loaded identity may arrive after the ordinary player status. Until then a
// matching account/slot cannot bypass a known durable recovery by starting anew.
func (identity automationIdentity) awaitingPersistentIdentity(observed automationIdentity) bool {
	if identity.PersistentCharacterID == "" || observed.PersistentCharacterID != "" {
		return false
	}
	identity.PersistentCharacterID = ""
	return identity.matches(observed)
}
