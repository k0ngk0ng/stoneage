package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

type activeRestoreProfileLister struct {
	profiles []airuntime.Profile
	err      error
}

func (lister *activeRestoreProfileLister) ListProfiles(context.Context) ([]airuntime.Profile, error) {
	if lister.err != nil {
		return nil, lister.err
	}
	return lister.profiles, nil
}

type activeRestoreRecorder struct {
	mu       sync.Mutex
	calls    []string
	failures map[string]error
}

func (recorder *activeRestoreRecorder) Restore(_ context.Context, profileID string) error {
	recorder.mu.Lock()
	recorder.calls = append(recorder.calls, profileID)
	err := recorder.failures[profileID]
	recorder.mu.Unlock()
	return err
}

func (recorder *activeRestoreRecorder) Calls() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.calls...)
}

func TestRestoreActiveProfilesRestoresOnlyDurableActiveProfilesAndContinues(t *testing.T) {
	credentialErr := errors.New("provider rejected api_key=restore-secret")
	lister := &activeRestoreProfileLister{profiles: []airuntime.Profile{
		{ID: "active-first", Status: airuntime.ProfileStatusActive},
		{ID: "paused", Status: airuntime.ProfileStatusPaused},
		{ID: "stopped", Status: airuntime.ProfileStatusStopped},
		{ID: "deleted", Status: airuntime.ProfileStatusDeleted},
		{ID: "active-failed", Status: airuntime.ProfileStatusActive},
		{ID: "active-last", Status: airuntime.ProfileStatusActive},
	}}
	recorder := &activeRestoreRecorder{failures: map[string]error{"active-failed": credentialErr}}

	err := restoreActiveProfiles(context.Background(), lister, recorder)
	if !errors.Is(err, credentialErr) {
		t.Fatalf("restore error=%v, want failed profile cause", err)
	}
	if got, want := recorder.Calls(), []string{"active-first", "active-failed", "active-last"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restore calls=%v, want %v", got, want)
	}
	if strings.Contains(err.Error(), "restore-secret") || strings.Contains(err.Error(), "api_key") {
		t.Fatalf("restore error leaked provider credential: %v", err)
	}
}

func TestRestoreActiveProfilesReturnsCredentialFreeListError(t *testing.T) {
	credentialErr := errors.New("database password=list-secret")
	err := restoreActiveProfiles(context.Background(), &activeRestoreProfileLister{err: credentialErr}, &activeRestoreRecorder{})
	if !errors.Is(err, credentialErr) {
		t.Fatalf("list error=%v, want original cause", err)
	}
	if strings.Contains(err.Error(), "list-secret") || strings.Contains(err.Error(), "password") {
		t.Fatalf("list error leaked provider credential: %v", err)
	}
}

type cancelingActiveRestoreRecorder struct {
	activeRestoreRecorder
	cancel context.CancelFunc
}

func (recorder *cancelingActiveRestoreRecorder) Restore(ctx context.Context, profileID string) error {
	err := recorder.activeRestoreRecorder.Restore(ctx, profileID)
	if recorder.cancel != nil {
		recorder.cancel()
		recorder.cancel = nil
	}
	return err
}

func TestRestoreActiveProfilesStopsRemainingQueueOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	recorder := &cancelingActiveRestoreRecorder{
		activeRestoreRecorder: activeRestoreRecorder{},
		cancel:                cancel,
	}
	lister := &activeRestoreProfileLister{profiles: []airuntime.Profile{
		{ID: "active-first", Status: airuntime.ProfileStatusActive},
		{ID: "active-second", Status: airuntime.ProfileStatusActive},
	}}

	err := restoreActiveProfiles(ctx, lister, recorder)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("restore cancellation error=%v, want context.Canceled", err)
	}
	if got, want := recorder.Calls(), []string{"active-first"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restore calls=%v after cancellation, want %v", got, want)
	}
}

func TestRestoreActiveProfilesHonorsAlreadyCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recorder := &activeRestoreRecorder{}
	err := restoreActiveProfiles(ctx, &activeRestoreProfileLister{profiles: []airuntime.Profile{{ID: "active", Status: airuntime.ProfileStatusActive}}}, recorder)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("restore error=%v, want context.Canceled", err)
	}
	if calls := recorder.Calls(); len(calls) != 0 {
		t.Fatalf("restore calls=%v for canceled context, want none", calls)
	}
}

func TestAIRuntimeWiringRestoreActiveIsNoOpWithoutRuntime(t *testing.T) {
	var wiring *aiRuntimeWiring
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := wiring.RestoreActive(ctx); err != nil {
		t.Fatalf("nil runtime restore error=%v, want nil", err)
	}
}
