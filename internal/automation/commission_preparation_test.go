package automation

import (
	"math"
	"testing"
)

func TestCommissionCapacityIsStrictAndCannotBeWaivedByFunding(t *testing.T) {
	c := Condition{Kind: "gold_reward_capacity", Value: 500}
	o := Observation{Connected: true, Ready: true, Gold: 499, Flags: map[string]bool{"gold_limit:known": true}, OwnProgress: map[string]int{"gold_limit": 1000}, UnlimitedFunds: true}
	if !c.Match(o) {
		t.Fatal("valid reward capacity rejected")
	}
	for _, gold := range []int64{500, 1000, -1, math.MaxInt64} {
		o.Gold = gold
		if c.Match(o) {
			t.Fatalf("accepted gold %d", gold)
		}
	}
	o.Gold = 0
	delete(o.Flags, "gold_limit:known")
	if c.Match(o) {
		t.Fatal("unknown capacity accepted")
	}
}
func TestCommissionPetGuardsRejectUnknownSpeciesAndInsufficientSlots(t *testing.T) {
	o := Observation{Connected: true, Ready: true, Flags: map[string]bool{"pets:known": true}, Pets: []Entity{{ID: "original", SpeciesID: 112, SpeciesIDKnown: true}}}
	space := Condition{Kind: "pet_free_slots", Value: 4}
	species := Condition{Kind: "pet_species_absent", Value: 1}
	if !space.Match(o) || !species.Match(o) {
		t.Fatal("valid pets rejected")
	}
	o.Pets[0].SpeciesIDKnown = false
	if species.Match(o) {
		t.Fatal("unknown species accepted")
	}
	o.Pets[0].SpeciesIDKnown = true
	o.Pets[0].SpeciesID = 1
	if species.Match(o) {
		t.Fatal("conflicting original accepted")
	}
	o.Pets = append(o.Pets, Entity{ID: "second"})
	if space.Match(o) {
		t.Fatal("insufficient slots accepted")
	}
	delete(o.Flags, "pets:known")
	o.Pets = nil
	if space.Match(o) {
		t.Fatal("unknown count accepted")
	}
}
