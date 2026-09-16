package aisupervisor

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func lifeTestProfile(id string, seconds int) airuntime.Profile {
	profile := testSupervisorProfile(id)
	profile.Goal = airuntime.Goal{
		Kind: "life",
		Life: &airuntime.LifePolicy{DecisionIntervalSeconds: seconds},
	}
	return profile
}

func loadPersistedCheckpoint(t *testing.T, store *airuntime.Store, profileID string) (airuntime.Checkpoint, persistedState) {
	t.Helper()
	checkpoint, err := store.GetCheckpoint(context.Background(), profileID)
	if err != nil {
		t.Fatal(err)
	}
	var state persistedState
	if err := json.Unmarshal(checkpoint.State, &state); err != nil {
		t.Fatal(err)
	}
	return checkpoint, state
}

func savePersistedCheckpoint(t *testing.T, store *airuntime.Store, profile airuntime.Profile, state persistedState) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveCheckpointCAS(context.Background(), profile.ID, 0, data, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorPersistsLifeNextDecisionAt(t *testing.T) {
	store := testSupervisorStore(t)
	profile, err := store.CreateProfile(context.Background(), lifeTestProfile("life-persist", 60))
	if err != nil {
		t.Fatal(err)
	}
	session := &fakeSession{
		runner:   &fakeRunner{},
		wake:     make(chan struct{}, 2),
		snapshot: Snapshot{GameReady: true, ProgressKey: "idle"},
	}
	factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}
	supervisor, err := New(context.Background(), store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(session.runner.Requests()) == 1 })
	waitFor(t, func() bool {
		status := profileStatus(t, supervisor, profile.ID)
		return !status.NextDecisionAt.IsZero()
	})
	checkpoint, state := loadPersistedCheckpoint(t, store, profile.ID)
	if state.NextDecisionAt.IsZero() {
		t.Fatalf("checkpoint omitted life next decision time: %s", checkpoint.State)
	}
	if !state.NextDecisionAt.After(time.Now().UTC().Add(45 * time.Second)) {
		t.Fatalf("checkpoint next decision time = %s, want roughly one minute from now", state.NextDecisionAt)
	}
	status := profileStatus(t, supervisor, profile.ID)
	if !status.NextDecisionAt.Equal(state.NextDecisionAt) {
		t.Fatalf("status next decision time = %s, checkpoint = %s", status.NextDecisionAt, state.NextDecisionAt)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorLifeTimerRunsWithWakeChannelAndNoGameEvent(t *testing.T) {
	store := testSupervisorStore(t)
	profile, err := store.CreateProfile(context.Background(), lifeTestProfile("life-timer", 60))
	if err != nil {
		t.Fatal(err)
	}
	clockMu := sync.Mutex{}
	now := time.Now().UTC()
	clockNow := now
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clockNow
	}
	snapshot := Snapshot{GameReady: true, ProgressKey: "unchanged", Context: json.RawMessage(`{"uncertain_actions":[{"handle":"chat-still-unknown","status":"unknown","evidence":{"requested_action":{"kind":"chat","text":"hello"}}}]}`)}
	next := now.Add(time.Minute)
	savePersistedCheckpoint(t, store, profile, persistedState{
		ProfileVersion: profile.Version,
		ThreadID:       "thread-life",
		ModelTurnDone:  true,
		WaitingForWake: true,
		NextDecisionAt: next,
		Snapshot:       snapshot,
		LastProgressAt: now,
	})
	session := &fakeSession{
		runner:   &fakeRunner{},
		wake:     make(chan struct{}, 4),
		snapshot: snapshot,
	}
	factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}
	supervisor, err := New(context.Background(), store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	supervisor.cfg.Clock = clock
	if err := supervisor.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		status := profileStatus(t, supervisor, profile.ID)
		return status.State == StateWaiting && status.NextDecisionAt.Equal(next)
	})
	// The wake is intentionally unrelated to a game-state change. It must
	// cause an observation but cannot bypass the life decision boundary.
	session.wake <- struct{}{}
	time.Sleep(25 * time.Millisecond)
	if calls := len(session.runner.Requests()); calls != 0 {
		t.Fatalf("repeated wake triggered %d early turns", calls)
	}
	clockMu.Lock()
	clockNow = next.Add(time.Nanosecond)
	clockMu.Unlock()
	waitFor(t, func() bool { return len(session.runner.Requests()) == 1 })
	if !strings.Contains(session.runner.Requests()[0].Prompt, "chat-still-unknown") {
		t.Fatal("heartbeat lost the unresolved message rather than preserving it")
	}
	// A successful timer decision schedules the next one from the current
	// clock; it must not immediately replay missed intervals.
	time.Sleep(25 * time.Millisecond)
	if calls := len(session.runner.Requests()); calls != 1 {
		t.Fatalf("timer decision replayed into %d turns", calls)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorLifeIdleWaitDoesNotConsumeNoProgressBudget(t *testing.T) {
	store := testSupervisorStore(t)
	profile, err := store.CreateProfile(context.Background(), lifeTestProfile("life-no-progress", 60))
	if err != nil {
		t.Fatal(err)
	}
	var observations atomic.Int32
	session := &fakeSession{
		runner:   &fakeRunner{},
		wake:     make(chan struct{}, 2),
		snapshot: Snapshot{GameReady: true, ProgressKey: "resting"},
	}
	session.observe = func(context.Context) (Snapshot, error) {
		observations.Add(1)
		return Snapshot{GameReady: true, ProgressKey: "resting"}, nil
	}
	factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}
	config := supervisorTestConfig()
	config.NoProgressLimit = 1
	config.NoProgressWindow = 10 * time.Millisecond
	supervisor, err := New(context.Background(), store, factory, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(session.runner.Requests()) == 1 })
	time.Sleep(60 * time.Millisecond)
	before := observations.Load()
	checkpointBefore, err := store.GetCheckpoint(context.Background(), profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	session.wake <- struct{}{}
	waitFor(t, func() bool { return observations.Load() > before })
	waitFor(t, func() bool {
		checkpoint, checkpointErr := store.GetCheckpoint(context.Background(), profile.ID)
		return checkpointErr == nil && checkpoint.Version > checkpointBefore.Version
	})
	status := profileStatus(t, supervisor, profile.ID)
	if status.State == StatePaused || status.NoProgressCount != 0 {
		t.Fatalf("life idle wait consumed no-progress budget: %#v", status)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorExpiredLifeDecisionRunsOnceFromCurrentBoundary(t *testing.T) {
	store := testSupervisorStore(t)
	profile, err := store.CreateProfile(context.Background(), lifeTestProfile("life-expired", 60))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	savePersistedCheckpoint(t, store, profile, persistedState{
		ProfileVersion: profile.Version,
		ThreadID:       "thread-expired",
		ModelTurnDone:  true,
		WaitingForWake: true,
		NextDecisionAt: now.Add(-24 * time.Hour),
		Snapshot:       Snapshot{GameReady: true, ProgressKey: "unchanged"},
		LastProgressAt: now.Add(-24 * time.Hour),
	})
	session := &fakeSession{
		runner:   &fakeRunner{},
		wake:     make(chan struct{}, 2),
		snapshot: Snapshot{GameReady: true, ProgressKey: "unchanged"},
	}
	factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}
	supervisor, err := New(context.Background(), store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(session.runner.Requests()) == 1 })
	time.Sleep(30 * time.Millisecond)
	if calls := len(session.runner.Requests()); calls != 1 {
		t.Fatalf("expired decision replayed historical ticks: %d turns", calls)
	}
	_, state := loadPersistedCheckpoint(t, store, profile.ID)
	if !state.NextDecisionAt.After(time.Now().UTC()) {
		t.Fatalf("expired decision was not rescheduled from current time: %s", state.NextDecisionAt)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorRecoveredLifeTurnSchedulesFreshDecisionBoundary(t *testing.T) {
	store := testSupervisorStore(t)
	ctx := context.Background()
	profileInput := lifeTestProfile("life-recovered", 60)
	profileInput.Status = airuntime.ProfileStatusActive
	profile, err := store.CreateProfile(ctx, profileInput)
	if err != nil {
		t.Fatal(err)
	}
	dueAt := time.Now().UTC().Add(time.Minute)
	reminder, err := store.CreateSchedule(ctx, profile.ID, airuntime.ScheduleInput{Kind: "reminder", Prompt: "recover the same reminder", RunAt: dueAt})
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.BeginTokenAttempt(ctx, profile.ID, time.Now(), 1)
	if err != nil {
		t.Fatal(err)
	}
	due, err := store.ClaimDueScheduledTasks(ctx, profile.ID, reservation.ID, dueAt, 8)
	if err != nil || len(due) != 1 {
		t.Fatalf("claim recovered reminder: %+v %v", due, err)
	}
	if err := store.PrepareTokenAttempt(ctx, reservation, airuntime.TokenAttemptMetadata{Prompt: appendScheduledPrompt("recover life", due)}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTokenAttemptDispatched(ctx, reservation); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	savePersistedCheckpoint(t, store, profile, persistedState{
		ProfileVersion:   profile.Version,
		PendingAttemptID: reservation.ID,
		WaitingForWake:   true,
		NextDecisionAt:   now.Add(-24 * time.Hour),
		Snapshot:         Snapshot{GameReady: true, ProgressKey: "unchanged"},
		LastProgressAt:   now.Add(-24 * time.Hour),
	})
	runner := &recoveryTransport{fakeRunner: &fakeRunner{}, recovered: make(chan aicodex.RunRequest, 1)}
	session := &fakeSession{snapshot: Snapshot{GameReady: true, ProgressKey: "unchanged"}}
	factory := recoveryTransportFactory{
		fakeFactory: &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}},
		runner:      runner,
	}
	supervisor, err := New(ctx, store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		checkpoint, getErr := store.GetCheckpoint(ctx, profile.ID)
		return getErr == nil && !contains(string(checkpoint.State), "pending_attempt_id")
	})
	if requests := runner.Requests(); len(requests) != 0 {
		t.Fatalf("recovered life turn started %d fresh turns", len(requests))
	}
	select {
	case request := <-runner.recovered:
		if request.RequestID != reservation.ID {
			t.Fatalf("recovery replaced original request: %#v", request)
		}
		if !strings.Contains(request.Prompt, reminder.Prompt) {
			t.Fatal("recovered attempt lost reminder context")
		}
	default:
		t.Fatal("recovery transport was not called")
	}
	_, state := loadPersistedCheckpoint(t, store, profile.ID)
	delivered, err := store.GetSchedule(ctx, profile.ID, reminder.ID)
	if err != nil || delivered.Status != airuntime.ScheduleDelivered || delivered.Occurrences != 1 {
		t.Fatalf("recovered reminder not acknowledged exactly once: %+v %v", delivered, err)
	}
	if !state.NextDecisionAt.After(time.Now().UTC()) {
		t.Fatalf("recovered turn kept expired life deadline: %s", state.NextDecisionAt)
	}
	time.Sleep(25 * time.Millisecond)
	if requests := runner.Requests(); len(requests) != 0 {
		t.Fatalf("recovered life turn replayed into %d fresh turns", len(requests))
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorLifeChangedObservationRunsBeforeIdleInterval(t *testing.T) {
	store := testSupervisorStore(t)
	profile, err := store.CreateProfile(context.Background(), lifeTestProfile("life-event", 60))
	if err != nil {
		t.Fatal(err)
	}
	session := &fakeSession{
		runner:   &fakeRunner{},
		wake:     make(chan struct{}, 4),
		snapshot: Snapshot{GameReady: true, ProgressKey: "before"},
	}
	factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}
	supervisor, err := New(context.Background(), store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		status := profileStatus(t, supervisor, profile.ID)
		return len(session.runner.Requests()) == 1 && !status.NextDecisionAt.IsZero() && status.State == StateWaiting
	})
	session.setSnapshot(Snapshot{GameReady: true, ProgressKey: "after"})
	session.wake <- struct{}{}
	waitFor(t, func() bool { return len(session.runner.Requests()) == 2 })
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLifeRevisionOnlyTrafficWaitsButIncomingChatWakes(t *testing.T) {
	store := testSupervisorStore(t)
	profile, err := store.CreateProfile(context.Background(), lifeTestProfile("life-revision", 60))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{GameReady: true, ProgressKey: "quiet", Context: json.RawMessage(`{"revision":1,"chat":[]}`)}
	session := &fakeSession{runner: &fakeRunner{}, wake: make(chan struct{}, 4), snapshot: snapshot}
	supervisor, err := New(context.Background(), store, &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	if err := supervisor.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return len(session.runner.Requests()) == 1 && profileStatus(t, supervisor, profile.ID).State == StateWaiting
	})
	supervisor.mu.Lock()
	managed := supervisor.profiles[profile.ID]
	supervisor.mu.Unlock()
	snapshot.Context = json.RawMessage(`{"revision":2,"chat":[]}`)
	session.setSnapshot(snapshot)
	session.wake <- struct{}{}
	waitFor(t, func() bool { return string(managed.view().snapshot.Context) == string(snapshot.Context) })
	time.Sleep(25 * time.Millisecond)
	if len(session.runner.Requests()) != 1 || !managed.view().waitingForWake {
		t.Fatal("protocol revision alone triggered another model decision")
	}
	// A real message must wake immediately with the newest revision retained
	// in the model's observation, rather than waiting for the idle heartbeat.
	snapshot.Context = json.RawMessage(`{"revision":3,"chat":[{"text":"hello from another player"}]}`)
	session.setSnapshot(snapshot)
	session.wake <- struct{}{}
	waitFor(t, func() bool { return len(session.runner.Requests()) == 2 })
	prompt := session.runner.Requests()[1].Prompt
	if !strings.Contains(prompt, `"revision":3`) || !strings.Contains(prompt, "hello from another player") {
		t.Fatal("message wake lost the current action revision or incoming chat")
	}
}

