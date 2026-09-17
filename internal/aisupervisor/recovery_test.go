package aisupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

type recoveryTransport struct {
	*fakeRunner
	recovered chan aicodex.RunRequest
}

func (runner *recoveryTransport) Recover(_ context.Context, request aicodex.RunRequest) (aicodex.Result, error) {
	runner.recovered <- request
	return completedResult(request), nil
}

type recoveryTransportFactory struct {
	*fakeFactory
	runner Runner
}

func TestSupervisorUncertainContainerTurnRetainsOriginalReservation(t *testing.T) {
	store := testSupervisorStore(t)
	ctx := context.Background()
	input := testSupervisorProfile("uncertain-container")
	input.Status = airuntime.ProfileStatusActive
	profile, err := store.CreateProfile(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recoveryTransport{fakeRunner: &fakeRunner{run: func(context.Context, aicodex.RunRequest) (aicodex.Result, error) {
		return aicodex.Result{}, errors.New("container result unavailable")
	}}, recovered: make(chan aicodex.RunRequest, 1)}
	session := &fakeSession{snapshot: Snapshot{GameReady: true}}
	factory := recoveryTransportFactory{fakeFactory: &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}, runner: runner}
	supervisor, err := New(ctx, store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	if err := supervisor.Start(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return profileStatus(t, supervisor, profile.ID).State == StatePaused })
	requests := runner.Requests()
	attempt, err := store.PendingTokenAttempt(ctx, profile.ID)
	if err != nil || len(requests) != 1 || attempt.ID != requests[0].RequestID || attempt.State != airuntime.TokenAttemptUnknown || attempt.Outcome != nil {
		t.Fatalf("uncertain turn was settled or lost: %+v requests=%+v err=%v", attempt, requests, err)
	}
	usage, err := store.TokenUsage(ctx, profile.ID, time.Now())
	if err != nil || usage.Attempts != 1 || usage.ChargedTokens != 0 || usage.ReservedTokens == 0 {
		t.Fatalf("uncertain turn accounting: %+v %v", usage, err)
	}
	// The operator's continue action goes through Start, including the
	// paused-to-active profile version change and a new game session.
	if err := supervisor.Start(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		checkpoint, err := store.GetCheckpoint(ctx, profile.ID)
		return err == nil && containsCheckpointThread(checkpoint.State, "thread-1") && !contains(string(checkpoint.State), "pending_attempt_id")
	})
	select {
	case recovered := <-runner.recovered:
		if recovered.RequestID != requests[0].RequestID || recovered.Prompt != requests[0].Prompt {
			t.Fatalf("continue replaced the unresolved request: %+v", recovered)
		}
	default:
		t.Fatal("continue did not reconcile the original request")
	}
	usage, err = store.TokenUsage(ctx, profile.ID, time.Now())
	if err != nil || usage.Attempts != 1 || usage.ReservedTokens != 0 || usage.ChargedTokens == 0 || len(runner.Requests()) != 1 {
		t.Fatalf("continue duplicated the model turn or accounting: %+v %v", usage, err)
	}
}

func (factory recoveryTransportFactory) Open(ctx context.Context, profile airuntime.Profile) (AgentSession, error) {
	session, err := factory.fakeFactory.Open(ctx, profile)
	session.Runner = factory.runner
	return session, err
}

