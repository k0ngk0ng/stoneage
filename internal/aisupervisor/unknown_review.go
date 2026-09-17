package aisupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"log"
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

// UnknownReviewReconciler is an optional transport hook used only by an
// explicit Start. It may stop an orphaned execution after proving that the
// supervisor session which owned it has already exited. Read-only recovery
// inspection must never call this hook.
type UnknownReviewReconciler interface {
	ReconcileUnknown(context.Context, string, string) error
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
	err := supervisor.reviewUnknown(ctx, request)
	outcome := "completed"
	if err != nil {
		outcome = "failed"
	}
	log.Printf("event=ai_unknown_turn_review_%s profile=%q request_id=%q execution_state=%s stopped=%t", outcome, request.ProfileID, request.AttemptID, request.Execution.State, request.Execution.ContainerStopped)
	return err
}

// reviewUnknown contains the durable review transaction shared by the
// operator endpoint and the explicit Start path. The caller has already
// serialized the profile's lifecycle through supervisor.starts when this is
// invoked from Start.
func (supervisor *Supervisor) reviewUnknown(ctx context.Context, request UnknownRecoveryRequest) error {
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
		// Local/non-idempotent runners retain the historical recovery behavior
		// in runProfile. The explicit pre-start review is available only when
		// the transport can inspect and acknowledge the unknown execution.
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

const unknownReviewStartActor = "operator_start"

// recoverUnknownBeforeStart resolves an unsettled unknown turn immediately
// before opening a new agent session. It is deliberately reachable from the
// explicit Start path only; Restore and the read-only UnknownRecovery API do
// not stop containers or change accounting.
func (supervisor *Supervisor) recoverUnknownBeforeStart(ctx context.Context, profileID string, expectedVersion int64, actor string, requireFactory bool) error {
	profile, err := supervisor.store.GetProfile(ctx, profileID)
	if err != nil {
		return err
	}
	if expectedVersion > 0 && profile.Version != expectedVersion {
		return ErrProfileChanged
	}
	attempt, err := supervisor.store.PendingTokenAttempt(ctx, profileID)
	if errors.Is(err, airuntime.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if attempt.State != airuntime.TokenAttemptUnknown {
		// Prepared/dispatched attempts retain the existing idempotent recovery
		// path in runProfile. Only an already quarantined unknown attempt needs
		// the explicit review gate before opening a new session.
		return nil
	}
	if profile.Status != airuntime.ProfileStatusPaused && profile.Status != airuntime.ProfileStatusStopped {
		// An active profile may be a supervisor/process restart boundary. Let
		// runProfile use its existing transport recovery path; the explicit
		// paused/stopped start gate applies after an operator-visible pause.
		return nil
	}
	checkpoint, err := supervisor.store.GetCheckpoint(ctx, profileID)
	if err != nil {
		return err
	}
	var state persistedState
	if json.Unmarshal(checkpoint.State, &state) != nil || state.PendingAttemptID != attempt.ID {
		return ErrAttemptRecovery
	}
	factory, ok := supervisor.factory.(UnknownReviewFactory)
	if !ok {
		// A transport without an inspect/review seam falls back to the
		// existing RecoveryRunner path in runProfile.
		if requireFactory {
			return ErrFactoryUnavailable
		}
		return nil
	}
	// The reconciler is intentionally optional so local/fake transports can
	// still use the same review protocol. A production container factory
	// implements it to drain the old broker/container claim first.
	if reconciler, ok := supervisor.factory.(UnknownReviewReconciler); ok {
		log.Printf("event=ai_unknown_turn_reconcile profile=%q request_id=%q", profileID, attempt.ID)
		if err := reconciler.ReconcileUnknown(ctx, profileID, attempt.ID); err != nil {
			return err
		}
	}
	execution, err := factory.InspectUnknown(ctx, profileID, attempt.ID)
	if err != nil {
		return err
	}
	if !execution.ContainerStopped {
		return ErrAttemptStillRunning
	}
	request := UnknownRecoveryRequest{
		UnknownRecoveryStatus: UnknownRecoveryStatus{
			ProfileID:         profile.ID,
			ProfileVersion:    profile.Version,
			AttemptID:         attempt.ID,
			AttemptUpdatedAt:  attempt.UpdatedAt,
			CheckpointVersion: checkpoint.Version,
			ReservedTokens:    attempt.Charge,
			Ready:             true,
			Execution:         execution,
		},
		Actor:  actor,
		Reason: airuntime.UnknownReviewReason,
	}
	err = supervisor.reviewUnknown(ctx, request)
	outcome := "completed"
	if err != nil {
		outcome = "failed"
	}
	log.Printf("event=ai_unknown_turn_review_%s profile=%q request_id=%q execution_state=%s stopped=%t", outcome, profileID, attempt.ID, execution.State, execution.ContainerStopped)
	return err
}
