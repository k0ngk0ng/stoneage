package battlepolicy

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func addControlSkills(f *Frame, columns ...int) {
	for i := range f.Slots {
		s := &f.Slots[i]
		if s.Actor != "pet" {
			continue
		}
		for row := range f.Entities {
			for _, col := range columns {
				c := Candidate{ID: fmt.Sprintf("status-%d-%d-%d", i, row, col), Target: row, Supported: true}
				c.Features[0], c.Features[15], c.Features[col] = 1, 1, 1
				s.Candidates = append(s.Candidates, c)
			}
		}
	}
}

func controlEnemies(f Frame) []int {
	var out []int
	for row, x := range f.Entities {
		if x[eAlly] == 0 && x[eBench] == 0 {
			out = append(out, row)
		}
	}
	return out
}

func TestControlJointPlanAndObservationIsolation(t *testing.T) {
	for _, mode := range []int{1, 2, 3, 4, 5} {
		f := sustainFrame(t, mode)
		addControlSkills(&f, 16, 17, 18, 19)
		enemies := controlEnemies(f)
		wound(&f, enemies[0], 40)
		before, _ := json.Marshal(f)
		choices, err := RuleChoices(f, "control")
		if err != nil {
			t.Fatal(err)
		}
		status := map[int]int{}
		attacks := map[int]int{}
		for i, j := range choices {
			c := f.Slots[i].Candidates[j]
			if !c.Supported {
				t.Fatal("unsupported action")
			}
			if c.Features[0] == 1 {
				attacks[c.Target]++
			}
			if c.Features[15] == 1 {
				status[c.Target]++
				if c.Target == enemies[0] || c.Features[17] != 1 {
					t.Fatal("did not control off-focus enemy", c)
				}
			}
		}
		if len(status) != mode {
			t.Fatal("missing coordinated controls", mode, status)
		}
		for target, count := range status {
			if count != 1 || attacks[target] != 1 {
				t.Fatal("control target also takes planned damage", target, count, attacks)
			}
		}
		after, _ := json.Marshal(f)
		again, err := RuleChoices(f, "control")
		if err != nil || !reflect.DeepEqual(choices, again) || string(before) != string(after) {
			t.Fatal("non-deterministic or mutated observation", err)
		}
	}
}

func TestControlSkillSubsetsAndTargetBoundaries(t *testing.T) {
	for _, column := range []int{16, 17, 18, 19} {
		f := sustainFrame(t, 1)
		addControlSkills(&f, column)
		enemies := controlEnemies(f)
		wound(&f, enemies[0], 40)
		choices, err := RuleChoices(f, "control")
		if err != nil {
			t.Fatal(err)
		}
		if f.Slots[1].Candidates[choices[1]].Features[column] != 1 {
			t.Fatal("available status omitted", column)
		}
		for _, reason := range []string{"unknown", "dead", "low-hp", "unsupported", "status"} {
			t.Run(fmt.Sprintf("%d/%s", column, reason), func(t *testing.T) {
				f := sustainFrame(t, 1)
				addControlSkills(&f, column)
				for _, row := range controlEnemies(f) {
					switch reason {
					case "unknown":
						f.Entities[row][eHPKnown] = 0
					case "dead":
						f.Entities[row][eAlive], f.Entities[row][eDead] = 0, 1
					case "low-hp":
						wound(&f, row, 20)
					case "status":
						f.Entities[row][eFlags+3] = 1
					}
				}
				if reason == "unsupported" {
					for i := range f.Slots {
						for j := range f.Slots[i].Candidates {
							if f.Slots[i].Candidates[j].Features[15] == 1 {
								f.Slots[i].Candidates[j].Supported = false
							}
						}
					}
				}
				choices, err := RuleChoices(f, "control")
				if err != nil {
					t.Fatal(err)
				}
				for i, j := range choices {
					if f.Slots[i].Candidates[j].Features[15] == 1 {
						t.Fatal("status against ineligible target", i, f.Slots[i].Candidates[j], f.Entities[f.Slots[i].Candidates[j].Target])
					}
				}
			})
		}
	}
}

