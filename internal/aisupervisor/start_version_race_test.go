package aisupervisor

import (
	"context"
	"errors"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

// An explicit start reads the profile version and the unknown-turn recovery
// then compare-and-swaps against it. A pause that is still settling writes a
// newer version inside that window, which is why CI saw an occasional
// "stage=recovery code=profile_changed" failure (and why an operator clicking
// start right after a pause could see the same). The recovery now re-reads and
// retries while the version keeps moving, and this test pins both halves: the
// raw call still rejects a stale version, and the start path recovers from it.
func TestStartRecoversWhenTheProfileVersionMovesUnderIt(t *testing.T) {
	store := testSupervisorStore(t)
	ctx := context.Background()
	input := testSupervisorProfile("version-race")
	input.Status = airuntime.ProfileStatusPaused
	profile, err := store.CreateProfile(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	supervisor := &Supervisor{store: store}

	stale := profile.Version
	settled := airuntime.ProfileStatusPaused
	updated, err := store.UpdateProfileCAS(ctx, profile.ID, stale, airuntime.ProfilePatch{Status: &settled})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version == stale {
		t.Fatalf("the settling write did not advance the version: %d", stale)
	}

	// The raw recovery is what the start path used to call: a version that
	// moved under it is rejected.
	if err := supervisor.recoverUnknownBeforeStart(ctx, profile.ID, stale, unknownReviewStartActor, false); !errors.Is(err, ErrProfileChanged) {
		t.Fatalf("stale version = %v, want ErrProfileChanged", err)
	}

	// The start path adopts the newer version and proceeds.
	initial := profile
	if err := supervisor.recoverUnknownWithFreshVersion(ctx, profile.ID, stale, &initial); err != nil {
		t.Fatalf("recovery with a fresh version: %v", err)
	}
	if initial.Version != updated.Version {
		t.Fatalf("recovery kept version %d, want %d", initial.Version, updated.Version)
	}
}
