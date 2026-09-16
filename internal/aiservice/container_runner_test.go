package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

type containerRunnerFakeBroker struct {
	mu      sync.Mutex
	runs    []airunner.ExecuteRequest
	lookups []string
	run     func(context.Context, airunner.ExecuteRequest) (aibroker.RunResult, error)
	lookup  func(context.Context, string, string) (aibroker.RunResult, error)
}

func (broker *containerRunnerFakeBroker) Run(ctx context.Context, request airunner.ExecuteRequest) (aibroker.RunResult, error) {
	broker.mu.Lock()
	broker.runs = append(broker.runs, request)
	run := broker.run
	broker.mu.Unlock()
	if run != nil {
		return run(ctx, request)
	}
	return completedContainerBrokerResult(request), nil
}

func (broker *containerRunnerFakeBroker) Lookup(ctx context.Context, profileID, requestID string) (aibroker.RunResult, error) {
	broker.mu.Lock()
	broker.lookups = append(broker.lookups, profileID+"\x00"+requestID)
	lookup := broker.lookup
	broker.mu.Unlock()
	if lookup != nil {
		return lookup(ctx, profileID, requestID)
	}
	return aibroker.RunResult{}, aibroker.ErrJournalNotFound
}

func (broker *containerRunnerFakeBroker) runCount() int {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	return len(broker.runs)
}

func (broker *containerRunnerFakeBroker) lookupCount() int {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	return len(broker.lookups)
}

func (broker *containerRunnerFakeBroker) runRequests() []airunner.ExecuteRequest {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	result := make([]airunner.ExecuteRequest, len(broker.runs))
	copy(result, broker.runs)
	return result
}

func newContainerRunnerForTest(t *testing.T, broker ContainerBroker, timeouts ...time.Duration) (*ContainerRunner, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "profile-state")
	key := "container-model-secret"
	token := strings.Repeat("z", 43)
	var timeout time.Duration
	if len(timeouts) > 0 {
		timeout = timeouts[0]
	}
	runner, err := NewContainerRunner(ContainerRunnerConfig{
		ProfileID: "container-profile", StateRoot: root, Broker: broker,
		TurnTimeout: timeout,
		RequestTemplate: airunner.ExecuteRequest{
			Model:  airunner.Model{Provider: "deepseek", BaseURL: "https://api.deepseek.com/", Model: "deepseek-flash", APIKey: key},
			Skills: []airunner.Skill{{Name: "stoneage-play", Version: "1.0.0", Digest: "sha256:digest"}},
			MCP:    airunner.MCP{Endpoint: "http://gateway:9080/v1/game", Token: token, CharacterID: "character-1", Generation: 4},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner, key + "\x00" + token
}

func containerIntentRequest(prompt string, resume bool, threadID string) aicodex.RunRequest {
	return aicodex.RunRequest{ProfileID: "container-profile", Prompt: prompt, Resume: resume, ThreadID: threadID}
}

func containerCallerIntentRequest(callerID, prompt string, resume bool, threadID string) aicodex.RunRequest {
	return aicodex.RunRequest{ProfileID: "container-profile", RequestID: callerID, Prompt: prompt, Resume: resume, ThreadID: threadID}
}

func completedContainerBrokerResult(request airunner.ExecuteRequest) aibroker.RunResult {
	result := airunner.Result{
		ProfileID: request.ProfileID, ThreadID: "container-thread-1", LastMessage: "container-ok",
		Events:     []airunner.Event{{Type: "item.completed", ItemType: "agent_message", Item: json.RawMessage(`{"type":"agent_message","text":"container-ok"}`)}},
		Usage:      airunner.Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10},
		Turn:       airunner.Turn{Status: "completed", ID: "turn-1", Usage: airunner.Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10}},
		Process:    airunner.Process{Status: "exited", ExitCode: 0},
		Checkpoint: airunner.Checkpoint{Version: 1, ProfileID: request.ProfileID, ThreadID: "container-thread-1", State: "completed", TurnStatus: "completed", TurnCompleted: true},
	}
	return aibroker.RunResult{State: aibroker.RunCompleted, Response: airunner.Response{OK: true, ProfileID: request.ProfileID, RequestID: request.RequestID, Result: &result}}
}

