package aisupervisor

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

const executorChangeActor = "executor_change"

var ErrExecutorChangeActive = errors.New("aisupervisor: executor change requires a paused or stopped profile")

// ExecutorChangePreparer is an optional factory hook for executor-specific
// state that is not owned by the supervisor checkpoint. The supervisor calls
// it while the executor-change fence is held and before committing the fresh
// supervisor checkpoint, so a failed preparation leaves the old supervisor
// identity intact. Implementations must be idempotent and must preserve any
// pending or unknown provider execution.
type ExecutorChangePreparer interface {
	PrepareExecutorChange(context.Context, string) error
}

// PrepareExecutorChange fences a profile before its model executor is moved
// to another runtime. It is an explicit migration operation: it never opens a
// session, never starts a model turn, and never runs during Restore. Unknown
// turns are reconciled and reviewed first so the old reservation remains in
// the audit/accounting history while the next executor gets a fresh thread.
func (supervisor *Supervisor) PrepareExecutorChange(ctx context.Context, profileID string) error {
	_, err := supervisor.prepareExecutorChange(ctx, profileID, 0)
	return err
}

// PrepareExecutorChangeAtVersion is the compare-and-swap form used by
// destructive admin operations. It fences and cleans the old executor only
// when the profile still has the version observed by the caller, then returns
// the new profile version written by the fence.
func (supervisor *Supervisor) PrepareExecutorChangeAtVersion(ctx context.Context, profileID string, expectedVersion int64) (version int64, resultErr error) {
	return supervisor.prepareExecutorChange(ctx, profileID, expectedVersion)
}

