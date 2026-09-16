package aiservice

import "testing"

func TestHealerCatalogRequiresReviewedRatesAndCopiesThem(t *testing.T) {
	document := validNPCRegistryDocument()
	document.NPCs[0].Healer = &HealerRates{PaidFromLevel: 10, HPRateMilli: 500}
	registry, err := decodeNPCRegistry(marshalNPCRegistryDocument(t, document), catalogTestFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	contracts, err := registry.HealerContracts()
	if err != nil || len(contracts) != 1 || contracts["trainer"].HPRateMilli != 500 {
		t.Fatalf("contracts=%+v err=%v", contracts, err)
	}
	copy, _ := registry.Lookup("trainer")
	copy.Healer.HPRateMilli = 999
	copy2, _ := registry.Lookup("trainer")
	if copy2.Healer.HPRateMilli != 500 {
		t.Fatal("lookup shared mutable healer rates")
	}
	for _, rate := range []int32{0, -1, 1000001} {
		document.NPCs[0].Healer.HPRateMilli = rate
		if _, err := decodeNPCRegistry(marshalNPCRegistryDocument(t, document), catalogTestFingerprint); err == nil {
			t.Fatalf("accepted rate %d", rate)
		}
	}
	document.NPCs[0].Healer = &HealerRates{PaidFromLevel: 10, HPRateMilli: 500}
	document.NPCs[0].Verified = false
	if _, err := decodeNPCRegistry(marshalNPCRegistryDocument(t, document), catalogTestFingerprint); err == nil {
		t.Fatal("unverified healer accepted")
	}
}

func TestOrdinaryNPCCatalogDoesNotEnableHealing(t *testing.T) {
	registry, err := decodeNPCRegistry(marshalNPCRegistryDocument(t, validNPCRegistryDocument()), catalogTestFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	contracts, err := registry.HealerContracts()
	if err != nil || len(contracts) != 0 {
		t.Fatalf("contracts=%v err=%v", contracts, err)
	}
}
