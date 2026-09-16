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

func (runner *ContainerRunner) reviewUnknown(ctx context.Context, attemptID string, expected aisupervisor.UnknownExecution, actor, reason string) error {
	reviewer, ok := runner.broker.(containerUnknownReviewer)
	if !ok {
		return ErrContainerRunnerConfig
	}
	release, err := acquireContainerRunnerLock(ctx, runner.lockPath)
	if err != nil {
		return err
	}
	defer release()
	checkpoint, exists, err := runner.loadCheckpoint()
	if err != nil {
		return err
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