func TestSupervisorOutcomeWriteFailureRetainsRecoverableRequest(t *testing.T) {
	store := testSupervisorStore(t)
	ctx := context.Background()
	input := testSupervisorProfile("outcome-write-failure")
	input.Status = airuntime.ProfileStatusActive
	profile, err := store.CreateProfile(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`CREATE TRIGGER reject_test_outcome BEFORE UPDATE OF outcome_json ON ai_token_attempts BEGIN SELECT RAISE(FAIL, 'test outcome write failure'); END`); err != nil {
		t.Fatal(err)
	}
	runner := &recoveryTransport{fakeRunner: &fakeRunner{}, recovered: make(chan aicodex.RunRequest, 1)}
	session := &fakeSession{snapshot: Snapshot{GameReady: true}}
	factory := recoveryTransportFactory{fakeFactory: &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}, runner: runner}
	supervisor, err := New(ctx, store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	if err := supervisor.Start(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return profileStatus(t, supervisor, profile.ID).State == StatePaused })
	requests := runner.Requests()
	pending, err := store.PendingTokenAttempt(ctx, profile.ID)
	if err != nil || len(requests) != 1 || pending.ID != requests[0].RequestID || pending.State != airuntime.TokenAttemptUnknown || pending.Outcome != nil {
		t.Fatalf("failed outcome write lost recovery state: %+v %v", pending, err)
	}
	usage, err := store.TokenUsage(ctx, profile.ID, time.Now())
	if err != nil || usage.ChargedTokens != 0 || usage.ReservedTokens == 0 {
		t.Fatalf("unrecorded outcome was settled: %+v %v", usage, err)
	}
	if _, err := store.DB().Exec(`DROP TRIGGER reject_test_outcome`); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		checkpoint, err := store.GetCheckpoint(ctx, profile.ID)
		return err == nil && containsCheckpointThread(checkpoint.State, "thread-1") && !contains(string(checkpoint.State), "pending_attempt_id")
	})
	select {
	case request := <-runner.recovered:
		if request.RequestID != pending.ID {
			t.Fatal("write failure recovery replaced request")
		}
	default:
		t.Fatal("write failure did not invoke recovery")
	}
	usage, err = store.TokenUsage(ctx, profile.ID, time.Now())
	if err != nil || usage.Attempts != 1 || usage.ReservedTokens != 0 || len(runner.Requests()) != 1 {
		t.Fatalf("write failure duplicated request or reservation: %+v %v", usage, err)
	}
}

func TestSupervisorReconcilesDispatchedAndUnknownThroughRecoveryTransport(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		store := testSupervisorStore(t)
		ctx := context.Background()
		input := testSupervisorProfile("transport-recovery")
		input.Status = airuntime.ProfileStatusActive
		profile, err := store.CreateProfile(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		reservation, err := store.BeginTokenAttempt(ctx, profile.ID, time.Now(), 1)
		if err != nil {
			t.Fatal(err)
		}
		metadata := airuntime.TokenAttemptMetadata{Prompt: "original request", Resume: true, ThreadID: "original-thread"}
		if err := store.PrepareTokenAttempt(ctx, reservation, metadata); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkTokenAttemptDispatched(ctx, reservation); err != nil {
			t.Fatal(err)
		}
		if unknown {
			if err := store.MarkTokenAttemptUnknown(ctx, reservation, "interrupted transport"); err != nil {
				t.Fatal(err)
			}
		}
		recoveryCheckpoint(t, store, profile, reservation.ID)
		runner := &recoveryTransport{fakeRunner: &fakeRunner{}, recovered: make(chan aicodex.RunRequest, 1)}
		session := &fakeSession{snapshot: Snapshot{GameReady: true}}
		factory := recoveryTransportFactory{fakeFactory: &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}, runner: runner}
		supervisor, err := New(ctx, store, factory, supervisorTestConfig())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = supervisor.Close() })
		if err := supervisor.Start(ctx, profile.ID); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool {
			checkpoint, err := store.GetCheckpoint(ctx, profile.ID)
			return err == nil && containsCheckpointThread(checkpoint.State, metadata.ThreadID) && !contains(string(checkpoint.State), "pending_attempt_id")
		})
		select {
		case request := <-runner.recovered:
			if request.RequestID != reservation.ID || request.Prompt != metadata.Prompt || request.ThreadID != metadata.ThreadID || !request.Resume {
				t.Fatalf("recovery changed request: %+v", request)
			}
		default:
			t.Fatal("recovery transport was never called")
		}
		if len(runner.Requests()) != 0 {
			t.Fatal("recovery started a fresh model turn")
		}
		usage, err := store.TokenUsage(ctx, profile.ID, time.Now())
		if err != nil || usage.Attempts != 1 || usage.ChargedTokens != 1 || usage.ReservedTokens != 0 {
			t.Fatalf("recovery accounting: %+v %v", usage, err)
		}
		if err := supervisor.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func recoveryCheckpoint(t *testing.T, store *airuntime.Store, profile airuntime.Profile, attemptID string) {
	t.Helper()
	state := persistedState{
		ProfileVersion:   profile.Version,
		PendingAttemptID: attemptID,
		Snapshot:         Snapshot{GameReady: true},
	}
	data, err := jsonMarshalRecovery(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveCheckpointCAS(context.Background(), profile.ID, 0, data, "test"); err != nil {
		t.Fatal(err)
	}
}

// Keep the test helper local so recovery tests do not depend on the exact
// JSON encoding details outside this package.
func jsonMarshalRecovery(state persistedState) ([]byte, error) {
	return json.Marshal(state)
}

func recoveredOutcome(threadID string) airuntime.TokenAttemptOutcome {
	return airuntime.TokenAttemptOutcome{
		ThreadID: threadID, TurnStatus: string(aicodex.TurnCompleted),
		ProcessStatus: string(aicodex.ProcessExited), TurnCompleted: true,
		CheckpointState: string(aicodex.CheckpointCompleted),
		Usage:           airuntime.TokenUsage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3},
	}
}

