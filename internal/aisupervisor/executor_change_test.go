package aisupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

type executorChangePreparerFactory struct {
	*fakeFactory
	mu    sync.Mutex
	calls int
	err   error
}

func (factory *executorChangePreparerFactory) PrepareExecutorChange(context.Context, string) error {
	factory.mu.Lock()
	factory.calls++
	err := factory.err
	factory.mu.Unlock()
	return err
}

func (factory *executorChangePreparerFactory) prepareCalls() int {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.calls
}

func TestPrepareExecutorChangeResetsExecutorStateAndPreservesDurableIntent(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	input := testSupervisorProfile("executor-change-state")
	input.Status = airuntime.ProfileStatusPaused
	profile, err := store.CreateProfile(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	note, err := store.UpsertAgentNote(ctx, profile.ID, "route", "return to the riverside after changing executors")
	if err != nil {
		t.Fatal(err)
	}
	event, err := store.AppendEvent(ctx, profile.ID, "game.memory_seed", "test", []byte(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	memory, err := store.RecordConfirmedMemory(ctx, profile.ID, airuntime.MemoryInput{
		Kind: "relationship", Subject: "friend", Content: []byte(`{"name":"Mina"}`),
		SourceEventID: event.ID, Confirmed: true, Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := store.CreateSchedule(ctx, profile.ID, airuntime.ScheduleInput{
		Kind: "reminder", Title: "riverside", Prompt: "Return to the riverside.", RunAt: now.Add(time.Hour), Actor: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	activity := LifeActivity{Kind: "wander", StartedAt: now.Add(-time.Minute), Until: now.Add(20 * time.Minute)}
	nextDecision := now.Add(10 * time.Minute)
	oldSnapshot := Snapshot{
		GameReady: true, GoalComplete: true, ProgressKey: "old-executor-progress",
		ActiveTasks: []TaskHandle{{Handle: "old-task", Status: TaskStatusRunning}},
		Context:     json.RawMessage(`{"executor":"old"}`),
	}
	checkpointState := persistedState{
		Activity:         activity,
		ProfileVersion:   profile.Version,
		ThreadID:         "old-thread",
		NextDecisionAt:   nextDecision,
		ModelTurnDone:    true,
		WaitingForWake:   true,
		Snapshot:         oldSnapshot,
		RunFailures:      4,
		ObserveErrors:    3,
		NoProgress:       2,
		LastProgressAt:   now.Add(-2 * time.Minute),
		LastNoProgressAt: now.Add(-time.Minute),
		LastError:        "old executor error",
	}
	checkpointData, err := json.Marshal(checkpointState)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := store.SaveCheckpointCAS(ctx, profile.ID, 0, checkpointData, "test")
	if err != nil {
		t.Fatal(err)
	}

	session := &fakeSession{runner: &fakeRunner{}, snapshot: Snapshot{GameReady: true}}
	factory := &executorChangePreparerFactory{fakeFactory: &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}}
	supervisor, err := New(ctx, store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()

	preparedVersion, err := supervisor.PrepareExecutorChangeAtVersion(ctx, profile.ID, profile.Version)
	if err != nil {
		t.Fatal(err)
	}
	if preparedVersion != profile.Version+1 {
		t.Fatalf("prepared profile version = %d, want %d", preparedVersion, profile.Version+1)
	}
	if factory.openCount() != 0 {
		t.Fatalf("executor migration opened %d game sessions", factory.openCount())
	}
	if factory.prepareCalls() != 1 {
		t.Fatalf("executor migration factory prepare calls=%d, want 1", factory.prepareCalls())
	}

	updated, err := store.GetProfile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != airuntime.ProfileStatusPaused || updated.Version != preparedVersion {
		t.Fatalf("profile after migration = %+v", updated)
	}
	resetCheckpoint, resetState := loadPersistedCheckpoint(t, store, profile.ID)
	if resetCheckpoint.Version != checkpoint.Version+1 {
		t.Fatalf("checkpoint version = %d, want %d", resetCheckpoint.Version, checkpoint.Version+1)
	}
	if resetState.ProfileVersion != preparedVersion || resetState.ThreadID != "" || resetState.ModelTurnDone || resetState.WaitingForWake || resetState.PendingAttemptID != "" {
		t.Fatalf("executor identity survived migration: %+v", resetState)
	}
	if !reflect.DeepEqual(resetState.Activity, activity) || !resetState.NextDecisionAt.Equal(nextDecision) {
		t.Fatalf("durable life intent changed: activity=%+v next=%s", resetState.Activity, resetState.NextDecisionAt)
	}
	if !reflect.DeepEqual(resetState.Snapshot, Snapshot{}) || resetState.RunFailures != 0 || resetState.ObserveErrors != 0 || resetState.NoProgress != 0 || !resetState.LastProgressAt.IsZero() || !resetState.LastNoProgressAt.IsZero() {
		t.Fatalf("old executor state survived migration: %+v", resetState)
	}

	notes, err := store.ListAgentNotes(ctx, profile.ID, 10)
	if err != nil || len(notes) != 1 || !reflect.DeepEqual(notes[0], note) {
		t.Fatalf("agent notes changed: notes=%+v err=%v", notes, err)
	}
	memories, err := store.ListMemories(ctx, profile.ID, 10)
	if err != nil || len(memories) != 1 || !reflect.DeepEqual(memories[0], memory) {
		t.Fatalf("memories changed: memories=%+v err=%v", memories, err)
	}
	unchangedSchedule, err := store.GetSchedule(ctx, profile.ID, schedule.ID)
	if err != nil || !reflect.DeepEqual(unchangedSchedule, schedule) {
		t.Fatalf("schedule changed: schedule=%+v want=%+v err=%v", unchangedSchedule, schedule, err)
	}

	if err := supervisor.Start(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(session.runner.Requests()) > 0 })
	request := session.runner.Requests()[0]
	if request.Resume || request.ThreadID != "" {
		t.Fatalf("next executor resumed old conversation: %+v", request)
	}
}

func TestPrepareExecutorChangeRejectsActiveProfile(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	input := testSupervisorProfile("executor-change-active")
	input.Status = airuntime.ProfileStatusActive
	profile, err := store.CreateProfile(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := New(ctx, store, &fakeFactory{sessions: map[string]*fakeSession{}}, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	if err := supervisor.PrepareExecutorChange(ctx, profile.ID); !errors.Is(err, ErrExecutorChangeActive) {
		t.Fatalf("active profile migration error = %v", err)
	}
	current, err := store.GetProfile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != profile.Version || current.Status != airuntime.ProfileStatusActive {
		t.Fatalf("active profile changed after rejected migration: %+v", current)
	}
}

func TestPrepareExecutorChangeRejectsStaleProfileVersion(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	input := testSupervisorProfile("executor-change-stale")
	input.Status = airuntime.ProfileStatusPaused
	profile, err := store.CreateProfile(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	state, err := json.Marshal(persistedState{ProfileVersion: profile.Version, ThreadID: "old-thread", ModelTurnDone: true})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := store.SaveCheckpointCAS(ctx, profile.ID, 0, state, "test")
	if err != nil {
		t.Fatal(err)
	}
	goal := profile.Goal
	goal.TargetLevel++
	updated, err := store.UpdateProfileCAS(ctx, profile.ID, profile.Version, airuntime.ProfilePatch{Goal: &goal, Actor: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := New(ctx, store, &fakeFactory{sessions: map[string]*fakeSession{}}, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	if _, err := supervisor.PrepareExecutorChangeAtVersion(ctx, profile.ID, profile.Version); !errors.Is(err, ErrProfileChanged) {
		t.Fatalf("stale executor migration error = %v", err)
	}
	current, err := store.GetProfile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != updated.Version || current.Goal.TargetLevel != updated.Goal.TargetLevel {
		t.Fatalf("stale migration changed profile: %+v", current)
	}
	unchanged, err := store.GetCheckpoint(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Version != checkpoint.Version || string(unchanged.State) != string(checkpoint.State) {
		t.Fatalf("stale migration changed checkpoint: %+v", unchanged)
	}
}

func TestPrepareExecutorChangeReviewsUnknownAndPreservesAccounting(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	profile, attempt := seedPausedUnknown(t, store, "executor-change-unknown")
	factory := &startRecoveryFactory{
		reviewFactory: &reviewFactory{
			fakeFactory: &fakeFactory{sessions: map[string]*fakeSession{profile.ID: {runner: &fakeRunner{}, snapshot: Snapshot{GameReady: true}}}},
			execution:   UnknownExecution{RequestID: "broker-old", State: "unknown", ContainerStopped: true, UpdatedAt: attempt.UpdatedAt},
		},
	}
	supervisor, err := New(ctx, store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()

	preparedVersion, err := supervisor.PrepareExecutorChangeAtVersion(ctx, profile.ID, profile.Version)
	if err != nil {
		t.Fatal(err)
	}
	if preparedVersion != profile.Version+1 || factory.reconcileCalls != 1 || factory.calls != 1 || factory.openCount() != 0 {
		t.Fatalf("unknown migration sequence version=%d reconcile=%d review=%d opens=%d", preparedVersion, factory.reconcileCalls, factory.calls, factory.openCount())
	}
	oldAttempt, err := store.GetTokenAttempt(ctx, attempt.ID)
	if err != nil || oldAttempt.State != airuntime.TokenAttemptUnknown || oldAttempt.SettledAt.IsZero() {
		t.Fatalf("unknown attempt was not durably reviewed: %+v err=%v", oldAttempt, err)
	}
	if _, err := store.PendingTokenAttempt(ctx, profile.ID); !errors.Is(err, airuntime.ErrNotFound) {
		t.Fatalf("reviewed attempt remained pending: %v", err)
	}
	review, err := store.GetUnknownAttemptReview(ctx, profile.ID, attempt.ID)
	if err != nil || review.Actor != executorChangeActor {
		t.Fatalf("unknown review history = %+v err=%v", review, err)
	}
	usage, err := store.TokenUsage(ctx, profile.ID, time.Now())
	if err != nil || usage.ChargedTokens != attempt.Charge || usage.FailedAttempts != 1 || usage.ReservedTokens != 0 {
		t.Fatalf("unknown review accounting = %+v err=%v", usage, err)
	}
	_, state := loadPersistedCheckpoint(t, store, profile.ID)
	if state.PendingAttemptID != "" || state.ThreadID != "" || state.ModelTurnDone || state.Snapshot.GameReady {
		t.Fatalf("old executor state survived unknown migration: %+v", state)
	}
}

func TestPrepareExecutorChangePauseOrStopWinsReview(t *testing.T) {
	for _, target := range []struct {
		name   string
		status string
		call   func(context.Context, *Supervisor, string) error
	}{
		{name: "pause", status: airuntime.ProfileStatusPaused, call: func(ctx context.Context, s *Supervisor, id string) error { return s.Pause(ctx, id) }},
		{name: "stop", status: airuntime.ProfileStatusStopped, call: func(ctx context.Context, s *Supervisor, id string) error { return s.Stop(ctx, id) }},
	} {
		t.Run(target.name, func(t *testing.T) {
			ctx := context.Background()
			store := testSupervisorStore(t)
			profile, attempt := seedPausedUnknown(t, store, "executor-change-"+target.name)
			entered := make(chan struct{})
			release := make(chan struct{})
			factory := &startRecoveryFactory{
				reviewFactory: &reviewFactory{
					fakeFactory: &fakeFactory{sessions: map[string]*fakeSession{profile.ID: {runner: &fakeRunner{}, snapshot: Snapshot{GameReady: true}}}},
					execution:   UnknownExecution{RequestID: "broker-old", State: "unknown", ContainerStopped: true, UpdatedAt: attempt.UpdatedAt},
					entered:     entered,
					release:     release,
				},
			}
			supervisor, err := New(ctx, store, factory, supervisorTestConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer supervisor.Close()
			prepared := make(chan error, 1)
			go func() { prepared <- supervisor.PrepareExecutorChange(ctx, profile.ID) }()
			<-entered
			if err := supervisor.Start(ctx, profile.ID); !errors.Is(err, ErrAttemptRecovery) {
				t.Fatalf("start during executor migration = %v", err)
			}
			if err := target.call(ctx, supervisor, profile.ID); err != nil {
				t.Fatal(err)
			}
			close(release)
			if err := <-prepared; err == nil {
				t.Fatal("executor migration succeeded after concurrent lifecycle transition")
			}
			current, err := store.GetProfile(ctx, profile.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.Status != target.status {
				t.Fatalf("profile status after concurrent %s = %q", target.name, current.Status)
			}
			pending, err := store.GetTokenAttempt(ctx, attempt.ID)
			if err != nil || !pending.SettledAt.IsZero() || pending.State != airuntime.TokenAttemptUnknown {
				t.Fatalf("unknown attempt changed after cancelled migration: %+v err=%v", pending, err)
			}
			if _, err := store.GetUnknownAttemptReview(ctx, profile.ID, attempt.ID); !errors.Is(err, airuntime.ErrNotFound) {
				t.Fatalf("cancelled migration committed unknown review: %v", err)
			}
			_, checkpointState := loadPersistedCheckpoint(t, store, profile.ID)
			if checkpointState.PendingAttemptID != attempt.ID || checkpointState.ThreadID != "old-thread" {
				t.Fatalf("cancelled migration overwrote checkpoint: %+v", checkpointState)
			}
		})
	}
}
