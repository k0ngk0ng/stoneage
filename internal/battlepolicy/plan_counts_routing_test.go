package battlepolicy

import (
	"reflect"
	"testing"
)

func TestPlanCountsRoutingAndInterleavedMembers(t *testing.T) {
	f, err := Encode(fixture(5, 0), History{First: true})
	if err != nil {
		t.Fatal(err)
	}
	choices, err := RuleChoices(f, "focus")
	if err != nil {
		t.Fatal(err)
	}
	// Reverse entity row storage without changing world/seat semantics.
	remapped := f
	remapped.Entities = make([][EntityFeatures]float32, len(f.Entities))
	remapped.Members = append([]int(nil), f.Members...)
	remapped.Slots = append([]Slot(nil), f.Slots...)
	row := func(i int) int {
		if i < 0 {
			return i
		}
		return len(f.Entities) - 1 - i
	}
	for i, v := range f.Entities {
		remapped.Entities[row(i)] = v
	}
	for i, v := range f.Members {
		remapped.Members[i] = row(v)
	}
	for i, slot := range f.Slots {
		remapped.Slots[i].Entity = row(slot.Entity)
		remapped.Slots[i].Observation = "another-routing-id"
		remapped.Slots[i].Candidates = append([]Candidate(nil), slot.Candidates...)
		for j, c := range slot.Candidates {
			remapped.Slots[i].Candidates[j].Target = row(c.Target)
			remapped.Slots[i].Candidates[j].ID = "renamed-" + c.ID
		}
	}
	if err := remapped.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"", "member"} {
		for i := range f.Slots {
			a, err := PlanTargetCounts(f, i, choices[:i], scope)
			if err != nil {
				t.Fatal(err)
			}
			b, err := PlanTargetCounts(remapped, i, choices[:i], scope)
			if err != nil || !reflect.DeepEqual(a, b) {
				t.Fatal("routing/row order changed counts", scope, i, err)
			}
		}
	}
	interleaved := f
	interleaved.Slots = nil
	var reordered, indices []int
	for _, actor := range []string{"player", "pet"} {
		for i, slot := range f.Slots {
			if slot.Actor == actor {
				interleaved.Slots = append(interleaved.Slots, slot)
				reordered = append(reordered, choices[i])
				indices = append(indices, i)
			}
		}
	}
	if err := interleaved.Validate(); err != nil {
		t.Fatal(err)
	}
	for i, original := range indices {
		a, err := PlanTargetCounts(f, original, choices[:original], "member")
		if err != nil {
			t.Fatal(err)
		}
		b, err := PlanTargetCounts(interleaved, i, reordered[:i], "member")
		if err != nil || !reflect.DeepEqual(a, b) {
			t.Fatal("interleaving leaked other members' plans", i, err)
		}
	}
}
