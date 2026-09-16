package aiservice

import (
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"sort"
)

// Supply discovery shares the exact reviewed catalog used by item.stock.
// It attests the offer, not reachability from an arbitrary character position.
type StockOfferSummary struct {
	Name       string `json:"name,omitempty"`
	Alias      string `json:"alias"`
	TemplateID int32  `json:"template_id"`
	UnitPrice  int64  `json:"unit_price"`
	NPC        string `json:"npc"`
	Floor      int    `json:"floor"`
	X          int    `json:"x"`
	Y          int    `json:"y"`
}

func (b *GameBackend) stockKnowledge() []StockOfferSummary {
	return ReviewedStockOffers(b.Knowledge, b.stockOffers)
}

// ReviewedStockOffers is shared by model and human catalog discovery.
func ReviewedStockOffers(knowledge *aiknowledge.Knowledge, contracts map[string]StockContract) []StockOfferSummary {
	offers := make([]StockOfferSummary, 0, len(contracts))
	for alias, offer := range contracts {
		if knowledge == nil || alias == "" || offer.NPC.SourceFingerprint != knowledge.Fingerprint() {
			continue
		}
		if _, err := offer.registry(1); err != nil {
			continue
		}
		offers = append(offers, StockOfferSummary{Name: offer.Name, Alias: alias, TemplateID: offer.TemplateID,
			UnitPrice: offer.UnitPrice, NPC: offer.NPC.Name, Floor: offer.NPC.Floor, X: offer.X, Y: offer.Y})
	}
	sort.Slice(offers, func(i, j int) bool { return offers[i].Alias < offers[j].Alias })
	return offers
}
