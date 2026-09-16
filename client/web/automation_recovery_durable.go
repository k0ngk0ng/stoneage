package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type durableAutomationRecovery struct {
	Owner      string             `json:"owner"`
	Version    int                `json:"version"`
	Identity   automationIdentity `json:"identity"`
	Config     AutomationConfig   `json:"config"`
	ActivityAt time.Time          `json:"activity_at"`
}

// Stamp before the first checkpoint write, not after Start launches its runner.
// A crash at any later point retains the original player/config binding.
type webRecoveryPlanStore struct {
	automation.Store
	identity automationIdentity
	config   AutomationConfig
}

func (executor *AutomationExecutor) recoveryPlanStore(identity automationIdentity, config AutomationConfig) automation.Store {
	if !identity.valid() || !identity.canonicalCharacterID() || !aigame.ValidPersistentCharacterID(identity.PersistentCharacterID) {
		return executor.config.Plans // Older servers retain process-local recovery.
	}
	return &webRecoveryPlanStore{Store: executor.config.Plans, identity: identity.normalized(), config: cloneAutomationConfig(config)}
}

func (store *webRecoveryPlanStore) stamp(checkpoint *automation.Checkpoint) error {
	if checkpoint.Plan.CharacterID != store.identity.CharacterID || (checkpoint.Plan.Mode != string(aicontrol.Quest) && checkpoint.Plan.Mode != string(aicontrol.Leveling)) {
		return errAutomationRecoveryIdentity
	}
	data, err := json.Marshal(durableAutomationRecovery{Owner: "web-player", Version: 1, Identity: store.identity, Config: store.config, ActivityAt: checkpoint.UpdatedAt})
	if err != nil {
		return err
	}
	checkpoint.OwnerContext = data
	return nil
}

func (store *webRecoveryPlanStore) Create(ctx context.Context, checkpoint automation.Checkpoint) error {
	if err := store.stamp(&checkpoint); err != nil {
		return err
	}
	return store.Store.Create(ctx, checkpoint)
}

func (store *webRecoveryPlanStore) Save(ctx context.Context, checkpoint automation.Checkpoint, expected uint64) error {
	if err := store.stamp(&checkpoint); err != nil {
		return err
	}
	return store.Store.Save(ctx, checkpoint, expected)
}

// restoreDurableRecoveries runs only during Web startup, while the database
// exclusive recovery process lock is held and before sessions can start work.
// No backend is built and no game packet is sent here.
func (executor *AutomationExecutor) restoreDurableRecoveries(ctx context.Context, checkpoints []automation.Checkpoint) error {
	now := time.Now().UTC()
	seen := map[automationIdentity]bool{}
	for _, checkpoint := range checkpoints {
		if len(checkpoint.OwnerContext) == 0 {
			continue
		}
		var metadata durableAutomationRecovery
		if err := json.Unmarshal(checkpoint.OwnerContext, &metadata); err != nil {
			return fmt.Errorf("decode automation recovery: %w", err)
		}
		if metadata.Owner != "web-player" {
			continue
		}
		identity := metadata.Identity.normalized()
		mode := aicontrol.Mode(checkpoint.Plan.Mode)
		if metadata.Version != 1 || !identity.valid() || !identity.canonicalCharacterID() || !aigame.ValidPersistentCharacterID(identity.PersistentCharacterID) || identity.CharacterID != checkpoint.Plan.CharacterID || (mode != aicontrol.Quest && mode != aicontrol.Leveling) || metadata.ActivityAt.IsZero() || metadata.ActivityAt.After(now) {
			return fmt.Errorf("invalid durable Web recovery for checkpoint %q", checkpoint.Plan.ID)
		}
		newest := !seen[identity]
		seen[identity] = true
		if checkpoint.Status == automation.Completed || checkpoint.Phase == "cancelled" {
			continue
		}
		if checkpoint.Status == automation.Running {
			var err error
			checkpoint, err = pauseCheckpointPreservingPhase(ctx, executor.config.Plans, checkpoint.Plan.ID, "服务已重启，请确认是否恢复任务")
			if err != nil {
				return fmt.Errorf("pause interrupted Web automation: %w", err)
			}
		}
		// ActivityAt belongs to the last real run write. Restarting repeatedly
		// must not extend the 24-hour recovery window via UpdatedAt changes.
		if !newest || now.Sub(metadata.ActivityAt) > automationRecoveryMaxAge {
			continue
		}
		executor.registerRecovery(&automationRecoveryRecord{identity: identity, handle: checkpoint.Plan.ID, mode: mode, reason: checkpoint.Reason, config: metadata.Config, detachedAt: metadata.ActivityAt})
	}
	return nil
}