func TestSupervisorRecoversRecordedAttemptWithoutRunningAgain(t *testing.T) {
	store := testSupervisorStore(t)
	profileInput := testSupervisorProfile("recover-recorded")
	profileInput.Status = airuntime.ProfileStatusActive
	profile, err := store.CreateProfile(context.Background(), profileInput)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.BeginTokenAttempt(context.Background(), profile.ID, time.Now(), 1)
	if err != nil {
		t.Fatal(err)
	}
	metadata := airuntime.TokenAttemptMetadata{Prompt: "keep this exact prompt"}
	if err := store.PrepareTokenAttempt(context.Background(), reservation, metadata); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTokenAttemptDispatched(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	recoveryCheckpoint(t, store, profile, reservation.ID)
	if err := store.RecordTokenAttemptResult(context.Background(), reservation, recoveredOutcome("recovered-thread"), false); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{}
	session := &fakeSession{runner: runner, snapshot: Snapshot{GameReady: true}}
	supervisor, err := New(context.Background(), store, &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		attempt, getErr := store.GetTokenAttempt(context.Background(), reservation.ID)
		if getErr != nil || attempt.State != airuntime.TokenAttemptSettled {
			return false
		}
		checkpoint, checkpointErr := store.GetCheckpoint(context.Background(), profile.ID)
		return checkpointErr == nil && !contains(string(checkpoint.State), "pending_attempt_id")
	})
	if requests := runner.Requests(); len(requests) != 0 {
		t.Fatalf("recorded recovery ran %d new turns: %#v", len(requests), requests)
	}
	attempt, err := store.GetTokenAttempt(context.Background(), reservation.ID)
	if err != nil || attempt.Prompt != metadata.Prompt || attempt.ThreadID != "" {
		// ThreadID is request metadata for the initial turn and remains empty;
		// the recovered result carries the newly-created exact thread.
		t.Fatalf("recovered attempt=%+v err=%v", attempt, err)
	}
	usage, err := store.TokenUsage(context.Background(), profile.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if usage.ChargedTokens != 1 || usage.ReservedTokens != 0 {
		t.Fatalf("recovered accounting=%+v", usage)
	}
	checkpoint, err := store.GetCheckpoint(context.Background(), profile.ID)
	if err != nil || !containsCheckpointThread(checkpoint.State, "recovered-thread") || contains(string(checkpoint.State), "pending_attempt_id") {
		t.Fatalf("recovered checkpoint=%s err=%v", checkpoint.State, err)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorRecoversSettledAttemptAfterCheckpointCrash(t *testing.T) {
	store := testSupervisorStore(t)
	profileInput := testSupervisorProfile("recover-settled")
	profileInput.Status = airuntime.ProfileStatusActive
	profile, err := store.CreateProfile(context.Background(), profileInput)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.BeginTokenAttempt(context.Background(), profile.ID, time.Now(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareTokenAttempt(context.Background(), reservation, airuntime.TokenAttemptMetadata{Prompt: "settled prompt"}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTokenAttemptResult(context.Background(), reservation, recoveredOutcome("settled-thread"), false); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishTokenAttempt(context.Background(), reservation, airuntime.TokenUsage{}, false); err != nil {
		t.Fatal(err)
	}
	// The settlement committed, but the supervisor checkpoint still points to
	// the attempt. This is the crash window after billing and before persist.
	recoveryCheckpoint(t, store, profile, reservation.ID)
	runner := &fakeRunner{}
	session := &fakeSession{runner: runner, snapshot: Snapshot{GameReady: true}}
	supervisor, err := New(context.Background(), store, &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		checkpoint, getErr := store.GetCheckpoint(context.Background(), profile.ID)
		return getErr == nil && containsCheckpointThread(checkpoint.State, "settled-thread")
	})
	if requests := runner.Requests(); len(requests) != 0 {
		t.Fatalf("settled recovery ran %d new turns", len(requests))
	}
	usage, err := store.TokenUsage(context.Background(), profile.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if usage.ChargedTokens != 1 || usage.Attempts != 1 {
		t.Fatalf("settled recovery double-charged=%+v", usage)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorUnknownAttemptPreservesReservationAndPauses(t *testing.T) {
	store := testSupervisorStore(t)
	profileInput := testSupervisorProfile("recover-unknown")
	profileInput.Status = airuntime.ProfileStatusActive
	profile, err := store.CreateProfile(context.Background(), profileInput)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.BeginTokenAttempt(context.Background(), profile.ID, time.Now(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PrepareTokenAttempt(context.Background(), reservation, airuntime.TokenAttemptMetadata{Prompt: "unknown prompt"}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkTokenAttemptDispatched(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	recoveryCheckpoint(t, store, profile, reservation.ID)
	runner := &fakeRunner{}
	session := &fakeSession{runner: runner, snapshot: Snapshot{GameReady: true}}
	supervisor, err := New(context.Background(), store, &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return profileStatus(t, supervisor, profile.ID).State == StatePaused })
	if requests := runner.Requests(); len(requests) != 0 {
		t.Fatalf("unknown recovery ran %d new turns", len(requests))
	}
	attempt, err := store.GetTokenAttempt(context.Background(), reservation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != airuntime.TokenAttemptUnknown || attempt.Prompt != "unknown prompt" {
		t.Fatalf("unknown attempt=%+v", attempt)
	}
	usage, err := store.TokenUsage(context.Background(), profile.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if usage.ReservedTokens != 1 || usage.ChargedTokens != 0 {
		t.Fatalf("unknown reservation was released=%+v", usage)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorPreparedAttemptReusesOriginalRequest(t *testing.T) {
	store := testSupervisorStore(t)
	profileInput := testSupervisorProfile("recover-prepared")
	profileInput.Status = airuntime.ProfileStatusActive
	profile, err := store.CreateProfile(context.Background(), profileInput)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.BeginTokenAttempt(context.Background(), profile.ID, time.Now(), 1)
	if err != nil {
		t.Fatal(err)
	}
	metadata := airuntime.TokenAttemptMetadata{Prompt: "prepared prompt", Resume: true, ThreadID: "prior-thread"}
	if err := store.PrepareTokenAttempt(context.Background(), reservation, metadata); err != nil {
		t.Fatal(err)
	}
	recoveryCheckpoint(t, store, profile, reservation.ID)
	runner := &fakeRunner{}
	session := &fakeSession{runner: runner, snapshot: Snapshot{GameReady: true}}
	supervisor, err := New(context.Background(), store, &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Start(context.Background(), profile.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		attempt, getErr := store.GetTokenAttempt(context.Background(), reservation.ID)
		return getErr == nil && attempt.State == airuntime.TokenAttemptSettled
	})
	requests := runner.Requests()
	if len(requests) != 1 || requests[0].RequestID != reservation.ID || requests[0].Prompt != metadata.Prompt || !requests[0].Resume || requests[0].ThreadID != metadata.ThreadID {
		t.Fatalf("prepared recovery request=%#v", requests)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
}

// Compile-time check that the ordinary fake runner is intentionally not an
// idempotent recovery transport.
var _ Runner = (*fakeRunner)(nil)