func TestContainerRunnerPersistsIDBeforeRunAndReusesCompletedOutcome(t *testing.T) {
	broker := &containerRunnerFakeBroker{}
	runner, secrets := newContainerRunnerForTest(t, broker)
	request := containerIntentRequest("observe", false, "")
	result, err := runner.Run(context.Background(), request)
	if err != nil || result.ThreadID != "container-thread-1" || result.Usage.TotalTokens != 10 {
		t.Fatalf("first result=%+v err=%v", result, err)
	}
	if broker.runCount() != 1 {
		t.Fatalf("broker run count=%d, want 1", broker.runCount())
	}
	data, err := os.ReadFile(runner.CheckpointPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), strings.Split(secrets, "\x00")[0]) || strings.Contains(string(data), strings.Split(secrets, "\x00")[1]) {
		t.Fatalf("checkpoint contains credential: %s", data)
	}
	info, err := os.Stat(runner.CheckpointPath())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("checkpoint mode=%v err=%v", info.Mode().Perm(), err)
	}
	var checkpoint containerRunCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.State != containerRunCompleted || checkpoint.RequestID == "" || checkpoint.Intent != hashContainerIntent(request) {
		t.Fatalf("checkpoint=%+v", checkpoint)
	}

	resultAgain, err := runner.Run(context.Background(), request)
	if err != nil || resultAgain.ThreadID != result.ThreadID || resultAgain.LastMessage != result.LastMessage {
		t.Fatalf("replayed result=%+v err=%v", resultAgain, err)
	}
	if broker.runCount() != 1 || broker.lookupCount() != 0 {
		t.Fatalf("replay broker calls runs=%d lookups=%d", broker.runCount(), broker.lookupCount())
	}

	newResult, err := runner.Run(context.Background(), containerIntentRequest("next", true, result.ThreadID))
	if err != nil || newResult.ThreadID != result.ThreadID {
		t.Fatalf("next result=%+v err=%v", newResult, err)
	}
	if broker.runCount() != 2 {
		t.Fatalf("new intent broker run count=%d, want 2", broker.runCount())
	}
	runs := broker.runRequests()
	if runs[0].RequestID == runs[1].RequestID || runs[1].Run.ThreadID != result.ThreadID || !runs[1].Run.Resume {
		t.Fatalf("request IDs/resume not isolated: %#v", runs)
	}
}

func TestContainerRunnerSameCallerRequestIDReplaysOnlyOneBrokerRun(t *testing.T) {
	broker := &containerRunnerFakeBroker{}
	runner, _ := newContainerRunnerForTest(t, broker)
	request := containerCallerIntentRequest("attempt_same", "observe", false, "")
	first, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("first result=%+v err=%v", first, err)
	}
	second, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("retry result=%+v err=%v", second, err)
	}
	if first.ThreadID != second.ThreadID || first.LastMessage != second.LastMessage || broker.runCount() != 1 || broker.lookupCount() != 0 {
		t.Fatalf("same caller was not replayed: first=%+v second=%+v runs=%d lookups=%d", first, second, broker.runCount(), broker.lookupCount())
	}
	data, err := os.ReadFile(runner.CheckpointPath())
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint containerRunCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.CallerRequestID != request.RequestID || checkpoint.RequestID != request.RequestID {
		t.Fatalf("caller identity not persisted: %+v", checkpoint)
	}
}

