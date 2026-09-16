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
	runner, _ := newContainerRunnerForTest(t, broker, 250*time.Millisecond)
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

func TestContainerRunnerRejectsInvalidTurnTimeout(t *testing.T) {
	_, err := NewContainerRunner(ContainerRunnerConfig{TurnTimeout: -time.Second})
	if !errors.Is(err, ErrContainerRunnerConfig) {
		t.Fatal("negative timeout accepted")
	}
}
