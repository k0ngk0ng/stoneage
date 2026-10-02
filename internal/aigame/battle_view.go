package aigame

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// BattleCandidate describes what the observed client menu can submit. The
// server still validates effects, resources and hidden status; this is not an
// oracle claiming that every candidate will take effect.
type BattleCandidate struct {
	ItemTemplateID      int32  `json:"item_template_id,omitempty"`
	ItemTemplateIDKnown bool   `json:"item_template_id_known,omitempty"`
	MagicID             int32  `json:"magic_id,omitempty"`
	MagicIDKnown        bool   `json:"magic_id_known,omitempty"`
	MPCost              int32  `json:"mp_cost,omitempty"`
	ID                  string `json:"id"`
	Actor               string `json:"actor"`
	Kind                string `json:"kind"`
	Index               int32  `json:"index"`
	Target              int32  `json:"target"`
	SkillID             int32  `json:"skill_id,omitempty"`
	Name                string `json:"name,omitempty"`
	Ready               bool   `json:"ready"`
	Command             string `json:"-"`
}

// BattleView excludes accounts, chat, world state and opponents' private data.
// Existing fields retain their normal observation casing under own/battle.
type BattleView struct {
	SchemaVersion      int               `json:"schema_version"`
	ID                 string            `json:"observation_id"`
	Revision           uint64            `json:"revision"`
	CharacterID        string            `json:"character_id"`
	MatchID            string            `json:"match_id"`
	Turn               int32             `json:"turn"`
	Mode               int               `json:"mode"`
	Withdrawn          bool              `json:"withdrawn"`
	Battle             BattleSnapshot    `json:"battle"`
	Own                PlayerSnapshot    `json:"own"`
	Magic              []MagicSnapshot   `json:"magic"`
	Pets               []PetSnapshot     `json:"pets"`
	Inventory          []InventoryItem   `json:"inventory"`
	InventoryKnown     bool              `json:"inventory_known"`
	Candidates         []BattleCandidate `json:"candidates"`
	CandidateSemantics string            `json:"candidate_semantics"`
}

type BattleSelection struct {
	MatchID       string `json:"match_id"`
	Turn          int32  `json:"turn"`
	ObservationID string `json:"observation_id"`
	CandidateID   string `json:"candidate_id"`
}