func TestContainerRunnerDifferentCallerMustResumeExactCompletedThread(t *testing.T) {
	broker := &containerRunnerFakeBroker{}
	runner, _ := newContainerRunnerForTest(t, broker)
	firstRequest := containerCallerIntentRequest("attempt_one", "observe", false, "")
	first, err := runner.Run(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []aicodex.RunRequest{
		containerCallerIntentRequest("attempt_two", "observe", false, ""),
		containerCallerIntentRequest("attempt_two", "observe", true, "wrong-thread"),
	} {
		if _, err := runner.Run(context.Background(), request); !errors.Is(err, ErrContainerRunnerRecovery) {
			t.Fatalf("request=%+v error=%v, want recovery", request, err)
		}
	}
	if broker.runCount() != 1 {
		t.Fatalf("rejected caller started a broker run: %d", broker.runCount())
	}
	second, err := runner.Run(context.Background(), containerCallerIntentRequest("attempt_two", "observe", true, first.ThreadID))
	if err != nil || second.ThreadID != first.ThreadID {
		t.Fatalf("exact resume result=%+v err=%v", second, err)
	}
	if broker.runCount() != 2 {
		t.Fatalf("exact resume broker runs=%d, want 2", broker.runCount())
	}
	runs := broker.runRequests()
	if runs[0].RequestID != "attempt_one" || runs[1].RequestID != "attempt_two" || !runs[1].Run.Resume || runs[1].Run.ThreadID != first.ThreadID {
		t.Fatalf("caller/broker IDs or resume mismatch: %+v", runs)
	}
}

func TestContainerRunnerPendingOrUnknownNewCallerReconcilesOldRequest(t *testing.T) {
	for _, state := range []containerRunState{containerRunPending, containerRunUnknown} {
		t.Run(string(state), func(t *testing.T) {
			broker := &containerRunnerFakeBroker{}
			broker.lookup = func(_ context.Context, profileID, requestID string) (aibroker.RunResult, error) {
				if profileID != "container-profile" || requestID != "old-attempt" {
					t.Fatalf("lookup identity=%q/%q", profileID, requestID)
				}
				return aibroker.RunResult{State: aibroker.RunRunning}, aibroker.ErrRunRunning
			}
			runner, _ := newContainerRunnerForTest(t, broker)
			request := containerCallerIntentRequest("new-attempt", "observe", false, "")
			checkpoint := containerRunCheckpoint{Version: containerRunnerVersion, ProfileID: runner.profileID, RequestID: "old-attempt", CallerRequestID: "old-caller", Intent: hashContainerIntent(request), State: state, UpdatedAt: time.Now().UTC()}
			if err := runner.saveCheckpoint(checkpoint); err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Run(context.Background(), request); !errors.Is(err, ErrContainerRunnerRecovery) {
				t.Fatalf("reconciliation error=%v", err)
			}
			if broker.lookupCount() != 1 || broker.runCount() != 0 {
				t.Fatalf("new caller bypassed old reconciliation: lookups=%d runs=%d", broker.lookupCount(), broker.runCount())
			}
		})
	}
}

func TestContainerRunnerDifferentCallerCannotClaimReconciledCompletion(t *testing.T) {
	broker := &containerRunnerFakeBroker{}
	runner, _ := newContainerRunnerForTest(t, broker)
	request := containerCallerIntentRequest("new-attempt", "observe", false, "")
	checkpoint := containerRunCheckpoint{Version: containerRunnerVersion, ProfileID: runner.profileID, RequestID: "old-attempt", CallerRequestID: "old-caller", Intent: hashContainerIntent(request), State: containerRunUnknown, UpdatedAt: time.Now().UTC()}
	if err := runner.saveCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	broker.lookup = func(_ context.Context, profileID, requestID string) (aibroker.RunResult, error) {
		return completedContainerBrokerResult(airunner.ExecuteRequest{ProfileID: profileID, RequestID: requestID}), nil
	}
	result, err := runner.Run(context.Background(), request)
	if !errors.Is(err, ErrContainerRunnerRecovery) || result.ThreadID != "container-thread-1" {
		t.Fatalf("reconciled old completion result=%+v err=%v", result, err)
	}
	if broker.lookupCount() != 1 || broker.runCount() != 0 {
		t.Fatalf("new caller started a broker run: lookups=%d runs=%d", broker.lookupCount(), broker.runCount())
	}
	data, readErr := os.ReadFile(runner.CheckpointPath())
	if readErr != nil {
		t.Fatal(readErr)
	}
	var updated containerRunCheckpoint
	if err := json.Unmarshal(data, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.State != containerRunCompleted || updated.CallerRequestID != "old-caller" {
		t.Fatalf("old completion was not retained: %+v", updated)
	}
}

func TestContainerRunnerRecoverUsesOriginalReservationWithoutNewRun(t *testing.T) {
	broker := &containerRunnerFakeBroker{}
	runner, _ := newContainerRunnerForTest(t, broker)
	request := containerCallerIntentRequest("original-reservation", "observe", false, "")
	checkpoint := containerRunCheckpoint{Version: containerRunnerVersion, ProfileID: runner.profileID,
		RequestID: request.RequestID, CallerRequestID: request.RequestID,
		Intent: hashContainerIntent(request), State: containerRunUnknown, UpdatedAt: time.Now().UTC()}
	if err := runner.saveCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	broker.lookup = func(_ context.Context, profileID, requestID string) (aibroker.RunResult, error) {
		if requestID != request.RequestID {
			t.Fatalf("recovery replaced reservation: %s", requestID)
		}
		return completedContainerBrokerResult(airunner.ExecuteRequest{ProfileID: profileID, RequestID: requestID}), nil
	}
	result, err := runner.Recover(context.Background(), request)
	if err != nil || result.ThreadID != "container-thread-1" || result.Usage.TotalTokens != 10 {
		t.Fatalf("original result was not recovered: %+v %v", result, err)
	}
	if broker.runCount() != 0 || broker.lookupCount() != 1 {
		t.Fatal("recovery started a new model run")
	}
	request.RequestID = ""
	if _, err := runner.Recover(context.Background(), request); !errors.Is(err, ErrContainerRunnerRecovery) {
		t.Fatalf("recovery without an original reservation was accepted: %v", err)
	}
}

func TestContainerRunnerFileLockWaitIsCancellable(t *testing.T) {
	broker := &containerRunnerFakeBroker{}
	started := make(chan struct{})
	release := make(chan struct{})
	broker.run = func(ctx context.Context, request airunner.ExecuteRequest) (aibroker.RunResult, error) {
		close(started)
		select {
		case <-release:
			return completedContainerBrokerResult(request), nil
		case <-ctx.Done():
			return aibroker.RunResult{State: aibroker.RunUnknown}, ctx.Err()
		}
	}
	root := filepath.Join(t.TempDir(), "profile-state")
	config := ContainerRunnerConfig{
		ProfileID: "container-profile", StateRoot: root, Broker: broker,
		RequestTemplate: airunner.ExecuteRequest{Model: airunner.Model{APIKey: "key"}, MCP: airunner.MCP{Token: strings.Repeat("x", 43)}},
	}
	firstRunner, err := NewContainerRunner(config)
	if err != nil {
		t.Fatal(err)
	}
	secondRunner, err := NewContainerRunner(config)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, runErr := firstRunner.Run(context.Background(), containerCallerIntentRequest("first-attempt", "observe", false, ""))
		firstDone <- runErr
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := secondRunner.Run(ctx, containerCallerIntentRequest("second-attempt", "observe", false, "")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended lock error=%v, want deadline", err)
	}
	if broker.runCount() != 1 {
		t.Fatalf("contended runner entered broker: %d runs", broker.runCount())
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first run error=%v", err)
	}
}

func TestContainerRunnerUnknownRequiresLookupAndBlocksDifferentIntent(t *testing.T) {
	unknownErr := errors.New("provider response was lost")
	broker := &containerRunnerFakeBroker{}
	broker.run = func(context.Context, airunner.ExecuteRequest) (aibroker.RunResult, error) {
		return aibroker.RunResult{State: aibroker.RunUnknown}, unknownErr
	}
	runner, secrets := newContainerRunnerForTest(t, broker)
	request := containerIntentRequest("observe", false, "")
	_, err := runner.Run(context.Background(), request)
	if !errors.Is(err, ErrContainerRunnerUnknown) || strings.Contains(err.Error(), unknownErr.Error()) || strings.Contains(err.Error(), strings.Split(secrets, "\x00")[0]) {
		t.Fatalf("unknown error=%v", err)
	}
	if broker.runCount() != 1 {
		t.Fatal("initial broker run was not called exactly once")
	}
	if _, err := runner.Run(context.Background(), containerIntentRequest("different", false, "")); !errors.Is(err, ErrContainerRunnerRecovery) {
		t.Fatalf("different intent error=%v", err)
	}
	if broker.runCount() != 1 {
		t.Fatal("different intent started a new broker run")
	}
	broker.lookup = func(_ context.Context, profileID, requestID string) (aibroker.RunResult, error) {
		return completedContainerBrokerResult(airunner.ExecuteRequest{ProfileID: profileID, RequestID: requestID}), nil
	}
	result, err := runner.Run(context.Background(), request)
	if err != nil || result.ThreadID != "container-thread-1" {
		t.Fatalf("recovered result=%+v err=%v", result, err)
	}
	if broker.runCount() != 1 || broker.lookupCount() != 1 {
		t.Fatalf("recovery broker calls runs=%d lookups=%d", broker.runCount(), broker.lookupCount())
	}
}

func TestContainerRunnerPendingReusesPersistedRequestIDAfterBrokerNotFound(t *testing.T) {
	broker := &containerRunnerFakeBroker{}
	runner, _ := newContainerRunnerForTest(t, broker)
	request := containerIntentRequest("pending", false, "")
	checkpoint := containerRunCheckpoint{Version: containerRunnerVersion, ProfileID: runner.profileID, RequestID: "container-preclaimed", Intent: hashContainerIntent(request), State: containerRunPending, UpdatedAt: time.Now().UTC()}
	if err := runner.saveCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), request)
	if err != nil || result.ThreadID == "" {
		t.Fatalf("pending recovery result=%+v err=%v", result, err)
	}
	if broker.lookupCount() != 1 || broker.runCount() != 1 {
		t.Fatalf("broker calls lookups=%d runs=%d", broker.lookupCount(), broker.runCount())
	}
	if got := broker.runRequests()[0].RequestID; got != checkpoint.RequestID {
		t.Fatalf("recovery changed request ID from %q to %q", checkpoint.RequestID, got)
	}
}

