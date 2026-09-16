package aigame

import (
	"strconv"
	"strings"
)

// Native gmsv/include/battle.h BP flags. Bit 2 is boomerang, not pet menu off.
const (
	BattlePlayerMenuOff int32 = 1 << 1
	BattlePetMenuOff    int32 = 1 << 3
	BattleEnemySurprise int32 = 1 << 4
)

// HasActivePet uses the battle roster, not the five field pet slots. A
// standby pet is not necessarily participating in this battle.
func (b BattleSnapshot) HasActivePet() bool {
	if !b.MyNoKnown || !((b.MyNo >= 0 && b.MyNo < 5) || (b.MyNo >= 10 && b.MyNo < 15)) {
		return false
	}
	for _, actor := range b.Participants {
		if actor.BattleID == b.MyNo+5 && !actor.Dead && actor.HP > 0 {
			return true
		}
	}
	return false
}

func (b BattleSnapshot) commandPhaseReady() bool {
	// RS/RD and a successful local escape are terminal result boundaries. The
	// server may keep the battle socket active until EO, but no further B
	// command is legal in that interval.
	return b.Active && b.BPReceived && b.BCReceived && !b.Movie && !b.Ended && b.Result == "" && b.LastCommand != "EO"
}

// PlayerCommandReady includes the mandatory N default when the player menu
// is unavailable. It does not imply that an attack or escape is allowed.
func (b BattleSnapshot) PlayerCommandReady() bool {
	return b.commandPhaseReady() && !b.PlayerSubmitted
}

// PetCommandReady becomes true after the player's command. Native battles
// with a live pet require both commands before the server can run the turn.
func (b BattleSnapshot) PetCommandReady() bool {
	return b.commandPhaseReady() && b.PlayerSubmitted && !b.PetSubmitted && b.HasActivePet()
}

func (b *BattleSnapshot) updateCommandReadiness() {
	b.CommandReady = b.PlayerCommandReady() || b.PetCommandReady()
}

// The native movie may contain many concatenated commands. Only the exact
// local BE|e<slot>|f1| tuple proves escape; another participant disappearing,
// a failed f0 attempt or a roster without enemies is not that proof.
func (b *BattleSnapshot) observeEscapeMovie(parts []string) {
	if !b.MyNoKnown || !b.Active {
		return
	}
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] != "BE" || !strings.HasPrefix(parts[i+1], "e") || parts[i+2] != "f1" {
			continue
		}
		raw := strings.TrimPrefix(parts[i+1], "e")
		if len(raw) < 1 || len(raw) > 2 || strings.ContainsAny(raw, "+-") {
			continue
		}
		slot, err := strconv.ParseUint(raw, 16, 8)
		if err == nil && int32(slot) == b.MyNo {
			b.Result = "escaped"
			b.CommandReady = false
			return
		}
	}
}
