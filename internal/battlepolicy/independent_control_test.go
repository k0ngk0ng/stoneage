package battlepolicy

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestIndependentControlKeepsObservationButNotTeammateReservations(t *testing.T) {
	for mode := 1; mode <= 5; mode++ {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			f := sustainFrame(t, mode)
			addControlSkills(&f, 16, 17, 18, 19)
			wound(&f, controlEnemies(f)[0], 40)
			before, _ := json.Marshal(f)
			independent, err := RuleChoices(f, "independent-control")
			if err != nil {
				t.Fatal(err)
			}
			central, err := RuleChoices(f, "control")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.Selections(independent); err != nil {
				t.Fatal("independent plan cannot be dispatched", err)
			}
			if mode == 1 && !reflect.DeepEqual(central, independent) {
				t.Fatal("single-member behavior changed")
			}
			targets := map[int]int{}
			for i, j := range independent {
				c := f.Slots[i].Candidates[j]
				if c.Features[15] == 1 {
					targets[c.Target]++
				}
			}
			if len(targets) != 1 {
				t.Fatal("members unexpectedly shared target reservations", targets)
			}
			for _, count := range targets {
				if count != mode {
					t.Fatal("not every member chose independently", count)
				}
			}
			// Other members' legal options can change, but their provisional
			// choices must not alter this member's own plan. Public entities,
			// history and own candidate rows are held fixed.
			for member := 0; member < mode; member++ {
				var changed Frame
				if err := json.Unmarshal(before, &changed); err != nil {
					t.Fatal(err)
				}
				for i := range changed.Slots {
					if changed.Slots[i].Member == member {
						continue
					}
					for j := range changed.Slots[i].Candidates {
						c := &changed.Slots[i].Candidates[j]
						c.Supported = c.Supported && (c.Features[1] == 1 || c.Features[2] == 1)
					}
				}
				other, err := RuleChoices(changed, "independent-control")
				if err != nil {
					t.Fatal(err)
				}
				for i, slot := range f.Slots {
					if slot.Member == member && other[i] != independent[i] {
						t.Fatal("teammate candidate changes leaked into own planning", member, i)
					}
				}
			}
			after, _ := json.Marshal(f)
			again, err := RuleChoices(f, "independent-control")
			if err != nil || string(before) != string(after) || !reflect.DeepEqual(again, independent) {
				t.Fatal("rule mutated observation or was nondeterministic", err)
			}
		})
	}
}

func TestIndependentControlCanSeeAndHealOtherMembers(t *testing.T) {
	for mode := 2; mode <= 5; mode++ {
		f := sustainFrame(t, mode)
		addTeacherHeal(&f, false, false)
		target := f.Members[mode-1]
		wound(&f, target, 30)
		for _, name := range []string{"control", "independent-control"} {
			choices, err := RuleChoices(f, name)
			if err != nil {
				t.Fatal(err)
			}
			heals := 0
			for i, j := range choices {
				c := f.Slots[i].Candidates[j]
				if c.Features[22] == 1 {
					heals++
					if c.Target != target {
						t.Fatal("shared public wound was not observed")
					}
				}
			}
			want := 1
			if name == "independent-control" {
				want = mode
			}
			if heals != want {
				t.Fatal("wrong healing reservation scope", mode, name, heals)
			}
		}
	}
}
