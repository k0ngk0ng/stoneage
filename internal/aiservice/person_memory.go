package aiservice

import (
	"context"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

// Store first confirmed sighting and first confirmed shared party separately.
// Neither fact implies friendship, trust, or a model-inferred relationship.
func (recorder *MemoryRecorder) recordPeople(ctx context.Context, profile string, o aimcp.Observation, state *memoryObservationState) error {
	if !o.Connected || !o.Ready {
		return nil
	}
	if state.seenPeople == nil || len(state.seenPeople) > 512 {
		state.seenPeople = map[string]bool{}
	}
	remaining := 16
	record := func(id, name, role string) error {
		if remaining <= 0 || id == o.PersistentCharacterID || !aigame.ValidPersistentCharacterID(id) {
			return nil
		}
		key := recorderEventKey("person-first-observation-v1", id, role)
		if state.seenPeople[key] {
			return nil
		}
		remaining--
		if err := recorder.record(ctx, profile, key, "social.encounter", id, map[string]any{
			"persistent_character_id": id, "identity_evidence": "server_person_v1", "role": role, "name": name, "observer_floor": o.Floor,
		}); err != nil {
			return err
		}
		state.seenPeople[key] = true
		return nil
	}
	for _, p := range o.Party {
		if err := record(p.PersistentCharacterID, p.Name, "party"); err != nil {
			return err
		}
	}
	for _, p := range o.Actors {
		if err := record(p.PersistentCharacterID, p.Name, "visible"); err != nil {
			return err
		}
	}
	return nil
}
