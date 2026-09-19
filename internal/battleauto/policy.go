package battleauto

import (
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

// Policy is what the caller may tune. Everything else is fixed by the game:
// the order enemies are attacked in, and the fact that the character never
// runs away.
type Policy struct {
	// HealBelowPercent is the HP percentage at or below which a heal is
	// preferred over an attack. Zero disables healing entirely.
	HealBelowPercent int32
	// HealItems and HealMagic select which sources a heal may come from. The
	// caller sets both; an item is always preferred over a spell.
	HealItems bool
	HealMagic bool
	// SeekEncounters walks a short back-and-forth pattern between battles. The
	// server rolls the encounter on the movement itself, so this is what turns
	// answering turns into leaving the character fighting until stopped.
	SeekEncounters bool
}

// DefaultPolicy heals below 30% from the bag first and from magic second.
func DefaultPolicy() Policy {
	return Policy{HealBelowPercent: 30, HealItems: true, HealMagic: true}
}

// Decision is one turn's command plus why it was chosen, so a caller can
// report the automation's reasoning without re-deriving it.
type Decision struct {
	Action aigame.Action
	Reason string
}

// Item target type 5 means "the user only", so such an item cannot be handed
// to the pet.
const itemTargetSelfOnly = 5

// Decide returns the command for the current turn. ok is false when the turn
// is not ready for this policy to act on, which happens between turns and
// whenever the server has not both opened the menu and sent the roster.
func Decide(snapshot aigame.Snapshot, tables *aiknowledge.RecoveryTables, policy Policy) (Decision, bool) {
	battle := snapshot.Battle
	/* EO is the client's acknowledgement of a terminal result, and it is sent
	   once. The projection keeps the ended battle -- result and all -- until
	   the next one replaces it, so a policy that kept answering it would do
	   nothing else: for the walking loop that means never looking for another
	   fight, which looks exactly like a loop that does not work. */
	if battle.LastCommand == "EO" {
		return Decision{}, false
	}
	if battle.Ended || battle.Result != "" {
		return Decision{Action: aigame.EndBattle(), Reason: "battle result is in"}, true
	}
	// A wiped side is the client's cue to end the battle; the server does not
	// send a result for it, so waiting for one leaves the character stuck in a
	// fight that is already over.
	if battle.MySideDefeated() {
		return Decision{Action: aigame.EndBattle(), Reason: "the party is down, ending the battle"}, true
	}
	if !battle.Active {
		return Decision{}, false
	}

	if battle.PlayerCommandReady() {
		// A closed menu or a surprise attack leaves exactly one legal command;
		// the server rejects anything else, so it is not a choice.
		if battle.BPFlags&(aigame.BattlePlayerMenuOff|aigame.BattleEnemySurprise) != 0 {
			return Decision{Action: aigame.Battle("N"), Reason: "the player menu is closed this turn"}, true
		}
		if decision, ok := healDecision(snapshot, tables, policy); ok {
			return decision, true
		}
		if enemy, ok := ChooseEnemy(snapshot); ok {
			return Decision{
				Action: aigame.Battle("H|" + Hex(enemy.BattleID)),
				Reason: "attacking " + enemy.Name,
			}, true
		}
		return Decision{Action: aigame.Battle("N"), Reason: "no living enemy to attack"}, true
	}

	if battle.PetCommandReady() {
		command := PetCommand(snapshot)
		return Decision{Action: aigame.Battle(command), Reason: "pet command " + command}, true
	}
	return Decision{}, false
}

// hurt is one member of the party the policy may heal.
type hurt struct {
	name     string
	hp       int32
	maxHP    int32
	battleID int32
	isSelf   bool
}

func (h hurt) missing() int32 { return h.maxHP - h.hp }

// mostHurt returns the party member furthest below the threshold, or false
// when nobody is. The character wins a tie with its pet: if both are equally
// low, the one whose death ends the battle is treated first.
func mostHurt(snapshot aigame.Snapshot, belowPercent int32) (hurt, bool) {
	if belowPercent <= 0 {
		return hurt{}, false
	}
	candidates := make([]hurt, 0, 2)
	candidates = append(candidates, hurt{
		name:     snapshot.Player.Name,
		hp:       snapshot.Player.HP,
		maxHP:    snapshot.Player.MaxHP,
		battleID: snapshot.Battle.MyNo,
		isSelf:   true,
	})
	if snapshot.Battle.HasActivePet() {
		// The pet is a roster row of its own (that is what HasActivePet
		// checks), so its HP comes from that row rather than from the
		// companion fields on the character's row.
		for _, participant := range snapshot.Battle.Participants {
			if participant.BattleID != MyPetID(snapshot) {
				continue
			}
			name := participant.Name
			if name == "" {
				name = "the pet"
			}
			candidates = append(candidates, hurt{
				name:     name,
				hp:       participant.HP,
				maxHP:    participant.MaxHP,
				battleID: MyPetID(snapshot),
			})
			break
		}
	}

	var chosen hurt
	found := false
	for _, candidate := range candidates {
		if candidate.maxHP <= 0 || candidate.hp <= 0 {
			// A dead or unknown member is not a heal target: reviving and
			// guessing are both outside this policy.
			continue
		}
		if candidate.hp*100 > candidate.maxHP*belowPercent {
			continue
		}
		if !found || candidate.hp*chosen.maxHP < chosen.hp*candidate.maxHP {
			chosen, found = candidate, true
		}
	}
	return chosen, found
}

func healDecision(snapshot aigame.Snapshot, tables *aiknowledge.RecoveryTables, policy Policy) (Decision, bool) {
	if tables == nil {
		return Decision{}, false
	}
	patient, ok := mostHurt(snapshot, policy.HealBelowPercent)
	if !ok {
		return Decision{}, false
	}

	if policy.HealItems {
		if slot, item, ok := pickItem(snapshot, tables, patient); ok {
			return Decision{
				Action: aigame.Battle("I|" + Hex(slot) + "|" + Hex(patient.battleID)),
				Reason: "healing " + patient.name + " with " + item.Name,
			}, true
		}
	}
	if policy.HealMagic {
		if index, spell, ok := pickSpell(snapshot, tables, patient); ok {
			return Decision{
				Action: aigame.Battle("J|" + Hex(index) + "|" + Hex(patient.battleID)),
				Reason: "healing " + patient.name + " with " + spell.Name,
			}, true
		}
	}
	return Decision{}, false
}

// pickItem chooses from the bag. The smallest item that covers the missing HP
// is used, so a strong item is not spent on a scratch; if nothing covers it,
// the largest available item is used instead of giving up.
func pickItem(snapshot aigame.Snapshot, tables *aiknowledge.RecoveryTables, patient hurt) (int32, aiknowledge.ItemRecovery, bool) {
	missing := patient.missing()
	var best aiknowledge.ItemRecovery
	var bestSlot int32
	found := false
	for _, item := range snapshot.Inventory {
		if item.Name == "" {
			continue
		}
		recovery, ok := tables.Items[item.Name]
		if !ok {
			continue
		}
		if !patient.isSelf && item.Target == itemTargetSelfOnly {
			continue
		}
		if !found || itemPickedIsBetter(recovery.Amount, best.Amount, missing) {
			best, bestSlot, found = recovery, item.Index, true
		}
	}
	return bestSlot, best, found
}

// itemPickedIsBetter orders two candidate amounts: a covering one beats a
// non-covering one, and among covers the smaller wins.
func itemPickedIsBetter(candidate, current, missing int32) bool {
	if current <= 0 {
		return true
	}
	candidateCovers := candidate >= missing
	currentCovers := current >= missing
	if candidateCovers != currentCovers {
		return candidateCovers
	}
	if candidateCovers {
		return candidate < current
	}
	return candidate > current
}

// pickSpell chooses a known spell that can reach the patient, by the same
// smallest-sufficient rule as items.
func pickSpell(snapshot aigame.Snapshot, tables *aiknowledge.RecoveryTables, patient hurt) (int32, aiknowledge.MagicRecovery, bool) {
	byID := make(map[int32]aiknowledge.MagicRecovery, len(tables.Magic))
	for _, spell := range tables.Magic {
		byID[spell.ID] = spell
	}
	missing := patient.missing()
	var best aiknowledge.MagicRecovery
	var bestIndex int32
	found := false
	for index, skill := range snapshot.Skills {
		spell, ok := byID[skill.ID]
		if !ok || !spellReaches(spell, patient) {
			continue
		}
		if !found || itemPickedIsBetter(spell.Amount, best.Amount, missing) {
			best, bestIndex, found = spell, int32(index), true
		}
	}
	return bestIndex, best, found
}

// spellReaches reports whether a spell's target type can land on the patient.
// The two single-ally types matter most: a self-only spell can never save the
// pet, and a spell that refuses its caster can never save the character.
func spellReaches(spell aiknowledge.MagicRecovery, patient hurt) bool {
	switch spell.Target {
	case 0: // the caster only
		return patient.isSelf
	case 1: // one chosen ally
		return true
	case 6: // one chosen ally, never the caster
		return !patient.isSelf
	case 2, 8: // a whole side
		return true
	default:
		return false
	}
}
