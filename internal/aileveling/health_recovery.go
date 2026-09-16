package aileveling

import (
	"context"
	"errors"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func (c *Coordinator) recoverWorldHealth(ctx context.Context, checkpoint *automation.Checkpoint, before aigame.Snapshot) (automation.Checkpoint, error) {
	if c.HealthRecovery == nil {
		return c.pauseUnavailable(ctx, checkpoint, ErrNoRecovery, "生命值低于一半，缺少已验证的补给恢复，任务已暂停")
	}
	return c.runWorldHealthRecovery(ctx, checkpoint, before, c.HealthRecovery.Heal, func(after aigame.Snapshot) bool {
		return after.Player.HP > before.Player.HP
	})
}

func (c *Coordinator) recoverWorldPetHealth(ctx context.Context, checkpoint *automation.Checkpoint, before aigame.Snapshot, pet aigame.PetSnapshot) (automation.Checkpoint, error) {
	healer, ok := c.HealthRecovery.(PetHealthRecovery)
	if !ok || pet.Slot < 0 || pet.Slot >= 5 || pet.HP <= 0 || pet.MaxHP <= 0 || pet.Graphic <= 0 || pet.Name == "" {
		return c.pauseUnavailable(ctx, checkpoint, ErrNoRecovery, "出战宠物需要治疗，缺少已验证的恢复能力或宠物状态，任务已暂停")
	}
	return c.runWorldHealthRecovery(ctx, checkpoint, before, func(work context.Context) error {
		return healer.HealPet(work, pet)
	}, func(after aigame.Snapshot) bool {
		if !after.Player.BattlePetSlotKnown || after.Player.BattlePetSlot != pet.Slot {
			return false
		}
		return recoveredPetMatches(after, pet)
	})
}

func (c *Coordinator) recoverWorldRidePetHealth(ctx context.Context, checkpoint *automation.Checkpoint, before aigame.Snapshot, pet aigame.PetSnapshot) (automation.Checkpoint, error) {
	healer, ok := c.HealthRecovery.(PetHealthRecovery)
	if !ok || !before.Player.RidePetKnown || before.Player.RidePet < 0 || before.Player.RidePet >= 5 ||
		pet.Slot != before.Player.RidePet || pet.HP <= 0 || pet.MaxHP <= 0 || pet.Graphic <= 0 || pet.Name == "" {
		return c.pauseUnavailable(ctx, checkpoint, ErrNoRecovery, "骑宠需要治疗，缺少已验证的恢复能力或宠物状态，任务已暂停")
	}
	return c.runWorldHealthRecovery(ctx, checkpoint, before, func(work context.Context) error {
		return healer.HealPet(work, pet)
	}, func(after aigame.Snapshot) bool {
		if !after.Player.RidePetKnown || after.Player.RidePet != pet.Slot {
			return false
		}
		return recoveredPetMatches(after, pet)
	})
}

func recoveredPetMatches(after aigame.Snapshot, expected aigame.PetSnapshot) bool {
	for _, current := range after.Pets {
		if current.Slot != expected.Slot {
			continue
		}
		return current.IdentityKnown && current.StableID != "" && (!expected.IdentityKnown || current.StableID == expected.StableID) &&
			current.Name == expected.Name && current.FreeName == expected.FreeName && current.Graphic == expected.Graphic && current.Level == expected.Level &&
			current.MaxHP == expected.MaxHP && current.HP > expected.HP && current.HP <= current.MaxHP
	}
	return false
}

func samePetRoles(before, after aigame.Snapshot) bool {
	if before.Player.BattlePetSlotKnown != after.Player.BattlePetSlotKnown || before.Player.RidePetKnown != after.Player.RidePetKnown {
		return false
	}
	if before.Player.BattlePetSlotKnown && before.Player.BattlePetSlot != after.Player.BattlePetSlot {
		return false
	}
	if before.Player.RidePetKnown && before.Player.RidePet != after.Player.RidePet {
		return false
	}
	return true
}

func (c *Coordinator) runWorldHealthRecovery(ctx context.Context, checkpoint *automation.Checkpoint, before aigame.Snapshot, heal func(context.Context) error, confirmed func(aigame.Snapshot) bool) (automation.Checkpoint, error) {
	// Persist before invoking an adapter that may consume an item. A crash or
	// error after that point must never automatically repeat the whole call.
	checkpoint.Phase = "prepared"
	if err := c.save(ctx, checkpoint); err != nil {
		return *checkpoint, err
	}
	remaining := time.Duration(checkpoint.Plan.MaximumSeconds)*time.Second - c.now().Sub(checkpoint.StartedAt)
	if remaining > 10*time.Second {
		remaining = 10 * time.Second
	}
	work, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	if err := c.checkOwnership(c.generationFor(checkpoint.Plan.ID)); err != nil {
		return c.pauseUnavailable(ctx, checkpoint, err, "控制权已变化，恢复已停止")
	}
	if err := heal(work); err != nil {
		return c.pauseUnavailable(ctx, checkpoint, err, "补给恢复未确认，任务已暂停；禁止自动重试")
	}
	after, _, err := c.observe(work)
	if err != nil {
		return c.pauseUnavailable(ctx, checkpoint, err, "恢复后状态未确认，任务已暂停；禁止自动重试")
	}
	if err := c.checkOwnership(c.generationFor(checkpoint.Plan.ID)); err != nil {
		return c.pauseUnavailable(ctx, checkpoint, err, "控制权已变化，恢复已停止")
	}
	if c.checkIdentity(after) != nil || !after.Connected || after.Phase != aigame.PhaseWorld || after.Battle.Active || !samePetRoles(before, after) ||
		!after.Player.HasStatus || after.Player.HP <= 0 || after.Player.HP > after.Player.MaxHP ||
		after.Player.MaxHP != before.Player.MaxHP || after.Player.Level != before.Player.Level ||
		after.Position.Floor != before.Position.Floor || after.Position.X != before.Position.X || after.Position.Y != before.Position.Y || !confirmed(after) {
		return c.pauseUnavailable(ctx, checkpoint, ErrNoRecovery, "恢复后的角色、位置或生命变化未确认，任务已暂停；禁止自动重试")
	}
	checkpoint.Phase = "ready"
	checkpoint.StepStartedAt = c.now()
	if err := c.save(ctx, checkpoint); err != nil {
		return *checkpoint, errors.Join(err, ErrUnknownDelivery)
	}
	// Reobserve on the next tick before either another single-item recovery or
	// movement. A healing call never authorizes a W/EV from the old snapshot.
	return *checkpoint, nil
}
