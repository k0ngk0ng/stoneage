package aiservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

func TestContainerRunnerAppliesConfiguredTurnTimeout(t *testing.T) {
	broker := &containerRunnerFakeBroker{run: func(ctx context.Context, _ airunner.ExecuteRequest) (aibroker.RunResult, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Error("model timeout did not reach broker")
		}
		<-ctx.Done()
		return aibroker.RunResult{}, ctx.Err()
	}}
	broker.lookup = func(context.Context, string, string) (aibroker.RunResult, error) {
		return aibroker.RunResult{State: aibroker.RunUnknown}, aibroker.ErrRunUnknown
	}
	runner, _ := newContainerRunnerForTest(t, broker, time.Second)
	parent, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	_, err := runner.Run(parent, containerCallerIntentRequest("timeout-turn", "observe", false, ""))
	if err == nil || broker.runCount() != 1 || time.Since(started) > 2*time.Second || parent.Err() != nil {
		t.Fatalf("model deadline not enforced: err=%v runs=%d", err, broker.runCount())
	}
	// A timed-out request is not a license to issue a second model call.
	_, _ = runner.Recover(parent, containerCallerIntentRequest("timeout-turn", "observe", false, ""))
	if broker.runCount() != 1 {
		t.Fatal("timed-out model turn was dispatched again")
	}
}

func TestContainerRunnerCarriesAndPersistsAbsoluteTurnDeadline(t *testing.T) {
	broker := &containerRunnerFakeBroker{}
	broker.run = func(_ context.Context, request airunner.ExecuteRequest) (aibroker.RunResult, error) {
		return aibroker.RunResult{State: aibroker.RunUnknown, Response: airunner.Response{ProfileID: request.ProfileID, RequestID: request.RequestID}}, aibroker.ErrRunUnknown
	}
	broker.lookup = func(context.Context, string, string) (aibroker.RunResult, error) {
		return aibroker.RunResult{State: aibroker.RunUnknown}, aibroker.ErrRunUnknown
	}
	runner, _ := newContainerRunnerForTest(t, broker, time.Second)
	request := containerCallerIntentRequest("deadline-turn", "observe", false, "")
	if _, err := runner.Run(context.Background(), request); !errors.Is(err, ErrContainerRunnerUnknown) {
		t.Fatalf("first timed turn error=%v", err)
	}
	runs := broker.runRequests()
	if len(runs) != 1 || runs[0].TurnDeadlineUnixMS <= time.Now().UnixMilli() {
		t.Fatalf("broker request did not carry a future absolute deadline: %+v", runs)
	}
	checkpoint, exists, err := runner.loadCheckpoint()
	if err != nil || !exists || checkpoint.TurnDeadlineUnixMS != runs[0].TurnDeadlineUnixMS {
		t.Fatalf("checkpoint deadline=%d exists=%v err=%v request=%d", checkpoint.TurnDeadlineUnixMS, exists, err, runs[0].TurnDeadlineUnixMS)
	}
	if _, err := runner.Recover(context.Background(), request); !errors.Is(err, ErrContainerRunnerUnknown) {
		t.Fatalf("recovery error=%v", err)
	}
	if broker.runCount() != 1 || broker.lookupCount() != 1 {
		t.Fatalf("recovery changed dispatch count: runs=%d lookups=%d", broker.runCount(), broker.lookupCount())
	}
	checkpointAfter, _, err := runner.loadCheckpoint()
	if err != nil || checkpointAfter.TurnDeadlineUnixMS != checkpoint.TurnDeadlineUnixMS {
		t.Fatalf("recovery changed persisted deadline: before=%d after=%d err=%v", checkpoint.TurnDeadlineUnixMS, checkpointAfter.TurnDeadlineUnixMS, err)
	}
}

func TestContainerRunnerRejectsInvalidTurnTimeout(t *testing.T) {
	_, err := NewContainerRunner(ContainerRunnerConfig{TurnTimeout: -time.Second})
	if !errors.Is(err, ErrContainerRunnerConfig) {
		t.Fatal("negative timeout accepted")
	}
}
