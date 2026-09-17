package aisupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

type startRecoveryFactory struct {
	*reviewFactory
	reconcileCalls int
	reconcileErr   error
}

func (f *startRecoveryFactory) ReconcileUnknown(context.Context, string, string) error {
	f.reconcileCalls++
	return f.reconcileErr
}

func seedPausedUnknown(t *testing.T, store *airuntime.Store, id string) (airuntime.Profile, airuntime.TokenAttempt) {
	t.Helper()
	profileInput := testSupervisorProfile(id)
	profileInput.Status = airuntime.ProfileStatusActive
	profile, err := store.CreateProfile(context.Background(), profileInput)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.BeginTokenAttempt(context.Background(), profile.ID, time.Now(), 17)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareTokenAttempt(context.Background(), reservation, airuntime.TokenAttemptMetadata{Prompt: "old prompt", Resume: true, ThreadID: "old-thread"}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTokenAttemptDispatched(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTokenAttemptUnknown(context.Background(), reservation, "deadline exceeded"); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := json.Marshal(persistedState{ProfileVersion: profile.Version, PendingAttemptID: reservation.ID, ThreadID: "old-thread", Snapshot: Snapshot{GameReady: true, ProgressKey: "stale"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveCheckpointCAS(context.Background(), profile.ID, 0, checkpoint, "test"); err != nil {
		t.Fatal(err)
	}
	paused := airuntime.ProfileStatusPaused
	profile, err = store.UpdateProfileCAS(context.Background(), profile.ID, profile.Version, airuntime.ProfilePatch{Status: &paused, Actor: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.GetTokenAttempt(context.Background(), reservation.ID)
	if err != nil {
		t.Fatal(err)
	}
	return profile, attempt
}

func TestStartWaitsForUnknownExecutionBeforeReviewing(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	profile, attempt := seedPausedUnknown(t, store, "start-unknown-wait")
	session := &fakeSession{runner: &fakeRunner{}, snapshot: Snapshot{GameReady: true}}
	factory := &startRecoveryFactory{
		reviewFactory: &reviewFactory{
			fakeFactory: &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}},
			execution:   UnknownExecution{RequestID: "broker-old", State: "unknown", ContainerStopped: true, UpdatedAt: attempt.UpdatedAt},
		},
		reconcileErr: ErrAttemptStillRunning,
	}
	supervisor, err := New(ctx, store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()

	if err := supervisor.Start(ctx, profile.ID); !errors.Is(err, ErrAttemptStillRunning) {
		t.Fatalf("start with live old execution = %v", err)
	}
	if factory.reconcileCalls != 1 || factory.openCount() != 0 || factory.calls != 0 {
		t.Fatalf("start opened/reviewed before old execution drained: reconcile=%d opens=%d reviews=%d", factory.reconcileCalls, factory.openCount(), factory.calls)
	}
	pending, err := store.GetTokenAttempt(ctx, attempt.ID)
	if err != nil || pending.State != airuntime.TokenAttemptUnknown || !pending.SettledAt.IsZero() {
		// SettledAt is zero for an unresolved attempt; keep this assertion
		// explicit so a future implementation cannot silently release it.
		t.Fatalf("unknown reservation changed: %+v err=%v", pending, err)
	}
}

func TestStartReviewsUnknownAndCreatesFreshTurn(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	profile, attempt := seedPausedUnknown(t, store, "start-unknown-review")
	runner := &fakeRunner{}
	session := &fakeSession{runner: runner, snapshot: Snapshot{GameReady: true, ProgressKey: "fresh"}}
	factory := &startRecoveryFactory{
		reviewFactory: &reviewFactory{
			fakeFactory: &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}},
			execution:   UnknownExecution{RequestID: "broker-old", State: "unknown", ContainerStopped: true, UpdatedAt: attempt.UpdatedAt},
		},
	}
	supervisor, err := New(ctx, store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()

	if err := supervisor.Start(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(runner.Requests()) > 0 })
	if factory.reconcileCalls != 1 || factory.calls != 1 || factory.openCount() != 1 {
		t.Fatalf("start recovery sequence reconcile=%d review=%d opens=%d", factory.reconcileCalls, factory.calls, factory.openCount())
	}
	requests := runner.Requests()
	if requests[0].RequestID == attempt.ID || requests[0].Resume || requests[0].ThreadID != "" {
		t.Fatalf("reviewed start reused old turn: %+v", requests[0])
	}
	oldAttempt, err := store.GetTokenAttempt(ctx, attempt.ID)
	if err != nil || oldAttempt.State != airuntime.TokenAttemptUnknown || oldAttempt.SettledAt.IsZero() {
		t.Fatalf("unknown reservation was not settled by review: %+v err=%v", oldAttempt, err)
	}
	// The fresh turn may already have reserved its own budget by the time the
	// runner request is observed. It must never reuse the reviewed attempt.
	if pending, pendingErr := store.PendingTokenAttempt(ctx, profile.ID); pendingErr == nil && pending.ID == attempt.ID {
		t.Fatalf("fresh turn reused reviewed reservation: %+v", pending)
	} else if pendingErr != nil && !errors.Is(pendingErr, airuntime.ErrNotFound) {
		t.Fatalf("inspect fresh reservation: %v", pendingErr)
	}
	reviewed, err := store.GetUnknownAttemptReview(ctx, profile.ID, attempt.ID)
	if err != nil || reviewed.Actor != unknownReviewStartActor {
		t.Fatalf("start review audit = %+v err=%v", reviewed, err)
	}
	current, err := store.GetProfile(ctx, profile.ID)
	if err != nil || current.Status != airuntime.ProfileStatusActive {
		t.Fatalf("profile was not activated after review: %+v err=%v", current, err)
	}
}

func TestPauseDuringReviewedStartPreventsSessionPublication(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	profile, attempt := seedPausedUnknown(t, store, "start-unknown-pause")
	release := make(chan struct{})
	session := &fakeSession{runner: &fakeRunner{}, snapshot: Snapshot{GameReady: true}}
	factory := &startRecoveryFactory{
		reviewFactory: &reviewFactory{
			fakeFactory: &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}, openGate: release},
			execution:   UnknownExecution{RequestID: "broker-old", State: "unknown", ContainerStopped: true, UpdatedAt: attempt.UpdatedAt},
		},
	}
	supervisor, err := New(ctx, store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	started := make(chan error, 1)
	go func() { started <- supervisor.Start(ctx, profile.ID) }()
	waitFor(t, func() bool { return factory.openCount() == 1 })
	if err := supervisor.Pause(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-started; err == nil {
		t.Fatal("pause during start was overwritten by session publication")
	}
	if session.closeCount() > 1 || len(session.runner.Requests()) != 0 {
		t.Fatalf("paused start leaked or ran a session: closes=%d requests=%d", session.closeCount(), len(session.runner.Requests()))
	}
	current, err := store.GetProfile(ctx, profile.ID)
	if err != nil || current.Status != airuntime.ProfileStatusPaused {
		t.Fatalf("profile status after pause/start race = %+v err=%v", current, err)
	}
}
