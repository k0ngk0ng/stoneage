package aiservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/aisupervisor"
	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
)

type containerUnknownReviewer interface {
	ReviewUnknown(context.Context, string, string, time.Time, string, string) (aibroker.JournalEntry, error)
}

type containerUnknownReadiness interface {
	UnknownReviewReady(context.Context, string, string, time.Time) (bool, error)
}

type containerUnknownReconciler interface {
	ReconcileUnknown(context.Context, string, string) error
}

// ReconcileUnknown drains the old broker/container execution for an explicit
// supervisor Start. It is intentionally separate from InspectUnknown so the
// admin recovery/status query remains read-only.
func (factory *Factory) ReconcileUnknown(ctx context.Context, profileID, attemptID string) error {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if factory.active[profileID] != nil {
		return aisupervisor.ErrAttemptStillRunning
	}
	runner, err := factory.reviewRunnerLocked(profileID)
	if err != nil {
		return err
	}
	return runner.reconcileUnknown(ctx, attemptID)
}

func (factory *Factory) InspectUnknown(ctx context.Context, profileID, attemptID string) (aisupervisor.UnknownExecution, error) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	runner, err := factory.reviewRunnerLocked(profileID)
	if err != nil {
		return aisupervisor.UnknownExecution{}, err
	}
	return runner.inspectUnknown(ctx, attemptID)
}
func (factory *Factory) ReviewUnknown(ctx context.Context, profileID, attemptID string, expected aisupervisor.UnknownExecution, actor, reason string) error {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	runner, err := factory.reviewRunnerLocked(profileID)
	if err != nil {
		return err
	}
	return runner.reviewUnknown(ctx, attemptID, expected, actor, reason)
}

// Construct only the journal reader. Recovery must not load credentials,
// acquire a new game lease, create a profile, or start a Codex process.
func (factory *Factory) reviewRunnerLocked(profileID string) (*ContainerRunner, error) {
	if factory.closed || factory.cfg.ContainerBroker == nil || !containerRunnerProfileIDPattern.MatchString(profileID) || factory.active[profileID] != nil {
		return nil, ErrContainerRunnerRecovery
	}
	root := filepath.Join(factory.cfg.StateRoot, profileID)
	guard, err := runtimepath.NewGuard()
	if err != nil || guard.Check(root) != nil {
		return nil, ErrContainerRunnerConfig
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, ErrContainerRunnerConfig
	}
	runner := &ContainerRunner{profileID: profileID, stateRoot: root, checkpoint: filepath.Join(root, containerRunnerCheckpointName), lockPath: filepath.Join(root, containerRunnerLockName), broker: factory.cfg.ContainerBroker, guard: guard}
	if guard.CheckAll(runner.checkpoint, runner.lockPath) != nil {
		return nil, ErrContainerRunnerConfig
	}
	return runner, nil
}

func (runner *ContainerRunner) inspectUnknown(ctx context.Context, attemptID string) (aisupervisor.UnknownExecution, error) {
	release, err := acquireContainerRunnerLock(ctx, runner.lockPath)
	if err != nil {
		return aisupervisor.UnknownExecution{}, err
	}
	defer release()
	checkpoint, exists, err := runner.loadCheckpoint()
	if err != nil {
		return aisupervisor.UnknownExecution{}, err
	}
	if execution, ok, err := runner.inspectUndispatched(ctx, attemptID, checkpoint, exists); ok || err != nil {
		return execution, err
	}
	if !exists || checkpoint.CallerRequestID != attemptID || (checkpoint.State != containerRunUnknown && checkpoint.State != containerRunPending) {
		return aisupervisor.UnknownExecution{}, ErrContainerRunnerRecovery
	}
	result, err := runner.broker.Lookup(ctx, runner.profileID, checkpoint.RequestID)
	if !errors.Is(err, aibroker.ErrRunUnknown) || result.State != aibroker.RunUnknown {
		return aisupervisor.UnknownExecution{}, ErrContainerRunnerRecovery
	}
	inspector, ok := runner.broker.(containerUnknownReadiness)
	if !ok {
		return aisupervisor.UnknownExecution{}, ErrContainerRunnerConfig
	}
	stopped, err := inspector.UnknownReviewReady(ctx, runner.profileID, checkpoint.RequestID, result.Entry.UpdatedAt)
	if err != nil {
		return aisupervisor.UnknownExecution{}, err
	}
	return aisupervisor.UnknownExecution{RequestID: checkpoint.RequestID, State: string(result.State), UpdatedAt: result.Entry.UpdatedAt, Reviewed: result.Entry.Review != nil, ContainerStopped: stopped}, nil
}

