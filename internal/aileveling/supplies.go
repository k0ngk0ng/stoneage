package aileveling

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// SupplyOrder is a quote from a trusted adapter, never a model-provided packet.
// MaximumCost must cover the whole shortfall even if travel consumes supplies.
type SupplyOrder struct {
	Action      automation.Action
	TemplateID  int32
	TargetCount int
	Destination aigame.Point
	Gold        int64
}

type Supplies interface {
	// Quote may query state but must never move, consume or purchase.
	Quote(context.Context, aigame.Snapshot, map[string]json.RawMessage) (*SupplyOrder, error)
	// Purchase confirms the native inventory and gold result. It must not retry
	// an uncertain purchase. Navigation back to the area is the next tick's job.
	Purchase(context.Context, SupplyOrder) error
}

// SupplyPlanError carries an adapter-reviewed diagnostic safe for the task
// receipt. Transport and raw protocol errors keep the generic pause reason.
type SupplyPlanError struct{ Reason string }

func (e *SupplyPlanError) Error() string { return e.Reason }

func supplyPlanFailureReason(err error) string {
	var detail *SupplyPlanError
	if errors.As(err, &detail) {
		return "补给计划未确认：" + detail.Reason
	}
	return "补给计划未确认，任务已暂停"
}

func (c *Coordinator) restock(ctx context.Context, checkpoint *automation.Checkpoint, order SupplyOrder, unlimited bool) (automation.Checkpoint, error) {
	if order.TemplateID <= 0 || order.TargetCount < 1 || order.TargetCount > 15 || order.Action.ExpectedRevision == 0 ||
		!c.authorizeCost(order.Gold, checkpoint, order.Action.MaximumCost, unlimited) {
		return c.pauseUnavailable(ctx, checkpoint, ErrNoRecovery, "补给数量或预算未获确认，任务已暂停")
	}
	checkpoint.Phase = "prepared"
	checkpoint.ReservedSpend += order.Action.MaximumCost
	if err := c.save(ctx, checkpoint); err != nil {
		return *checkpoint, err
	}
	remaining := time.Duration(checkpoint.Plan.MaximumSeconds)*time.Second - c.now().Sub(checkpoint.StartedAt)
	if remaining > 2*time.Minute {
		remaining = 2 * time.Minute
	}
	work, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	if err := c.checkOwnership(c.generationFor(checkpoint.Plan.ID)); err != nil {
		return c.pauseUnavailable(ctx, checkpoint, err, "控制权已变化，补给已停止")
	}
	if err := c.Supplies.Purchase(work, order); err != nil {
		return c.pauseUnavailable(ctx, checkpoint, err, "补货未确认，任务已暂停；禁止自动重复购买")
	}
	after, _, err := c.observe(work)
	if err != nil {
		return c.pauseUnavailable(ctx, checkpoint, err, "补货后状态未确认，禁止自动重复购买")
	}
	if err := c.checkOwnership(c.generationFor(checkpoint.Plan.ID)); err != nil {
		return c.pauseUnavailable(ctx, checkpoint, err, "控制权已变化，补给已停止")
	}
	count := 0
	for _, item := range after.AI.Items {
		if item.TemplateID == order.TemplateID && item.Slot >= 5 && item.Slot < 20 {
			count++
		}
	}
	if c.checkIdentity(after) != nil || !after.Connected || after.Phase != aigame.PhaseWorld || after.Battle.Active ||
		!after.Player.HasStatus || after.Player.HP <= 0 || !after.AI.Received || !after.AI.ItemsKnown ||
		count != order.TargetCount || after.Position.Floor != order.Destination.Floor ||
		after.Position.X != order.Destination.X || after.Position.Y != order.Destination.Y {
		return c.pauseUnavailable(ctx, checkpoint, ErrNoRecovery, "补货后的角色、位置或背包未确认，禁止自动重复购买")
	}
	checkpoint.Phase = "ready"
	checkpoint.StepStartedAt = c.now()
	if err := c.save(ctx, checkpoint); err != nil {
		return *checkpoint, errors.Join(err, ErrUnknownDelivery)
	}
	return *checkpoint, nil
}
