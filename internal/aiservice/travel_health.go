package aiservice

import (
	"errors"
	"fmt"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
)

var ErrTravelHealingRequired = errors.New("travel requires healing before entering an encounter area")

// checkTravelHealthAt applies the same encounter-area HP rule as W movement
// to a point that may be reached without a W packet, such as a mapwarp
// destination. Health comes from the observed character and selected pets;
// floor and coordinates identify the point being entered.
func (s *MovementSkill) checkTravelHealthAt(o aimcp.Observation, floor, x, y int) error {
	if s.SafeTravel {
		n, err := s.travelNavigator(o.Character.Level)
		if err != nil {
			return err
		}
		if n.blocked(floor, ainavigation.Point{X: x, Y: y}) {
			return ErrUnsafeTravelRoute
		}
	}
	if s.Backend == nil || s.Backend.Knowledge == nil || !s.encounterTile(floor, x, y) {
		return nil
	}
	return s.travelHealthError(o, floor, x, y)
}

func (s *MovementSkill) encounterTile(floor, x, y int) bool {
	if s.Backend == nil || s.Backend.Knowledge == nil {
		return false
	}
	for _, area := range s.Backend.Knowledge.Encounters {
		if area.Floor == floor && (area.EncounterProbability.Min > 0 || area.EncounterProbability.Max > 0) && area.Bounds.Contains(x, y) {
			return true
		}
	}
	return false
}

func (s *MovementSkill) travelHealthError(o aimcp.Observation, floor, x, y int) error {
	// Keep at least half the observed maximum HP before another encounter.
	// This is a minimum preparation rule, not a claim that stronger enemies
	// are survivable. Unknown maximum HP cannot establish readiness.
	hp, maximum := o.Character.HP, o.Character.MaxHP
	if maximum <= 0 || hp <= 0 || hp > maximum || hp < maximum/2+maximum%2 {
		return fmt.Errorf("%w: hp=%d max_hp=%d floor=%d position=(%d,%d)", ErrTravelHealingRequired, hp, maximum, floor, x, y)
	}

	if s.SafeTravel {
		for _, role := range []struct {
			name      string
			selection aimcp.PetSelection
		}{{"riding", o.RidingPet}, {"battle", o.BattlePet}} {
			selection := role.selection
			// Native KS is optional until a combat pet is selected. Riding status is
			// always present in full P and must be known before encounter travel.
			if !selection.Known {
				if role.name == "riding" {
					return ErrTravelPetStateUnknown
				}
				continue
			}
			if selection.Slot == -1 {
				continue
			}
			pet := selection.Pet
			if selection.Slot < 0 || selection.Slot >= 5 || pet == nil || pet.MaxHP <= 0 || pet.HP <= 0 || pet.HP > pet.MaxHP {
				return ErrTravelPetStateUnknown
			}
			if pet.HP < pet.MaxHP/2+pet.MaxHP%2 {
				if pet.ID == "" && (selection.LocalIdentity == "" || selection.LocalEpoch == 0) {
					return ErrTravelPetStateUnknown
				}
				return &travelPetHealingRequired{role: role.name, slot: selection.Slot, id: pet.ID, localIdentity: selection.LocalIdentity, localEpoch: selection.LocalEpoch}
			}
		}
	}
	return nil
}

// checkTravelHealth runs at the common W boundary, including cross-map
// segments and safe pre-write revision retries. Town movement remains available
// to reach a healer. Encounter rows with missing enemy groups still count:
// incomplete game data is not evidence that a route is safe.
func (s *MovementSkill) checkTravelHealth(o aimcp.Observation, action aigame.Action) error {
	if s.Backend == nil || s.Backend.Knowledge == nil {
		return nil
	}
	x, y := o.X, o.Y
	risky := s.encounterTile(o.Floor, x, y)
	riskyX, riskyY := x, y
	deltas := [...][2]int{{0, -1}, {1, -1}, {1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}}
	for _, direction := range []byte(action.Route) {
		if direction < 'a' || direction > 'h' {
			return errors.New("invalid travel direction")
		}
		delta := deltas[direction-'a']
		x, y = x+delta[0], y+delta[1]
		if !risky && s.encounterTile(o.Floor, x, y) {
			risky = true
			riskyX, riskyY = x, y
		}
	}
	if risky {
		return s.travelHealthError(o, o.Floor, riskyX, riskyY)
	}
	return nil
}

var ErrTravelPetStateUnknown = errors.New("赶路所需的骑宠或出战宠物状态未确认，或宠物已死亡")

type travelPetHealingRequired struct {
	role          string
	slot          int
	id            string
	localIdentity string
	localEpoch    uint64
}

func (e *travelPetHealingRequired) Error() string {
	if e.role == "riding" {
		return "赶路前需要治疗骑宠"
	}
	return "赶路前需要治疗出战宠物"
}