func (runner *ContainerRunner) reconcileUnknown(ctx context.Context, attemptID string) error {
	release, err := acquireContainerRunnerLock(ctx, runner.lockPath)
	if err != nil {
		return err
	}
	defer release()
	checkpoint, exists, err := runner.loadCheckpoint()
	if err != nil {
		return err
	}
	if _, ok, err := runner.inspectUndispatched(ctx, attemptID, checkpoint, exists); ok || err != nil {
		return err
	}
	if !exists || checkpoint.CallerRequestID != attemptID || (checkpoint.State != containerRunPending && checkpoint.State != containerRunUnknown) {
		return ErrContainerRunnerRecovery
	}
	reconciler, ok := runner.broker.(containerUnknownReconciler)
	if !ok {
		return ErrContainerRunnerConfig
	}
	if err := reconciler.ReconcileUnknown(ctx, runner.profileID, checkpoint.RequestID); err != nil {
		if errors.Is(err, aibroker.ErrRunRunning) {
			return aisupervisor.ErrAttemptStillRunning
		}
		return err
	}
	return nil
}

func (runner *ContainerRunner) reviewUnknown(ctx context.Context, attemptID string, expected aisupervisor.UnknownExecution, actor, reason string) error {
	release, err := acquireContainerRunnerLock(ctx, runner.lockPath)
	if err != nil {
		return err
	}
	defer release()
	checkpoint, exists, err := runner.loadCheckpoint()
	if err != nil {
		return err
	}
	if expected.State == "not_dispatched" && expected.RequestID == attemptID {
		if _, safe, err := runner.inspectUndispatched(ctx, attemptID, checkpoint, exists); err != nil {
			return err
		} else if !safe {
			return ErrContainerRunnerRecovery
		}
		// The broker durably records every dispatch first. Its confirmed
		// absence plus an absent/older completed checkpoint proves this turn
		// never reached a runtime. Preserve the reviewed runtime namespace
		// and mark only the conversation boundary fresh. A retry is safe.
		if exists {
			checkpoint.ExecutorReset = true
			return runner.saveCheckpoint(checkpoint)
		}
		return nil
	}
	reviewer, ok := runner.broker.(containerUnknownReviewer)
	if !ok {
		return ErrContainerRunnerConfig
	}
	if !exists || checkpoint.CallerRequestID != attemptID || checkpoint.RequestID != expected.RequestID || expected.State != string(aibroker.RunUnknown) || (checkpoint.State != containerRunPending && checkpoint.State != containerRunUnknown) {
		return ErrContainerRunnerRecovery
	}
	entry, err := reviewer.ReviewUnknown(ctx, runner.profileID, checkpoint.RequestID, expected.UpdatedAt, actor, reason)
	if err != nil {
		return err
	}
	if entry.State != aibroker.RunUnknown || entry.Review == nil {
		return ErrContainerRunnerRecovery
	}
	checkpoint.State = containerRunUnknown
	checkpoint.Reviewed = true
	return runner.saveCheckpoint(checkpoint)
}

// inspectUndispatched runs with the profile checkpoint lock held. A lookup
// failure is never interpreted as absence, and pending/unknown checkpoints
// always retain the normal reconciliation path.
func (runner *ContainerRunner) inspectUndispatched(ctx context.Context, attemptID string, checkpoint containerRunCheckpoint, exists bool) (aisupervisor.UnknownExecution, bool, error) {
	if !containerRunnerRequestIDPattern.MatchString(attemptID) {
		return aisupervisor.UnknownExecution{}, false, ErrContainerRunnerRecovery
	}
	if exists && (checkpoint.State != containerRunCompleted || checkpoint.CallerRequestID == attemptID || checkpoint.RequestID == attemptID) {
		return aisupervisor.UnknownExecution{}, false, nil
	}
	_, err := runner.broker.Lookup(ctx, runner.profileID, attemptID)
	if errors.Is(err, aibroker.ErrJournalNotFound) {
		return aisupervisor.UnknownExecution{RequestID: attemptID, State: "not_dispatched", UpdatedAt: checkpoint.UpdatedAt, ContainerStopped: true}, true, nil
	}
	if err != nil {
		return aisupervisor.UnknownExecution{}, false, err
	}
	return aisupervisor.UnknownExecution{}, false, ErrContainerRunnerRecovery
}
