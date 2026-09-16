package aisupervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

type fakeRunner struct {
	mu       sync.Mutex
	requests []aicodex.RunRequest
	run      func(context.Context, aicodex.RunRequest) (aicodex.Result, error)
}

func (runner *fakeRunner) Run(ctx context.Context, request aicodex.RunRequest) (aicodex.Result, error) {
	runner.mu.Lock()
	runner.requests = append(runner.requests, request)
	fn := runner.run
	runner.mu.Unlock()
	if fn == nil {
		return completedResult(request), nil
	}
	return fn(ctx, request)
}

func (runner *fakeRunner) Requests() []aicodex.RunRequest {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]aicodex.RunRequest(nil), runner.requests...)
}

type fakeSession struct {
	runner *fakeRunner
	wake   chan struct{}

	mu       sync.Mutex
	snapshot Snapshot
	observe  func(context.Context) (Snapshot, error)
	closes   int
	closeFn  func()
}

func (session *fakeSession) agentSession() AgentSession {
	return AgentSession{
		Runner:  session.runner,
		Observe: session.observeSnapshot,
		Close:   session.close,
		Wake:    session.wake,
	}
}

func (session *fakeSession) observeSnapshot(ctx context.Context) (Snapshot, error) {
	session.mu.Lock()
	fn := session.observe
	snapshot := cloneSnapshot(session.snapshot)
	session.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return snapshot, nil
}

func (session *fakeSession) setSnapshot(snapshot Snapshot) {
	session.mu.Lock()
	session.snapshot = cloneSnapshot(snapshot)
	session.mu.Unlock()
}

func (session *fakeSession) close() {
	session.mu.Lock()
	session.closes++
	fn := session.closeFn
	session.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (session *fakeSession) closeCount() int {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.closes
}

type fakeFactory struct {
	mu       sync.Mutex
	sessions map[string]*fakeSession
	openErr  error
	opens    int
	openGate <-chan struct{}
}

func (factory *fakeFactory) Open(ctx context.Context, profile airuntime.Profile) (AgentSession, error) {
	factory.mu.Lock()
	factory.opens++
	session := factory.sessions[profile.ID]
	err := factory.openErr
	gate := factory.openGate
	factory.mu.Unlock()
	if gate != nil {
		select {
		case <-ctx.Done():
			return AgentSession{}, ctx.Err()
		case <-gate:
		}
	}
	if err != nil {
		return AgentSession{}, err
	}
	if session == nil {
		return AgentSession{}, fmt.Errorf("no fake session for %q", profile.ID)
	}
	return session.agentSession(), nil
}

func (factory *fakeFactory) openCount() int {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.opens
}

func testSupervisorStore(t *testing.T) *airuntime.Store {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "build", "ai"))
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(root, "supervisor-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	store, err := airuntime.OpenStore(filepath.Join(directory, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testSupervisorProfile(id string) airuntime.Profile {
	return airuntime.Profile{
		ID:               id,
		Account:          airuntime.AccountIdentity{ID: "account-" + id},
		Character:        airuntime.CharacterIdentity{ID: "character-" + id},
		Goal:             airuntime.Goal{Kind: "level", TargetLevel: 10, StopWhenCompleted: true},
		Skills:           []airuntime.SkillVersion{{Name: "walk", Version: "1"}},
		DailyTokenBudget: 100,
	}
}

func supervisorTestConfig() Config {
	return Config{
		PollInterval:           5 * time.Millisecond,
		ObserveTimeout:         100 * time.Millisecond,
		TurnTimeout:            500 * time.Millisecond,
		FailureBackoff:         time.Millisecond,
		MaxFailureBackoff:      5 * time.Millisecond,
		MaxRunFailures:         3,
		MaxObservationFailures: 3,
		NoProgressLimit:        32,
		NoProgressWindow:       time.Hour,
		TokenCharge:            1,
	}
}