func TestContainerRunnerRejectsIncompleteCompletionAndDoesNotRetryUnknown(t *testing.T) {
	broker := &containerRunnerFakeBroker{}
	broker.run = func(_ context.Context, request airunner.ExecuteRequest) (aibroker.RunResult, error) {
		outcome := completedContainerBrokerResult(request)
		outcome.Response.Result.Process.Status = "signaled"
		return outcome, nil
	}
	runner, _ := newContainerRunnerForTest(t, broker)
	request := containerIntentRequest("incomplete", false, "")
	_, err := runner.Run(context.Background(), request)
	if !errors.Is(err, ErrContainerRunnerIncomplete) {
		t.Fatalf("incomplete error=%v", err)
	}
	if broker.runCount() != 1 {
		t.Fatal("initial incomplete run count mismatch")
	}
	broker.lookup = func(context.Context, string, string) (aibroker.RunResult, error) {
		return aibroker.RunResult{State: aibroker.RunUnknown}, aibroker.ErrRunUnknown
	}
	if _, err := runner.Run(context.Background(), request); !errors.Is(err, ErrContainerRunnerUnknown) {
		t.Fatalf("unknown replay error=%v", err)
	}
	if broker.runCount() != 1 {
		t.Fatal("unknown replay started a new broker run")
	}
}

