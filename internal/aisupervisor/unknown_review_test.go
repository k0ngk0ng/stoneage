package aisupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

type reviewFactory struct {
	*fakeFactory
	execution       UnknownExecution
	calls           int
	failAfterReview bool
	entered         chan struct{}
	release         chan struct{}
}

func (f *reviewFactory) InspectUnknown(context.Context, string, string) (UnknownExecution, error) {
	return f.execution, nil
}
func (f *reviewFactory) ReviewUnknown(_ context.Context, _ string, _ string, expected UnknownExecution, actor, reason string) error {
	f.calls++
	if f.entered != nil {
		close(f.entered)
		<-f.release
	}
	if expected.RequestID != f.execution.RequestID {
		return ErrAttemptRecovery
	}
	f.execution.Reviewed = true
	if f.failAfterReview {
		f.failAfterReview = false
		return errors.New("checkpoint write interrupted")
	}
	return nil
}

func TestSupervisorUnknownReviewSurvivesPartialTransportCommit(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "retry"
		if shutdown {
			name = "shutdown"
		}
		t.Run(name, func(t *testing.T) { testSupervisorUnknownReview(t, shutdown) })
	}
}
func testSupervisorUnknownReview(t *testing.T, shutdown bool) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	input := testSupervisorProfile("review-profile")
	input.Status = airuntime.ProfileStatusActive
	profile, err := store.CreateProfile(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.BeginTokenAttempt(ctx, profile.ID, time.Now(), 17)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PrepareTokenAttempt(ctx, reservation, airuntime.TokenAttemptMetadata{Prompt: "old request", Resume: true, ThreadID: "old-thread"}); err != nil {
		t.Fatal(err)
	}
	if err = store.MarkTokenAttemptDispatched(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	if err = store.MarkTokenAttemptUnknown(ctx, reservation, "response missing"); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(persistedState{ProfileVersion: profile.Version, PendingAttemptID: reservation.ID, ThreadID: "old-thread", Snapshot: Snapshot{GameReady: true, ProgressKey: "stale"}})
	if _, err = store.SaveCheckpointCAS(ctx, profile.ID, 0, state, "test"); err != nil {
		t.Fatal(err)
	}
	paused := airuntime.ProfileStatusPaused
	profile, err = store.UpdateProfileCAS(ctx, profile.ID, profile.Version, airuntime.ProfilePatch{Status: &paused, Actor: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	factory := &reviewFactory{fakeFactory: &fakeFactory{sessions: map[string]*fakeSession{profile.ID: {runner: runner, snapshot: Snapshot{GameReady: true, ProgressKey: "fresh"}}}}, execution: UnknownExecution{RequestID: "broker-old", State: "unknown", ContainerStopped: true, UpdatedAt: time.Now().UTC()}, failAfterReview: true}
	supervisor, err := New(ctx, store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	factory.execution.ContainerStopped = false
	blocked, err := supervisor.UnknownRecovery(ctx, profile.ID)
	if err != nil || blocked.Ready {
		t.Fatalf("idle supervisor exposed live container as ready: %+v %v", blocked, err)
	}
	factory.execution.ContainerStopped = true
	status, err := supervisor.UnknownRecovery(ctx, profile.ID)
	if err != nil || !status.Ready || status.AttemptID != reservation.ID {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	request := UnknownRecoveryRequest{UnknownRecoveryStatus: status, Actor: "operator", Reason: airuntime.UnknownReviewReason}
	if shutdown {
		factory.entered = make(chan struct{})
		factory.release = make(chan struct{})
		factory.failAfterReview = false
		reviewed := make(chan error, 1)
		go func() { reviewed <- supervisor.ReviewUnknown(ctx, request) }()
		<-factory.entered
		if err = supervisor.Start(ctx, profile.ID); !errors.Is(err, ErrAttemptRecovery) {
			t.Fatalf("start during review must reject, not report a started session: %v", err)
		}
		closed := make(chan error, 1)
		go func() { closed <- supervisor.Close() }()
		waitFor(t, func() bool { supervisor.mu.Lock(); defer supervisor.mu.Unlock(); return supervisor.closed })
		select {
		case <-closed:
			t.Fatal("Close did not wait for review")
		default:
		}
		close(factory.release)
		if err = <-reviewed; !errors.Is(err, ErrClosed) && !errors.Is(err, context.Canceled) {
			t.Fatalf("review committed after shutdown: %v", err)
		}
		if err = <-closed; err != nil {
			t.Fatal(err)
		}
		if _, err = store.PendingTokenAttempt(ctx, profile.ID); err != nil {
			t.Fatalf("shutdown released reservation: %v", err)
		}
		if _, err = store.GetUnknownAttemptReview(ctx, profile.ID, reservation.ID); !errors.Is(err, airuntime.ErrNotFound) {
			t.Fatalf("shutdown committed audit: %v", err)
		}
		return
	}
	if err = supervisor.ReviewUnknown(ctx, request); err == nil {
		t.Fatal("injected interruption accepted")
	}
	if _, err = store.PendingTokenAttempt(ctx, profile.ID); err != nil {
		t.Fatal("partial review released pending attempt", err)
	}
	if factory.openCount() != 0 {
		t.Fatal("review opened game/model session")
	}
	if err = supervisor.ReviewUnknown(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err = supervisor.ReviewUnknown(ctx, request); err != nil {
		t.Fatal("HTTP retry failed", err)
	}
	if factory.calls != 2 {
		t.Fatalf("completed review replayed transport: %d", factory.calls)
	}
	cp, err := store.GetCheckpoint(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fresh persistedState
	if err = json.Unmarshal(cp.State, &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.PendingAttemptID != "" || fresh.ThreadID != "" || fresh.ModelTurnDone || fresh.Snapshot.GameReady || fresh.Snapshot.ProgressKey != "" {
		t.Fatalf("stale state survived review: %+v", fresh)
	}
	if err = supervisor.Start(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(runner.Requests()) > 0 })
	next := runner.Requests()[0]
	if next.RequestID == reservation.ID || next.RequestID == "" || next.Resume || next.ThreadID != "" {
		t.Fatalf("review replayed old model turn: %+v", next)
	}
}
