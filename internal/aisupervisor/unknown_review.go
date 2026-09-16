package aisupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

// UnknownExecution is the credential-free transport recovery identity.
type UnknownExecution struct {
	RequestID        string    `json:"request_id"`
	State            string    `json:"state"`
	UpdatedAt        time.Time `json:"updated_at"`
	Reviewed         bool      `json:"reviewed"`
	ContainerStopped bool      `json:"container_stopped"`
}

type UnknownReviewFactory interface {
	InspectUnknown(context.Context, string, string) (UnknownExecution, error)
	ReviewUnknown(context.Context, string, string, UnknownExecution, string, string) error
}

type UnknownRecoveryStatus struct {
	ProfileID         string           `json:"profile_id"`
	ProfileVersion    int64            `json:"profile_version"`
	AttemptID         string           `json:"attempt_id"`
	AttemptUpdatedAt  time.Time        `json:"attempt_updated_at"`
	CheckpointVersion int64            `json:"checkpoint_version"`
	ReservedTokens    int64            `json:"reserved_tokens"`
	Ready             bool             `json:"ready"`
	Execution         UnknownExecution `json:"execution"`
}

type UnknownRecoveryRequest struct {
	UnknownRecoveryStatus
	Actor  string `json:"-"`
	Reason string `json:"reason"`
}

func (supervisor *Supervisor) UnknownRecovery(ctx context.Context, profileID string) (UnknownRecoveryStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	view := UnknownRecoveryStatus{ProfileID: profileID}
	profile, err := supervisor.store.GetProfile(ctx, profileID)
	if err != nil {
		return view, err
	}
	view.ProfileVersion = profile.Version
	attempt, err := supervisor.store.PendingTokenAttempt(ctx, profileID)
	if err != nil {
		return view, err
	}
	if attempt.State != airuntime.TokenAttemptUnknown {
		return view, ErrAttemptRecovery
	}
	checkpoint, err := supervisor.store.GetCheckpoint(ctx, profileID)
	if err != nil {
		return view, err
	}
	var state persistedState
	if json.Unmarshal(checkpoint.State, &state) != nil || state.PendingAttemptID != attempt.ID {
		return view, ErrAttemptRecovery
	}
	view.AttemptID = attempt.ID
	view.AttemptUpdatedAt = attempt.UpdatedAt
	view.CheckpointVersion = checkpoint.Version
	view.ReservedTokens = attempt.Charge
	supervisor.mu.Lock()
	idle := supervisor.profileReviewIdleLocked(profileID)
	supervisor.mu.Unlock()
	view.Ready = idle && (profile.Status == airuntime.ProfileStatusPaused || profile.Status == airuntime.ProfileStatusStopped)
	factory, ok := supervisor.factory.(UnknownReviewFactory)
	if !ok {
		return view, ErrFactoryUnavailable
	}
	view.Execution, err = factory.InspectUnknown(ctx, profileID, attempt.ID)
	view.Ready = view.Ready && err == nil && view.Execution.ContainerStopped
	return view, err
}

// The caller holds mu. A paused label alone is insufficient: the run goroutine
// must have exited, thereby revoking game capabilities and draining actions.
func (supervisor *Supervisor) profileReviewIdleLocked(profileID string) bool {
	if supervisor.closed || supervisor.starts[profileID] != nil {
		return false
	}
	if managed := supervisor.profiles[profileID]; managed != nil {
		select {
		case <-managed.done:
		default:
			return false
		}
	}
	return true
}

func (supervisor *Supervisor) ReviewUnknown(ctx context.Context, request UnknownRecoveryRequest) (resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.Reason != airuntime.UnknownReviewReason || request.Actor == "" {
		return airuntime.ErrAttemptConflict
	}
	supervisor.mu.Lock()
	if !supervisor.profileReviewIdleLocked(request.ProfileID) {
		supervisor.mu.Unlock()
		return ErrAlreadyRunning
	}
	call := &startCall{done: make(chan struct{}), review: true}
	supervisor.starts[request.ProfileID] = call
	supervisor.wg.Add(1)
	supervisor.mu.Unlock()
	reviewCtx, cancel := context.WithCancel(ctx)
	stopRootCancel := context.AfterFunc(supervisor.root, cancel)
	defer stopRootCancel()
	defer cancel()
	ctx = reviewCtx
	defer func() {
		supervisor.mu.Lock()
		call.err = resultErr
		delete(supervisor.starts, request.ProfileID)
		close(call.done)
		supervisor.mu.Unlock()
		supervisor.wg.Done()
	}()
	// After a lost HTTP response, the committed audit record proves this exact
	// acknowledgement already finished. Never inspect/alter a later request.
	if review, err := supervisor.store.GetUnknownAttemptReview(ctx, request.ProfileID, request.AttemptID); err == nil {
		if review.Actor == request.Actor && review.Reason == request.Reason {
			return nil
		}
		return airuntime.ErrAttemptConflict
	} else if !errors.Is(err, airuntime.ErrNotFound) {
		return err
	}
	profile, err := supervisor.store.GetProfile(ctx, request.ProfileID)
	if err != nil {
		return err
	}
	if profile.Version != request.ProfileVersion || (profile.Status != airuntime.ProfileStatusPaused && profile.Status != airuntime.ProfileStatusStopped) {
		return airuntime.ErrConflict
	}
	attempt, err := supervisor.store.PendingTokenAttempt(ctx, request.ProfileID)
	if err != nil {
		return err
	}
	if attempt.ID != request.AttemptID || attempt.State != airuntime.TokenAttemptUnknown || !attempt.UpdatedAt.Equal(request.AttemptUpdatedAt) {
		return airuntime.ErrAttemptConflict
	}
	checkpoint, err := supervisor.store.GetCheckpoint(ctx, request.ProfileID)
	if err != nil {
		return err
	}
	if checkpoint.Version != request.CheckpointVersion {
		return airuntime.ErrConflict
	}
	var previous persistedState
	if json.Unmarshal(checkpoint.State, &previous) != nil || previous.PendingAttemptID != attempt.ID {
		return ErrAttemptRecovery
	}
	factory, ok := supervisor.factory.(UnknownReviewFactory)
	if !ok {
		return ErrFactoryUnavailable
	}
	if err = factory.ReviewUnknown(ctx, profile.ID, attempt.ID, request.Execution, request.Actor, request.Reason); err != nil {
		return err
	}
	// Start a fresh conversation and re-observe the game. Persistent profile
	// plans/memory survive; stale turn completion and task snapshots do not.
	next, _ := json.Marshal(persistedState{ProfileVersion: profile.Version, LastError: "previous turn outcome remains unknown; reviewed by operator"})
	// Linearize the final store commit against Close. Once closed is set,
	// transport-only partial progress must remain recoverable on restart.
	supervisor.mu.Lock()
	if supervisor.closed {
		supervisor.mu.Unlock()
		return ErrClosed
	}
	err = supervisor.store.ReviewUnknownAttempt(ctx, airuntime.ReviewUnknownAttemptRequest{ProfileID: profile.ID, AttemptID: attempt.ID, ExpectedUpdatedAt: request.AttemptUpdatedAt, ExpectedCheckpointVersion: request.CheckpointVersion, ExpectedProfileVersion: request.ProfileVersion, CheckpointState: next, Actor: request.Actor, Reason: request.Reason})
	if err == nil {
		delete(supervisor.profiles, profile.ID)
	}
	supervisor.mu.Unlock()
	return err
}
