package aibroker

import (
	"context"
	"errors"
)

// ReconcileUnknown drains an abandoned execution before an explicit restart.
// It never dispatches a request or treats an uncertain outcome as success.
// A failed lifecycle query retains the old claim so no replacement can run.
func (broker *Broker) ReconcileUnknown(ctx context.Context, profileID, requestID string) error {
	if broker == nil || broker.journal == nil || broker.docker == nil {
		return ErrInvalidConfig
	}
	if !profileIDPattern.MatchString(profileID) || !requestIDPattern.MatchString(requestID) {
		return ErrInvalidRequest
	}
	entry, err := broker.journal.Get(ctx, profileID, requestID)
	if err != nil {
		return err
	}
	key, _ := entryKey(profileID, requestID)
	broker.mu.Lock()
	_, active := broker.active[entry.ContainerName]
	transport := broker.transport[entry.ContainerName]
	_, recoverable := broker.recovery[key]
	closed := broker.closed
	broker.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if active || transport || (entry.State == RunRunning && !recoverable) {
		return ErrRunRunning
	}
	if entry.State == RunCompleted {
		return nil
	}
	inspector, ok := broker.docker.(DockerInspector)
	if !ok {
		return ErrDocker
	}
	bounded, cancel := context.WithTimeout(ctx, broker.config.StopTimeout)
	defer cancel()
	state, err := inspector.Inspect(bounded, entry.ContainerName)
	if err != nil && !errors.Is(err, ErrContainerNotFound) {
		return ErrDocker
	}
	if err == nil && !terminalContainerState(state) {
		if err = broker.docker.Stop(bounded, entry.ContainerName); err != nil {
			return ErrDocker
		}
		state, err = inspector.Inspect(bounded, entry.ContainerName)
		if err != nil && !errors.Is(err, ErrContainerNotFound) {
			return ErrDocker
		}
		if err == nil && !terminalContainerState(state) {
			return ErrRunRunning
		}
	}
	if entry.State == RunRunning {
		_, err = broker.reconcileRecovered(ctx, entry)
		return err
	}
	return nil
}