func TestControlAvoidsSleepingOrStoneFocus(t *testing.T) {
	for _, bit := range []int{4, 5, 6, 13} {
		f := sustainFrame(t, 1)
		addControlSkills(&f, 16, 17, 18, 19)
		enemies := controlEnemies(f)
		wound(&f, enemies[0], 40)
		f.Entities[enemies[0]][eFlags+bit] = 1
		choices, err := RuleChoices(f, "control")
		if err != nil {
			t.Fatal(err)
		}
		for i, j := range choices {
			c := f.Slots[i].Candidates[j]
			if c.Features[0] == 1 && c.Target == enemies[0] {
				t.Fatal("attacks controlled target while free enemy available", bit)
			}
		}
		f.Entities[enemies[1]][eFlags+bit] = 1
		choices, err = RuleChoices(f, "control")
		if err != nil {
			t.Fatal(err)
		}
		if f.Slots[0].Candidates[choices[0]].Features[0] != 1 {
			t.Fatal("all controlled enemies stall combat")
		}
	}
}

func TestControlRetainsHealingAndOutgoingPetOwnership(t *testing.T) {
	for _, healing := range []bool{false, true} {
		f := sustainFrame(t, 1)
		active := f.Slots[1].Entity
		wound(&f, active, 20)
		if healing {
			addTeacherHeal(&f, false, false)
		}
		addControlSkills(&f, 16, 17, 18, 19)
		choices, err := RuleChoices(f, "control")
		if err != nil {
			t.Fatal(err)
		}
		want := 30
		if healing {
			want = 22
		}
		if f.Slots[0].Candidates[choices[0]].Features[want] != 1 {
			t.Fatal("control replaced recovery/replacement plan")
		}
		if f.Slots[1].Entity != active || f.Slots[1].Candidates[choices[1]].Features[15] != 1 {
			t.Fatal("outgoing pet lost action ownership")
		}
	}
}

func TestControlDoesNotDisableAnUnavoidableDamageTarget(t *testing.T) {
	f := sustainFrame(t, 2)
	addControlSkills(&f, 17, 19)
	enemies := controlEnemies(f)
	focus, attacked := enemies[0], enemies[1]
	wound(&f, focus, 40)
	// One player can only damage the off-focus target. The commander must
	// account for that submitted plan even though it cannot redirect it.
	for j := range f.Slots[2].Candidates {
		c := &f.Slots[2].Candidates[j]
		if c.Features[0] == 1 && c.Target != attacked {
			c.Supported = false
		}
	}
	choices, err := RuleChoices(f, "control")
	if err != nil {
		t.Fatal(err)
	}
	if c := f.Slots[2].Candidates[choices[2]]; c.Features[0] != 1 || c.Target != attacked {
		t.Fatal("fixture lost planned attack")
	}
	for i, j := range choices {
		c := f.Slots[i].Candidates[j]
		if c.Features[15] == 1 && (c.Target == focus || c.Target == attacked) {
			t.Fatal("stone/sleep conflicts with another actor's damage")
		}
	}
}

func TestControlRejectsEveryKnownStatusAndAllowsFocusPressure(t *testing.T) {
	for _, bit := range []int{3, 4, 5, 6, 7, 8, 11, 12, 13, 14, 15} {
		f := sustainFrame(t, 1)
		addControlSkills(&f, 16, 17, 18, 19)
		for _, row := range controlEnemies(f) {
			f.Entities[row][eFlags+bit] = 1
		}
		choices, err := RuleChoices(f, "control")
		if err != nil {
			t.Fatal(err)
		}
		for i, j := range choices {
			if f.Slots[i].Candidates[j].Features[15] == 1 {
				t.Fatal("reapplied status to incompatible public state", bit)
			}
		}
	}
	for _, column := range []int{16, 18} {
		f := sustainFrame(t, 1)
		addControlSkills(&f, column)
		focus := controlEnemies(f)[0]
		wound(&f, focus, 40)
		for j := range f.Slots[1].Candidates {
			c := &f.Slots[1].Candidates[j]
			if c.Features[15] == 1 && c.Target != focus {
				c.Supported = false
			}
		}
		choices, err := RuleChoices(f, "control")
		if err != nil {
			t.Fatal(err)
		}
		c := f.Slots[1].Candidates[choices[1]]
		if c.Features[column] != 1 || c.Target != focus {
			t.Fatal("poison/confusion unnecessarily excluded focused enemy", column)
		}
	}
}
