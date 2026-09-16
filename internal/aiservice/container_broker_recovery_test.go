package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

// The broker and adapter are real; only the daemon boundary is a fixture.
// This checks that recovered output settles the original adapter checkpoint
// without submitting another model turn or charging a different caller.
func TestContainerRunnerRecoversBrokerOrphanCompletion(t *testing.T) {
	ctx := context.Background()
	request := containerCallerIntentRequest("orphan-attempt", "observe", false, "")
	response := completedContainerBrokerResult(airunner.ExecuteRequest{ProfileID: request.ProfileID, RequestID: request.RequestID}).Response
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	daemon := &completedOrphanDocker{raw: raw}
	journal := aibroker.NewMemoryJournal()
	entry := aibroker.JournalEntry{ProfileID: request.ProfileID, RequestID: request.RequestID,
		PayloadHash: strings.Repeat("a", 64), State: aibroker.RunRunning,
		ContainerName: "sa-adapter-orphan", VolumeName: "sa-adapter-volume", UpdatedAt: time.Now()}
	if created, err := journal.Create(ctx, entry); err != nil || !created {
		t.Fatalf("seed old broker claim: created=%v err=%v", created, err)
	}
	broker, err := aibroker.New(aibroker.Config{Docker: daemon, Journal: journal, Image: "fixture:local", Network: "none"})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	runner, _ := newContainerRunnerForTest(t, broker)
	checkpoint := containerRunCheckpoint{Version: containerRunnerVersion, ProfileID: request.ProfileID,
		RequestID: request.RequestID, CallerRequestID: request.RequestID, Intent: hashContainerIntent(request),
		State: containerRunUnknown, UpdatedAt: time.Now().UTC()}
	if err := runner.saveCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Recover(ctx, request)
	if err != nil || result.ThreadID != response.Result.ThreadID || result.Usage.TotalTokens != 10 {
		t.Fatalf("recover original attempt: thread=%q tokens=%d err=%v", result.ThreadID, result.Usage.TotalTokens, err)
	}
	if daemon.runs != 0 || daemon.reads != 1 || daemon.removes == 0 {
		t.Fatalf("recovery daemon calls: runs=%d reads=%d removes=%d", daemon.runs, daemon.reads, daemon.removes)
	}
	// Replaying the same caller uses the durable result after container cleanup.
	if _, err := runner.Recover(ctx, request); err != nil || daemon.runs != 0 || daemon.reads != 1 {
		t.Fatalf("recovery replay dispatched work: err=%v runs=%d reads=%d", err, daemon.runs, daemon.reads)
	}
}

type completedOrphanDocker struct {
	raw                  []byte
	runs, reads, removes int
}

func (docker *completedOrphanDocker) Run(context.Context, aibroker.RunSpec, []byte) (aibroker.DockerResult, error) {
	docker.runs++
	return aibroker.DockerResult{}, errors.New("recovery must not dispatch")
}

func (*completedOrphanDocker) Stop(context.Context, string) error { return nil }

func (*completedOrphanDocker) Inspect(context.Context, string) (aibroker.DockerContainerState, error) {
	return aibroker.DockerContainerExited, nil
}

func (docker *completedOrphanDocker) ReadResult(context.Context, string) (aibroker.DockerResult, error) {
	docker.reads++
	return aibroker.DockerResult{ExitCode: 0, Stdout: docker.raw}, nil
}

func (docker *completedOrphanDocker) Remove(context.Context, string) error {
	docker.removes++
	return nil
}
