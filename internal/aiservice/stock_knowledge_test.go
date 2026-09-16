package aiservice

import (
	"context"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

func TestSupplyDiscoveryOmitsDisabledAndUnverifiedOffers(t *testing.T) {
	for _, mode := range []string{"disabled", "fingerprint", "invalid-offer"} {
		t.Run(mode, func(t *testing.T) {
			stock, _, _ := stockFixture(t)
			backend := stock.Backend
			if mode != "disabled" {
				offer := stock.Contracts["meat"]
				if mode == "fingerprint" {
					offer.NPC.SourceFingerprint = "other"
				} else {
					offer.TemplateID = 0
				}
				backend.stockOffers = map[string]StockContract{"meat": offer}
			}
			result, err := backend.QueryKnowledge(context.Background(), backend.Binding, aimcp.KnowledgeQuery{Kind: "rule", Text: "supply"})
			if err != nil || len(result.Entries) != 0 {
				t.Fatalf("unavailable offer advertised: %+v %v", result, err)
			}
		})
	}
}
