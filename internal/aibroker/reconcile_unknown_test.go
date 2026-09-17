package aibroker

import (
	"context"
	"errors"
	"testing"
)

type failedOutcomeJournal struct{ *MemoryJournal }

func (j *failedOutcomeJournal) UpdateIfState(ctx context.Context, state RunState, entry JournalEntry) error {
	if entry.State == RunCompleted {
		return errors.New("fixture durable write failure")
	}
	return j.MemoryJournal.UpdateIfState(ctx, state, entry)
}

func TestLookupReconcilesFailedOutcomeWithoutRestart(t *testing.T) {
	j := &failedOutcomeJournal{NewMemoryJournal()}
	d := &inspectDocker{states: []DockerContainerState{DockerContainerExited}}
	b := newFakeBroker(t, d, j)
	t.Cleanup(func() { _ = b.Close() })
	req := testRequest("failed-write", "request-1")
	if _, err := b.Run(context.Background(), req); !errors.Is(err, ErrRunUnknown) {
		t.Fatalf("expected uncertain durable outcome: %v", err)
	}
	result, err := b.Lookup(context.Background(), req.ProfileID, req.RequestID)
	if !errors.Is(err, ErrRunUnknown) || result.State != RunUnknown {
		t.Fatalf("failed write did not reconcile: state=%s err=%v", result.State, err)
	}
	if len(d.Calls()) != 1 {
		t.Fatal("reconciliation dispatched another model turn")
	}
}

func TestReconcileUnknownNeverStopsAnActiveTransport(t *testing.T) {
	j := NewMemoryJournal()
	d := &inspectDocker{}
	b := newFakeBroker(t, d, j)
	t.Cleanup(func() { _ = b.Close() })
	entry := runningJournalEntry("transport-fenced", "request-1")
	if _, err := j.Create(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	b.transport = map[string]bool{entry.ContainerName: true}
	if err := b.ReconcileUnknown(context.Background(), entry.ProfileID, entry.RequestID); !errors.Is(err, ErrRunRunning) {
		t.Fatalf("active transport was not fenced: %v", err)
	}
	if len(d.Stops()) != 0 || len(d.InspectCalls()) != 0 {
		t.Fatal("active transport was touched by recovery")
	}
}

func TestReconcileUnknownStopsAndRechecksOrphan(t *testing.T) {
	j := NewMemoryJournal()
	d := &inspectDocker{states: []DockerContainerState{DockerContainerRunning, DockerContainerExited}}
	b := newFakeBroker(t, d, j)
	t.Cleanup(func() { _ = b.Close() })
	entry := runningJournalEntry("unknown-orphan", "request-1")
	entry.State = RunUnknown
	if _, err := j.Create(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if err := b.ReconcileUnknown(context.Background(), entry.ProfileID, entry.RequestID); err != nil {
		t.Fatal(err)
	}
	if len(d.Stops()) != 1 || len(d.InspectCalls()) != 2 {
		t.Fatal("orphan termination was not verified")
	}
	current, _ := j.Get(context.Background(), entry.ProfileID, entry.RequestID)
	if current.State != RunUnknown || current.Review != nil {
		t.Fatal("termination changed unknown outcome or released claim")
	}
}
