package battlepolicy

import (
	"reflect"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func reserveFixture(mode, side int) []aigame.BattleView {
	team := fixture(mode, side)
	for i, v := range team {
		v.Own.RidePet, v.Own.RidePetKnown = -1, true
		v.Own.StandbyPetMask, v.Own.StandbyPetMaskKnown = 31, true
		v.Own.SummonPetMask, v.Own.SummonPetMaskKnown = 31, true
		for slot := int32(1); slot < 5; slot++ {
			pet := v.Pets[0]
			pet.Slot = slot
			pet.HP = 20 * slot
			pet.Attack = 10 * slot
			pet.Skills = []aigame.PetSkillSnapshot{{Index: 0, ID: 2, Field: 1, Target: 5}}
			v.Pets = append(v.Pets, pet)
		}
		next := aigame.NewBattleView(aigame.Snapshot{Phase: aigame.PhaseBattle, Player: v.Own, Battle: v.Battle, Pets: v.Pets})
		next.Mode = mode
		team[i] = next
	}
	return team
}

func TestReserveRepresentationAndJointSwitch(t *testing.T) {
	team := reserveFixture(5, 0)
	f, err := Encode(team, History{First: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(f.Entities) != 40 || len(f.Slots) != 10 {
		t.Fatal("bench became acting team", len(f.Entities), len(f.Slots))
	}
	choices := make([]int, len(f.Slots))
	for i, s := range f.Slots {
		found := false
		for j, c := range s.Candidates {
			if s.Actor == "player" && c.Features[30] == 1 && c.Supported {
				x := f.Entities[c.Target]
				if x[eBench] != 1 || x[ePet] != 1 || x[eAlly] != 1 || x[eSummonable] != 1 || x[57] != 1 || x[56] != 0 {
					t.Fatal("reserve identity or skills lost", x)
				}
				choices[i] = j
				found = true
				break
			}
			if s.Actor == "pet" && c.Features[0] == 1 && c.Supported {
				choices[i] = j
				found = true
				break
			}
		}
		if !found {
			t.Fatal("missing reserve choice or original pet attack", i)
		}
	}
	plans, err := f.Selections(choices)
	if err != nil {
		t.Fatal(err)
	}
	for i, plan := range plans {
		if len(plan) != 2 {
			t.Fatal("incomplete commander plan", i)
		}
		s := aigame.Snapshot{Phase: aigame.PhaseBattle, Player: team[i].Own, Battle: team[i].Battle, Pets: team[i].Pets}
		for _, choice := range plan {
			if _, err := aigame.ResolveBattleSelection(s, choice); err != nil {
				t.Fatal("switch invalidated outgoing pet plan", err)
			}
			s.Battle.PlayerSubmitted = true
		}
	}
	swapped, err := Encode(reserveFixture(5, 1), History{First: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.Entities, swapped.Entities) {
		t.Fatal("absolute side leaked into reserves")
	}
	for i, s := range f.Slots {
		for j, c := range s.Candidates {
			other := swapped.Slots[i].Candidates[j]
			if c.Features != other.Features || c.Target != other.Target || c.Supported != other.Supported {
				t.Fatal("switch candidate depends on side")
			}
		}
	}
	// The frame validator must never let a reserve acquire a command slot.
	f.Slots[1].Entity = 20
	if f.Validate() == nil {
		t.Fatal("reserve issued an independent action")
	}
}

func TestGroupHealingExcludesReservePets(t *testing.T) {
	team := reserveFixture(1, 0)
	team[0].Candidates = append(team[0].Candidates, aigame.BattleCandidate{ID: "group-heal", Actor: "player", Kind: "magic", MagicID: 20, MagicIDKnown: true, MPCost: 20, Target: 20})
	f, err := Encode(team, History{First: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Slots[0].Candidates {
		if c.ID == "group-heal" {
			if c.Features[26] != .2 {
				t.Fatal("group heal counted bench pets", c.Features[26])
			}
			return
		}
	}
	t.Fatal("group heal missing")
}

func TestSwitchHistoryHasPublicActorAndOrder(t *testing.T) {
	for _, kind := range []string{"wait", "pet_recall", "pet_summon"} {
		a := encodeEffect(aigame.BattleLogEntry{Kind: kind, Actor: 0, Target: 5}, 0)
		b := encodeEffect(aigame.BattleLogEntry{Kind: kind, Actor: 10, Target: 15}, 1)
		if a != b || a[8] != 1 || a[6] != 0 {
			t.Fatal("switch history identity invalid", kind)
		}
	}
}
