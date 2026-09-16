package aiservice

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

// HealPet is field healing, not a battle revive. Native ID targets 1..5
// select owned pet slots 0..4. The preceding battle must have ended before
// this call; K does not expose CHAR_ISDIE. No predicted recovery amount is
// used, and an error after submitting ID never retries the item use.
func (r *TravelItemRecovery) HealPet(ctx context.Context, expected aigame.PetSnapshot) error {
	if r == nil || r.Healing == nil || r.Healing.Backend == nil || r.Healing.Backend.Knowledge == nil ||
		expected.Slot < 0 || expected.Slot >= 5 || expected.HP <= 0 || expected.MaxHP <= 0 || expected.Graphic <= 0 || expected.Name == "" {
		return ErrHealingItemUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	b := r.Healing.Backend
	npc := NewNPCSkill(b, nil)
	initial, err := npc.observe(ctx)
	if err != nil {
		return err
	}
	role := petRecoveryRole(initial, expected.Slot)
	if role == 0 {
		return ErrHealingItemUnavailable
	}
	before, err := refreshInventoryIdentity(ctx, npc)
	if err != nil {
		return err
	}
	if !petRecoveryWorld(before) {
		return errors.New("pet recovery requires living world status")
	}
	pet, err := petRecoverySubject(before, expected, role)
	if err != nil {
		return err
	}
	if pet.HP >= pet.MaxHP/2+pet.MaxHP%2 {
		return nil
	}
	if pet.HP != expected.HP {
		return aigame.ErrStaleRevision
	}

	aliases := make([]string, 0, len(r.Healing.Contracts))
	for alias, c := range r.Healing.Contracts {
		if c.Verified && c.TemplateID > 0 && c.BaseHP > 0 && c.SourceFingerprint == b.Knowledge.Fingerprint() {
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases)
	itemSlot, template := int32(-1), int32(0)
	for _, alias := range aliases {
		c := r.Healing.Contracts[alias]
		for _, item := range before.AI.Items {
			if item.TemplateID == c.TemplateID && item.Slot >= 5 && item.Slot < 20 && (itemSlot < 0 || item.Slot < itemSlot) {
				itemSlot, template = item.Slot, c.TemplateID
			}
		}
		if itemSlot >= 0 {
			break
		}
	}
	if itemSlot < 0 {
		return ErrHealingItemUnavailable
	}
	count := healingItemCount(before, template)
	unchanged := func(now aigame.Snapshot) (aigame.PetSnapshot, error) {
		if !petRecoveryWorld(now) || now.Position.Floor != before.Position.Floor || now.Position.X != before.Position.X || now.Position.Y != before.Position.Y ||
			now.Player.HP != before.Player.HP || now.Player.MaxHP != before.Player.MaxHP || now.Player.Level != before.Player.Level {
			return aigame.PetSnapshot{}, errors.New("pet recovery world state changed")
		}
		return petRecoverySubject(now, pet, role)
	}
	err = npc.submit(ctx, before.Revision, func(now aigame.Snapshot) (aigame.Action, error) {
		current, err := unchanged(now)
		if err != nil {
			return aigame.Action{}, err
		}
		if current.HP != pet.HP || !now.AI.Received || !now.AI.ItemsKnown || healingItemCount(now, template) != count {
			return aigame.Action{}, ErrHealingItemUnavailable
		}
		found := false
		for _, item := range now.AI.Items {
			if item.Slot == itemSlot && item.TemplateID == template {
				found = true
				break
			}
		}
		if !found {
			return aigame.Action{}, ErrHealingItemUnavailable
		}
		return aigame.UseItem(now.Position.X, now.Position.Y, itemSlot, pet.Slot+1), nil
	})
	if err != nil {
		return err
	}
	if _, err := refreshInventoryIdentity(ctx, npc); err != nil {
		return err
	}
	_, err = waitHealerState(ctx, npc, func(now aigame.Snapshot) (bool, error) {
		current, err := unchanged(now)
		if err != nil {
			return false, err
		}
		return now.AI.Received && now.AI.ItemsKnown && now.AIObservationRevision > before.Revision &&
			healingItemCount(now, template) == count-1 && current.HP > pet.HP && current.HP <= pet.MaxHP, nil
	})
	return err
}

func petRecoveryWorld(s aigame.Snapshot) bool {
	return s.Connected && s.Phase == aigame.PhaseWorld && !s.Battle.Active && s.Player.HasStatus &&
		s.Player.HP > 0 && s.Player.MaxHP > 0 && s.Player.HP <= s.Player.MaxHP
}

// Bind the role before inventory refresh, then retain it through submission
// and confirmation. Switching between riding and battle roles is a state change.
func petRecoveryRole(s aigame.Snapshot, slot int32) uint8 {
	var role uint8
	if s.Player.BattlePetSlotKnown && s.Player.BattlePetSlot == slot {
		role |= 1
	}
	if s.Player.RidePetKnown && s.Player.RidePet == slot {
		role |= 2
	}
	return role
}

func petRecoverySubject(s aigame.Snapshot, expected aigame.PetSnapshot, role uint8) (aigame.PetSnapshot, error) {
	if role == 0 || petRecoveryRole(s, expected.Slot) != role {
		return aigame.PetSnapshot{}, ErrHealingItemUnavailable
	}
	for _, pet := range s.Pets {
		if pet.Slot != expected.Slot {
			continue
		}
		// A full K packet invalidates the stable ID. Enrich that same local
		// incarnation only; a reused slot must not inherit a healing request.
		if !expected.IdentityKnown && (expected.Identity == "" || expected.IdentityEpoch == 0 ||
			pet.Identity != expected.Identity || pet.IdentityEpoch != expected.IdentityEpoch) {
			return aigame.PetSnapshot{}, ErrHealingItemUnavailable
		}
		if !pet.IdentityKnown || pet.StableID == "" || (expected.IdentityKnown && pet.StableID != expected.StableID) ||
			pet.Name != expected.Name || pet.FreeName != expected.FreeName || pet.Graphic != expected.Graphic ||
			pet.MaxHP != expected.MaxHP || pet.Level != expected.Level || pet.HP <= 0 || pet.HP > pet.MaxHP {
			return aigame.PetSnapshot{}, ErrHealingItemUnavailable
		}
		return pet, nil
	}
	return aigame.PetSnapshot{}, ErrHealingItemUnavailable
}