func (supervisor *Supervisor) prepareExecutorChange(ctx context.Context, profileID string, expectedVersion int64) (preparedVersion int64, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	call, err := supervisor.claimExecutorChange(profileID)
	if err != nil {
		return 0, err
	}
	defer supervisor.finishExecutorChange(profileID, call, &resultErr)

	changeCtx, cancel := context.WithCancel(ctx)
	stopRootCancel := context.AfterFunc(supervisor.root, cancel)
	defer stopRootCancel()
	defer cancel()
	supervisor.mu.Lock()
	invalidated, closed := call.invalidated, supervisor.closed
	if !invalidated && !closed {
		call.cancel = cancel
	}
	supervisor.mu.Unlock()
	if invalidated || closed {
		if invalidated {
			return 0, ErrProfileChanged
		}
		return 0, ErrClosed
	}

	profile, err := supervisor.store.GetProfile(changeCtx, profileID)
	if err != nil {
		return 0, err
	}
	if expectedVersion > 0 && profile.Version != expectedVersion {
		return 0, ErrProfileChanged
	}
	if profile.Status != airuntime.ProfileStatusPaused && profile.Status != airuntime.ProfileStatusStopped {
		return 0, ErrExecutorChangeActive
	}
	if err := supervisor.ensureExecutorChangeLive(profileID, call); err != nil {
		return 0, err
	}
	if err := supervisor.recoverUnknownBeforeStart(changeCtx, profileID, profile.Version, executorChangeActor, true); err != nil {
		return 0, err
	}
	if err := supervisor.ensureExecutorChangeLive(profileID, call); err != nil {
		return 0, err
	}

	// ReviewUnknown commits a clean checkpoint when an unknown attempt exists.
	// Re-read both records after that transaction so this migration never
	// resets a newer profile edit or leaves a second unsettled attempt behind.
	profile, err = supervisor.store.GetProfile(changeCtx, profileID)
	if err != nil {
		return 0, err
	}
	if expectedVersion > 0 && profile.Version != expectedVersion {
		return 0, ErrProfileChanged
	}
	if profile.Status != airuntime.ProfileStatusPaused && profile.Status != airuntime.ProfileStatusStopped {
		return 0, ErrExecutorChangeActive
	}
	_, pendingErr := supervisor.store.PendingTokenAttempt(changeCtx, profileID)
	if pendingErr == nil {
		return 0, ErrAttemptRecovery
	}
	if !errors.Is(pendingErr, airuntime.ErrNotFound) {
		return 0, pendingErr
	}
	checkpoint, checkpointErr := supervisor.store.GetCheckpoint(changeCtx, profileID)
	if checkpointErr != nil && !errors.Is(checkpointErr, airuntime.ErrNotFound) {
		return 0, checkpointErr
	}
	previous := persistedState{}
	checkpointVersion := int64(0)
	if checkpointErr == nil {
		checkpointVersion = checkpoint.Version
		if err := json.Unmarshal(checkpoint.State, &previous); err != nil {
			return 0, ErrAttemptRecovery
		}
		if previous.PendingAttemptID != "" {
			return 0, ErrAttemptRecovery
		}
	}
	// The old life activity and wake deadline are durable agent intent. The
	// game snapshot, thread, turn-completion marker, failure counters and
	// transient task state belong to the old executor and are intentionally
	// discarded so the next Start observes the game afresh.
	// Touching the profile through its CAS path creates a new version even
	// though its paused/stopped status is unchanged. A Start that began before
	// this migration therefore cannot publish a session afterward.
	status := profile.Status
	updated, err := supervisor.store.UpdateProfileCAS(changeCtx, profile.ID, profile.Version, airuntime.ProfilePatch{Status: &status, Actor: executorChangeActor})
	if err != nil {
		return 0, err
	}
	if err := supervisor.ensureExecutorChangeLive(profileID, call); err != nil {
		return 0, err
	}
	if preparer, ok := supervisor.factory.(ExecutorChangePreparer); ok {
		if err := preparer.PrepareExecutorChange(changeCtx, profile.ID); err != nil {
			return 0, err
		}
	}
	next := persistedState{
		Activity:       previous.Activity,
		ProfileVersion: updated.Version,
		NextDecisionAt: previous.NextDecisionAt,
		LastError:      "executor change prepared; awaiting a fresh session",
	}
	state, err := json.Marshal(next)
	if err != nil {
		return 0, err
	}
	if _, err := supervisor.store.SaveCheckpointCAS(changeCtx, profile.ID, checkpointVersion, state, executorChangeActor); err != nil {
		return 0, err
	}

	supervisor.mu.Lock()
	invalidated, closed = call.invalidated, supervisor.closed
	if !invalidated && !closed {
		// A completed managed object contains the old session snapshot and thread
		// identity. Drop it only after the durable reset has committed.
		delete(supervisor.profiles, profileID)
	}
	supervisor.mu.Unlock()
	if invalidated {
		return 0, ErrProfileChanged
	}
	if closed {
		return 0, ErrClosed
	}
	return updated.Version, nil
}

func (supervisor *Supervisor) claimExecutorChange(profileID string) (*startCall, error) {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.closed {
		return nil, ErrClosed
	}
	if supervisor.starts[profileID] != nil {
		return nil, ErrAlreadyRunning
	}
	if managed := supervisor.profiles[profileID]; managed != nil {
		select {
		case <-managed.done:
		default:
			return nil, ErrAlreadyRunning
		}
	}
	call := &startCall{done: make(chan struct{}), review: true}
	supervisor.starts[profileID] = call
	supervisor.wg.Add(1)
	return call, nil
}

func (supervisor *Supervisor) ensureExecutorChangeLive(profileID string, call *startCall) error {
	supervisor.mu.Lock()
	defer supervisor.mu.Unlock()
	if supervisor.closed {
		return ErrClosed
	}
	if supervisor.starts[profileID] != call || call.invalidated {
		return ErrProfileChanged
	}
	return nil
}

func (supervisor *Supervisor) finishExecutorChange(profileID string, call *startCall, result *error) {
	supervisor.mu.Lock()
	if result != nil && call.invalidated && *result == nil {
		*result = ErrProfileChanged
	}
	if result != nil {
		call.err = *result
	}
	if supervisor.starts[profileID] == call {
		delete(supervisor.starts, profileID)
	}
	close(call.done)
	supervisor.mu.Unlock()
	supervisor.wg.Done()
}
