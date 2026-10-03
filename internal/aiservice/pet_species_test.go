package aiservice

import (
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

func TestPetSpeciesSurvivesSharedObservationWithoutInventingIdentity(t *testing.T) {
	snapshot := aigame.Snapshot{Pets: []aigame.PetSnapshot{
		{Slot: 0, StableID: "pet-a", IdentityKnown: true, SpeciesID: 0, SpeciesIDKnown: true, EventFlagKnown: true},
		{Slot: 2, StableID: "pet-b", IdentityKnown: true, SpeciesID: 113, SpeciesIDKnown: true, EventFlag: 1, EventFlagKnown: true},
		{Slot: 3, SpeciesID: 113, SpeciesIDKnown: true},
	}}
	observed := ProjectObservation(aimcp.Binding{}, snapshot)
	if observed.Character.Slot != nil || len(observed.Pets) != 3 || observed.Pets[2].ID != "" {
		t.Fatalf("invented slot or identity: %+v", observed)
	}
	automated := projectAutomationObservation(observed)
	if len(automated.Pets) != 2 {
		t.Fatalf("task projection accepted an unknown instance: %+v", automated.Pets)
	}
	for i, want := range snapshot.Pets[:2] {
		got := automated.Pets[i]
		if got.ID != want.StableID || got.Slot == nil || *got.Slot != int(want.Slot) || !got.SpeciesIDKnown || got.SpeciesID != int(want.SpeciesID) || !got.EventFlagKnown || got.EventFlag != int(want.EventFlag) {
			t.Fatalf("pet metadata changed across projections: %+v", got)
		}
	}
}
