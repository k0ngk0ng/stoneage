package battleenv

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func reserveScenario(mode int) Scenario {
	s := Scenario{Seed: 42, Level: 35, Mode: mode, MaxTurns: 20, HealingMagic: 20, HealingItems: 2}
	for i := 0; i < 2*mode; i++ {
		s.Builds = append(s.Builds, Build{30, 30, 30, 30})
		s.PetBuilds = append(s.PetBuilds, Build{30, 30, 30, 30})
		s.Reserves = append(s.Reserves, []ReservePet{{Build: Build{60, 20, 20, 20}, SkillMask: 3 | (1 << 3)}, {Build: Build{20, 20, 20, 60}, SkillMask: 3 | (1 << 6)}})
	}
	return s
}

func TestReserveScenarioValidation(t *testing.T) {
	s := reserveScenario(5)
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Scenario){
		func(s *Scenario) { s.Reserves[0] = nil },
		func(s *Scenario) { s.Reserves[0][0].Build[0]++ },
		func(s *Scenario) { s.Reserves[1][1].SkillMask = 0 },
		func(s *Scenario) { s.Reserves[1][1].SkillMask = 255 },
		func(s *Scenario) { s.Reserves = s.Reserves[:9] },
		func(s *Scenario) { s.PetBuilds = nil },
	} {
		bad := s
		bad.Reserves = CloneReserves(s.Reserves)
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid reserve scenario accepted")
		}
	}
}

func TestNativeReservesSwitchSkillsAndReset(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit native engine required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	engine, err := Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for _, mode := range []int{1, 5} {
		for repeat := 0; repeat < 3; repeat++ {
			s := reserveScenario(mode)
			state, err := engine.Reset(ctx, s)
			if err != nil {
				t.Fatal(mode, repeat, err)
			}
			for _, v := range state.Views {
				if len(v.Pets) != 3 || !v.Own.StandbyPetMaskKnown || v.Own.StandbyPetMask != 7 || !v.Own.SummonPetMaskKnown || v.Own.SummonPetMask != 7 {
					t.Fatal("missing reserve observation", v.Own, v.Pets)
				}
				for slot, ids := range [][]int32{{1, 2, 3, 60, 80, 90, 110}, {1, 2, 60}, {1, 2, 110}} {
					var got []int32
					for _, skill := range v.Pets[slot].Skills {
						got = append(got, skill.ID)
					}
					if !reflect.DeepEqual(got, ids) {
						t.Fatal("wrong public reserve skills", slot, got)
					}
				}
				if v.Pets[1].MaxHP == v.Pets[2].MaxHP {
					t.Fatal("different allocations collapsed")
				}
			}
			for _, target := range []int32{1, 2, 0} {
				plans := make([][]aigame.BattleSelection, len(state.Views))
				for i, v := range state.Views {
					for _, c := range v.Candidates {
						if c.Actor == "player" && c.Kind == "switch_pet" && c.Index == target || c.Actor == "pet" && c.Kind == "wait" {
							plans[i] = append(plans[i], aigame.BattleSelection{MatchID: v.MatchID, Turn: v.Turn, ObservationID: v.ID, CandidateID: c.ID})
						}
					}
					if len(plans[i]) != 2 {
						t.Fatal("missing switch or outgoing pet action", mode, target, plans[i])
					}
				}
				state, err = engine.Advance(ctx, plans)
				if err != nil {
					t.Fatal(err)
				}
				for _, v := range state.Views {
					if !v.Own.BattlePetSlotKnown || v.Own.BattlePetSlot != target {
						t.Fatal("native switch failed", target, v.Own.BattlePetSlot)
					}
					var ids []int32
					seen := map[int32]bool{}
					for _, c := range v.Candidates {
						if c.Actor == "pet" && c.Kind == "skill" && !seen[c.SkillID] {
							ids = append(ids, c.SkillID)
							seen[c.SkillID] = true
						}
					}
					want := [][]int32{{1, 2, 3, 60, 80, 90, 110}, {1, 2, 60}, {1, 2, 110}}[target]
					if !reflect.DeepEqual(ids, want) {
						t.Fatal("active pet inherited another pet's skill slots", target, ids)
					}
				}
			}
			t.Logf("mode=%d repeat=%d: three distinct pets, masks, allocations, skills and switches verified", mode, repeat)
		}
	}
}
