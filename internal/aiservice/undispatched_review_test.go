package aiservice

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
)

func TestReviewUndispatchedTurnAfterCompletedExecutorChange(t *testing.T) {
	ctx := context.Background()
	broker := &containerRunnerFakeBroker{}
	runner, _ := newContainerRunnerForTest(t, broker)
	if _, err := runner.Run(ctx, containerCallerIntentRequest("old-completed", "old", false, "")); err != nil {
		t.Fatal(err)
	}
	old, _, err := runner.loadCheckpoint()
	if err != nil {
		t.Fatal(err)
	}
	old.ReviewedRequestID = "previous-reviewed-turn"
	if err := runner.saveCheckpoint(old); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(ctx, containerCallerIntentRequest("never-dispatched", "fresh", false, "")); !errors.Is(err, ErrContainerRunnerRecovery) {
		t.Fatalf("expected stale checkpoint failure, got %v", err)
	}
	if broker.runCount() != 1 {
		t.Fatal("unexpected broker dispatch")
	}
	if err := runner.reconcileUnknown(ctx, "never-dispatched"); err != nil {
		t.Fatal(err)
	}
	status, err := runner.inspectUnknown(ctx, "never-dispatched")
	if err != nil || status.State != "not_dispatched" || !status.ContainerStopped {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	for i := 0; i < 2; i++ {
		if err := runner.reviewUnknown(ctx, "never-dispatched", status, "operator_start", "accept_uncertain_outcome"); err != nil {
			t.Fatal(err)
		}
	}
	cp, exists, err := runner.loadCheckpoint()
	if err != nil || !exists || !cp.ExecutorReset {
		t.Fatalf("fresh executor boundary missing: %+v %v", cp, err)
	}
	if _, err := runner.Run(ctx, containerCallerIntentRequest("new-executor", "fresh", false, "")); err != nil {
		t.Fatal(err)
	}
	if broker.runCount() != 2 {
		t.Fatal("fresh executor did not dispatch")
	}
	if broker.runRequests()[1].ReviewedRequestID != "previous-reviewed-turn" {
		t.Fatal("fresh turn lost the reviewed runtime namespace")
	}
}

func TestUndispatchedReviewRequiresAuthoritativeAbsence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		state     containerRunState
		lookupErr error
	}{
		{"pending", containerRunPending, aibroker.ErrJournalNotFound},
		{"unknown", containerRunUnknown, aibroker.ErrJournalNotFound},
		{"lookup_failure", containerRunCompleted, context.DeadlineExceeded},
		{"broker_has_run", containerRunCompleted, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			broker := &containerRunnerFakeBroker{}
			runner, _ := newContainerRunnerForTest(t, broker)
			if _, err := runner.Run(ctx, containerCallerIntentRequest("old", "old", false, "")); err != nil {
				t.Fatal(err)
			}
			cp, _, err := runner.loadCheckpoint()
			if err != nil {
				t.Fatal(err)
			}
			cp.State = tc.state
			if err := runner.saveCheckpoint(cp); err != nil {
				t.Fatal(err)
			}
			broker.lookup = func(context.Context, string, string) (aibroker.RunResult, error) {
				return aibroker.RunResult{}, tc.lookupErr
			}
			_, ok, _ := runner.inspectUndispatched(ctx, "new", cp, true)
			if ok {
				t.Fatal("unproven absence permitted review")
			}
			if _, err := os.Stat(runner.checkpoint); err != nil {
				t.Fatal("inspection changed checkpoint")
			}
		})
	}
}
