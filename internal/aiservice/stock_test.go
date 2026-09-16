package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type stockSession struct {
	*npcSkillSession
	bag                                    []aigame.AIInventoryItem
	purchases                              int
	quantity                               int
	funded, uncertain, noItems, wrongActor bool
	fillReservedBeforeBuy                  bool
}

func (s *stockSession) ExecuteExpected(ctx context.Context, rev uint64, a aigame.Action) error {
	if err := s.npcSkillSession.ExecuteExpected(ctx, rev, a); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case a.Kind == aigame.ActionStatus && strings.HasPrefix(a.Command, "AI:"):
		s.snapshot.AI = aigame.AIObservation{Received: true, ItemsKnown: true, RequestID: strings.TrimPrefix(a.Command, "AI:"), Items: append([]aigame.AIInventoryItem(nil), s.bag...)}
		s.snapshot.AIObservationRevision = s.snapshot.Revision
	case a.Kind == aigame.ActionTalk:
		s.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 6, Sequence: 240, ObjectID: 42, Open: true, ButtonType: 1}
	case a.Kind == aigame.ActionWindow && a.WindowSequence == 240:
		s.snapshot.ActiveWindow = &aigame.WindowSnapshot{Type: 7, Sequence: 242, ObjectID: 42, Open: true, ButtonType: 1}
		if s.fillReservedBeforeBuy {
			for i := 0; i < 12; i++ {
				s.bag = append(s.bag, aigame.AIInventoryItem{Slot: int32(5 + i), TemplateID: 7})
			}
		}
		if s.wrongActor {
			s.snapshot.ActiveWindow.ObjectID = 999
		}
	case a.Kind == aigame.ActionWindow && a.WindowSequence == 242:
		fields := strings.Split(a.Text, "|")
		if len(fields) != 2 || fields[0] != "1" {
			return errors.New("wrong purchase data")
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil || n < 1 || n > 15 {
			return errors.New("invalid purchase quantity")
		}
		s.purchases++
		s.quantity = n
		if s.uncertain {
			return errors.New("unknown purchase outcome")
		}
		if !s.noItems {
			for i := 0; i < n; i++ {
				s.bag = append(s.bag, aigame.AIInventoryItem{Slot: int32(5 + len(s.bag)), TemplateID: 2344})
			}
		}
		if !s.funded {
			s.snapshot.Player.Gold -= int32(12 * n)
		}
		s.snapshot.AI.ItemsKnown = false
	}
	return nil
}
func stockFixture(t *testing.T) (*StockSkill, *stockSession, automation.Action) {
	npc, base, spec := npcSkillFixture(t, true)
	base.snapshot.Position.Direction = 2
	base.snapshot.Player.Gold = 50
	npc.Backend.Knowledge = &aiknowledge.Knowledge{Digest: spec.SourceFingerprint}
	session := &stockSession{npcSkillSession: base}
	npc.Backend.Session = session
	stock := &StockSkill{Backend: npc.Backend, Contracts: map[string]StockContract{"meat": {NPC: spec, TemplateID: 2344, ShopIndex: 1, UnitPrice: 12, X: int(base.snapshot.Position.X), Y: int(base.snapshot.Position.Y)}}}
	return stock, session, automation.Action{Skill: "item.stock", ExpectedRevision: 12, MaximumCost: 24, Arguments: json.RawMessage(`{"item":"meat","target_count":2}`)}
}
func TestStockPurchasesOnlyShortfallAndRequiresConfirmation(t *testing.T) {
	for _, mode := range []string{"empty", "one-existing", "enough", "funded", "uncertain", "no-items", "wrong-actor", "budget", "capacity", "cash"} {
		t.Run(mode, func(t *testing.T) {
			s, g, a := stockFixture(t)
			switch mode {
			case "one-existing", "enough":
				g.bag = []aigame.AIInventoryItem{{Slot: 5, TemplateID: 2344}}
				if mode == "enough" {
					g.bag = append(g.bag, aigame.AIInventoryItem{Slot: 6, TemplateID: 2344})
				}
			case "funded":
				g.funded = true
				g.snapshot.Player.Gold = 0
				s.Backend.Funding = func(context.Context) (bool, error) { return true, nil }
			case "uncertain":
				g.uncertain = true
			case "no-items":
				g.noItems = true
			case "wrong-actor":
				g.wrongActor = true
			case "budget":
				a.MaximumCost = 23
			case "capacity":
				for i := 0; i < 14; i++ {
					g.bag = append(g.bag, aigame.AIInventoryItem{Slot: int32(5 + i), TemplateID: 7})
				}
			case "cash":
				g.snapshot.Player.Gold = 0
			}
			err := s.Execute(context.Background(), a)
			success := mode == "empty" || mode == "one-existing" || mode == "enough" || mode == "funded"
			if (err == nil) != success {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
			expected := 0
			if success && mode != "enough" || mode == "uncertain" || mode == "no-items" {
				expected = 1
			}
			if g.purchases != expected {
				t.Fatalf("purchases=%d expected=%d err=%v", g.purchases, expected, err)
			}
			if mode == "one-existing" && g.quantity != 1 {
				t.Fatalf("bought %d instead of shortfall", g.quantity)
			}
			if success {
				a.ExpectedRevision = g.snapshot.Revision
				if err := s.Execute(context.Background(), a); err != nil {
					t.Fatal(err)
				}
				if g.purchases != expected {
					t.Fatal("repeated confirmed stock target bought again")
				}
			}
		})
	}
}
func TestStockRejectsInvalidTargetsAndOffers(t *testing.T) {
	for _, target := range []int{0, -1, 16} {
		s, _, a := stockFixture(t)
		a.Arguments = json.RawMessage(fmt.Sprintf(`{"item":"meat","target_count":%d}`, target))
		if s.ValidateSkill(context.Background(), a) == nil {
			t.Fatal("accepted target", target)
		}
	}
}

func TestStockPreservesRequestedQuestSlots(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		other, existing, reserve int
		wantOK                   bool
		quantity                 int
	}{
		{"fits-exactly", 11, 0, 2, true, 2},
		{"would-fill-reserved-slot", 12, 0, 2, false, 0},
		{"shortfall-only", 11, 1, 2, true, 1},
		{"already-stocked-but-full", 13, 2, 1, false, 0},
		{"already-stocked-with-room", 11, 2, 2, true, 0},
		{"negative", 0, 0, -1, false, 0},
		{"impossible", 0, 0, 14, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, g, action := stockFixture(t)
			for i := 0; i < tc.other+tc.existing; i++ {
				id := int32(7)
				if i >= tc.other {
					id = 2344
				}
				g.bag = append(g.bag, aigame.AIInventoryItem{Slot: int32(5 + i), TemplateID: id})
			}
			action.Arguments = json.RawMessage(fmt.Sprintf(`{"item":"meat","target_count":2,"reserve_slots":%d}`, tc.reserve))
			err := s.Execute(context.Background(), action)
			if (err == nil) != tc.wantOK || g.quantity != tc.quantity {
				t.Fatalf("err=%v quantity=%d", err, g.quantity)
			}
			if !tc.wantOK && g.purchases != 0 {
				t.Fatal("capacity rejection purchased items")
			}
		})
	}
}

func TestStockRechecksReservedSlotsBeforePurchase(t *testing.T) {
	s, game, action := stockFixture(t)
	game.fillReservedBeforeBuy = true
	action.Arguments = json.RawMessage(`{"item":"meat","target_count":2,"reserve_slots":2}`)
	if err := s.Execute(context.Background(), action); err == nil || game.purchases != 0 {
		t.Fatalf("changed inventory bought into reserved slots: err=%v purchases=%d", err, game.purchases)
	}
}
