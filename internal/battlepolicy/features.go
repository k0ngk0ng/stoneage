// Package battlepolicy converts public client observations into a versioned
// commander policy input. It never imports engine state or opens a connection.
package battlepolicy

import (
	"fmt"
	"math"
	"sort"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

const (
	LegacyFeatureVersion    = "commander-observed-v6"
	RecipientFeatureVersion = "commander-observed-v7"
	FeatureVersion          = "commander-observed-v8"
	LegacyActionVersion     = "attack-guard-switch-v6"
	ActionVersion           = "attack-guard-switch-guardian-v7"
	EntityFeatures          = 64
	CandidateFeatures       = 48
	EventFeatures           = 128
	MaxHistoryEffects       = 512
)

// Layout is append-only within this schema. Unassigned columns remain zero;
// changing scaling, meanings or coverage requires a new feature/action version.
const (
	eAlly = iota
	ePlayer
	ePet
	eAlive
	eDead
	eHP
	eMaxHP
	eHPRatio
	eHPKnown
	eLevel
	eLevelKnown
	eMode
	eTurn
	eSeat
	eOwnerSeat
	eRide
	eMP
	eMaxMP
	eMPRatio
	eMPKnown
	eAttack
	eDefense
	eQuick
	eStatsKnown
	eVital
	eStrength
	eToughness
	eDexterity
	eBuildKnown
	eEarth
	eWater
	eFire
	eWind
	eElementsKnown
	eFlags // 16 independently encoded public BC flag bits at [34,50).
)

const (
	eHealingItems      = 50
	eHealingItemsKnown = 51
	eBench             = 52
	eSummonable        = 53
	eSummonKnown       = 54
	eGuardianSkill     = 55
	eSkillEvidence     = 63
)

type Candidate struct {
	ID        string                     `json:"id"`     // Routing only, never a numerical feature.
	Target    int                        `json:"target"` // Entity row, -1 for no individual target.
	Supported bool                       `json:"supported"`
	Features  [CandidateFeatures]float32 `json:"features"`
}
type Slot struct {
	Member      int         `json:"member"` // Index in the caller's own-team slice.
	Actor       string      `json:"actor"`
	Entity      int         `json:"entity"`
	Observation string      `json:"observation"`
	Candidates  []Candidate `json:"candidates"`
}
type Frame struct {
	Schema           string                    `json:"schema"`
	Actions          string                    `json:"actions"`
	Match            string                    `json:"match"`
	Turn             int32                     `json:"turn"`
	Mode             int                       `json:"mode"`
	Side             int                       `json:"side"`
	Members          []int                     `json:"members"`                     // Caller member -> entity row; -1 means publicly withdrawn.
	CompletedPlayers []bool                    `json:"completed_players,omitempty"` // Routing only: verified dead-player BA acknowledgements.
	Entities         [][EntityFeatures]float32 `json:"entities"`
	Events           [EventFeatures]float32    `json:"events"`
	EventSequence    [][EventFeatures]float32  `json:"event_sequence,omitempty"`
	Slots            []Slot                    `json:"slots"`
	Excluded         int                       `json:"excluded"` // Unsupported candidates remain in the mask.
}

func flag(b bool) float32 {
	if b {
		return 1
	}
	return 0
}

// Signed log preserves magnitude and does not clamp high-level actors together.
func scale(n int32) float32 {
	return float32(math.Copysign(math.Log1p(math.Abs(float64(n)))/math.Log(1001), float64(n)))
}
func ratio(n, d int32) float32 {
	if d <= 0 {
		return 0
	}
	return float32(max(0, min(1, float64(n)/float64(d))))
}
func canonical(id int32, side int) int {
	if int(id)/10 == side {
		return int(id) % 10
	}
	return 10 + int(id)%10
}
func roster(v aigame.BattleView) (map[int32]aigame.BattleParticipant, error) {
	out := map[int32]aigame.BattleParticipant{}
	for _, p := range v.Battle.Participants {
		if p.BattleID < 0 || p.BattleID >= 20 {
			return nil, fmt.Errorf("invalid battle entity")
		}
		if _, exists := out[p.BattleID]; exists {
			return nil, fmt.Errorf("duplicate battle entity")
		}
		// Display labels do not establish equality or affect features.
		p.Name, p.Title, p.PetName = "", "", ""
		out[p.BattleID] = p
	}
	if len(out) < 2 {
		return nil, fmt.Errorf("incomplete battle roster")
	}
	return out, nil
}

// Encode expects ALL members of ONE team at a fresh, pre-submission decision
// boundary. Pet readiness is intentionally not used as an action mask: live
// clients enable pet submission only after the player's command is written.
// Switching does not change the acting pet until native settlement. Reserve
// rows inform the commander, but never create additional acting slots.
func Encode(team []aigame.BattleView, history History) (Frame, error) {
	return EncodeVersion(team, history, FeatureVersion)
}

// EncodeVersion selects semantics from the model/record's declared contract,
// never from whichever journal fields happen to be present on this client.
func EncodeVersion(team []aigame.BattleView, history History, features string) (Frame, error) {
	if !SupportedFeatures(features) {
		return Frame{}, fmt.Errorf("unsupported observation features %q", features)
	}
	if len(team) < 1 || len(team) > 5 {
		return Frame{}, fmt.Errorf("one complete own team required")
	}
	first := team[0]
	side := int(first.Battle.MyNo) / 10
	f := Frame{Schema: features, Actions: ActionsForFeatures(features), Match: first.MatchID, Turn: first.Turn, Mode: first.Mode, Side: side}
	if f.Match == "" || f.Turn < 0 || f.Mode != len(team) || side < 0 || side > 1 {
		return Frame{}, fmt.Errorf("invalid team boundary")
	}
	participants, err := roster(first)
	if err != nil {
		return Frame{}, err
	}
	members := make([]int, len(team))
	byID := map[int32]int{}
	for i, v := range team {
		b := v.Battle
		if v.SchemaVersion != 1 || v.ID == "" || v.MatchID != f.Match || v.Turn != f.Turn || v.Mode != f.Mode ||
			!b.MyNoKnown || b.MyNo < 0 || b.MyNo >= 15 || int(b.MyNo)/10 != side || b.MyNo%10 >= 5 ||
			!b.Active || b.Ended || b.Movie || !b.BPReceived || !b.BCReceived || b.PlayerSubmitted && !b.DeadPlayerCommandComplete() || b.PetSubmitted ||
			b.Clock.RulesVersion != first.Battle.Clock.RulesVersion {
			return Frame{}, fmt.Errorf("member %d: mixed or unready observation", i)
		}
		if _, exists := byID[b.MyNo]; exists {
			return Frame{}, fmt.Errorf("duplicate controlled member")
		}
		_, present := participants[b.MyNo]
		if v.Withdrawn != b.Withdrawn() || !present && (!v.Withdrawn || f.Turn == 0 || len(v.Candidates) != 0) || present && !b.PlayerCommandReady() && !b.DeadPlayerCommandComplete() {
			return Frame{}, fmt.Errorf("controlled member has no verified command or spectator observation")
		}
		other, e := roster(v)
		if e != nil {
			return Frame{}, e
		}
		if len(other) != len(participants) {
			return Frame{}, fmt.Errorf("inconsistent team rosters")
		}
		for id, p := range participants {
			if other[id] != p {
				return Frame{}, fmt.Errorf("inconsistent entity %d across team observations", id)
			}
		}
		byID[b.MyNo], members[i] = i, i
		if b.DeadPlayerCommandComplete() {
			if f.CompletedPlayers == nil {
				f.CompletedPlayers = make([]bool, len(team))
			}
			f.CompletedPlayers[i] = true
		}
	}
	sort.Slice(members, func(i, j int) bool { return team[members[i]].Battle.MyNo < team[members[j]].Battle.MyNo })
	ids := make([]int32, 0, len(participants))
	for id := range participants {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return canonical(ids[i], side) < canonical(ids[j], side) })
	rows := map[int32]int{}
	bench := map[[2]int32]int{} // Own member index/owned slot -> reserve row.
	for _, id := range ids {
		p := participants[id]
		var x [EntityFeatures]float32
		ally, player := int(id)/10 == side, id%10 < 5
		x[eAlly], x[ePlayer], x[ePet] = flag(ally), flag(player), flag(!player)
		x[eAlive], x[eDead] = flag(!p.Dead && p.HP > 0), flag(p.Dead)
		if p.MaxHP > 0 {
			x[eHP], x[eMaxHP], x[eHPRatio], x[eHPKnown] = scale(p.HP), scale(p.MaxHP), ratio(p.HP, p.MaxHP), 1
		}
		if p.Level > 0 {
			x[eLevel], x[eLevelKnown] = scale(p.Level), 1
		}
		x[eMode], x[eTurn], x[eSeat], x[eOwnerSeat], x[eRide] = float32(f.Mode)/5, scale(f.Turn), float32(id%5)/4, float32(id%5)/4, flag(p.RideFlag != 0)
		for bit := 0; bit < 16; bit++ {
			x[eFlags+bit] = flag(uint32(p.Flags)&(1<<bit) != 0)
		}
		owner := id
		if !player {
			owner -= 5
		}
		if mi, ok := byID[owner]; ok && ally {
			v := team[mi]
			if player && v.InventoryKnown {
				count, known := 0, true
				for _, item := range v.Inventory {
					if item.Index < 5 || item.Index >= 20 {
						continue
					}
					known = known && item.TemplateIDKnown
					if item.TemplateIDKnown && item.TemplateID == 1234 {
						count++
					}
				}
				if known {
					x[eHealingItems], x[eHealingItemsKnown] = float32(count)/15, 1
				}
			}
			if player && v.Own.CombatStatsKnown {
				o := v.Own
				x[eMP], x[eMaxMP], x[eMPRatio], x[eMPKnown] = scale(v.Battle.MyMP), scale(o.MaxMP), ratio(v.Battle.MyMP, o.MaxMP), 1
				x[eAttack], x[eDefense], x[eQuick], x[eStatsKnown] = scale(o.Attack), scale(o.Defense), scale(o.Quick), 1
				x[eVital], x[eStrength], x[eToughness], x[eDexterity], x[eBuildKnown] = scale(o.Vital), scale(o.Strength), scale(o.Toughness), scale(o.Dexterity), 1
				x[eEarth], x[eWater], x[eFire], x[eWind], x[eElementsKnown] = float32(o.Earth)/100, float32(o.Water)/100, float32(o.Fire)/100, float32(o.Wind)/100, 1
			} else if !player && v.Own.BattlePetSlotKnown {
				for _, pet := range v.Pets {
					// Same ownership check as the public candidate generator.
					if pet.Slot != v.Own.BattlePetSlot || !pet.CombatStatsKnown || pet.Graphic <= 0 || pet.Graphic != p.Graphic {
						continue
					}
					original := first.Battle.Participants
					name := ""
					for _, actor := range original {
						if actor.BattleID == id {
							name = actor.Name
						}
					}
					if name != pet.Name && (pet.FreeName == "" || name != pet.FreeName) {
						continue
					}
					x[eMP], x[eMaxMP], x[eMPRatio], x[eMPKnown] = scale(pet.MP), scale(pet.MaxMP), ratio(pet.MP, pet.MaxMP), 1
					x[eAttack], x[eDefense], x[eQuick], x[eStatsKnown] = scale(pet.Attack), scale(pet.Defense), scale(pet.Quick), 1
					encodeOwnedPetVersion(&x, pet, v.Own, features)
				}
			}
		}
		rows[id] = len(f.Entities)
		f.Entities = append(f.Entities, x)
	}
	for _, mi := range members {
		v := team[mi]
		if v.Withdrawn || !v.Own.BattlePetSlotKnown {
			continue
		}
		pets := append([]aigame.PetSnapshot(nil), v.Pets...)
		sort.Slice(pets, func(i, j int) bool { return pets[i].Slot < pets[j].Slot })
		for _, pet := range pets {
			if pet.Slot < 0 || pet.Slot >= 5 || pet.UseFlag == 0 || pet.Slot == v.Own.BattlePetSlot {
				continue
			}
			key := [2]int32{int32(mi), pet.Slot}
			if _, duplicate := bench[key]; duplicate {
				return Frame{}, fmt.Errorf("duplicate own reserve pet")
			}
			var x [EntityFeatures]float32
			x[eAlly], x[ePet], x[eBench] = 1, 1, 1
			x[eAlive], x[eDead] = flag(pet.HP > 0), flag(pet.MaxHP > 0 && pet.HP <= 0)
			if pet.MaxHP > 0 {
				x[eHP], x[eMaxHP], x[eHPRatio], x[eHPKnown] = scale(pet.HP), scale(pet.MaxHP), ratio(pet.HP, pet.MaxHP), 1
			}
			if pet.Level > 0 {
				x[eLevel], x[eLevelKnown] = scale(pet.Level), 1
			}
			x[eMode], x[eTurn], x[eOwnerSeat] = float32(f.Mode)/5, scale(f.Turn), float32(v.Battle.MyNo%5)/4
			if pet.CombatStatsKnown {
				x[eAttack], x[eDefense], x[eQuick], x[eStatsKnown] = scale(pet.Attack), scale(pet.Defense), scale(pet.Quick), 1
			}
			encodeOwnedPetVersion(&x, pet, v.Own, features)
			bench[key] = len(f.Entities)
			f.Entities = append(f.Entities, x)
		}
	}
	for _, mi := range members {
		v := team[mi]
		if v.Withdrawn {
			continue
		}
		for _, actor := range []string{"player", "pet"} {
			if actor == "player" && v.Battle.DeadPlayerCommandComplete() {
				continue
			}
			id := v.Battle.MyNo
			if actor == "pet" {
				id += 5
			}
			row, present := rows[id]
			s := Slot{Member: mi, Actor: actor, Entity: row, Observation: v.ID}
			seen := map[string]bool{}
			count := 0
			for _, c := range v.Candidates {
				if c.Actor != "player" && c.Actor != "pet" {
					return Frame{}, fmt.Errorf("unknown candidate actor")
				}
				if c.Actor != actor {
					continue
				}
				if c.ID == "" || seen[c.ID] {
					return Frame{}, fmt.Errorf("missing or duplicate candidate identity")
				}
				seen[c.ID] = true
				if !present {
					return Frame{}, fmt.Errorf("candidate actor missing from roster")
				}
				n := encodeCandidateVersion(c, id, side, rows, f.Entities, features)
				if c.Actor == "player" && c.Kind == "switch_pet" {
					n = Candidate{ID: c.ID, Target: -1}
					n.Features[5], n.Features[7] = 1, 1
					if c.Index == -1 {
						n.Features[31] = 1
						if r, ok := rows[id+5]; ok {
							n.Target = r
						}
						n.Supported = aigame.BattlePetSwitchAllowed(v.Own, nil, -1)
					} else if r, ok := bench[[2]int32{int32(mi), c.Index}]; ok {
						n.Features[30], n.Features[11] = 1, 1
						n.Target = r
						n.Supported = f.Entities[r][eSummonable] == 1
					}
				}
				if n.Supported {
					count++
				} else {
					f.Excluded++
				}
				s.Candidates = append(s.Candidates, n)
			}
			if len(s.Candidates) == 0 {
				if actor == "player" {
					return Frame{}, fmt.Errorf("missing player candidates")
				}
				continue
			}
			if len(s.Candidates) > 512 || count == 0 {
				return Frame{}, fmt.Errorf("unsupported or oversized actor candidate set")
			}
			// Sort on semantic features and canonical target, never side-specific
			// target text in the routing ID. IDs only break equivalent ties.
			sort.Slice(s.Candidates, func(i, j int) bool {
				a, b := s.Candidates[i], s.Candidates[j]
				for k := range a.Features {
					if a.Features[k] != b.Features[k] {
						return a.Features[k] < b.Features[k]
					}
				}
				if a.Target != b.Target {
					return a.Target < b.Target
				}
				return a.ID < b.ID
			})
			f.Slots = append(f.Slots, s)
		}
	}
	for _, v := range team {
		row, present := rows[v.Battle.MyNo]
		if !present {
			row = -1
		}
		f.Members = append(f.Members, row)
	}
	f.Events, f.EventSequence, err = EncodeHistorySequenceVersion(team[members[0]], history, features)
	if err != nil {
		return Frame{}, err
	}
	return f, nil
}

