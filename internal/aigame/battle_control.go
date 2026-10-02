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

// BattlePetSwitchAllowed describes the observed native menu restrictions.
// An invalid S slot is dangerous: the engine converts it to recall. Require
// actual own-state evidence before submitting; effects remain server-owned.
func BattlePetSwitchAllowed(player PlayerSnapshot, pet *PetSnapshot, slot int32) bool {
	if !player.BattlePetSlotKnown || player.BattlePetSlot < -1 || player.BattlePetSlot >= 5 {
		return false
	}
	if slot == -1 {
		return player.BattlePetSlot >= 0
	}
	return slot >= 0 && slot < 5 && slot != player.BattlePetSlot &&
		player.RidePetKnown && player.RidePet != slot &&
		player.StandbyPetMaskKnown && player.StandbyPetMask&(1<<uint(slot)) != 0 &&
		player.SummonPetMaskKnown && player.SummonPetMask&(1<<uint(slot)) != 0 &&
		pet != nil && pet.Slot == slot && pet.UseFlag != 0 && pet.HP > 0
}

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

// MySideDefeated reports that every player row on the character's side is
// down. Pet rows are deliberately ignored, which is what the preserved Web
// client does (battleServerSideDefeated): a pet outliving its master does not
// keep the battle alive, and counting it left the client waiting in a battle
// the server had already stopped resolving. The client answers this with EO
// rather than waiting for a result packet.
func (b BattleSnapshot) MySideDefeated() bool {
	if !b.Active || !b.MyNoKnown {
		return false
	}
	mine := battleSideOf(b.MyNo)
	if mine < 0 {
		return false
	}
	seen := false
	for _, actor := range b.Participants {
		if battleSideOf(actor.BattleID) != mine || actor.BattleID%10 >= 5 {
			continue
		}
		seen = true
		if !actor.Dead && actor.HP > 0 {
			return false
		}
	}
	return seen
}

func battleSideOf(battleID int32) int {
	if battleID >= 0 && battleID < 10 {
		return 0
	}
	if battleID >= 10 && battleID < 20 {
		return 1
	}
	return -1
}

func (b BattleSnapshot) commandPhaseReady() bool {
	// RS/RD and a successful local escape are terminal result boundaries. The
	// server may keep the battle socket active until EO, but no further B
	// command is legal in that interval.
	return b.Active && b.BPReceived && b.BCReceived && !b.Movie && !b.Ended && b.Result == "" && b.LastCommand != "EO" && !b.Withdrawn()
}

// Withdrawn means an arena member's original player slot is absent from the
// complete public roster. Incapacitated/dead entries that remain in the roster
// are different: their pet may still require an action. A withdrawn member
// receives spectator snapshots until settlement but has no command menu.
func (b BattleSnapshot) Withdrawn() bool {
	if b.LadderID == "" || !b.Active || !b.BPReceived || !b.BCReceived || !b.MyNoKnown || b.MyNo < 0 || b.MyNo >= 15 || b.MyNo%10 >= 5 {
		return false
	}
	for _, p := range b.Participants {
		if p.BattleID == b.MyNo {
			return false
		}
	}
	return true
}

// PlayerCommandReady includes the mandatory N default when the player menu
// is unavailable. It does not imply that an attack or escape is allowed.
func (b BattleSnapshot) PlayerCommandReady() bool {
	return b.commandPhaseReady() && !b.PlayerSubmitted
}

// DeadPlayerCommandComplete identifies a rostered dead player whose action bit
// is already set by the server. Native command waiting skips dead entries; BA
// can therefore contain their C_OK bit without a local command this turn. This
// is not evidence that a living player's partial plan may be replaced. Pets
// remain independent: a surviving pet can still need its owner's command slot.
func (b BattleSnapshot) DeadPlayerCommandComplete() bool {
	if !b.commandPhaseReady() || !b.MyNoKnown || b.MyNo < 0 || b.MyNo >= 15 || b.MyNo%10 >= 5 ||
		b.LadderID == "" || !b.BAReceived || !b.PlayerSubmitted || b.LastCommand != "" ||
		b.BPFlags&BattlePlayerMenuOff == 0 || b.AnimationFlags&(1<<uint(b.MyNo)) == 0 {
		return false
	}
	for _, p := range b.Participants {
		if p.BattleID == b.MyNo {
			return p.Dead && p.HP == 0
		}
	}
	return false
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
