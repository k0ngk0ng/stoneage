package aiservice

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type taskGame struct {
	mu          sync.Mutex
	observation automation.Observation
	writes      int
}

func (g *taskGame) Observe(context.Context) (automation.Observation, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.observation, nil
}
func (g *taskGame) Execute(context.Context, automation.Action) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.writes++
	return nil
}

type taskBuilder struct{ plan automation.Plan }

func (b taskBuilder) Task(context.Context, aimcp.TaskRequest) (automation.Plan, error) {
	return b.plan, nil
}
func (b taskBuilder) Leveling(context.Context, aimcp.LevelingRequest) (automation.Plan, error) {
	return b.plan, nil
}
func taskFixture(t *testing.T) (*Tasks, *taskGame, context.CancelFunc) {
	t.Helper()
	s, err := automation.OpenStore(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	lease, cancel := context.WithCancel(context.Background())
	g := &taskGame{observation: automation.Observation{CharacterID: "a:0", Connected: true, Ready: true, Revision: 1, Character: automation.Entity{HP: 10, Level: 1}}}
	p := automation.Plan{ID: "quest-test", CharacterID: "a:0", KnowledgeRevision: "verified-data", Mode: "quest", MaximumSeconds: 60, Budget: automation.Budget{Known: true}, Completion: []automation.Condition{{Kind: "character_level", Value: 2}}, Steps: []automation.Step{{ID: "one", Action: automation.Action{Skill: "test"}, TimeoutSeconds: 30, CostKnown: true, Success: []automation.Condition{{Kind: "character_level", Value: 2}}}}}
	tasks := &Tasks{Engine: &automation.Engine{Game: g, Store: s, PollInterval: time.Millisecond}, Builder: taskBuilder{p}, CharacterID: "a:0", Lease: lease}
	t.Cleanup(func() { cancel(); tasks.Close(); s.Close() })
	return tasks, g, cancel
}
func awaitTask(t *testing.T, tasks *Tasks, id string, predicate func(aimcp.TaskReceipt) bool) aimcp.TaskReceipt {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r, err := tasks.Status(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(r) {
			return r
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("task did not reach expected state")
	return aimcp.TaskReceipt{}
}
func TestTasksOnlyConfirmServerObservedCompletion(t *testing.T) {
	tasks, g, _ := taskFixture(t)
	r, err := tasks.StartTask(context.Background(), aimcp.TaskRequest{TaskID: "test"})
	if err != nil || r.Status != aimcp.ReceiptRunning {
		t.Fatalf("start: %+v %v", r, err)
	}
	// The HTTP request can finish while the lease continues execution.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		g.mu.Lock()
		n := g.writes
		g.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	r, err = tasks.Status(context.Background(), r.Handle)
	if err != nil || r.Status != aimcp.ReceiptRunning {
		t.Fatalf("packet write completed task: %+v %v", r, err)
	}
	g.mu.Lock()
	g.observation.Character.Level = 2
	g.observation.Revision++
	g.mu.Unlock()
	r = awaitTask(t, tasks, r.Handle, func(r aimcp.TaskReceipt) bool { return r.Status == aimcp.ReceiptConfirmed })
	var evidence struct {
		Observation automation.Observation `json:"observation"`
	}
	if err := json.Unmarshal(r.Evidence, &evidence); err != nil || evidence.Observation.Character.Level != 2 {
		t.Fatalf("missing observed level: %s %v", r.Evidence, err)
	}
}
func TestTasksPauseOnLeaseRevocation(t *testing.T) {
	tasks, _, cancel := taskFixture(t)
	r, err := tasks.StartTask(context.Background(), aimcp.TaskRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	awaitTask(t, tasks, r.Handle, func(r aimcp.TaskReceipt) bool { return r.State == "paused" })
	if _, err := tasks.StartTask(context.Background(), aimcp.TaskRequest{}); err == nil {
		t.Fatal("started after lease cancellation")
	}
}
func TestOldCompletionWithoutEvidenceRemainsUnknown(t *testing.T) {
	r := taskReceipt(automation.Checkpoint{Plan: automation.Plan{ID: "old"}, Status: automation.Completed})
	if r.Status != aimcp.ReceiptUnknown {
		t.Fatal("fabricated evidence for older checkpoint")
	}
}
