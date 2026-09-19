// Package battleauto decides and submits one battle command per turn.
//
// The rules are deliberately small: heal whoever is hurt most, otherwise
// attack the first living enemy, and never run away. The decisions are pure
// functions over a snapshot so they can be tested without a server; the loop
// that submits them lives in run.go.
//
// The roster helpers here began life inside the leveling coordinator. They
// moved rather than being copied, so there is one definition of "which row is
// mine" and "which enemy is next" for both the AI leveling run and this one.
package battleauto

import (
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// Side reports which half of the roster a battle id belongs to: 0 for the
// first five rows, 1 for the second five, -1 for anything else.
func Side(battleID int32) int {
	if battleID >= 0 && battleID < 10 {
		return 0
	}
	if battleID >= 10 && battleID < 20 {
		return 1
	}
	return -1
}

// Hex renders a battle id the way every battle command carries it.
func Hex(id int32) string {
	return strings.ToUpper(strconv.FormatInt(int64(id), 16))
}

// ChooseEnemy returns the lowest-numbered living opponent. The server orders
// the roster, so this is the "attack them in order" rule the native client
// uses and the one a player expects.
func ChooseEnemy(snapshot aigame.Snapshot) (aigame.BattleParticipant, bool) {
	mine := Side(snapshot.Battle.MyNo)
	if mine < 0 {
		return aigame.BattleParticipant{}, false
	}
	var selected aigame.BattleParticipant
	found := false
	for _, participant := range snapshot.Battle.Participants {
		if participant.BattleID < 0 || participant.BattleID >= 20 ||
			Side(participant.BattleID) == mine || participant.Dead || participant.HP <= 0 {
			continue
		}
		if !found || participant.BattleID < selected.BattleID {
			selected, found = participant, true
		}
	}
	return selected, found
}

// HasNoLiveEnemy reports whether every opponent is down. It requires a
// non-empty roster, so an empty snapshot is not mistaken for a won battle.
func HasNoLiveEnemy(snapshot aigame.Snapshot) bool {
	mine := Side(snapshot.Battle.MyNo)
	if mine < 0 {
		return false
	}
	for _, participant := range snapshot.Battle.Participants {
		if Side(participant.BattleID) != mine && participant.BattleID >= 0 && participant.BattleID < 20 &&
			!participant.Dead && participant.HP > 0 {
			return false
		}
	}
	return len(snapshot.Battle.Participants) > 0
}

// MyPetID is the roster row the server reserves for the active pet.
func MyPetID(snapshot aigame.Snapshot) int32 {
	return snapshot.Battle.MyNo + 5
}

// LiveEnemies counts the opponents still standing. It is what tells a player
// watching the panel how much of the fight is left.
func LiveEnemies(snapshot aigame.Snapshot) int {
	mine := Side(snapshot.Battle.MyNo)
	if mine < 0 {
		return 0
	}
	count := 0
	for _, participant := range snapshot.Battle.Participants {
		if participant.BattleID < 0 || participant.BattleID >= 20 ||
			Side(participant.BattleID) == mine || participant.Dead || participant.HP <= 0 {
			continue
		}
		count++
	}
	return count
}

// PetCommand picks the pet's command for this turn. 2.5 petskill.txt defines
// ID 1 as the normal attack on a single character target; its observed slot is
// used rather than assuming slot zero. Anything unproven falls back to the
// pet doing nothing, which is always legal.
func PetCommand(snapshot aigame.Snapshot) string {
	const wait = "W|FF|FF"
	if !snapshot.Player.BattlePetSlotKnown || snapshot.Player.BattlePetSlot < 0 || snapshot.Player.BattlePetSlot >= 5 ||
		snapshot.Battle.BPFlags&(aigame.BattlePetMenuOff|aigame.BattleEnemySurprise) != 0 || !snapshot.Battle.HasActivePet() {
		return wait
	}
	var active aigame.BattleParticipant
	for _, actor := range snapshot.Battle.Participants {
		if actor.BattleID == MyPetID(snapshot) {
			active = actor
			break
		}
	}
	target, ok := ChooseEnemy(snapshot)
	if !ok {
		return wait
	}
	for _, pet := range snapshot.Pets {
		if pet.Slot != snapshot.Player.BattlePetSlot || pet.Graphic <= 0 || pet.Graphic != active.Graphic ||
			(active.Name != pet.Name && (pet.FreeName == "" || active.Name != pet.FreeName)) {
			continue
		}
		for _, skill := range pet.Skills {
			if skill.ID == 1 && skill.Index >= 0 && skill.Index < 7 && skill.Field == 1 &&
				skill.Target == 6 && !skill.DeadTarget {
				return "W|" + Hex(skill.Index) + "|" + Hex(target.BattleID)
			}
		}
	}
	return wait
}
