package battlepolicy

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func sustainFrame(t *testing.T, mode int) Frame {
	t.Helper()
	f, e := Encode(reserveFixture(mode, 0), History{First: true})
	if e != nil {
		t.Fatal(e)
	}
	for row := range f.Entities {
		x := &f.Entities[row]
		x[eHP], x[eMaxHP], x[eHPRatio] = scale(100), scale(100), 1
		if x[eBench] == 1 {
			x[56] = 1
		}
	}
	return f
}

func wound(f *Frame, row int, hp int32) {
	f.Entities[row][eHP] = scale(hp)
	f.Entities[row][eHPRatio] = float32(hp) / 100
}

func addTeacherHeal(f *Frame, group, item bool) {
	for i := range f.Slots {
		s := &f.Slots[i]
		if s.Actor != "player" {
			continue
		}
		for row, x := range f.Entities {
			if group && row != 0 {
				continue
			}
			c := Candidate{ID: fmt.Sprintf("heal-%d-%d", i, row), Target: row, Supported: true}
			c.Features[5], c.Features[7], c.Features[22], c.Features[23] = 1, x[eAlly], 1, scale(65)
			if item {
				c.Features[29], c.Features[23] = 1, scale(20)
			}
			if group {
				c.Target = -1
				c.Features[25] = 1
				c.Features[23] = scale(50)
			}
			s.Candidates = append(s.Candidates, c)
		}
	}
}

func TestSustainCoordinatesHealingWithoutMutatingObservation(t *testing.T) {
	for _, group := range []bool{false, true} {
		f := sustainFrame(t, 2)
		target := f.Members[1]
		wound(&f, target, 30)
		addTeacherHeal(&f, group, false)
		before, _ := json.Marshal(f)
		choices, e := RuleChoices(f, "sustain")
		if e != nil {
			t.Fatal(e)
		}
		heals := 0
		for i, j := range choices {
			c := f.Slots[i].Candidates[j]
			if c.Features[22] == 1 {
				heals++
				if !group && c.Target != target {
					t.Fatal("healed wrong target")
				}
			}
		}
		if heals != 1 {
			t.Fatal("teammates duplicated nominal healing", group, heals)
		}
		after, _ := json.Marshal(f)
		if string(before) != string(after) {
			t.Fatal("teacher changed real observation to its planned outcome")
		}
		again, e := RuleChoices(f, "sustain")
		if e != nil || !reflect.DeepEqual(choices, again) {
			t.Fatal("teacher is not frozen/deterministic", e)
		}
		// Full HP, enemy injuries, unseen HP and bench injuries cannot create
		// a heal. Cover them together, with actions still available in frame.
		wound(&f, target, 100)
		for row, x := range f.Entities {
			if x[eAlly] == 0 || x[eBench] == 1 {
				wound(&f, row, 10)
			}
		}
		wound(&f, f.Members[0], 10)
		f.Entities[f.Members[0]][eHPKnown] = 0
		choices, e = RuleChoices(f, "sustain")
		if e != nil {
			t.Fatal(e)
		}
		for i, j := range choices {
			if f.Slots[i].Candidates[j].Features[22] == 1 {
				t.Fatal("healed unknown/bench/enemy state")
			}
		}
	}
}

func TestSustainItemsAndPetReplacement(t *testing.T) {
	f := sustainFrame(t, 1)
	wound(&f, f.Members[0], 30)
	addTeacherHeal(&f, false, true)
	choices, e := RuleChoices(f, "sustain")
	if e != nil {
		t.Fatal(e)
	}
	if f.Slots[0].Candidates[choices[0]].Features[29] != 1 {
		t.Fatal("did not use known consumable")
	}
	f = sustainFrame(t, 1)
	active := f.Slots[1].Entity
	wound(&f, active, 20)
	original, e := RuleChoices(f, "focus")
	if e != nil {
		t.Fatal(e)
	}
	choices, e = RuleChoices(f, "sustain")
	if e != nil {
		t.Fatal(e)
	}
	c := f.Slots[0].Candidates[choices[0]]
	if c.Features[30] != 1 || f.Entities[c.Target][eBench] != 1 {
		t.Fatal("did not replace critically injured pet")
	}
	if f.Slots[1].Entity != active || choices[1] != original[1] {
		t.Fatal("outgoing pet lost its current action")
	}
	for row := range f.Entities {
		if f.Entities[row][eBench] == 1 {
			f.Entities[row][eSummonKnown] = 0
		}
	}
	choices, e = RuleChoices(f, "sustain")
	if e != nil {
		t.Fatal(e)
	}
	if f.Slots[0].Candidates[choices[0]].Features[30] == 1 {
		t.Fatal("guessed unknown reserve eligibility")
	}
}

func TestSustainTeammateHealAvoidsUnnecessarySwitch(t *testing.T) {
	f := sustainFrame(t, 2)
	active := f.Slots[3].Entity
	wound(&f, active, 10)
	addTeacherHeal(&f, false, false)
	// Only the first teammate has a healer; the pet owner should keep the
	// existing pet after seeing the first teammate's planned recovery.
	for j := range f.Slots[2].Candidates {
		if f.Slots[2].Candidates[j].Features[22] == 1 {
			f.Slots[2].Candidates[j].Supported = false
		}
	}
	choices, e := RuleChoices(f, "sustain")
	if e != nil {
		t.Fatal(e)
	}
	if f.Slots[0].Candidates[choices[0]].Features[22] != 1 || f.Slots[2].Candidates[choices[2]].Features[30] == 1 {
		t.Fatal("independent instead of joint treatment/replacement")
	}
}
