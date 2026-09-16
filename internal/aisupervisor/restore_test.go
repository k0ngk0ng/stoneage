package aisupervisor

import (
	"context"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

func TestRestoreActivePlayerStartsOnce(t *testing.T) {
	store := testSupervisorStore(t)
	input := lifeTestProfile("restore-active", 60)
	input.Status = airuntime.ProfileStatusActive
	profile, err := store.CreateProfile(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	session := &fakeSession{runner: &fakeRunner{}, snapshot: Snapshot{GameReady: true}}
	factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}}
	supervisor, err := New(context.Background(), store, factory, supervisorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	for i := 0; i < 2; i++ {
		if err := supervisor.Restore(context.Background(), profile.ID); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, func() bool { return len(session.runner.Requests()) == 1 })
	if factory.openCount() != 1 {
		t.Fatal("startup recovery opened duplicate sessions")
	}
}

func TestRestoreDoesNotEnablePausedOrStoppedPlayers(t *testing.T) {
	for _, status := range []string{airuntime.ProfileStatusPaused, airuntime.ProfileStatusStopped} {
		t.Run(status, func(t *testing.T) {
			store := testSupervisorStore(t)
			input := testSupervisorProfile("restore-" + status)
			input.Status = status
			profile, err := store.CreateProfile(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			factory := &fakeFactory{}
			supervisor, err := New(context.Background(), store, factory, supervisorTestConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer supervisor.Close()
			if err := supervisor.Restore(context.Background(), profile.ID); err != nil {
				t.Fatal(err)
			}
			current, err := store.GetProfile(context.Background(), profile.ID)
			if err != nil || current.Status != status || current.Version != profile.Version || factory.openCount() != 0 {
				t.Fatal("startup recovery activated a disabled player")
			}
		})
	}
}

func TestRestoreRespectsPauseDuringGameLogin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := testSupervisorStore(t)
	input := testSupervisorProfile("restore-pause")
	input.Status = airuntime.ProfileStatusActive
	profile, err := store.CreateProfile(ctx, input)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	release := make(chan struct{})
	session := &fakeSession{runner: &fakeRunner{}, snapshot: Snapshot{GameReady: true}}
	factory := &fakeFactory{sessions: map[string]*fakeSession{profile.ID: session}, openGate: release}
	supervisor, err := New(ctx, store, factory, supervisorTestConfig())
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = supervisor.Close() })
	restored := make(chan error, 1)
	go func() { restored <- supervisor.Restore(ctx, profile.ID) }()
	waitFor(t, func() bool { return factory.openCount() == 1 })
	if err := supervisor.Pause(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-restored; err != nil {
		t.Fatal(err)
	}
	current, err := store.GetProfile(ctx, profile.ID)
	if err != nil || current.Status != airuntime.ProfileStatusPaused || session.closeCount() != 1 || len(session.runner.Requests()) != 0 {
		t.Fatal("recovery overrode the operator pause or leaked the login session")
	}
}
