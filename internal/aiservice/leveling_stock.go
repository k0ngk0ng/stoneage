package aiservice

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aileveling"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// LevelingStock uses the same reviewed shop and non-recursive travel recovery
// as item.stock. The request selects a catalog alias, never a URL or packet.
type LevelingStock struct {
	Stock        *StockSkill
	HealingItems map[string]HealingItemContract
}

func (s *LevelingStock) Quote(ctx context.Context, snapshot aigame.Snapshot, parameters map[string]json.RawMessage) (*aileveling.SupplyOrder, error) {
	alias, target, _, err := levelingSupplySettings(parameters)
	if err != nil || alias == "" {
		return nil, err
	}
	if s == nil || s.Stock == nil {
		return nil, ErrHealingItemUnavailable
	}
	c, ok := s.Stock.Contracts[alias]
	if !ok {
		return nil, ErrHealingItemUnavailable
	}
	raw, _ := json.Marshal(stockArguments{Item: alias, TargetCount: target, ReserveSlots: 2})
	action := automation.Action{Skill: "item.stock", Arguments: raw, MaximumCost: int64(target) * c.UnitPrice}
	if err := s.Stock.ValidateSkill(ctx, action); err != nil {
		return nil, err
	}
	if !snapshot.AI.Received || !snapshot.AI.ItemsKnown {
		var err error
		snapshot, err = refreshInventoryIdentity(ctx, NewNPCSkill(s.Stock.Backend, nil))
		if err != nil {
			return nil, err
		}
	}
	return s.Preview(ctx, snapshot, parameters)
}

// Preview quotes only already observed state; it never refreshes inventory,
// moves, opens a shop, or purchases. Web preflight must stay read-only.
func (s *LevelingStock) Preview(ctx context.Context, snapshot aigame.Snapshot, parameters map[string]json.RawMessage) (*aileveling.SupplyOrder, error) {
	alias, target, reorder, err := levelingSupplySettings(parameters)
	if err != nil || alias == "" {
		return nil, err
	}
	if s == nil || s.Stock == nil {
		return nil, ErrHealingItemUnavailable
	}
	c, ok := s.Stock.Contracts[alias]
	if !ok {
		return nil, ErrHealingItemUnavailable
	}
	raw, _ := json.Marshal(stockArguments{Item: alias, TargetCount: target, ReserveSlots: 2})
	action := automation.Action{Skill: "item.stock", Arguments: raw, MaximumCost: int64(target) * c.UnitPrice}
	if err := s.Stock.ValidateSkill(ctx, action); err != nil {
		return nil, err
	}
	if !snapshot.AI.Received || !snapshot.AI.ItemsKnown {
		return nil, errors.New("stock requires known inventory")
	}
	if !petRecoveryWorld(snapshot) {
		return nil, errors.New("stock quote requires living world state")
	}
	if s.availableRecoveryItems(snapshot, c.TemplateID) > reorder {
		return nil, nil
	}
	if _, _, err := stockQuote(snapshot, c, target, 2, action.MaximumCost); err != nil {
		return nil, &aileveling.SupplyPlanError{Reason: err.Error()}
	}
	action.ExpectedRevision = snapshot.Revision
	return &aileveling.SupplyOrder{Action: action, TemplateID: c.TemplateID, TargetCount: target,
		Destination: aigame.Point{Floor: int32(c.NPC.Floor), X: int32(c.X), Y: int32(c.Y)}, Gold: int64(snapshot.Player.Gold)}, nil
}

func (s *LevelingStock) availableRecoveryItems(snapshot aigame.Snapshot, purchasedTemplate int32) int {
	templates := map[int32]bool{purchasedTemplate: true}
	for _, item := range s.HealingItems {
		if item.Verified && item.TemplateID > 0 && item.BaseHP > 0 && item.SourceFingerprint == s.Stock.Backend.Knowledge.Fingerprint() {
			templates[item.TemplateID] = true
		}
	}
	slots := map[int32]bool{}
	for _, item := range snapshot.AI.Items {
		if item.Slot >= 5 && item.Slot < 20 && templates[item.TemplateID] {
			slots[item.Slot] = true
		}
	}
	return len(slots)
}

func (s *LevelingStock) Purchase(ctx context.Context, order aileveling.SupplyOrder) error {
	return s.Stock.Execute(ctx, order.Action)
}

func levelingSupplySettings(parameters map[string]json.RawMessage) (string, int, int, error) {
	var alias string
	if raw, ok := parameters["supply_item"]; ok {
		if err := json.Unmarshal(raw, &alias); err != nil || alias == "" {
			return "", 0, 0, errors.New("supply_item must be a reviewed stock alias")
		}
	} else {
		if parameters["supply_target_count"] != nil || parameters["supply_reorder_count"] != nil {
			return "", 0, 0, errors.New("supply thresholds require supply_item")
		}
		return "", 0, 0, nil
	}
	target, reorder := 10, 3
	for key, value := range map[string]*int{"supply_target_count": &target, "supply_reorder_count": &reorder} {
		if raw, ok := parameters[key]; ok {
			if err := json.Unmarshal(raw, value); err != nil {
				return "", 0, 0, err
			}
		}
	}
	if target < 2 || target > 13 || reorder < 1 || reorder >= target {
		return "", 0, 0, errors.New("invalid leveling supply thresholds")
	}
	return alias, target, reorder, nil
}
