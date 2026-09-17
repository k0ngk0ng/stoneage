package aiservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aisupervisor"
)

type leaseDoneTestSession struct {
	done <-chan struct{}
}

func (session leaseDoneTestSession) Observe(context.Context) (aigame.Snapshot, error) {
	return aigame.Snapshot{}, nil
}

func (session leaseDoneTestSession) ExecuteExpected(context.Context, uint64, aigame.Action) error {
	return nil
}

func (session leaseDoneTestSession) LeaseDone() <-chan struct{} { return session.done }

type leaseBoundTestRunner struct {
	runCalled     chan context.Context
	recoverCalled chan context.Context
}

type leaseOnlyTestRunner struct{}

func (leaseOnlyTestRunner) Run(ctx context.Context, _ aicodex.RunRequest) (aicodex.Result, error) {
	<-ctx.Done()
	return aicodex.Result{}, ctx.Err()
}

func (runner *leaseBoundTestRunner) Run(ctx context.Context, _ aicodex.RunRequest) (aicodex.Result, error) {
	runner.runCalled <- ctx
	<-ctx.Done()
	return aicodex.Result{}, ctx.Err()
}

func (runner *leaseBoundTestRunner) Recover(ctx context.Context, _ aicodex.RunRequest) (aicodex.Result, error) {
	runner.recoverCalled <- ctx
	<-ctx.Done()
	return aicodex.Result{}, ctx.Err()
}

func TestRunnerLeaseRevocationCancelsRunAndRecovery(t *testing.T) {
	done := make(chan struct{})
	runner := &leaseBoundTestRunner{
		runCalled:     make(chan context.Context, 1),
		recoverCalled: make(chan context.Context, 1),
	}
	bound := bindRunnerToSessionLease(runner, context.Background(), leaseDoneTestSession{done: done})
	recovery, ok := bound.(aisupervisor.RecoveryRunner)
	if !ok {
		t.Fatalf("recovery capability was lost: %T", bound)
	}

	runResult := make(chan error, 1)
	go func() {
		_, err := bound.Run(context.Background(), aicodex.RunRequest{ProfileID: "profile-1"})
		runResult <- err
	}()
	select {
	case <-runner.runCalled:
	case <-time.After(time.Second):
		t.Fatal("runner was not called")
	}
	close(done)
	select {
	case err := <-runResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("run did not observe lease revocation")
	}

	recoverResult := make(chan error, 1)
	go func() {
		_, err := recovery.Recover(context.Background(), aicodex.RunRequest{ProfileID: "profile-1"})
		recoverResult <- err
	}()
	select {
	case <-runner.recoverCalled:
	case <-time.After(time.Second):
		t.Fatal("recovery runner was not called")
	}
	select {
	case err := <-recoverResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("recovery error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery did not inherit lease revocation")
	}
}

func TestRunnerLeaseBindingPreservesOptionalRecoveryInterface(t *testing.T) {
	done := make(chan struct{})
	bound := bindRunnerToSessionLease(leaseOnlyTestRunner{}, context.Background(), leaseDoneTestSession{done: done})
	if _, ok := bound.(aisupervisor.RecoveryRunner); ok {
		t.Fatalf("plain runner unexpectedly implements RecoveryRunner: %T", bound)
	}
}

func TestRunnerLeaseContextCancellationStopsInFlightRun(t *testing.T) {
	gateLease, cancelGate := context.WithCancel(context.Background())
	defer cancelGate()
	runner := &leaseBoundTestRunner{runCalled: make(chan context.Context, 1)}
	bound := bindRunnerToSessionLease(runner, gateLease, leaseDoneTestSession{})
	result := make(chan error, 1)
	go func() {
		_, err := bound.Run(context.Background(), aicodex.RunRequest{ProfileID: "profile-1"})
		result <- err
	}()
	select {
	case <-runner.runCalled:
	case <-time.After(time.Second):
		t.Fatal("runner was not called")
	}
	cancelGate()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("run did not observe gate lease cancellation")
	}
}

func TestSessionLeaseRevocationClosesGate(t *testing.T) {
	done := make(chan struct{})
	gate := aicontrol.New()
	state, _, err := gate.Switch(gate.State().Generation, aicontrol.Agent, "test")
	if err != nil {
		t.Fatal(err)
	}
	stop := watchSessionLeaseGate(gate, leaseDoneTestSession{done: done})
	t.Cleanup(stop)
	close(done)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		err = gate.Dispatch(context.Background(), state.Generation, aicontrol.Agent, func(context.Context) error { return nil })
		if errors.Is(err, aicontrol.ErrClosed) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("lease revocation did not close gate")
}