func TestContainerRunnerCancellationPersistsUnknown(t *testing.T) {
	broker := &containerRunnerFakeBroker{}
	broker.run = func(ctx context.Context, _ airunner.ExecuteRequest) (aibroker.RunResult, error) {
		<-ctx.Done()
		return aibroker.RunResult{State: aibroker.RunUnknown}, ctx.Err()
	}
	runner, _ := newContainerRunnerForTest(t, broker)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := runner.Run(ctx, containerIntentRequest("cancel", false, ""))
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrContainerRunnerUnknown) {
		t.Fatalf("cancellation error=%v", err)
	}
	broker.lookup = func(context.Context, string, string) (aibroker.RunResult, error) {
		return aibroker.RunResult{State: aibroker.RunUnknown}, aibroker.ErrRunUnknown
	}
	if _, err := runner.Run(context.Background(), containerIntentRequest("cancel", false, "")); !errors.Is(err, ErrContainerRunnerUnknown) {
		t.Fatalf("unknown cancellation recovery error=%v", err)
	}
	if broker.runCount() != 1 {
		t.Fatal("canceled request was retried")
	}
}

func TestContainerRunnerLookupCancellationKeepsRecoveryState(t *testing.T) {
	broker := &containerRunnerFakeBroker{}
	lookupStarted := make(chan struct{})
	broker.lookup = func(ctx context.Context, _ string, _ string) (aibroker.RunResult, error) {
		close(lookupStarted)
		<-ctx.Done()
		return aibroker.RunResult{}, ctx.Err()
	}
	runner, _ := newContainerRunnerForTest(t, broker)
	request := containerIntentRequest("lookup-cancel", false, "")
	checkpoint := containerRunCheckpoint{Version: containerRunnerVersion, ProfileID: runner.profileID, RequestID: "container-lookup-cancel", Intent: hashContainerIntent(request), State: containerRunUnknown, UpdatedAt: time.Now().UTC()}
	if err := runner.saveCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := runner.Run(ctx, request)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrContainerRunnerRecovery) {
		t.Fatalf("lookup cancellation error=%v", err)
	}
	<-lookupStarted
	if broker.runCount() != 0 {
		t.Fatalf("lookup cancellation started a broker run: %d", broker.runCount())
	}
	data, readErr := os.ReadFile(runner.CheckpointPath())
	if readErr != nil {
		t.Fatal(readErr)
	}
	var persisted containerRunCheckpoint
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.State != containerRunUnknown || persisted.RequestID != checkpoint.RequestID {
		t.Fatalf("lookup cancellation changed recovery checkpoint: %+v", persisted)
	}
}

func TestContainerRunnerRejectsSymlinkStateRootAndMissingCredential(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	broker := &containerRunnerFakeBroker{}
	if _, err := NewContainerRunner(ContainerRunnerConfig{ProfileID: "container-profile", StateRoot: link, Broker: broker, RequestTemplate: airunner.ExecuteRequest{Model: airunner.Model{APIKey: "key"}, MCP: airunner.MCP{Token: strings.Repeat("x", 43)}}}); !errors.Is(err, ErrContainerRunnerConfig) {
		t.Fatalf("symlink state root error=%v", err)
	}
	if _, err := NewContainerRunner(ContainerRunnerConfig{ProfileID: "container-profile", StateRoot: filepath.Join(root, "missing-key"), Broker: broker, RequestTemplate: airunner.ExecuteRequest{MCP: airunner.MCP{Token: strings.Repeat("x", 43)}}}); !errors.Is(err, ErrContainerRunnerCredential) {
		t.Fatalf("missing key error=%v", err)
	}
}
