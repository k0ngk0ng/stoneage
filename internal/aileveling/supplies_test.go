package aileveling

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type coordinatorSupplyTestAdapter struct {
	mu sync.Mutex

	order         *SupplyOrder
	quoteCalls    int
	quoteParams   map[string]json.RawMessage
	purchaseCalls int
	purchase      func(context.Context, SupplyOrder) error
}

func (s *coordinatorSupplyTestAdapter) Quote(_ context.Context, _ aigame.Snapshot, parameters map[string]json.RawMessage) (*SupplyOrder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quoteCalls++
	s.quoteParams = make(map[string]json.RawMessage, len(parameters))
	for key, value := range parameters {
		s.quoteParams[key] = append(json.RawMessage(nil), value...)
	}
	if s.order == nil {
		return nil, nil
	}
	order := *s.order
	return &order, nil
}

func (s *coordinatorSupplyTestAdapter) Purchase(ctx context.Context, order SupplyOrder) error {
	s.mu.Lock()
	s.purchaseCalls++
	purchase := s.purchase
	s.mu.Unlock()
	if purchase == nil {
		return nil
	}
	return purchase(ctx, order)
}

func (s *coordinatorSupplyTestAdapter) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quoteCalls, s.purchaseCalls
}

func (s *coordinatorSupplyTestAdapter) parameters() map[string]json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	parameters := make(map[string]json.RawMessage, len(s.quoteParams))
	for key, value := range s.quoteParams {
		parameters[key] = append(json.RawMessage(nil), value...)
	}
	return parameters
}

func coordinatorSupplyFixture(t *testing.T, maximumSpend int64) (*Coordinator, *fakeGame, *aicontrol.Gate, *memoryStore, *coordinatorSupplyTestAdapter, aimcpReceipt) {
	t.Helper()
	game := &fakeGame{snapshot: worldSnapshot()}
	coordinator, gate, store := newCoordinator(t, game, &fakeNavigator{navigation: Navigation{InArea: true}})
	receipt := startCharacter(t, coordinator, StartRequest{
		TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30, MaximumSpend: maximumSpend,
	})
	destination := aigame.Point{Floor: 1, X: 4, Y: 5}
	order := &SupplyOrder{
		Action: automation.Action{
			Skill:            "item.stock",
			ExpectedRevision: game.snapshot.Revision,
			MaximumCost:      5,
		},
		TemplateID: 2344, TargetCount: 2, Destination: destination, Gold: int64(game.snapshot.Player.Gold),
	}
	supplies := &coordinatorSupplyTestAdapter{order: order}
	coordinator.Supplies = supplies
	return coordinator, game, gate, store, supplies, aimcpReceipt{Handle: receipt.Handle}
}

// aimcpReceipt keeps this fixture independent of the concrete receipt fields
// used by the rest of the leveling tests.
type aimcpReceipt struct{ Handle string }

func setCoordinatorSupplyObservation(game *fakeGame, order SupplyOrder, count int, position aigame.Point) {
	items := make([]aigame.AIInventoryItem, 0, count)
	for index := 0; index < count; index++ {
		items = append(items, aigame.AIInventoryItem{Slot: int32(5 + index), TemplateID: order.TemplateID})
	}
	game.mu.Lock()
	defer game.mu.Unlock()
	game.snapshot.Position = position
	game.snapshot.AI = aigame.AIObservation{Received: true, ItemsKnown: true, Items: items}
	game.snapshot.Revision++
}

func TestSupplyRestockPersistsPreparedBeforePurchase(t *testing.T) {
	coordinator, game, _, store, supplies, receipt := coordinatorSupplyFixture(t, 10)
	prepared := false
	supplies.purchase = func(_ context.Context, order SupplyOrder) error {
		checkpoint, err := store.Load(context.Background(), receipt.Handle)
		if err != nil {
			t.Fatalf("load checkpoint before purchase: %v", err)
		}
		if checkpoint.Phase != "prepared" || checkpoint.ReservedSpend != order.Action.MaximumCost {
			t.Fatalf("purchase preceded prepared fence: checkpoint=%+v", checkpoint)
		}
		game.mu.Lock()
		actions := len(game.actions)
		game.mu.Unlock()
		if actions != 0 {
			t.Fatalf("game action happened before purchase: %d", actions)
		}
		prepared = true
		setCoordinatorSupplyObservation(game, order, order.TargetCount, order.Destination)
		return nil
	}

	checkpoint, err := coordinator.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Phase != "ready" || checkpoint.Status != automation.Running || !prepared {
		t.Fatalf("restock checkpoint=%+v err=%v prepared=%v", checkpoint, err, prepared)
	}
	if _, purchases := supplies.counts(); purchases != 1 {
		t.Fatalf("purchases=%d, want one", purchases)
	}
}

