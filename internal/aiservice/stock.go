package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// StockContract is a reviewed native item-shop offer, not model input.
// ShopIndex is one-based; the interaction point must be reachable and inside
// the NPC's native talk range. This only covers single-slot, non-stacking items.
type StockContract struct {
	Name       string // Optional operator-reviewed display label; never a protocol selector.
	NPC        NPCSpec
	TemplateID int32
	ShopIndex  int
	UnitPrice  int64
	X, Y       int
}
type StockSkill struct {
	Backend   *GameBackend
	Movement  *MovementSkill
	Contracts map[string]StockContract
}
type stockArguments struct {
	Item         string `json:"item"`
	TargetCount  int    `json:"target_count"`
	ReserveSlots int    `json:"reserve_slots,omitempty"`
}

func (c StockContract) registry(count int) (NPCRegistry, error) {
	if c.TemplateID <= 0 || c.ShopIndex < 1 || c.ShopIndex > 120 || c.UnitPrice <= 0 || c.UnitPrice > 2147483647/15 || count < 1 || count > 15 {
		return nil, errors.New("invalid reviewed stock offer")
	}
	radius := c.NPC.TalkRange
	if radius == 0 {
		radius = 1
	}
	if c.X < 0 || c.Y < 0 || abs(c.X-c.NPC.X) > radius || abs(c.Y-c.NPC.Y) > radius || (c.X == c.NPC.X && c.Y == c.NPC.Y) {
		return nil, errors.New("invalid stock interaction point")
	}
	n := c.NPC
	n.WindowType, n.WindowSequence, n.WindowObjectID = 0, 0, 0
	n.Choices = nil
	n.WindowObjectFromActor = false
	n.Windows = []NPCWindowSpec{
		{Type: 6, Sequence: 240, WindowObjectFromActor: true, Choices: map[int]NPCChoice{1: {Button: 1, Data: "1"}}},
		{Type: 7, Sequence: 242, WindowObjectFromActor: true, Choices: map[int]NPCChoice{1: {Button: 1, Data: fmt.Sprintf("%d|%d", c.ShopIndex, count), MaximumCost: c.UnitPrice * int64(count)}}},
	}
	return NewNPCRegistry([]NPCSpec{n})
}
func (s *StockSkill) contract(a automation.Action) (StockContract, stockArguments, error) {
	var args stockArguments
	if s == nil || s.Backend == nil || s.Backend.Session == nil || s.Backend.Gate == nil || s.Backend.Knowledge == nil {
		return StockContract{}, args, aimcp.ErrBackend
	}
	if a.Skill != "item.stock" || a.MaximumCost < 0 {
		return StockContract{}, args, aimcp.ErrInvalidParams
	}
	if err := decodeArguments(a.Arguments, &args); err != nil {
		return StockContract{}, args, err
	}
	c, ok := s.Contracts[args.Item]
	if !ok || args.TargetCount < 1 || args.TargetCount > 15 || args.ReserveSlots < 0 || args.ReserveSlots > 15-args.TargetCount {
		return c, args, aimcp.ErrInvalidParams
	}
	if c.NPC.SourceFingerprint != s.Backend.Knowledge.Fingerprint() {
		return c, args, ErrNPCUnverified
	}
	_, err := c.registry(1)
	return c, args, err
}
func (s *StockSkill) ValidateSkill(ctx context.Context, a automation.Action) error {
	_, _, err := s.contract(a)
	if err != nil {
		return err
	}
	return ctx.Err()
}

func freeStockSlots(snapshot aigame.Snapshot) int {
	used := map[int32]bool{}
	for _, item := range snapshot.AI.Items {
		if item.Slot >= 5 && item.Slot < 20 {
			used[item.Slot] = true
		}
	}
	return 15 - len(used)
}
func stockQuote(snapshot aigame.Snapshot, c StockContract, target, reserve int, maximum int64) (int, int64, error) {
	if !snapshot.AI.Received || !snapshot.AI.ItemsKnown {
		return 0, 0, errors.New("stock requires known inventory")
	}
	need := target - healingItemCount(snapshot, c.TemplateID)
	additionalSlots := need
	if additionalSlots < 0 {
		additionalSlots = 0
	}
	if additionalSlots+reserve > freeStockSlots(snapshot) {
		return 0, 0, fmt.Errorf("insufficient backpack capacity: free=%d purchase=%d reserve=%d", freeStockSlots(snapshot), additionalSlots, reserve)
	}
	if need <= 0 {
		return 0, 0, nil
	}
	cost := int64(need) * c.UnitPrice
	if cost > maximum {
		return 0, 0, errors.New("stock quote exceeds maximum cost")
	}
	return need, cost, nil
}