// Initial effect vocabulary is explicit, not a numeric interpretation of IDs.
// The native controlled scenario validates these IDs against loaded function,
// target and option fields. An artifact is bound to that actual rules digest.
func encodeCandidate(c aigame.BattleCandidate, actor int32, side int, rows map[int32]int, entities [][EntityFeatures]float32) Candidate {
	return encodeCandidateVersion(c, actor, side, rows, entities, FeatureVersion)
}

func encodeCandidateVersion(c aigame.BattleCandidate, actor int32, side int, rows map[int32]int, entities [][EntityFeatures]float32, features string) Candidate {
	n := Candidate{ID: c.ID, Target: -1}
	kind := 4 // Unknown; kept as an excluded candidate for data auditing.
	heal := c.Kind == "magic" && c.Actor == "player" && c.MagicIDKnown && (c.MagicID == 10 || c.MagicID == 20) && c.MPCost >= 0
	item := c.Kind == "item" && c.Actor == "player" && c.ItemTemplateIDKnown && c.ItemTemplateID == 1234 && c.Index >= 5 && c.Index < 20
	switch {
	case c.Kind == "attack" && c.Actor == "player", c.Kind == "skill" && c.Actor == "pet" && c.SkillID == 1:
		kind = 0
	case c.Kind == "guard" && c.Actor == "player", c.Kind == "skill" && c.Actor == "pet" && c.SkillID == 2:
		kind = 1
	case c.Kind == "wait":
		kind = 2
	case c.Kind == "skill" && c.Actor == "pet" && c.SkillID == 3:
		kind = 3
	case features == FeatureVersion && c.Kind == "skill" && c.Actor == "pet" && c.SkillID == 20:
		kind = 0 // Attack the selected target while protecting this pet's owner.
		n.Features[32], n.Features[21] = 1, -.2
		if owner, ok := rows[actor-5]; ok {
			n.Features[33], n.Features[34], n.Features[35] = entities[owner][eHPRatio], entities[owner][eMaxHP], entities[owner][eHPKnown]
		}
	case c.Kind == "skill" && c.Actor == "pet":
		status := map[int32]int{60: 16, 80: 17, 90: 18, 110: 19}[c.SkillID]
		if status != 0 {
			kind = 0 // An attack which can also inflict a status, not a guaranteed debuff.
			n.Features[15], n.Features[status] = 1, 1
			n.Features[20], n.Features[21] = scale(3), -.3 // nominal duration, attack modifier
		}
	}
	if item {
		n.Features[22], n.Features[23], n.Features[29] = 1, scale(20), 1
	} else if heal {
		power := int32(65)
		if c.MagicID == 20 {
			power = 50
		}
		n.Features[22], n.Features[23], n.Features[24] = 1, scale(power), scale(c.MPCost)
	} else {
		n.Features[kind] = 1
	}
	n.Features[5], n.Features[6] = flag(c.Actor == "player"), flag(c.Actor == "pet")
	if target, ok := rows[c.Target]; ok && c.Target >= 0 && c.Target < 20 {
		n.Target = target
		x := entities[target]
		n.Features[7], n.Features[8], n.Features[9] = flag(int(c.Target)/10 == side), flag(int(c.Target)/10 != side), flag(c.Target == actor)
		n.Features[10], n.Features[11], n.Features[12], n.Features[13], n.Features[14] = x[ePlayer], x[ePet], x[eHPRatio], x[eMaxHP], x[eHPKnown]
	}
	// A guard skill's candidate may use a group/sentinel target; its native
	// semantics are self-defense. Attacks must have a live individual target.
	n.Supported = kind < 4 && (kind == 1 || kind == 2 || n.Target >= 0 && entities[n.Target][eAlive] == 1)
	if item || heal && c.MagicID == 10 {
		n.Supported = n.Target >= 0 && entities[n.Target][eAlive] == 1
	}
	if heal && c.MagicID == 20 && (c.Target == 20 || c.Target == 21) {
		n.Features[25] = 1
		targetSide := int(c.Target) - 20
		n.Features[7], n.Features[8] = flag(targetSide == side), flag(targetSide != side)
		count, known, hp := 0, 0, float32(0)
		for _, entity := range entities {
			if entity[eBench] == 0 && entity[eAlly] == flag(targetSide == side) && entity[eAlive] == 1 {
				count++
				if entity[eHPKnown] == 1 {
					known++
					hp += entity[eHPRatio]
				}
			}
		}
		if count > 0 {
			n.Features[26], n.Features[28] = float32(count)/10, float32(known)/float32(count)
			if known > 0 {
				n.Features[27] = hp / float32(known)
			}
			n.Supported = true
		}
	}
	return n
}

