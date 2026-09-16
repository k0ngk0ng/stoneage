package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aileveling"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

// GameTasks joins the quest executor and battle-aware leveling coordinator.
// Both must use the same automation store, whose character index enforces
// exclusivity even across service restarts.
type GameTasks struct {
	Quests      *Tasks
	Leveling    *aileveling.Coordinator
	Lease       context.Context
	Wake        chan<- struct{}
	mu          sync.Mutex
	levelHandle string
	levelDone   chan struct{}
	closed      bool
}

func (t *GameTasks) StartTask(ctx context.Context, q aimcp.TaskRequest) (aimcp.TaskReceipt, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.Quests == nil {
		return aimcp.TaskReceipt{}, aimcp.ErrBackend
	}
	return t.Quests.StartTask(ctx, q)
}
func (t *GameTasks) StartLeveling(ctx context.Context, q aimcp.LevelingRequest) (aimcp.TaskReceipt, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.Leveling == nil || t.Lease == nil || t.Lease.Err() != nil {
		return aimcp.TaskReceipt{}, aimcp.ErrBackend
	}
	if t.levelHandle != "" {
		return aimcp.TaskReceipt{}, errors.New("leveling already active")
	}
	r := aileveling.StartRequest{TargetKind: q.TargetKind, TargetID: q.TargetID, TargetLevel: q.TargetLevel, TargetPolicy: q.TargetPolicy, MaximumSeconds: q.MaximumSeconds, MaximumDeaths: q.MaximumDeaths}
	for key, raw := range q.Parameters {
		var err error
		switch key {
		case "reserve_gold":
			err = json.Unmarshal(raw, &r.ReserveGold)
		case "maximum_spend":
			err = json.Unmarshal(raw, &r.MaximumSpend)
		case "area_id":
			err = json.Unmarshal(raw, &r.AreaID)
		case "targets":
			err = json.Unmarshal(raw, &r.Targets)
		case "supply_item", "supply_target_count", "supply_reorder_count":
			if r.Parameters == nil {
				r.Parameters = make(map[string]json.RawMessage)
			}
			r.Parameters[key] = append(json.RawMessage(nil), raw...)
		default:
			return aimcp.TaskReceipt{}, aimcp.ErrInvalidParams
		}
		if err != nil || string(raw) == "null" {
			return aimcp.TaskReceipt{}, aimcp.ErrInvalidParams
		}
	}
	if alias, _, _, err := levelingSupplySettings(r.Parameters); err != nil || (alias != "" && t.Leveling.Supplies == nil) {
		return aimcp.TaskReceipt{}, aimcp.ErrInvalidParams
	}
	receipt, err := t.Leveling.Start(ctx, r)
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if receipt.Status != aimcp.ReceiptRunning {
		return receipt, nil
	}
	t.levelHandle, t.levelDone = receipt.Handle, make(chan struct{})
	done := t.levelDone
	go func() {
		err := t.Leveling.Run(t.Lease, receipt.Handle)
		if err != nil {
			persist, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			// Tick normally persists the precise failure and its pending-action
			// phase before returning an error. Do not replace that evidence with
			// a generic cancellation, which also discards the submitted phase.
			status, statusErr := t.Leveling.Status(persist, receipt.Handle)
			if statusErr != nil || status.Status == aimcp.ReceiptRunning {
				_, _ = t.Leveling.Cancel(persist, receipt.Handle, "练级执行中断，需要核验游戏状态")
			}
			cancel()
		}
		t.mu.Lock()
		t.levelHandle = ""
		close(done)
		t.mu.Unlock()
		if t.Wake != nil {
			select {
			case t.Wake <- struct{}{}:
			default:
			}
		}
	}()
	return receipt, nil
}
func (t *GameTasks) Status(ctx context.Context, id string) (aimcp.TaskReceipt, error) {
	if strings.HasPrefix(id, "level-") {
		if t.Leveling == nil {
			return aimcp.TaskReceipt{}, aimcp.ErrBackend
		}
		return t.Leveling.Status(ctx, id)
	}
	if t.Quests == nil {
		return aimcp.TaskReceipt{}, aimcp.ErrBackend
	}
	return t.Quests.Status(ctx, id)
}
func (t *GameTasks) Cancel(ctx context.Context, q aimcp.CancelRequest) (aimcp.TaskReceipt, error) {
	if strings.HasPrefix(q.Handle, "level-") {
		if t.Leveling == nil {
			return aimcp.TaskReceipt{}, aimcp.ErrBackend
		}
		return t.Leveling.Cancel(ctx, q.Handle, "任务已取消")
	}
	if t.Quests == nil {
		return aimcp.TaskReceipt{}, aimcp.ErrBackend
	}
	return t.Quests.Cancel(ctx, q)
}
func (t *GameTasks) Active(ctx context.Context) ([]aimcp.TaskReceipt, error) {
	var result []aimcp.TaskReceipt
	if t.Quests != nil {
		var err error
		result, err = t.Quests.Active(ctx)
		if err != nil {
			return nil, err
		}
	}
	t.mu.Lock()
	handle := t.levelHandle
	t.mu.Unlock()
	if handle != "" {
		r, err := t.Status(ctx, handle)
		if err != nil {
			return nil, err
		}
		if r.Status == aimcp.ReceiptRunning || r.Status == aimcp.ReceiptPending {
			result = append(result, r)
		}
	}
	return result, nil
}
func (t *GameTasks) Close() {
	t.mu.Lock()
	t.closed = true
	handle, done := t.levelHandle, t.levelDone
	t.mu.Unlock()
	if t.Quests != nil {
		t.Quests.Close()
	}
	if handle != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		_, _ = t.Leveling.Cancel(ctx, handle, "AI session closed")
		cancel()
	}
	if done != nil {
		<-done
	}
}

var _ TaskController = (*GameTasks)(nil)