func completedResult(request aicodex.RunRequest) aicodex.Result {
	threadID := request.ThreadID
	if threadID == "" {
		threadID = "thread-1"
	}
	return aicodex.Result{
		ProfileID: request.ProfileID,
		ThreadID:  threadID,
		Usage:     aicodex.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3},
		Turn:      aicodex.TurnResult{Status: aicodex.TurnCompleted},
		Process:   aicodex.ProcessResult{Status: aicodex.ProcessExited, ExitCode: 0},
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}

func profileStatus(t *testing.T, supervisor *Supervisor, id string) Status {
	t.Helper()
	status, err := supervisor.Status(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestSupervisorResumesExactCheckpointThread(t *testing.T) {
	store := testSupervisorStore(t)
	profile, err := store.CreateProfile(context.Background(), testSupervisorProfile("resume"))
	if err != nil {
		t.Fatal(err)
	}
	session := &fakeSession{runner: &fakeRunner{}, wake: make(chan struct{}, 4), snapshot: Snapshot{GameReady: true, ProgressKey: "initial"}}
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
		checkpoint, checkpointErr := store.GetCheckpoint(context.Background(), profile.ID)
		return checkpointErr == nil && containsCheckpointThread(checkpoint.State, "thread-1")
	})
	if requests := session.runner.Requests(); len(requests) != 1 || requests[0].Resume || requests[0].ThreadID != "" {
		t.Fatalf("initial request = %#v", requests)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}

	// The first supervisor leaves the profile and its durable checkpoint
	// available for a new process. Change the observation before waking it so
	// the new event authorizes another exact-thread turn.
	session.setSnapshot(Snapshot{GameReady: true, ProgressKey: "after-event"})
	session.wake = make(chan struct{}, 4)
	supervisor2, err := New(context.Background(), store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor2.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	// The factory returns the same fake session object, so its new wake channel
	// is used by the new AgentSession.
	session.wake <- struct{}{}
	waitFor(t, func() bool { return len(session.runner.Requests()) == 2 })
	requests := session.runner.Requests()
	if requests[0].RequestID == "" || requests[1].RequestID == "" || requests[0].RequestID == requests[1].RequestID {
		t.Fatal("supervisor turns did not receive distinct durable request identities")
	}
	if !requests[1].Resume || requests[1].ThreadID != "thread-1" {
		t.Fatalf("resume request = %#v", requests[1])
	}
	if err := supervisor2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorGoalCompleteObservationDoesNotRunCodex(t *testing.T) {
	store := testSupervisorStore(t)
	profile, err := store.CreateProfile(context.Background(), testSupervisorProfile("complete"))
	if err != nil {
		t.Fatal(err)
	}
	session := &fakeSession{runner: &fakeRunner{}, snapshot: Snapshot{GameReady: true, GoalComplete: true}}
	factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}
	supervisor, err := New(context.Background(), store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return profileStatus(t, supervisor, profile.ID).State == StateCompleted })
	if calls := len(session.runner.Requests()); calls != 0 {
		t.Fatalf("goal-complete observation started %d Codex turns", calls)
	}
	updated, err := store.GetProfile(context.Background(), profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != airuntime.ProfileStatusStopped {
		t.Fatalf("profile status after completion = %q", updated.Status)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorPollsActiveTasksWithoutStartingAnotherTurn(t *testing.T) {
	store := testSupervisorStore(t)
	profile, err := store.CreateProfile(context.Background(), testSupervisorProfile("tasks"))
	if err != nil {
		t.Fatal(err)
	}
	session := &fakeSession{runner: &fakeRunner{}, wake: make(chan struct{}, 2), snapshot: Snapshot{
		GameReady:   true,
		ActiveTasks: []TaskHandle{{Handle: "task-1", Status: TaskStatusRunning}},
	}}
	factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}
	config := supervisorTestConfig()
	config.NoProgressLimit = 32
	supervisor, err := New(context.Background(), store, factory, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	if calls := len(session.runner.Requests()); calls != 0 {
		t.Fatalf("active task started %d Codex turns", calls)
	}
	session.setSnapshot(Snapshot{GameReady: true, ProgressKey: "task-done"})
	session.wake <- struct{}{}
	waitFor(t, func() bool { return len(session.runner.Requests()) == 1 })
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorDoesNotPauseUnchangedWaitingSnapshotBeforeNoProgressWindow(t *testing.T) {
	store := testSupervisorStore(t)
	profile, err := store.CreateProfile(context.Background(), testSupervisorProfile("waiting-window"))
	if err != nil {
		t.Fatal(err)
	}
	session := &fakeSession{runner: &fakeRunner{}, snapshot: Snapshot{GameReady: true}}
	factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}
	config := supervisorTestConfig()
	config.NoProgressLimit = 2
	config.NoProgressWindow = 100 * time.Millisecond
	supervisor, err := New(context.Background(), store, factory, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(session.runner.Requests()) == 1 })
	// Polling an unchanged, event-driven waiting snapshot is normal while a
	// game action or NPC response is in flight. It must not consume the
	// diagnosis count before the configured time window elapses.
	time.Sleep(25 * time.Millisecond)
	status := profileStatus(t, supervisor, profile.ID)
	if status.State == StatePaused || status.NoProgressCount != 0 {
		t.Fatalf("premature no-progress pause: %#v", status)
	}
	waitFor(t, func() bool { return profileStatus(t, supervisor, profile.ID).State == StatePaused })
	status = profileStatus(t, supervisor, profile.ID)
	if status.NoProgressCount != config.NoProgressLimit {
		t.Fatalf("diagnosis count = %d, want %d", status.NoProgressCount, config.NoProgressLimit)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorPromptUsesOnlyConfirmedMemoriesForProfile(t *testing.T) {
	store := testSupervisorStore(t)
	first, err := store.CreateProfile(context.Background(), testSupervisorProfile("memory-one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateProfile(context.Background(), testSupervisorProfile("memory-two"))
	if err != nil {
		t.Fatal(err)
	}
	firstEvent, err := store.AppendEvent(context.Background(), first.ID, "game.friend_confirmed", "game", []byte(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordConfirmedMemory(context.Background(), first.ID, airuntime.MemoryInput{
		Kind: "relationship", Subject: "friend", Content: []byte(`{"note":"first-memory"}`),
		SourceEventID: firstEvent.ID, Confirmed: true, Actor: "game",
	}); err != nil {
		t.Fatal(err)
	}
	secondEvent, err := store.AppendEvent(context.Background(), second.ID, "game.friend_confirmed", "game", []byte(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordConfirmedMemory(context.Background(), second.ID, airuntime.MemoryInput{
		Kind: "relationship", Subject: "friend", Content: []byte(`{"note":"second-memory"}`),
		SourceEventID: secondEvent.ID, Confirmed: true, Actor: "game",
	}); err != nil {
		t.Fatal(err)
	}
	supervisor, err := New(context.Background(), store, nil, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	supervisor.cfg.Prompt = func(airuntime.Profile, Snapshot) (string, error) { return "base prompt", nil }
	prompt, err := supervisor.buildPrompt(first, Snapshot{GameReady: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "first-memory") || strings.Contains(prompt, "second-memory") || !strings.HasPrefix(prompt, "base prompt") {
		t.Fatalf("profile memory prompt = %q", prompt)
	}
}

func TestSupervisorPauseRevokesSessionBeforeCancellingRunner(t *testing.T) {
	store := testSupervisorStore(t)
	profile, err := store.CreateProfile(context.Background(), testSupervisorProfile("pause-order"))
	if err != nil {
		t.Fatal(err)
	}
	runnerStarted := make(chan struct{})
	cancelObserved := make(chan struct{})
	closeCalled := make(chan struct{})
	var badOrder atomic.Bool
	runner := &fakeRunner{}
	runner.run = func(ctx context.Context, request aicodex.RunRequest) (aicodex.Result, error) {
		select {
		case <-runnerStarted:
		default:
			close(runnerStarted)
		}
		<-ctx.Done()
		select {
		case <-closeCalled:
		default:
			badOrder.Store(true)
		}
		close(cancelObserved)
		return aicodex.Result{ProfileID: request.ProfileID}, ctx.Err()
	}
	session := &fakeSession{runner: runner, snapshot: Snapshot{GameReady: true}}
	session.closeFn = func() {
		select {
		case <-closeCalled:
		default:
			close(closeCalled)
		}
	}
	factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}
	supervisor, err := New(context.Background(), store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runnerStarted:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	if err := supervisor.Pause(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelObserved:
	default:
		t.Fatal("pause returned before runner cancellation was observed")
	}
	if badOrder.Load() {
		t.Fatal("runner context was cancelled before session.Close")
	}
	if session.closeCount() != 1 {
		t.Fatalf("session close count = %d, want one", session.closeCount())
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorProfilesHaveIndependentSessions(t *testing.T) {
	store := testSupervisorStore(t)
	first, err := store.CreateProfile(context.Background(), testSupervisorProfile("one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateProfile(context.Background(), testSupervisorProfile("two"))
	if err != nil {
		t.Fatal(err)
	}
	firstSession := &fakeSession{runner: &fakeRunner{}, snapshot: Snapshot{GameReady: true}}
	secondSession := &fakeSession{runner: &fakeRunner{}, snapshot: Snapshot{GameReady: true}}
	factory := &fakeFactory{sessions: map[string]*fakeSession{first.ID: firstSession, second.ID: secondSession}}
	supervisor, err := New(context.Background(), store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background(), second.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return len(firstSession.runner.Requests()) == 1 && len(secondSession.runner.Requests()) == 1
	})
	if firstSession.runner.Requests()[0].ProfileID != first.ID || secondSession.runner.Requests()[0].ProfileID != second.ID {
		t.Fatal("profile sessions were not isolated")
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorFactoryFailureNeverMarksProfileActive(t *testing.T) {
	for _, test := range []struct {
		name    string
		factory Factory
	}{
		{name: "nil", factory: nil},
		{name: "open-error", factory: &fakeFactory{openErr: errors.New("connection failed"), sessions: map[string]*fakeSession{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := testSupervisorStore(t)
			profile, err := store.CreateProfile(context.Background(), testSupervisorProfile("factory-"+test.name))
			if err != nil {
				t.Fatal(err)
			}
			supervisor, err := New(context.Background(), store, test.factory, supervisorTestConfig())
			if err != nil {
				t.Fatal(err)
			}
			err = supervisor.Start(context.Background(), profile.ID)
			if test.name == "nil" && !errors.Is(err, ErrFactoryUnavailable) {
				t.Fatalf("nil factory error = %v", err)
			}
			if test.name == "open-error" && err == nil {
				t.Fatal("open error was swallowed")
			}
			loaded, loadErr := store.GetProfile(context.Background(), profile.ID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if loaded.Status != airuntime.ProfileStatusStopped {
				t.Fatalf("profile status after factory failure = %q", loaded.Status)
			}
			if closeErr := supervisor.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		})
	}
}

func TestSupervisorConcurrentStartOpensOneSession(t *testing.T) {
	store := testSupervisorStore(t)
	profile, err := store.CreateProfile(context.Background(), testSupervisorProfile("singleflight"))
	if err != nil {
		t.Fatal(err)
	}
	session := &fakeSession{runner: &fakeRunner{}, snapshot: Snapshot{GameReady: false}}
	openGate := make(chan struct{})
	factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}, openGate: openGate}
	supervisor, err := New(context.Background(), store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() { errs <- supervisor.Start(context.Background(), profile.ID) }()
	waitFor(t, func() bool { return factory.openCount() == 1 })
	go func() { errs <- supervisor.Start(context.Background(), profile.ID) }()
	time.Sleep(5 * time.Millisecond)
	if factory.openCount() != 1 {
		t.Fatalf("factory opened %d sessions while start was pending", factory.openCount())
	}
	close(openGate)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if factory.openCount() != 1 {
		t.Fatalf("factory opened %d sessions", factory.openCount())
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorRunFailuresBackoffThenPauseAndChargeFailures(t *testing.T) {
	store := testSupervisorStore(t)
	profileInput := testSupervisorProfile("failures")
	profileInput.DailyTokenBudget = 10
	profile, err := store.CreateProfile(context.Background(), profileInput)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{run: func(ctx context.Context, request aicodex.RunRequest) (aicodex.Result, error) {
		return aicodex.Result{ProfileID: request.ProfileID}, errors.New("temporary runner failure")
	}}
	session := &fakeSession{runner: runner, snapshot: Snapshot{GameReady: true}}
	factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}
	config := supervisorTestConfig()
	config.MaxRunFailures = 2
	supervisor, err := New(context.Background(), store, factory, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return profileStatus(t, supervisor, profile.ID).State == StatePaused })
	if calls := len(runner.Requests()); calls != 2 {
		t.Fatalf("runner calls = %d, want two bounded retries", calls)
	}
	usage, err := store.TokenUsage(context.Background(), profile.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if usage.Attempts != 2 || usage.FailedAttempts != 2 || usage.ChargedTokens != 2 {
		t.Fatalf("failure token accounting = %#v", usage)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorTokenBudgetRejectsTurnAndPersistsCompletedBoundary(t *testing.T) {
	t.Run("reject-before-run", func(t *testing.T) {
		store := testSupervisorStore(t)
		profileInput := testSupervisorProfile("budget-reject")
		profileInput.DailyTokenBudget = 1
		profile, err := store.CreateProfile(context.Background(), profileInput)
		if err != nil {
			t.Fatal(err)
		}
		session := &fakeSession{runner: &fakeRunner{}, snapshot: Snapshot{GameReady: true}}
		factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}
		config := supervisorTestConfig()
		config.TokenCharge = 2
		supervisor, err := New(context.Background(), store, factory, config)
		if err != nil {
			t.Fatal(err)
		}
		if err := supervisor.Start(context.Background(), profile.ID); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return profileStatus(t, supervisor, profile.ID).State == StatePaused })
		if calls := len(session.runner.Requests()); calls != 0 {
			t.Fatalf("budget rejection started %d turns", calls)
		}
		usage, err := store.TokenUsage(context.Background(), profile.ID, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if usage.OverBudgetAttempts != 1 || usage.Attempts != 1 {
			t.Fatalf("budget rejection accounting = %#v", usage)
		}
		if err := supervisor.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("persist-after-boundary", func(t *testing.T) {
		store := testSupervisorStore(t)
		profileInput := testSupervisorProfile("budget-boundary")
		profileInput.DailyTokenBudget = 1
		profile, err := store.CreateProfile(context.Background(), profileInput)
		if err != nil {
			t.Fatal(err)
		}
		session := &fakeSession{runner: &fakeRunner{}, snapshot: Snapshot{GameReady: true}}
		factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}
		config := supervisorTestConfig()
		supervisor, err := New(context.Background(), store, factory, config)
		if err != nil {
			t.Fatal(err)
		}
		if err := supervisor.Start(context.Background(), profile.ID); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return profileStatus(t, supervisor, profile.ID).State == StatePaused })
		checkpoint, err := store.GetCheckpoint(context.Background(), profile.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !containsCheckpointThread(checkpoint.State, "thread-1") {
			t.Fatalf("completed boundary checkpoint did not preserve thread: %s", checkpoint.State)
		}
		if err := supervisor.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func containsCheckpointThread(data []byte, thread string) bool {
	return len(data) > 0 && string(data) != "" &&
		// The checkpoint is JSON and this exact substring is sufficient for the
		// test while keeping the test independent of private persistence types.
		contains(string(data), `"thread_id":"`+thread+`"`)
}

func contains(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
