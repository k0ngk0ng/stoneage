package aiservice

import (
	"context"
	"errors"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

var ErrHealingItemUnavailable = errors.New("reviewed HP recovery item is not in the confirmed backpack")

// HealingItemContract covers a source-reviewed, single-use item that restores
// HP to its user in the world. BaseHP describes the reviewed native argument;
// the server may randomize and scale it. Completion uses actual HP increase
// and one consumed item, never a predicted recovery amount.
type HealingItemContract struct {
	TemplateID        int32
	BaseHP            int32
	ItemsetSHA256     string
	SourceFingerprint string
	Verified          bool
}

type ItemHealingSkill struct {
	Backend   *GameBackend
	Contracts map[string]HealingItemContract
}

func (s *ItemHealingSkill) contract(a automation.Action) (HealingItemContract, error) {
	if s == nil || s.Backend == nil || s.Backend.Session == nil || s.Backend.Gate == nil || s.Backend.Knowledge == nil {
		return HealingItemContract{}, aimcp.ErrBackend
	}
	if a.Skill != "item.heal" || a.MaximumCost != 0 {
		return HealingItemContract{}, aimcp.ErrInvalidParams
	}
	var args struct {
		Item string `json:"item"`
	}
	if err := decodeArguments(a.Arguments, &args); err != nil {
		return HealingItemContract{}, err
	}
	c, ok := s.Contracts[args.Item]
	if !ok || !c.Verified || c.TemplateID <= 0 || c.BaseHP <= 0 || c.SourceFingerprint == "" || c.SourceFingerprint != s.Backend.Knowledge.Fingerprint() {
		return HealingItemContract{}, ErrHealingItemUnavailable
	}
	return c, nil
}

func (s *ItemHealingSkill) ValidateSkill(ctx context.Context, a automation.Action) error {
	_, err := s.contract(a)
	if err != nil {
		return err
	}
	return ctx.Err()
}

func healingItemCount(s aigame.Snapshot, id int32) int {
	count := 0
	for _, item := range s.AI.Items {
		if item.TemplateID == id {
			count++
		}
	}
	return count
}

func (s *ItemHealingSkill) Execute(ctx context.Context, a automation.Action) error {
	c, err := s.contract(a)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	npc := NewNPCSkill(s.Backend, nil)
	before, err := npc.observe(ctx)
	if err != nil {
		return err
	}
	if a.ExpectedRevision == 0 || before.Revision != a.ExpectedRevision {
		return aigame.ErrStaleRevision
	}
	if !before.Connected || before.Phase != aigame.PhaseWorld || before.Battle.Active || !before.Player.HasStatus || before.Player.HP <= 0 || before.Player.MaxHP <= 0 || before.Player.HP > before.Player.MaxHP {
		return errors.New("item healing requires known living world status")
	}
	if before.Player.HP == before.Player.MaxHP {
		return nil
	}
	unchanged := func(now aigame.Snapshot) error {
		if now.Position.Floor != before.Position.Floor || now.Position.X != before.Position.X || now.Position.Y != before.Position.Y || now.Player.MaxHP != before.Player.MaxHP || now.Player.Level != before.Player.Level || !now.Player.HasStatus || now.Battle.Active {
			return errors.New("item healing state changed")
		}
		return nil
	}
	confirmed, err := refreshInventoryIdentity(ctx, npc)
	if err != nil {
		return err
	}
	if err := unchanged(confirmed); err != nil {
		return err
	}
	if confirmed.Player.HP != before.Player.HP {
		return aigame.ErrStaleRevision
	}
	slot := int32(-1)
	for _, item := range confirmed.AI.Items {
		if item.TemplateID == c.TemplateID && item.Slot >= 5 && item.Slot < 20 && (slot < 0 || item.Slot < slot) {
			slot = item.Slot
		}
	}
	if slot < 0 {
		return ErrHealingItemUnavailable
	}
	count := healingItemCount(confirmed, c.TemplateID)
	err = npc.submit(ctx, confirmed.Revision, func(now aigame.Snapshot) (aigame.Action, error) {
		if err := unchanged(now); err != nil {
			return aigame.Action{}, err
		}
		if !now.AI.Received || !now.AI.ItemsKnown || now.Player.HP != before.Player.HP || healingItemCount(now, c.TemplateID) != count {
			return aigame.Action{}, ErrHealingItemUnavailable
		}
		found := false
		for _, item := range now.AI.Items {
			if item.Slot == slot && item.TemplateID == c.TemplateID {
				found = true
			}
		}
		if !found {
			return aigame.Action{}, ErrHealingItemUnavailable
		}
		// Callfromcli_Util_getTargetCharaindex maps native target 0 to self.
		return aigame.UseItem(now.Position.X, now.Position.Y, slot, 0), nil
	})
	if err != nil {
		return err
	}
	if _, err := refreshInventoryIdentity(ctx, npc); err != nil {
		return err
	}
	_, err = waitHealerState(ctx, npc, func(now aigame.Snapshot) (bool, error) {
		if err := unchanged(now); err != nil {
			return false, err
		}
		return now.AI.Received && now.AI.ItemsKnown && now.AIObservationRevision > confirmed.Revision && healingItemCount(now, c.TemplateID) == count-1 && now.Player.HP > before.Player.HP && now.Player.HP <= before.Player.MaxHP, nil
	})
	return err
}
