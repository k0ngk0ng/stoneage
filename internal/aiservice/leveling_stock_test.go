package aiservice

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func TestLevelingStockQuotesThresholdAndPurchasesShortfall(t *testing.T) {
	for _, count := range []int{0, 3, 4} {
		stock, session, _ := stockFixture(t)
		session.snapshot.Player.HP, session.snapshot.Player.MaxHP = 100, 100
		session.funded = true
		stock.Backend.Funding = func(context.Context) (bool, error) { return true, nil }
		for i := 0; i < count; i++ {
			session.bag = append(session.bag, aigame.AIInventoryItem{Slot: int32(5 + i), TemplateID: 2344})
		}
		supplies := &LevelingStock{Stock: stock}
		order, err := supplies.Quote(context.Background(), session.snapshot, map[string]json.RawMessage{"supply_item": json.RawMessage(`"meat"`)})
		if err != nil || session.purchases != 0 {
			t.Fatalf("count=%d quote err=%v purchases=%d", count, err, session.purchases)
		}
		if count > 3 {
			if order != nil {
				t.Fatal("restocked above threshold")
			}
			continue
		}
		if order == nil || order.TargetCount != 10 || order.Action.MaximumCost != 120 || order.Action.ExpectedRevision != session.snapshot.Revision {
			t.Fatalf("bad quote: %+v", order)
		}
		if err := supplies.Purchase(context.Background(), *order); err != nil || session.purchases != 1 || session.quantity != 10-count {
			t.Fatalf("purchase err=%v count=%d quantity=%d", err, session.purchases, session.quantity)
		}
		order, err = supplies.Quote(context.Background(), session.snapshot, map[string]json.RawMessage{"supply_item": json.RawMessage(`"meat"`)})
		if err != nil || order != nil || session.purchases != 1 {
			t.Fatalf("confirmed stock requeued: %+v %v", order, err)
		}
	}
}

func TestLevelingStockRejectsInvalidConfigurationWithoutPurchase(t *testing.T) {
	for _, raw := range []string{`{"supply_item":"missing"}`, `{"supply_item":"meat","supply_reorder_count":10}`, `{"supply_item":"meat","supply_target_count":14}`, `{"supply_item":123}`} {
		stock, session, _ := stockFixture(t)
		var parameters map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &parameters); err != nil {
			t.Fatal(err)
		}
		if _, err := (&LevelingStock{Stock: stock}).Quote(context.Background(), session.snapshot, parameters); err == nil || session.purchases != 0 {
			t.Fatalf("invalid config accepted: %s err=%v", raw, err)
		}
	}
}
