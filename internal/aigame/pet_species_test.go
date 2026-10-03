package aigame

import (
	"strings"
	"testing"
)

func TestPetSpeciesIsAuthoritativeAndSlotScoped(t *testing.T) {
	s := &Session{state: newGameState(true)}
	base := partyObservationBase + "|pet=0,old-pet,8|pet=2,unknown,3"
	s.applyEvent(stringEvent("S", base+"|pet_species=0,113;2,0"))
	pets := s.Snapshot().Pets
	if len(pets) != 2 || pets[0].SpeciesID != 113 || !pets[0].SpeciesIDKnown || !pets[1].SpeciesIDKnown || pets[1].SpeciesID != 0 || pets[1].IdentityKnown {
		t.Fatalf("pets=%+v", pets)
	}
	s.applyEvent(stringEvent("S", "K0|4|25"))
	if p := s.Snapshot().Pets[0]; !p.SpeciesIDKnown || p.SpeciesID != 113 {
		t.Fatal("partial K cleared species", p)
	}
	s.applyEvent(stringEvent("S", base))
	for _, p := range s.Snapshot().Pets {
		if p.SpeciesIDKnown {
			t.Fatal("old server response retained species", p)
		}
	}
	s.applyEvent(stringEvent("S", base+"|pet_species=0,113;2,0"))
	s.applyEvent(stringEvent("S", partyObservationBase+"|pet=0,replacement,5|pet_species=0,114"))
	if p := s.Snapshot().Pets[0]; p.SpeciesID != 114 || p.StableID != "replacement" || p.HP != 0 {
		t.Fatal("slot replacement reused old metadata", p)
	}
	s.applyEvent(stringEvent("S", "K0|1|100|20|30|5|10|100|200|3|4|5|6|0|0|0|0|0|0|0|0|Pet|Owner"))
	if p := s.Snapshot().Pets[0]; p.SpeciesIDKnown {
		t.Fatal("full K falsely retained species", p)
	}
}

func TestPetSpeciesRejectsOrphanDuplicateAndMalformedEntries(t *testing.T) {
	for _, value := range []string{"0,-1", "5,113", "1,113", "0,113;0,114", "0,113,1", "0,x", "0,2147483648", "0,113;", "0,113|pet_species=-"} {
		if _, ok := parseAIObservation(strings.Split(partyObservationBase+"|pet=0,pet,1|pet_species="+value, "|")[1:]); ok {
			t.Fatal("accepted malformed species", value)
		}
	}
	if parsed, ok := parseAIObservation(strings.Split(partyObservationBase+"|pet_species=-", "|")[1:]); !ok || len(parsed.Pets) != 0 {
		t.Fatal(parsed, ok)
	}
}

func TestPetEventFlagHasIndependentKnownStateAndSlotLifetime(t *testing.T) {
	s := &Session{state: newGameState(true)}
	base := partyObservationBase + "|pet=0,old-pet,8|pet_species=0,113"
	s.applyEvent(stringEvent("S", base+"|pet_event=0,0"))
	if p := s.Snapshot().Pets[0]; !p.EventFlagKnown || p.EventFlag != 0 {
		t.Fatal("normal pet flag zero became unknown", p)
	}
	s.applyEvent(stringEvent("S", "K0|4|25"))
	if !s.Snapshot().Pets[0].EventFlagKnown {
		t.Fatal("partial K lost event flag")
	}
	s.applyEvent(stringEvent("S", base))
	if s.Snapshot().Pets[0].EventFlagKnown {
		t.Fatal("old server retained event flag")
	}
	s.applyEvent(stringEvent("S", base+"|pet_event=0,1"))
	s.applyEvent(stringEvent("S", strings.Replace(base, "old-pet", "new-pet", 1)+"|pet_event=0,0"))
	if p := s.Snapshot().Pets[0]; !p.EventFlagKnown || p.EventFlag != 0 || p.StableID != "new-pet" {
		t.Fatal(p)
	}
	s.applyEvent(stringEvent("S", "K0|1|100|20|30|5|10|100|200|3|4|5|6|0|0|0|0|0|0|0|0|Pet|Owner"))
	if s.Snapshot().Pets[0].EventFlagKnown {
		t.Fatal("full K retained old event flag")
	}
	for _, value := range []string{"0,-1", "1,0", "0,0;0,1", "0,x", "0,2147483648", "0,0|pet_event=-"} {
		if _, ok := parseAIObservation(strings.Split(base+"|pet_event="+value, "|")[1:]); ok {
			t.Fatal("accepted malformed pet_event", value)
		}
	}
}
