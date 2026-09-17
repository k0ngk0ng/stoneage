package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

type reviewIntegrationDocker struct {
	calls, removals int
	state           aibroker.DockerContainerState
	inspectErr      error
}

func (d *reviewIntegrationDocker) Run(_ context.Context, _ aibroker.RunSpec, input []byte) (aibroker.DockerResult, error) {
	d.calls++
	var request airunner.ExecuteRequest
	if err := json.Unmarshal(input, &request); err != nil {
		return aibroker.DockerResult{}, err
	}
	response := completedContainerBrokerResult(request).Response
	raw, _ := json.Marshal(response)
	return aibroker.DockerResult{ExitCode: 0, Stdout: raw}, nil
}
func (*reviewIntegrationDocker) Stop(context.Context, string) error { return nil }
func (d *reviewIntegrationDocker) Inspect(context.Context, string) (aibroker.DockerContainerState, error) {
	if d.inspectErr != nil {
		return "", d.inspectErr
	}
	if d.state != "" {
		return d.state, nil
	}
	return "", aibroker.ErrContainerNotFound
}
func (d *reviewIntegrationDocker) Remove(context.Context, string) error { d.removals++; return nil }

func TestUnknownReviewBrokerFactoryAndRunner(t *testing.T) {
	ctx := context.Background()
	daemon := &reviewIntegrationDocker{}
	journal, err := aibroker.OpenSQLiteJournal(filepath.Join(t.TempDir(), "broker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	request := containerCallerIntentRequest("old-review-attempt", "old prompt", true, "old-thread")
	// Keep the fixture's durable Docker identities consistent with the broker
	// contract. Reviewed recovery proves the old request belongs to this exact
	// profile volume before it selects a fresh Codex checkpoint namespace.
	entry := aibroker.JournalEntry{ProfileID: request.ProfileID, RequestID: request.RequestID, PayloadHash: strings.Repeat("a", 64), State: aibroker.RunUnknown, ContainerName: aibroker.ContainerName(request.ProfileID, request.RequestID), VolumeName: aibroker.ProfileVolumeName(request.ProfileID), UpdatedAt: time.Now().UTC()}
	if _, err = journal.Create(ctx, entry); err != nil {
		t.Fatal(err)
	}
	broker, err := aibroker.New(aibroker.Config{Docker: daemon, Journal: journal, Image: "fixture:local", Network: "none"})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	templateRunner, _ := newContainerRunnerForTest(t, broker)
	stateRoot := filepath.Join(t.TempDir(), request.ProfileID)
	runner, err := NewContainerRunner(ContainerRunnerConfig{ProfileID: request.ProfileID, StateRoot: stateRoot, Broker: broker, RequestTemplate: templateRunner.template})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := containerRunCheckpoint{Version: containerRunnerVersion, ProfileID: request.ProfileID, RequestID: request.RequestID, CallerRequestID: request.RequestID, State: containerRunUnknown, Intent: hashContainerIntent(request), UpdatedAt: time.Now().UTC()}
	if err = runner.saveCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	factory := &Factory{cfg: FactoryConfig{StateRoot: filepath.Dir(stateRoot), ContainerBroker: broker}, active: make(map[string]*factoryLease)}
	status, err := factory.InspectUnknown(ctx, request.ProfileID, request.RequestID)
	if err != nil || status.Reviewed {
		t.Fatalf("inspect=%+v %v", status, err)
	}
	for _, test := range []struct {
		state   aibroker.DockerContainerState
		stopped bool
	}{
		{aibroker.DockerContainerCreated, false}, {aibroker.DockerContainerRunning, false}, {aibroker.DockerContainerPaused, false}, {aibroker.DockerContainerRestarting, false}, {aibroker.DockerContainerRemoving, false}, {aibroker.DockerContainerExited, true}, {aibroker.DockerContainerDead, true}, {"", true},
	} {
		daemon.state = test.state
		current, err := factory.InspectUnknown(ctx, request.ProfileID, request.RequestID)
		if err != nil || current.ContainerStopped != test.stopped {
			t.Fatalf("container=%s status=%+v err=%v", test.state, current, err)
		}
	}
	daemon.inspectErr = errors.New("daemon unavailable")
	if _, err := factory.InspectUnknown(ctx, request.ProfileID, request.RequestID); err == nil {
		t.Fatal("inspection failure reported ready")
	}
	daemon.inspectErr = nil
	if daemon.calls != 0 || daemon.removals != 0 {
		t.Fatal("readiness changed Docker state")
	}
	unchanged, err := journal.Get(ctx, entry.ProfileID, entry.RequestID)
	if err != nil || unchanged.Review != nil || !unchanged.UpdatedAt.Equal(entry.UpdatedAt) {
		t.Fatal("readiness changed the journal")
	}
	daemon.state = aibroker.DockerContainerRunning
	if err := factory.ReviewUnknown(ctx, request.ProfileID, request.RequestID, status, "operator", aibroker.ReviewReasonAcceptUncertainOutcome); !errors.Is(err, aibroker.ErrRunRunning) {
		t.Fatalf("review did not recheck live container: %v", err)
	}
	daemon.state = ""
	// Simulate broker commit followed by interruption before the adapter write.
	if _, err = broker.ReviewUnknown(ctx, entry.ProfileID, entry.RequestID, status.UpdatedAt, "operator", aibroker.ReviewReasonAcceptUncertainOutcome); err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Run(ctx, containerCallerIntentRequest("new-attempt", "fresh", false, "")); !errors.Is(err, ErrContainerRunnerRecovery) {
		t.Fatalf("unreviewed adapter released: %v", err)
	}
	if err = factory.ReviewUnknown(ctx, request.ProfileID, request.RequestID, status, "operator", aibroker.ReviewReasonAcceptUncertainOutcome); err != nil {
		t.Fatal(err)
	}
	if err = factory.ReviewUnknown(ctx, request.ProfileID, request.RequestID, status, "operator", aibroker.ReviewReasonAcceptUncertainOutcome); err != nil {
		t.Fatal("idempotent review", err)
	}
	runner, err = NewContainerRunner(ContainerRunnerConfig{ProfileID: request.ProfileID, StateRoot: stateRoot, Broker: broker, RequestTemplate: templateRunner.template})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.Recover(ctx, request); !errors.Is(err, ErrContainerRunnerRecovery) {
		t.Fatalf("old request replay=%v", err)
	}
	if _, err = runner.Run(ctx, containerCallerIntentRequest("new-attempt", "fresh", true, "old-thread")); !errors.Is(err, ErrContainerRunnerRecovery) {
		t.Fatalf("old thread resumed=%v", err)
	}
	if daemon.calls != 0 {
		t.Fatalf("review dispatched %d calls", daemon.calls)
	}
	result, err := runner.Run(ctx, containerCallerIntentRequest("new-attempt", "fresh", false, ""))
	if err != nil || result.ThreadID == "" || daemon.calls != 1 {
		t.Fatalf("fresh turn=%+v err=%v calls=%d", result, err, daemon.calls)
	}
	old, err := broker.Lookup(ctx, entry.ProfileID, entry.RequestID)
	if !errors.Is(err, aibroker.ErrRunUnknown) || old.Entry.Review == nil || old.State != aibroker.RunUnknown {
		t.Fatalf("old outcome changed: %+v %v", old, err)
	}
}