func encodeOwnedPet(x *[EntityFeatures]float32, pet aigame.PetSnapshot, owner aigame.PlayerSnapshot) {
	encodeOwnedPetVersion(x, pet, owner, FeatureVersion)
}

func encodeOwnedPetVersion(x *[EntityFeatures]float32, pet aigame.PetSnapshot, owner aigame.PlayerSnapshot, features string) {
	x[eSummonKnown] = flag(owner.BattlePetSlotKnown && owner.RidePetKnown && owner.StandbyPetMaskKnown && owner.SummonPetMaskKnown)
	x[eSummonable] = flag(aigame.BattlePetSwitchAllowed(owner, &pet, pet.Slot))
	x[eRide] = flag(owner.RidePetKnown && owner.RidePet == pet.Slot)
	// These columns mean observed usable skills, not hidden/unreceived skills.
	x[eSkillEvidence] = flag(len(pet.Skills) > 0)
	for _, skill := range pet.Skills {
		if !pet.BattleSkillIndexAllowed(skill.Index) {
			continue
		}
		if features == FeatureVersion && skill.ID == 20 && skill.Field >= 0 && skill.Field <= 1 {
			x[eGuardianSkill] = 1
		}
		for i, id := range []int32{1, 2, 3, 60, 80, 90, 110} {
			if skill.ID == id && skill.Field >= 0 && skill.Field <= 1 {
				x[56+i] = 1
			}
		}
	}
}
