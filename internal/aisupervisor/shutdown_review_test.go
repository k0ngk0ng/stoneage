package aisupervisor

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

type startupBarrierFactory struct {
	entered chan struct{}
	release chan struct{}
	closed  chan struct{}
	session *fakeSession
}

func (f *startupBarrierFactory) Open(context.Context, airuntime.Profile) (AgentSession, error) {
	close(f.entered)
	<-f.release
	return f.session.agentSession(), nil
}
func (f *startupBarrierFactory) Close() error { close(f.closed); return nil }

func TestSupervisorCloseWaitsForInFlightProvisioningCleanup(t *testing.T) {
	ctx := context.Background()
	store := testSupervisorStore(t)
	profile, err := store.CreateProfile(ctx, testSupervisorProfile("shutdown-start"))
	if err != nil {
		t.Fatal(err)
	}
	factory := &startupBarrierFactory{entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}), session: &fakeSession{runner: &fakeRunner{}, snapshot: Snapshot{GameReady: true}}}
	supervisor, err := New(ctx, store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() { started <- supervisor.Start(ctx, profile.ID) }()
	<-factory.entered
	stopped := make(chan error, 1)
	go func() { stopped <- supervisor.Close() }()
	waitFor(t, func() bool { supervisor.mu.Lock(); defer supervisor.mu.Unlock(); return supervisor.closed })
	select {
	case <-factory.closed:
		t.Fatal("factory closed before provisioning cleanup")
	default:
	}
	select {
	case <-stopped:
		t.Fatal("Close returned before provisioning finished")
	default:
	}
	close(factory.release)
	if err = <-started; !errors.Is(err, context.Canceled) && !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after Close=%v", err)
	}
	if err = <-stopped; err != nil {
		t.Fatal(err)
	}
	if factory.session.closeCount() != 1 {
		t.Fatalf("provisioned session cleanup=%d", factory.session.closeCount())
	}
	current, err := store.GetProfile(ctx, profile.ID)
	if err != nil || current.Status != airuntime.ProfileStatusStopped {
		t.Fatalf("late startup changed profile: %+v %v", current, err)
	}
}
