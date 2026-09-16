package aiservice

import (
	"context"
	"errors"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func (s *MovementSkill) recoverTravelPet(ctx context.Context, required *travelPetHealingRequired) error {
	healer, ok := s.HealthRecovery.(interface {
		HealPet(context.Context, aigame.PetSnapshot) error
	})
	if !ok {
		return ErrHealingItemUnavailable
	}
	snapshot, err := NewNPCSkill(s.Backend, nil).observe(ctx)
	if err != nil {
		return err
	}
	if !petRecoveryWorld(snapshot) {
		return ErrTravelPetStateUnknown
	}
	role := uint8(1)
	if required.role == "riding" {
		role = 2
	}
	if petRecoveryRole(snapshot, int32(required.slot)) != role {
		return ErrTravelPetStateUnknown
	}
	for _, pet := range snapshot.Pets {
		if int(pet.Slot) != required.slot {
			continue
		}
		if (required.id != "" && (!pet.IdentityKnown || pet.StableID != required.id)) ||
			(required.id == "" && (required.localIdentity == "" || required.localEpoch == 0 || pet.Identity != required.localIdentity || pet.IdentityEpoch != required.localEpoch)) {
			return ErrTravelPetStateUnknown
		}
		// The adapter checks fresh identity, role, consumption and actual HP gain.
		// Its ambiguous result is returned directly; this operation is never retried.
		return healer.HealPet(ctx, pet)
	}
	return errors.Join(ErrTravelPetStateUnknown, ErrHealingItemUnavailable)
}
