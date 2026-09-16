package aicodex

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunnerConfiguredTurnTimeoutStopsChild(t *testing.T) {
	t.Setenv("AICD_HELPER_MODE", "sleep")
	runner := newTestRunner(t, writeFakeCodex(t), t.TempDir(), 250*time.Millisecond)
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	result, err := runner.NewTurn(parent, "model-deadline", "prompt")
	if !errors.Is(err, context.DeadlineExceeded) || parent.Err() != nil || time.Since(started) > 3*time.Second {
		t.Fatalf("configured deadline not enforced: err=%v process=%s", err, result.Process.Status)
	}
	if result.Turn.Status == TurnCompleted {
		t.Fatal("timeout reported success")
	}
}
