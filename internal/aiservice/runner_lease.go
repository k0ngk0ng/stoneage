package aiservice

import (
	"context"
	"sync"

	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aisupervisor"
)

// sessionLeaseDone is intentionally optional. Existing headless providers
// may not expose the Web session lifecycle, while Web-backed sessions use it
// to revoke an in-flight model turn on takeover, logout, or disconnect.
type sessionLeaseDone interface {
	LeaseDone() <-chan struct{}
}

// leaseBoundRunner binds both ownership fences to the actual model call. A
// game backend's operation checks alone are insufficient: Codex/container
// execution must receive cancellation as soon as the authenticated session is
// revoked.
type leaseBoundRunner struct {
	runner      aisupervisor.Runner
	gateLease   context.Context
	sessionDone <-chan struct{}
}

func (runner *leaseBoundRunner) Run(ctx context.Context, request aicodex.RunRequest) (aicodex.Result, error) {
	bound, cancel := contextForSessionLease(ctx, runner.gateLease, runner.sessionDone)
	defer cancel()
	return runner.runner.Run(bound, request)
}

// leaseBoundRecoveryRunner deliberately implements RecoveryRunner only when
// the wrapped transport implements it. Keeping this as a separate type
// preserves the supervisor's optional recovery path and its checkpoint
// semantics for local runners.
type leaseBoundRecoveryRunner struct {
	*leaseBoundRunner
	recovery aisupervisor.RecoveryRunner
}

func (runner *leaseBoundRecoveryRunner) Recover(ctx context.Context, request aicodex.RunRequest) (aicodex.Result, error) {
	bound, cancel := contextForSessionLease(ctx, runner.gateLease, runner.sessionDone)
	defer cancel()
	return runner.recovery.Recover(bound, request)
}

func bindRunnerToSessionLease(runner aisupervisor.Runner, gateLease context.Context, session GameSession) aisupervisor.Runner {
	if runner == nil || isNilRuntimeValue(runner) || isNilRuntimeValue(session) {
		return runner
	}
	lease, ok := session.(sessionLeaseDone)
	if !ok {
		return runner
	}
	done := lease.LeaseDone()
	if done == nil && gateLease == nil {
		return runner
	}
	bound := &leaseBoundRunner{runner: runner, gateLease: gateLease, sessionDone: done}
	if recovery, ok := runner.(aisupervisor.RecoveryRunner); ok {
		return &leaseBoundRecoveryRunner{leaseBoundRunner: bound, recovery: recovery}
	}
	return bound
}

func contextForSessionLease(parent, gateLease context.Context, sessionDone <-chan struct{}) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if gateLease == nil && sessionDone == nil {
		return parent, func() {}
	}
	bound, cancel := context.WithCancel(parent)
	stops := make([]func() bool, 0, 1)
	if gateLease != nil {
		if gateLease.Err() != nil {
			cancel()
		} else {
			stops = append(stops, context.AfterFunc(gateLease, cancel))
		}
	}
	if sessionDone != nil {
		select {
		case <-sessionDone:
			cancel()
		default:
			go func() {
				select {
				case <-sessionDone:
					cancel()
				case <-bound.Done():
				}
			}()
		}
	}
	return bound, func() {
		for _, stop := range stops {
			stop()
		}
		cancel()
	}
}

// watchSessionLeaseGate fences deterministic game tasks as soon as the Web
// session revokes the agent lease. Tasks already started inherit the gate's
// lease context, so closing the gate cancels their local polling loop while
// durable task/checkpoint records remain available for later reconciliation.
func watchSessionLeaseGate(gate *aicontrol.Gate, session GameSession) func() {
	if gate == nil || isNilRuntimeValue(session) {
		return nil
	}
	lease, ok := session.(sessionLeaseDone)
	if !ok {
		return nil
	}
	done := lease.LeaseDone()
	if done == nil {
		return nil
	}
	stop := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		select {
		case <-done:
			gate.Close()
		case <-stop:
		}
	}()
	return func() {
		stopOnce.Do(func() { close(stop) })
	}
}