func TestSupervisorLifePromptIncludesClockAndRestContext(t *testing.T) {
	store := testSupervisorStore(t)
	profile := lifeTestProfile("life-prompt", 60)
	profile.Personality = airuntime.Personality{Name: "calm", Prompt: "persistent preference"}
	if _, err := store.CreateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 9, 16, 7, 8, 9, 123456789, time.FixedZone("test", 8*60*60))
	config := supervisorTestConfig()
	config.Clock = func() time.Time { return fixed }
	supervisor, err := New(context.Background(), store, nil, config)
	if err != nil {
		t.Fatal(err)
	}
	defaultPrompt, err := supervisor.buildPrompt(profile, Snapshot{GameReady: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"Life mode", "configured life goal", "persisted preferences and personality", "reasonable rest", "current_time_utc", "2026-09-15T23:08:09.123456789Z"} {
		if !strings.Contains(defaultPrompt, marker) {
			t.Fatalf("life default prompt omitted %q: %s", marker, defaultPrompt)
		}
	}

	config.Prompt = func(airuntime.Profile, Snapshot) (string, error) { return "custom prompt", nil }
	customSupervisor, err := New(context.Background(), store, nil, config)
	if err != nil {
		t.Fatal(err)
	}
	customPrompt, err := customSupervisor.buildPrompt(profile, Snapshot{GameReady: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(customPrompt, "custom prompt") {
		t.Fatalf("custom life prompt discarded custom base: %s", customPrompt)
	}
	for _, marker := range []string{"Life mode", "persisted preferences and personality", "reasonable rest", `"current_time_utc":"2026-09-15T23:08:09.123456789Z"`} {
		if !strings.Contains(customPrompt, marker) {
			t.Fatalf("custom life prompt omitted %q: %s", marker, customPrompt)
		}
	}

	nonLife := testSupervisorProfile("non-life-prompt")
	if _, err := store.CreateProfile(context.Background(), nonLife); err != nil {
		t.Fatal(err)
	}
	nonLifePrompt, err := customSupervisor.buildPrompt(nonLife, Snapshot{GameReady: true})
	if err != nil {
		t.Fatal(err)
	}
	if nonLifePrompt != "custom prompt" {
		t.Fatalf("non-life custom prompt changed: %q", nonLifePrompt)
	}
}
