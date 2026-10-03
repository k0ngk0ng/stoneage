package aiservice

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// NPCPetDelivery is a reviewed ExChangeMan ACCEPT branch. It is attached to
// the destructive confirmation choice, never supplied by an action caller.
// Original builds consume on 235/YES; _NEWEVENT builds consume on 430/OK.
// The exact final message and item guards disambiguate shared NPC branches.
type NPCPetDelivery struct {
	ConfirmationButton int           `json:"confirmation_button,omitempty"`
	GoldReward         int64         `json:"gold_reward,omitempty"`
	Predicates         []string      `json:"predicates"`
	AcceptText         string        `json:"accept_text"`
	RequiredItems      map[int32]int `json:"required_items"`
	ForbiddenItems     []int32       `json:"forbidden_items,omitempty"`
}

func (d *NPCPetDelivery) button() int {
	if d.ConfirmationButton == 0 {
		return 4
	}
	return d.ConfirmationButton
}

type petDeliveryPredicate struct {
	species, level, event, count int32
	op                           byte
}

var petDeliveryPattern = regexp.MustCompile(`^(EV)?PET([<>=])([0-9]+)-([0-9]+)\*([1-5])$`)

func (d *NPCPetDelivery) predicates() ([]petDeliveryPredicate, error) {
	if d != nil && (d.GoldReward < 0 || d.GoldReward > 1000000000) {
		return nil, npcInvalid("invalid pet delivery reward")
	}
	if d == nil || len(d.Predicates) == 0 || len(d.Predicates) > 5 || strings.TrimSpace(d.AcceptText) == "" || len(d.RequiredItems) == 0 {
		return nil, npcInvalid("pet delivery requires predicates, exact confirmation text and item requirements")
	}
	if d.button() != 1 && d.button() != 4 {
		return nil, npcInvalid("pet delivery confirmation must be reviewed OK or YES")
	}
	for id, count := range d.RequiredItems {
		if id <= 0 || count < 1 || count > 15 {
			return nil, npcInvalid("invalid pet delivery item requirement")
		}
	}
	for _, id := range d.ForbiddenItems {
		if id <= 0 || d.RequiredItems[id] != 0 {
			return nil, npcInvalid("invalid or conflicting pet delivery item exclusion")
		}
	}
	var predicates []petDeliveryPredicate
	seen := map[[2]int32]bool{}
	total := int32(0)
	for _, expression := range d.Predicates {
		parts := petDeliveryPattern.FindStringSubmatch(expression)
		if parts == nil {
			// Native uncounted DelPet can remove EVERY matching pet. Do not
			// silently reinterpret it as one, or accept Pet_Name/EVDEL rules.
			return nil, npcInvalid("pet delivery requires an explicit bounded count: %q", expression)
		}
		level, e1 := strconv.ParseInt(parts[3], 10, 32)
		species, e2 := strconv.ParseInt(parts[4], 10, 32)
		if e1 != nil || e2 != nil {
			return nil, npcInvalid("pet delivery integer overflow")
		}
		p := petDeliveryPredicate{species: int32(species), level: int32(level), count: int32(parts[5][0] - '0'), op: parts[2][0]}
		if parts[1] == "EV" {
			p.event = 1
		}
		key := [2]int32{p.species, p.event}
		if seen[key] {
			return nil, npcInvalid("overlapping pet delivery predicates require a separate native review")
		}
		seen[key] = true
		total += p.count
		predicates = append(predicates, p)
	}
	if total > 5 {
		return nil, npcInvalid("pet delivery exceeds five owned slots")
	}
	return predicates, nil
}

func (d *NPCPetDelivery) clone() *NPCPetDelivery {
	if d == nil {
		return nil
	}
	copy := *d
	copy.Predicates = slices.Clone(d.Predicates)
	copy.RequiredItems = maps.Clone(d.RequiredItems)
	copy.ForbiddenItems = slices.Clone(d.ForbiddenItems)
	return &copy
}

func validatePetDeliveryIDs(ids []string) error {
	if len(ids) == 0 || len(ids) > 5 {
		return npcInvalid("pet delivery requires one to five explicitly authorized stable pet IDs")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || seen[id] {
			return npcInvalid("invalid or duplicate pet delivery identity")
		}
		seen[id] = true
	}
	return nil
}

