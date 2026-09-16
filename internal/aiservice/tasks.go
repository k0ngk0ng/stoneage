package aiservice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// PlanBuilder resolves user selectors against server-owned, verified data.
// It never accepts a plan or a protocol action from an MCP client.
type PlanBuilder interface {
	Task(context.Context, aimcp.TaskRequest) (automation.Plan, error)
	Leveling(context.Context, aimcp.LevelingRequest) (automation.Plan, error)
}

// Tasks serializes one character's deterministic executor. The lease context
// belongs to the control gate, so returning an HTTP response does not stop a
// task, whereas manual takeover does.
type Tasks struct {
	mu          sync.Mutex
	Engine      *automation.Engine
	Builder     PlanBuilder
	CharacterID string
	Lease       context.Context
	Wake        chan<- struct{}
	active      string
	cancel      context.CancelFunc
	done        chan struct{}
	closed      bool
}

func (t *Tasks) start(ctx context.Context, build func() (automation.Plan, error)) (aimcp.TaskReceipt, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.Lease == nil || t.Lease.Err() != nil || t.Engine == nil || t.Builder == nil || t.CharacterID == "" {
		return aimcp.TaskReceipt{}, aimcp.ErrBackend
	}
	if t.active != "" {
		return aimcp.TaskReceipt{}, errors.New("character already has a deterministic task")
	}
	plan, err := build()
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if plan.CharacterID != t.CharacterID {
		return aimcp.TaskReceipt{}, aimcp.ErrInvalidBinding
	}
	// Definition IDs are shared by characters and repeated runs. Each
	// execution needs a new durable handle, independent of the task selector.
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	plan.ID = "task-" + hex.EncodeToString(nonce[:])
	checkpoint, err := t.Engine.Start(ctx, plan)
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if checkpoint.Status == automation.Completed {
		return taskReceipt(checkpoint), nil
	}
	t.launchLocked(plan.ID)
	return taskReceipt(checkpoint), nil
}

func (t *Tasks) launchLocked(id string) {
	runCtx, cancel := context.WithCancel(t.Lease)
	t.active, t.cancel, t.done = id, cancel, make(chan struct{})
	done := t.done
	go func() {
		err := t.Engine.Run(runCtx, id)
		cancel()
		if err != nil {
			persist, stop := context.WithTimeout(context.Background(), 3*time.Second)
			_, _ = t.Engine.Pause(persist, id, "执行中断，需要重新核验游戏状态")
			stop()
		}
		t.mu.Lock()
		t.active, t.cancel = "", nil
		close(done)
		t.mu.Unlock()
		if t.Wake != nil {
			select {
			case t.Wake <- struct{}{}:
			default:
			}
		}
	}()
}

// Resume requires a currently valid lease and reconciles the persisted
// step with authoritative game state before allowing another submission.
func (t *Tasks) Resume(ctx context.Context, id string) (aimcp.TaskReceipt, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.Lease == nil || t.Lease.Err() != nil || t.Engine == nil {
		return aimcp.TaskReceipt{}, aimcp.ErrBackend
	}
	if t.active != "" {
		return aimcp.TaskReceipt{}, errors.New("character already has a deterministic task")
	}
	if _, err := t.Status(ctx, id); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	c, err := t.Engine.Resume(ctx, id)
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if c.Status == automation.Running {
		t.launchLocked(id)
	}
	return taskReceipt(c), nil
}
func (t *Tasks) StartTask(ctx context.Context, q aimcp.TaskRequest) (aimcp.TaskReceipt, error) {
	return t.start(ctx, func() (automation.Plan, error) { return t.Builder.Task(ctx, q) })
}
func (t *Tasks) StartLeveling(ctx context.Context, q aimcp.LevelingRequest) (aimcp.TaskReceipt, error) {
	return t.start(ctx, func() (automation.Plan, error) { return t.Builder.Leveling(ctx, q) })
}
func (t *Tasks) Status(ctx context.Context, id string) (aimcp.TaskReceipt, error) {
	if t.Engine == nil || t.Engine.Store == nil {
		return aimcp.TaskReceipt{}, aimcp.ErrBackend
	}
	c, err := t.Engine.Store.Load(ctx, id)
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if c.Plan.CharacterID != t.CharacterID {
		return aimcp.TaskReceipt{}, aimcp.ErrInvalidBinding
	}
	return taskReceipt(c), nil
}

func (t *Tasks) Active(ctx context.Context) ([]aimcp.TaskReceipt, error) {
	t.mu.Lock()
	id := t.active
	t.mu.Unlock()
	if id == "" {
		return nil, nil
	}
	r, err := t.Status(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.Status != aimcp.ReceiptRunning && r.Status != aimcp.ReceiptPending {
		return nil, nil
	}
	return []aimcp.TaskReceipt{r}, nil
}
func (t *Tasks) Cancel(ctx context.Context, q aimcp.CancelRequest) (aimcp.TaskReceipt, error) {
	if _, err := t.Status(ctx, q.Handle); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	t.mu.Lock()
	var done chan struct{}
	if t.active == q.Handle && t.cancel != nil {
		t.cancel()
		done = t.done
	}
	t.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return aimcp.TaskReceipt{}, ctx.Err()
		}
	}
	c, err := t.Engine.Pause(ctx, q.Handle, "任务已取消；已提交的游戏操作仍需核验")
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	r := taskReceipt(c)
	if c.Status != automation.Completed {
		r.Status = aimcp.ReceiptCancelled
	}
	return r, nil
}
func (t *Tasks) Close() {
	t.mu.Lock()
	t.closed = true
	if t.cancel != nil {
		t.cancel()
	}
	done := t.done
	t.mu.Unlock()
	if done != nil {
		<-done
	}
}

func taskReceipt(c automation.Checkpoint) aimcp.TaskReceipt {
	r := aimcp.TaskReceipt{Handle: c.Plan.ID, Status: aimcp.ReceiptRunning, State: string(c.Status), Reason: c.Reason}
	switch c.Status {
	case automation.Completed:
		if c.Confirmation == nil || !c.Plan.Complete(*c.Confirmation) {
			r.Status = aimcp.ReceiptUnknown
			r.Reason = "completion checkpoint has no authoritative observation; reconciliation required"
			return r
		}
		r.Status = aimcp.ReceiptConfirmed
		r.Evidence, _ = json.Marshal(map[string]any{"character_id": c.Plan.CharacterID, "knowledge_revision": c.Plan.KnowledgeRevision, "checkpoint_revision": c.Revision, "observation": c.Confirmation, "confirmed_at": c.UpdatedAt})
	case automation.Paused:
		r.Status = aimcp.ReceiptFailed
	}
	return r
}

var _ TaskController = (*Tasks)(nil)