// NewBattleView is pure. It never queries the server, advances a timer or
// modifies either the original snapshot or the established combat mechanism.
func NewBattleView(s Snapshot) BattleView {
	v := BattleView{SchemaVersion: 1, Revision: s.Revision, CharacterID: s.AI.PersistentCharacterID,
		Withdrawn: s.Battle.Withdrawn(),
		MatchID:   s.Battle.LadderID, Turn: s.Battle.Turn, Battle: s.Battle, Own: s.Player,
		Magic: append([]MagicSnapshot{}, s.Magic...), Pets: clonePetSnapshots(s.Pets),
		Inventory: append([]InventoryItem{}, s.Inventory...), InventoryKnown: s.AI.ItemsKnown, Candidates: []BattleCandidate{},
		CandidateSemantics: "observed_client_conditions; server_validates_final_effect"}
	v.Battle.Participants = append([]BattleParticipant{}, s.Battle.Participants...)
	if s.Ladder != nil && s.Ladder.Snapshot.Match != nil && s.Ladder.Snapshot.Match.ID == v.MatchID {
		v.Mode = s.Ladder.Snapshot.Match.Mode
		v.CharacterID = s.Ladder.Snapshot.Self.ID
	}
	// Sorting makes the identity independent of map iteration and roster order.
	sort.Slice(v.Battle.Participants, func(i, j int) bool { return v.Battle.Participants[i].BattleID < v.Battle.Participants[j].BattleID })
	sort.Slice(v.Pets, func(i, j int) bool { return v.Pets[i].Slot < v.Pets[j].Slot })
	sort.Slice(v.Inventory, func(i, j int) bool { return v.Inventory[i].Index < v.Inventory[j].Index })
	sort.Slice(v.Magic, func(i, j int) bool { return v.Magic[i].Index < v.Magic[j].Index })
	b := s.Battle
	if b.Clock.Known {
		v.Turn = b.Clock.ServerTurn
	}
	add := func(actor, kind, command string, index, target, skill int32, name string) {
		ready := b.PlayerCommandReady()
		if actor == "pet" {
			ready = b.PetCommandReady()
		}
		v.Candidates = append(v.Candidates, BattleCandidate{ID: fmt.Sprintf("%s:%s:%d:%d", actor, kind, index, target),
			Actor: actor, Kind: kind, Command: command, Index: index, Target: target, SkillID: skill, Name: name, Ready: ready})
	}
	if s.Phase == PhaseBattle && b.commandPhaseReady() && b.MyNoKnown && battleSideOf(b.MyNo) >= 0 {
		if b.BPFlags&(BattlePlayerMenuOff|BattleEnemySurprise) != 0 {
			add("player", "wait", "N", -1, -1, 0, "")
		} else {
			for _, p := range v.Battle.Participants {
				if p.BattleID < 0 || p.BattleID >= 20 || p.BattleID == b.MyNo || p.Dead || p.HP <= 0 {
					continue
				}
				if b.BPFlags&4 != 0 && p.BattleID/5 == b.MyNo/5 {
					continue
				}
				add("player", "attack", fmt.Sprintf("H|%X", p.BattleID), -1, p.BattleID, 0, "")
			}
			// Native G is guard; T is capture, not defense.
			add("player", "guard", "G", -1, b.MyNo, 0, "")
			if s.Player.BattlePetSlotKnown {
				if BattlePetSwitchAllowed(s.Player, nil, -1) {
					add("player", "switch_pet", "S|-1", -1, b.MyNo, 0, "")
				}
				if s.Player.StandbyPetMaskKnown {
					for _, pet := range v.Pets {
						if BattlePetSwitchAllowed(s.Player, &pet, pet.Slot) {
							add("player", "switch_pet", fmt.Sprintf("S|%d", pet.Slot), pet.Slot, b.MyNo, 0, pet.Name)
						}
					}
				}
			}
			for _, m := range v.Magic {
				if m.Index < 0 || m.Index >= 5 || m.UseFlag == 0 || m.Field < 0 || m.Field > 1 || m.MP > b.MyMP {
					continue
				}
				for _, target := range BattleTargets(b, "magic", m.Target, m.DeadTarget) {
					add("player", "magic", fmt.Sprintf("J|%X|%X", m.Index, target), m.Index, target, 0, m.Name)
					c := &v.Candidates[len(v.Candidates)-1]
					c.MagicID, c.MagicIDKnown, c.MPCost = m.ID, m.IDKnown, m.MP
				}
			}
			for _, item := range v.Inventory {
				if item.Index < 5 || item.Index >= 20 || item.Name == "" || item.Field < 0 || item.Field > 1 || item.Level > s.Player.Level {
					continue
				}
				for _, target := range BattleTargets(b, "item", item.Target, item.DeadTarget) {
					add("player", "item", fmt.Sprintf("I|%X|%X", item.Index, target), item.Index, target, 0, item.Name)
					c := &v.Candidates[len(v.Candidates)-1]
					c.ItemTemplateID, c.ItemTemplateIDKnown = item.TemplateID, item.TemplateIDKnown
				}
			}
		}
		if b.HasActivePet() {
			add("pet", "wait", "W|FF|FF", -1, -1, 0, "")
			if s.Player.BattlePetSlotKnown && b.BPFlags&(BattlePetMenuOff|BattleEnemySurprise) == 0 {
				for _, pet := range v.Pets {
					if pet.Slot != s.Player.BattlePetSlot {
						continue
					}
					// Require the selected slot to match the actual active actor.
					matched := false
					for _, actor := range b.Participants {
						if actor.BattleID == b.MyNo+5 && actor.Graphic == pet.Graphic && pet.Graphic > 0 &&
							(actor.Name == pet.Name || pet.FreeName != "" && actor.Name == pet.FreeName) {
							matched = true
						}
					}
					if !matched {
						continue
					}
					for _, skill := range pet.Skills {
						if !pet.BattleSkillIndexAllowed(skill.Index) || skill.Field < 0 || skill.Field > 1 {
							continue
						}
						for _, target := range BattleTargets(b, "pet", skill.Target, skill.DeadTarget) {
							add("pet", "skill", fmt.Sprintf("W|%X|%X", skill.Index, target), skill.Index, target, skill.ID, skill.Name)
						}
					}
				}
			}
		}
	}
	// Readiness is mutable during submission of one plan, but combat inputs
	// are not. A player command must not invalidate that plan's pet selection.
	identity := v
	identity.Revision = 0
	identity.Battle.PlayerSubmitted = false
	identity.Battle.PetSubmitted = false
	identity.Battle.CommandReady = false
	// BA's bitset reports which participants already submitted. It is not
	// a combat effect and must not invalidate the commander's remaining orders.
	identity.Battle.AnimationFlags = 0
	identity.Battle.LastCommand = ""
	identity.Battle.Clock = BattleClock{}
	identity.Candidates = append([]BattleCandidate{}, v.Candidates...)
	for i := range identity.Candidates {
		identity.Candidates[i].Ready = false
	}
	encoded, _ := json.Marshal(struct {
		Session string
		View    BattleView
	}{s.SessionToken, identity})
	sum := sha256.Sum256(encoded)
	v.ID = hex.EncodeToString(sum[:])
	return v
}