// Execute reaches the reviewed shop and purchases only the current shortfall.
// It never retries a mutation. The durable automation engine owns uncertain
// outcomes; an unconfirmed WN must not be replayed as another purchase.
func (s *StockSkill) Execute(ctx context.Context, a automation.Action) error {
	c, args, err := s.contract(a)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	npc := NewNPCSkill(s.Backend, nil)
	initial, err := npc.observe(ctx)
	if err != nil {
		return err
	}
	if a.ExpectedRevision == 0 || initial.Revision != a.ExpectedRevision {
		return aigame.ErrStaleRevision
	}
	current, err := refreshInventoryIdentity(ctx, npc)
	if err != nil {
		return err
	}
	need, initialCost, err := stockQuote(current, c, args.TargetCount, args.ReserveSlots, a.MaximumCost)
	if err != nil || need == 0 {
		return err
	}
	fundedBeforeTravel := false
	if s.Backend.Funding != nil {
		fundedBeforeTravel, err = s.Backend.Funding(ctx)
		if err != nil {
			return err
		}
	}
	if !current.Player.HasStatus || current.Player.Gold < 0 || (!fundedBeforeTravel && int64(current.Player.Gold) < initialCost) {
		return errors.New("insufficient confirmed funds for stock")
	}
	if current.Position.Floor != int32(c.NPC.Floor) || current.Position.X != int32(c.X) || current.Position.Y != int32(c.Y) {
		if s.Movement == nil {
			return ErrNPCOutOfRange
		}
		raw, _ := json.Marshal(movementArguments{Floor: c.NPC.Floor, X: c.X, Y: c.Y})
		if err := s.Movement.Execute(ctx, automation.Action{Skill: "move", ExpectedRevision: current.Revision, Arguments: raw}); err != nil {
			return err
		}
	}
	current, err = refreshInventoryIdentity(ctx, npc)
	if err != nil {
		return err
	}
	need, cost, err := stockQuote(current, c, args.TargetCount, args.ReserveSlots, a.MaximumCost)
	if err != nil || need == 0 {
		return err
	}
	registry, err := c.registry(need)
	if err != nil {
		return err
	}
	npc.Registry = registry
	spec, _ := registry.Lookup(c.NPC.Alias)
	beforeCount := healingItemCount(current, c.TemplateID)
	unchanged := func(now aigame.Snapshot) error {
		if now.Position.Floor != current.Position.Floor || now.Position.X != current.Position.X || now.Position.Y != current.Position.Y || now.Battle.Active {
			return errors.New("stock position or battle state changed")
		}
		_, _, err := validateTalkSnapshot(spec, npcTalkArguments{NPC: spec.Alias}, now)
		return err
	}
	if err := unchanged(current); err != nil {
		return err
	}
	talk, _ := json.Marshal(map[string]string{"npc": spec.Alias})
	if err := npc.Execute(ctx, automation.Action{Skill: "npc.talk", ExpectedRevision: current.Revision, Arguments: talk}); err != nil {
		return err
	}
	var beforeGold int32
	var funded bool
	for _, sequence := range []int32{240, 242} {
		snapshot, err := waitHealerState(ctx, npc, func(now aigame.Snapshot) (bool, error) {
			if err := unchanged(now); err != nil {
				return false, err
			}
			return now.ActiveWindow != nil && now.ActiveWindow.Open && now.ActiveWindow.Sequence == sequence, nil
		})
		if err != nil {
			return err
		}
		if sequence == 242 {
			snapshot, err = refreshInventoryIdentity(ctx, npc)
			if err != nil {
				return err
			}
		}
		windowArgs := npcWindowArguments{NPC: spec.Alias, WindowSequence: json.RawMessage(fmt.Sprint(sequence)), Choice: json.RawMessage("1")}
		if err := npc.submit(ctx, snapshot.Revision, func(now aigame.Snapshot) (aigame.Action, error) {
			if err := unchanged(now); err != nil {
				return aigame.Action{}, err
			}
			if err := validateWindowSnapshot(spec, windowArgs, now); err != nil {
				return aigame.Action{}, err
			}
			data := "1"
			if sequence == 242 {
				missing, quote, err := stockQuote(now, c, args.TargetCount, args.ReserveSlots, a.MaximumCost)
				if err != nil {
					return aigame.Action{}, err
				}
				if missing != need || quote != cost || healingItemCount(now, c.TemplateID) != beforeCount {
					return aigame.Action{}, errors.New("stock changed before purchase")
				}
				if s.Backend.Funding != nil {
					funded, err = s.Backend.Funding(ctx)
					if err != nil {
						return aigame.Action{}, err
					}
				}
				if !now.Player.HasStatus || now.Player.Gold < 0 || (!funded && int64(now.Player.Gold) < cost) {
					return aigame.Action{}, errors.New("insufficient confirmed funds for stock")
				}
				beforeGold = now.Player.Gold
				data = fmt.Sprintf("%d|%d", c.ShopIndex, need)
			}
			return aigame.Window(now.Position.X, now.Position.Y, sequence, now.ActiveWindow.ObjectID, 1, data), nil
		}); err != nil {
			return err
		}
	}
	final, err := refreshInventoryIdentity(ctx, npc)
	if err != nil {
		return err
	}
	if err := unchanged(final); err != nil {
		return err
	}
	expectedGold := int64(beforeGold)
	if !funded {
		expectedGold -= cost
	}
	if healingItemCount(final, c.TemplateID) != args.TargetCount || !final.Player.HasStatus || int64(final.Player.Gold) != expectedGold {
		return errors.New("purchase consumption and inventory were not confirmed")
	}
	return nil
}
