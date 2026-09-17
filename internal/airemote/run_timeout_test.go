package airemote

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
)

func TestModelRunOutlivesManagementTimeout(t *testing.T) {
	for _, bounded := range []bool{true, false} {
		t.Run(map[bool]string{true: "turn_deadline", false: "default_deadline"}[bounded], func(t *testing.T) {
			h := newHarness(t)
			defer h.hub.Close()
			h.hub.cfg.CommandTimeout = 10 * time.Millisecond
			ctx := context.Background()
			if bounded {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
				defer cancel()
			}
			type result struct {
				value aibroker.DockerResult
				err   error
			}
			done := make(chan result, 1)
			go func() {
				value, err := h.hub.Run(ctx, spec(), testPayload(t))
				done <- result{value, err}
			}()
			command := pollCommand(t, h.hub, h.worker)
			select {
			case got := <-done:
				t.Fatalf("model turn ended at management timeout: %v", got.err)
			case <-time.After(50 * time.Millisecond):
			}
			h.hub.finishCommand(command.CommandID, commandOutcome{result: ResultRequest{
				State: "exited", PayloadHash: command.PayloadHash, Stdout: []byte(`{"ok":true}`),
			}})
			select {
			case got := <-done:
				if got.err != nil || string(got.value.Stdout) != `{"ok":true}` {
					t.Fatalf("late model result lost: %+v", got)
				}
			case <-time.After(time.Second):
				t.Fatal("model result was not delivered")
			}
			route, _, err := h.hub.routeForContainer(command.ContainerName)
			if err != nil || route.State != "exited" {
				t.Fatalf("terminal route not persisted: %+v %v", route, err)
			}
		})
	}
}

func TestWorkerWaitKeepsCallerCancellationAndManagementTimeout(t *testing.T) {
	h := newHarness(t)
	defer h.hub.Close()
	h.hub.cfg.CommandTimeout = 10 * time.Millisecond
	for _, kind := range []string{KindStop, KindInspect, KindReadResult, KindRemove} {
		outcome := h.hub.wait(context.Background(), &commandWait{cmd: &Command{Kind: kind}, done: make(chan commandOutcome, 1)})
		if !errors.Is(outcome.err, context.DeadlineExceeded) {
			t.Fatalf("management command %s no longer times out: %v", kind, outcome.err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := h.hub.wait(ctx, &commandWait{cmd: &Command{Kind: KindRun}, done: make(chan commandOutcome, 1)})
	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("model run ignored caller cancellation: %v", outcome.err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	outcome = h.hub.wait(ctx, &commandWait{cmd: &Command{Kind: KindRun}, done: make(chan commandOutcome, 1)})
	if !errors.Is(outcome.err, context.DeadlineExceeded) {
		t.Fatalf("model run ignored turn deadline: %v", outcome.err)
	}
}