func TestSupplyRestockRequiresAuthoritativeBackpackQuantityAndPosition(t *testing.T) {
	for _, test := range []struct {
		name        string
		count       int
		position    func(SupplyOrder) aigame.Point
		wantSuccess bool
		wantPhase   string
	}{
		{name: "confirmed", count: 2, position: func(order SupplyOrder) aigame.Point { return order.Destination }, wantSuccess: true, wantPhase: "ready"},
		{name: "wrong quantity", count: 1, position: func(order SupplyOrder) aigame.Point { return order.Destination }, wantPhase: "prepared"},
		{name: "wrong position", count: 2, position: func(order SupplyOrder) aigame.Point { return aigame.Point{Floor: 1, X: 9, Y: 9} }, wantPhase: "prepared"},
	} {
		t.Run(test.name, func(t *testing.T) {
			coordinator, game, _, _, supplies, receipt := coordinatorSupplyFixture(t, 10)
			supplies.purchase = func(_ context.Context, order SupplyOrder) error {
				setCoordinatorSupplyObservation(game, order, test.count, test.position(order))
				return nil
			}
			checkpoint, err := coordinator.Tick(context.Background(), receipt.Handle)
			if (err == nil) != test.wantSuccess || checkpoint.Phase != test.wantPhase {
				t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
			}
			if test.wantSuccess && checkpoint.Status != automation.Running {
				t.Fatalf("successful restock status=%s", checkpoint.Status)
			}
			if !test.wantSuccess && checkpoint.Status != automation.Paused {
				t.Fatalf("unconfirmed restock status=%s", checkpoint.Status)
			}
			_, purchases := supplies.counts()
			if purchases != 1 {
				t.Fatalf("purchases=%d, want one", purchases)
			}
			if !test.wantSuccess {
				if _, err := coordinator.Tick(context.Background(), receipt.Handle); err != nil {
					t.Fatalf("paused restock tick: %v", err)
				}
				_, purchases = supplies.counts()
				if purchases != 1 {
					t.Fatalf("unconfirmed restock was repeated: purchases=%d", purchases)
				}
			}
		})
	}
}

func TestSupplyRestockRejectsBudgetBeforePurchase(t *testing.T) {
	coordinator, _, _, store, supplies, receipt := coordinatorSupplyFixture(t, 4)
	checkpoint, err := coordinator.Tick(context.Background(), receipt.Handle)
	if err == nil || checkpoint.Status != automation.Paused || checkpoint.Phase != "ready" {
		t.Fatalf("budget rejection checkpoint=%+v err=%v", checkpoint, err)
	}
	if _, purchases := supplies.counts(); purchases != 0 {
		t.Fatalf("budget rejection purchased supplies: %d", purchases)
	}
	saved, err := store.Load(context.Background(), receipt.Handle)
	if err != nil || saved.ReservedSpend != 0 {
		t.Fatalf("budget rejection reserved spend=%d err=%v", saved.ReservedSpend, err)
	}
}

func TestSupplyRestockUnknownResultIsNeverRebought(t *testing.T) {
	coordinator, game, _, _, supplies, receipt := coordinatorSupplyFixture(t, 10)
	unknown := errors.New("purchase result unknown")
	supplies.purchase = func(_ context.Context, order SupplyOrder) error {
		setCoordinatorSupplyObservation(game, order, order.TargetCount, order.Destination)
		return unknown
	}
	checkpoint, err := coordinator.Tick(context.Background(), receipt.Handle)
	if !errors.Is(err, unknown) || checkpoint.Status != automation.Paused || checkpoint.Phase != "prepared" {
		t.Fatalf("unknown purchase checkpoint=%+v err=%v", checkpoint, err)
	}
	if _, err := coordinator.Tick(context.Background(), receipt.Handle); err != nil {
		t.Fatalf("paused unknown purchase tick: %v", err)
	}
	quotes, purchases := supplies.counts()
	if quotes != 1 || purchases != 1 {
		t.Fatalf("unknown purchase was repeated: quotes=%d purchases=%d", quotes, purchases)
	}
}

func TestSupplyRestockStopsAfterTakeover(t *testing.T) {
	coordinator, _, gate, _, supplies, receipt := coordinatorSupplyFixture(t, 10)
	supplies.purchase = func(_ context.Context, _ SupplyOrder) error {
		_, err := gate.Takeover("manual takeover")
		return err
	}
	checkpoint, err := coordinator.Tick(context.Background(), receipt.Handle)
	if !errors.Is(err, aicontrol.ErrOwner) || checkpoint.Status != automation.Paused || checkpoint.Phase != "prepared" {
		t.Fatalf("takeover checkpoint=%+v err=%v", checkpoint, err)
	}
	if _, err := coordinator.Tick(context.Background(), receipt.Handle); err != nil {
		t.Fatalf("paused takeover tick: %v", err)
	}
	_, purchases := supplies.counts()
	if purchases != 1 {
		t.Fatalf("takeover repeated purchase: %d", purchases)
	}
}

func TestSupplyQuoteRestoresParametersAfterSettingsReset(t *testing.T) {
	game := &fakeGame{snapshot: worldSnapshot()}
	navigator := &fakeNavigator{navigation: Navigation{InArea: true}}
	coordinator, _, _ := newCoordinator(t, game, navigator)
	supplies := &coordinatorSupplyTestAdapter{}
	coordinator.Supplies = supplies
	parameters := map[string]json.RawMessage{
		"supply_item":         json.RawMessage(`"meat"`),
		"supply_target_count": json.RawMessage(`5`),
	}
	receipt := startCharacter(t, coordinator, StartRequest{
		TargetKind: "character", TargetLevel: 2, MaximumSeconds: 30,
		AreaID: 7, Parameters: parameters,
	})

	coordinator.mu.Lock()
	coordinator.settings = nil
	coordinator.mu.Unlock()

	checkpoint, err := coordinator.Tick(context.Background(), receipt.Handle)
	if err != nil || checkpoint.Status != automation.Running {
		t.Fatalf("tick after settings reset checkpoint=%+v err=%v", checkpoint, err)
	}
	got := supplies.parameters()
	if string(got["supply_item"]) != `"meat"` || string(got["supply_target_count"]) != "5" {
		t.Fatalf("quote parameters=%v, want persisted supply selectors", got)
	}
}