// BattleTargets follows native 2.5 target enums. Animation-only travel flags
// are deliberately absent from the authoritative decision-ready snapshot.
func BattleTargets(b BattleSnapshot, kind string, target int32, allowDead bool) []int32 {
	if !b.MyNoKnown || battleSideOf(b.MyNo) < 0 || target < 0 || target > 8 || target == 8 && kind != "magic" {
		return nil
	}
	caster := b.MyNo
	if kind == "pet" {
		caster += 5
	}
	switch target {
	case 0:
		return []int32{caster}
	case 2:
		return []int32{20 + int32(battleSideOf(b.MyNo))}
	case 3:
		return []int32{21 - int32(battleSideOf(b.MyNo))}
	case 4:
		return []int32{22}
	case 5:
		if kind == "item" || kind == "pet" {
			return []int32{caster}
		}
		return nil
	}
	var result []int32
	seen := map[int32]bool{}
	for _, p := range b.Participants {
		if p.BattleID < 0 || p.BattleID >= 20 || !allowDead && (p.Dead || p.HP <= 0) {
			continue
		}
		if target == 6 && p.BattleID == caster || target == 7 && (p.BattleID == b.MyNo || p.BattleID == b.MyNo+5) {
			continue
		}
		id := p.BattleID
		if target == 8 {
			id = 20 + int32(battleSideOf(id))
		}
		if !seen[id] {
			result = append(result, id)
			seen[id] = true
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// ResolveBattleSelection only returns an existing candidate from this exact
// observation. Callers must still submit through ExecuteExpected(revision).
func ResolveBattleSelection(s Snapshot, selection BattleSelection) (Action, error) {
	v := NewBattleView(s)
	if selection.ObservationID == "" || selection.ObservationID != v.ID || selection.MatchID != v.MatchID || selection.Turn != v.Turn {
		return Action{}, ErrStaleRevision
	}
	for _, c := range v.Candidates {
		if c.ID == selection.CandidateID {
			if !c.Ready {
				return Action{}, ErrBattleNotReady
			}
			return Battle(c.Command), nil
		}
	}
	return Action{}, fmt.Errorf("%w: unknown battle candidate", ErrInvalidAction)
}
