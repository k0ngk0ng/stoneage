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

func TestGuardianSkillMasksAndEnvironmentCompatibility(t *testing.T) {
	s := reserveScenario(1)
	s.PetSkillMasks = []int{131, 127}
	s.Reserves[0][0].SkillMask = 128
	if err := s.ValidateEnvironment("controlled-battle-v8"); err != nil {
		t.Fatal(err)
	}
	if s.ValidateEnvironment("controlled-battle-v7") == nil {
		t.Fatal("new skill configuration accepted by old environment")
	}
	for _, bad := range []int{0, -1, 255, 256, 1024} {
		if ValidPetSkillMask(bad) {
			t.Fatal("invalid mask accepted", bad)
		}
	}
	for _, good := range []int{1, 127, 128, 131, 254} {
		if !ValidPetSkillMask(good) {
			t.Fatal("valid seven-slot subset rejected", good)
		}
	}
	s.PetSkillMasks = []int{131}
	if s.Validate() == nil {
		t.Fatal("incomplete skill roster accepted")
	}
	s.PetSkillMasks = nil
	if s.ValidateEnvironment("controlled-battle-v7") == nil {
		t.Fatal("new reserve vocabulary accepted by old environment")
	}
	s.Reserves[0][0].SkillMask = 127
	if err := s.ValidateEnvironment("controlled-battle-v7"); err != nil {
		t.Fatal("old scenario no longer accepted", err)
	}
}

// Only public observations and actual native movie effects establish behavior.
// This does not set guardian flags or inject a synthetic successful hit.
func TestNativeGuardianSkillConfigurationAndEffect(t *testing.T) {
	raw := os.Getenv("STONEAGE_BATTLE_GUARDIAN_ENV_COMMAND")
	if raw == "" {
		t.Skip("explicit v8 native engine required")
	}
	var command []string
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	engine, err := Start(ctx, command, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if engine.Metadata().Scenario != "controlled-battle-v8" {
		t.Fatal("guardian test requires actual v8 worker")
	}
	for _, mode := range []int{1, 5} {
		s := reserveScenario(mode)
		for i := range s.Builds {
			s.Builds[i] = Build{90, 10, 10, 10}
			s.PetBuilds[i] = Build{60, 40, 10, 10}
			s.PetSkillMasks = append(s.PetSkillMasks, 131) // attack, guard, guardian
			s.Reserves[i][0].SkillMask = 128
		}
		state, err := engine.Reset(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		for _, view := range state.Views {
			var active, reserve []int32
			for _, skill := range view.Pets[0].Skills {
				active = append(active, skill.ID)
			}
			for _, skill := range view.Pets[1].Skills {
				reserve = append(reserve, skill.ID)
			}
			if !reflect.DeepEqual(active, []int32{1, 2, 20}) || !reflect.DeepEqual(reserve, []int32{20}) {
				t.Fatal("configured public skills lost or not packed", active, reserve)
			}
		}
		seenGuardian, seenAttack := false, false
		for turn := 0; turn < 2; turn++ {
			plans := make([][]aigame.BattleSelection, len(state.Views))
			for i, view := range state.Views {
				for _, candidate := range view.Candidates {
					want := candidate.Actor == "player" && (i == 0 && candidate.Kind == "attack" && candidate.Target == 10 || i != 0 && candidate.Kind == "guard")
					if candidate.Actor == "pet" {
						want = i == mode && candidate.Kind == "skill" && candidate.SkillID == 20 && candidate.Target == 0 || i != mode && candidate.Kind == "wait"
						if turn == 1 {
							want = candidate.Kind == "wait" // Guardian expires; do not protect on this turn.
						}
					}
					if want {
						plans[i] = append(plans[i], aigame.BattleSelection{MatchID: view.MatchID, Turn: view.Turn, ObservationID: view.ID, CandidateID: candidate.ID})
					}
				}
				if len(plans[i]) != 2 {
					t.Fatal("missing guardian/attack/guard/wait candidates", i, plans[i])
				}
			}
			state, err = engine.Advance(ctx, plans)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range state.Events[0].Events {
				for _, effect := range event.Effects {
					if effect.Kind != "attack" {
						continue
					}
					if turn == 0 && effect.Actor == 0 && effect.Target == 10 && effect.Recipient != nil && *effect.Recipient == 15 && effect.Guardian != nil && *effect.Guardian == 15 && effect.Damage > 0 {
						seenGuardian = true
					}
					// Native attack may miss or deal zero damage. Its actual BH
					// event still proves the selected target was attacked, unlike
					// a guard command or an assumed successful submission.
					if turn == 0 && effect.Actor == 15 && effect.Target == 0 {
						seenAttack = true
					}
					if turn == 1 && effect.Guardian != nil {
						t.Fatal("guardian leaked into next turn", effect)
					}
				}
			}
		}
		if !seenGuardian || !seenAttack {
			t.Fatal("guardian command did not both attack and protect its owner", mode, seenGuardian, seenAttack)
		}
		// Reset the same worker to the old omitted/default loadout. No stale
		// guardian skill or ownership should survive across games.
		s.PetSkillMasks = nil
		state, err = engine.Reset(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Views[0].Pets[0].Skills) != 7 || state.Views[0].Pets[0].Skills[6].ID != 110 {
			t.Fatal("default pet skills not restored on reset")
		}
		t.Logf("%dv%d: public guardian attack/protection, next-turn expiry and reset verified", mode, mode)
	}
}