// validate predicts native NPC_EventDelPet's ascending slot scan before WN.
// All selected instances must exactly equal the authorized IDs. In particular,
// an earlier matching pet cannot be replaced by a later authorized one.
func (d *NPCPetDelivery) validate(snapshot aigame.Snapshot, ids []string) error {
	predicates, err := d.predicates()
	if err != nil {
		return err
	}
	if err := validatePetDeliveryIDs(ids); err != nil {
		return err
	}
	// Native NPC_AcceptDel rejects equality as well as overflow. The limit
	// comes from the authenticated server, not a guessed rebirth formula.
	if d.GoldReward > 0 && !snapshot.AI.GoldLimitKnown {
		return npcInvalid("pet delivery requires the server's gold_limit observation")
	}
	if d.GoldReward > 0 && snapshot.AI.GoldLimitKnown && int64(snapshot.Player.Gold)+d.GoldReward >= int64(snapshot.AI.GoldLimit) {
		return npcInvalid("pet delivery reward exceeds available gold capacity; deposit gold before resuming")
	}
	if !snapshot.AI.Received || !snapshot.AI.ItemsKnown || snapshot.ActiveWindow == nil || snapshot.ActiveWindow.Data != d.AcceptText || snapshot.ActiveWindow.ButtonType&int32(d.button()) == 0 {
		return npcInvalid("pet delivery branch or own-state identity is not confirmed")
	}
	for id, count := range d.RequiredItems {
		if healingItemCount(snapshot, id) < count {
			return npcInvalid("pet delivery item %d is missing", id)
		}
	}
	for _, id := range d.ForbiddenItems {
		if healingItemCount(snapshot, id) != 0 {
			return npcInvalid("another quest item %d can select a different NPC branch", id)
		}
	}
	// AI is a complete, correlated list of owned slots. Mutable pet names,
	// graphics and the ordering of the general Pets slice are not evidence.
	pets := slices.Clone(snapshot.AI.Pets)
	if len(pets) > 5 || len(snapshot.Pets) != len(pets) {
		return npcInvalid("invalid owned pet count")
	}
	slices.SortFunc(pets, func(a, b aigame.PetSnapshot) int { return int(a.Slot - b.Slot) })
	identities := map[string]bool{}
	slots := map[int32]bool{}
	for _, pet := range pets {
		if pet.Slot < 0 || pet.Slot > 4 || slots[pet.Slot] || !pet.IdentityKnown || pet.StableID == "" || identities[pet.StableID] || !pet.SpeciesIDKnown || !pet.EventFlagKnown || pet.Level < 1 {
			return npcInvalid("pet delivery requires complete unique instance, species, event and level observations")
		}
		slots[pet.Slot], identities[pet.StableID] = true, true
		// A K replacement after the AI reply invalidates that slot; do not
		// resurrect the old AI identity at the final submission fence.
		matched := false
		for _, current := range snapshot.Pets {
			if current.Slot == pet.Slot && current.IdentityKnown && current.StableID == pet.StableID && current.SpeciesIDKnown && current.SpeciesID == pet.SpeciesID && current.EventFlagKnown && current.EventFlag == pet.EventFlag && current.Level == pet.Level {
				matched = true
			}
		}
		if !matched {
			return npcInvalid("pet identity changed after own-state refresh")
		}
	}
	allowed := map[string]bool{}
	for _, id := range ids {
		allowed[id] = true
	}
	selected := 0
	for _, p := range predicates {
		count := int32(0)
		for _, pet := range pets {
			levelMatch := p.op == '=' && pet.Level == p.level || p.op == '>' && pet.Level > p.level || p.op == '<' && pet.Level < p.level
			if pet.SpeciesID != p.species || pet.EventFlag != p.event || !levelMatch {
				continue
			}
			if !allowed[pet.StableID] {
				return fmt.Errorf("pet delivery would consume unauthorized pet %q in slot %d", pet.StableID, pet.Slot)
			}
			selected++
			count++
			if count == p.count {
				break
			}
		}
		if count != p.count {
			return npcInvalid("pet delivery requires %d matching pets of species %d", p.count, p.species)
		}
	}
	if selected != len(ids) {
		return npcInvalid("authorized pets differ from the NPC's actual selection")
	}
	return nil
}
