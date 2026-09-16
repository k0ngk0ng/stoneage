package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

const automationRecoveryMaxAge = 24 * time.Hour

var (
	errAutomationRecoveryIdentity = errors.New("automation recovery identity is unavailable")
	errAutomationRecoveryNotFound = errors.New("automation recovery is unavailable")
	errAutomationRecoveryBusy     = errors.New("automation recovery is already being claimed")
	errAutomationRecoveryNotReady = errors.New("automation recovery is not ready")
)

// automationRecoveryRecord is the live registry entry for one detached run.
// Valid persistent identities also have checkpoint-backed restart metadata.
// It deliberately holds no backend, socket, Engine or Coordinator. Those
// objects are lease-bound and are rebuilt only after an explicit recovery.
type automationRecoveryRecord struct {
	identity   automationIdentity
	handle     string
	mode       aicontrol.Mode
	reason     string
	config     AutomationConfig
	generation uint64
	detachedAt time.Time
	pending    bool
	claimed    bool
}

func cloneAutomationConfig(config AutomationConfig) AutomationConfig {
	copy := config
	copy.CharacterBuild = config.CharacterBuild.Clone()
	if config.Supply != nil {
		supply := *config.Supply
		copy.Supply = &supply
	}
	copy.Targets = append([]AutomationTarget(nil), config.Targets...)
	return copy
}

func (executor *AutomationExecutor) ensureRecoveryMapLocked() {
	if executor.recoveries == nil {
		executor.recoveries = make(map[string]*automationRecoveryRecord)
	}
}

func (executor *AutomationExecutor) clearRecoveryHandle(handle string) {
	if executor == nil || strings.TrimSpace(handle) == "" {
		return
	}
	executor.recoverMu.Lock()
	delete(executor.recoveries, handle)
	executor.recoverMu.Unlock()
}

// clearRecoveryRecord removes only the reservation owned by record. A late
// cleanup from an older detached session must not erase a newer offer (or a
// recovery that has already been claimed for the same handle).
func (executor *AutomationExecutor) clearRecoveryRecord(record *automationRecoveryRecord) {
	if executor == nil || record == nil {
		return
	}
	executor.recoverMu.Lock()
	if current := executor.recoveries[record.handle]; current == record {
		delete(executor.recoveries, record.handle)
	}
	executor.recoverMu.Unlock()
}

func (executor *AutomationExecutor) clearRecoveryIdentity(identity automationIdentity) {
	if executor == nil || !identity.valid() || !identity.canonicalCharacterID() {
		return
	}
	identity = identity.normalized()
	executor.recoverMu.Lock()
	for handle, record := range executor.recoveries {
		if record != nil && record.identity.matches(identity) {
			delete(executor.recoveries, handle)
		}
	}
	executor.recoverMu.Unlock()
}

func (executor *AutomationExecutor) registerRecovery(record *automationRecoveryRecord) {
	if executor == nil || record == nil {
		return
	}
	record.identity = record.identity.normalized()
	if strings.TrimSpace(record.handle) == "" || (record.mode != aicontrol.Quest && record.mode != aicontrol.Leveling) || !record.identity.valid() || !record.identity.canonicalCharacterID() {
		return
	}
	if record.detachedAt.IsZero() {
		record.detachedAt = time.Now().UTC()
	}
	record.config = cloneAutomationConfig(record.config)
	record.pending = false
	record.claimed = false
	executor.recoverMu.Lock()
	executor.ensureRecoveryMapLocked()
	// SQLite allows multiple historical paused checkpoints for a character.
	// The reconnect offer is intentionally singular: the newest detached run
	// supersedes an older in-process offer for the same authenticated role.
	for handle, existing := range executor.recoveries {
		if existing != nil && existing.identity.matches(record.identity) {
			if existing.claimed || existing.pending {
				// A recovery request has atomically claimed this role, or its
				// previous session is still completing the durable handoff. A
				// late detach must never replace either marker.
				executor.recoverMu.Unlock()
				return
			}
			delete(executor.recoveries, handle)
		}
	}
	executor.recoveries[record.handle] = record
	executor.recoverMu.Unlock()
}

// reserveRecovery publishes a handoff marker before Detach waits for the
// lease-bound runner. The marker intentionally has no backend or socket; it
// only occupies the authenticated identity so a reconnect cannot start or
// claim another run while the old session is still draining.
func (executor *AutomationExecutor) reserveRecovery(record *automationRecoveryRecord) bool {
	if executor == nil || record == nil {
		return false
	}
	record.identity = record.identity.normalized()
	if strings.TrimSpace(record.handle) == "" || (record.mode != aicontrol.Quest && record.mode != aicontrol.Leveling) || !record.identity.valid() || !record.identity.canonicalCharacterID() {
		return false
	}
	if record.detachedAt.IsZero() {
		record.detachedAt = time.Now().UTC()
	}
	record.config = cloneAutomationConfig(record.config)
	record.pending = true
	record.claimed = false

	executor.recoverMu.Lock()
	defer executor.recoverMu.Unlock()
	executor.ensureRecoveryMapLocked()
	now := time.Now().UTC()
	for handle, existing := range executor.recoveries {
		if existing == nil || existing.detachedAt.IsZero() || now.Sub(existing.detachedAt) > automationRecoveryMaxAge {
			delete(executor.recoveries, handle)
			continue
		}
		if !existing.identity.matches(record.identity) {
			continue
		}
		if existing.claimed || existing.pending {
			// A different detach or recovery owns this identity. Keep its
			// marker and let this old handle finish without registering over it.
			return false
		}
		delete(executor.recoveries, handle)
	}
	executor.recoveries[record.handle] = record
	return true
}

// publishRecovery turns the reservation into an offer only if it still owns
// the registry slot. This CAS-like identity check protects a newer recovery
// claim from a delayed detach completion.
func (executor *AutomationExecutor) publishRecovery(record *automationRecoveryRecord, reason string) {
	if executor == nil || record == nil {
		return
	}
	executor.recoverMu.Lock()
	defer executor.recoverMu.Unlock()
	current := executor.recoveries[record.handle]
	if current != record || current.claimed {
		return
	}
	if current.detachedAt.IsZero() || time.Since(current.detachedAt) > automationRecoveryMaxAge {
		delete(executor.recoveries, record.handle)
		return
	}
	if strings.TrimSpace(reason) != "" {
		current.reason = strings.TrimSpace(reason)
	}
	current.pending = false
}

func (executor *AutomationExecutor) recoveryBusy(identity automationIdentity) bool {
	if executor == nil || !identity.valid() || !identity.canonicalCharacterID() {
		return false
	}
	identity = identity.normalized()
	executor.recoverMu.Lock()
	defer executor.recoverMu.Unlock()
	now := time.Now().UTC()
	for handle, record := range executor.recoveries {
		if record == nil || record.detachedAt.IsZero() || now.Sub(record.detachedAt) > automationRecoveryMaxAge {
			delete(executor.recoveries, handle)
			continue
		}
		if record.identity.matches(identity) || record.identity.awaitingPersistentIdentity(identity) {
			// Any visible offer, claimed recovery, or pending handoff blocks a
			// direct Start. The caller must explicitly recover or discard it.
			return true
		}
	}
	return false
}

func recoveryIdentityForSession(ctx context.Context, session *AutomationSession) (automationIdentity, bool, error) {
	if session == nil || session.session == nil {
		return automationIdentity{}, false, errAutomationRecoveryIdentity
	}
	snapshot, err := session.Observe(ctx)
	if err != nil {
		return automationIdentity{}, false, err
	}
	if !snapshot.Connected || !snapshot.Player.HasStatus || (snapshot.Phase != aigame.PhaseWorld && snapshot.Phase != aigame.PhaseBattle) {
		return automationIdentity{}, false, nil
	}
	identity, ok := automationIdentityFromSnapshot(snapshot, session.session.serverLineID())
	return identity, ok, nil
}

func checkpointRecoverable(checkpoint automation.Checkpoint, record *automationRecoveryRecord) bool {
	if record == nil || checkpoint.Status != automation.Paused || checkpoint.Phase == "cancelled" {
		return false
	}
	if checkpoint.Plan.ID != record.handle || checkpoint.Plan.CharacterID != record.identity.CharacterID {
		return false
	}
	wantMode := string(record.mode)
	return checkpoint.Plan.Mode == wantMode
}

// Recovery reports one offer only after the newly connected stream has
// yielded a complete account/slot/name identity. Before authentication it is
// intentionally silent so normal login polling is unaffected.
func (executor *AutomationExecutor) Recovery(ctx context.Context, session *AutomationSession) (*AutomationRecovery, error) {
	if executor == nil {
		return nil, nil
	}
	identity, ready, err := recoveryIdentityForSession(ctx, session)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, nil
	}
	now := time.Now().UTC()
	executor.recoverMu.Lock()
	defer executor.recoverMu.Unlock()
	for handle, record := range executor.recoveries {
		if record == nil || record.detachedAt.IsZero() || now.Sub(record.detachedAt) > automationRecoveryMaxAge {
			delete(executor.recoveries, handle)
			continue
		}
		if record.identity.awaitingPersistentIdentity(identity) {
			return nil, errAutomationRecoveryNotReady
		}
		if !record.identity.matches(identity) {
			continue
		}
		if record.pending || record.claimed {
			return nil, errAutomationRecoveryBusy
		}
		checkpoint, loadErr := executor.config.Plans.Load(ctx, handle)
		if loadErr != nil {
			if errors.Is(loadErr, automation.ErrNotFound) {
				delete(executor.recoveries, handle)
				continue
			}
			return nil, loadErr
		}
		if checkpoint.Status == automation.Completed || checkpoint.Phase == "cancelled" {
			delete(executor.recoveries, handle)
			continue
		}
		if !checkpointRecoverable(checkpoint, record) {
			// A still-running checkpoint means detach has not finished its
			// durable handoff. Leave it hidden until the next poll.
			continue
		}
		reason := strings.TrimSpace(record.reason)
		if reason == "" {
			reason = strings.TrimSpace(checkpoint.Reason)
		}
		return &AutomationRecovery{Handle: handle, Mode: record.mode, Reason: reason}, nil
	}
	return nil, nil
}

func (executor *AutomationExecutor) claimRecovery(ctx context.Context, identity automationIdentity, handle string) (*automationRecoveryRecord, automation.Checkpoint, error) {
	handle = strings.TrimSpace(handle)
	if executor == nil || handle == "" {
		return nil, automation.Checkpoint{}, errAutomationRecoveryNotFound
	}
	executor.recoverMu.Lock()
	defer executor.recoverMu.Unlock()
	record := executor.recoveries[handle]
	if record == nil || record.detachedAt.IsZero() || time.Since(record.detachedAt) > automationRecoveryMaxAge {
		delete(executor.recoveries, handle)
		return nil, automation.Checkpoint{}, errAutomationRecoveryNotFound
	}
	if !record.identity.matches(identity) {
		return nil, automation.Checkpoint{}, errAutomationRecoveryIdentity
	}
	if record.pending || record.claimed {
		return nil, automation.Checkpoint{}, errAutomationRecoveryBusy
	}
	checkpoint, err := executor.config.Plans.Load(ctx, handle)
	if err != nil {
		if errors.Is(err, automation.ErrNotFound) {
			delete(executor.recoveries, handle)
			return nil, automation.Checkpoint{}, errAutomationRecoveryNotFound
		}
		return nil, automation.Checkpoint{}, err
	}
	if checkpoint.Status == automation.Completed || checkpoint.Phase == "cancelled" {
		delete(executor.recoveries, handle)
		return nil, checkpoint, errAutomationRecoveryNotReady
	}
	if !checkpointRecoverable(checkpoint, record) {
		return nil, checkpoint, errAutomationRecoveryNotReady
	}
	record.claimed = true
	copy := *record
	copy.config = cloneAutomationConfig(record.config)
	return &copy, checkpoint, nil
}

func (executor *AutomationExecutor) releaseRecoveryClaim(handle string) {
	if executor == nil || strings.TrimSpace(handle) == "" {
		return
	}
	executor.recoverMu.Lock()
	if record := executor.recoveries[handle]; record != nil {
		record.claimed = false
	}
	executor.recoverMu.Unlock()
}

func (executor *AutomationExecutor) consumeRecovery(handle string) {
	if executor == nil || strings.TrimSpace(handle) == "" {
		return
	}
	executor.recoverMu.Lock()
	delete(executor.recoveries, handle)
	executor.recoverMu.Unlock()
}

// Recover constructs a new backend and resumes the existing paused
// checkpoint. It never calls Start and therefore never creates a second plan.
func (executor *AutomationExecutor) Recover(ctx context.Context, session *AutomationSession, handle string) (AutomationHandle, error) {
	if executor == nil {
		return nil, errAutomationRecoveryNotFound
	}
	if ctx == nil {
		return nil, errors.New("automation lease is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, ready, err := recoveryIdentityForSession(ctx, session)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, errAutomationRecoveryIdentity
	}
	record, _, err := executor.claimRecovery(ctx, identity, handle)
	if err != nil {
		return nil, err
	}
	release := true
	defer func() {
		if release {
			executor.releaseRecoveryClaim(handle)
		}
	}()
	if session == nil || session.session == nil || session.session.gate == nil {
		return nil, errAutomationRecoveryIdentity
	}
	state := session.State()
	if session.mode != state.Mode || session.generation != state.Generation {
		return nil, aicontrol.ErrStale
	}
	if state.Generation == 0 || state.Mode != record.mode {
		return nil, aicontrol.ErrOwner
	}
	bound := &AutomationSession{ID: session.ID, session: session.session, mode: record.mode, generation: state.Generation}
	binding := aimcp.Binding{AccountID: identity.AccountID, CharacterID: identity.CharacterID, CharacterName: identity.CharacterName, Generation: state.Generation}
	request := AutomationStartRequest{SessionID: bound.ID, Mode: record.mode, Generation: state.Generation, Config: cloneAutomationConfig(record.config)}
	handleValue, err := executor.buildWebAutomationHandle(ctx, bound, request, binding)
	if err != nil {
		return nil, err
	}
	if !handleValue.identity.matches(record.identity) {
		handleValue.closeBackend()
		return nil, errAutomationRecoveryIdentity
	}
	handleValue.taskHandle = record.handle
	if err := handleValue.resumeFresh(ctx); err != nil {
		handleValue.closeBackend()
		return nil, err
	}
	executor.consumeRecovery(record.handle)
	release = false
	return handleValue, nil
}

// DiscardRecovery explicitly turns a detached paused checkpoint into a
// cancelled checkpoint. It performs no game write, and it is allowed to
// discard a submitted/unknown phase only because the player requested it.
func (executor *AutomationExecutor) DiscardRecovery(ctx context.Context, session *AutomationSession, handle string) error {
	if executor == nil {
		return errAutomationRecoveryNotFound
	}
	identity, ready, err := recoveryIdentityForSession(ctx, session)
	if err != nil {
		return err
	}
	if !ready {
		return errAutomationRecoveryIdentity
	}
	record, checkpoint, err := executor.claimRecovery(ctx, identity, handle)
	if err != nil {
		return err
	}
	_ = record
	release := true
	defer func() {
		if release {
			executor.releaseRecoveryClaim(handle)
		}
	}()
	if checkpoint.Status == automation.Completed {
		executor.consumeRecovery(handle)
		release = false
		return nil
	}
	persist, cancel := automationDetachedPersistenceContext(ctx)
	defer cancel()
	old := checkpoint.Revision
	checkpoint.Revision++
	checkpoint.Status = automation.Paused
	checkpoint.Phase = "cancelled"
	checkpoint.Reason = "断线前任务已由玩家取消"
	checkpoint.UpdatedAt = time.Now().UTC()
	if err := executor.config.Plans.Save(persist, checkpoint, old); err != nil {
		return fmt.Errorf("discard automation recovery: %w", err)
	}
	executor.consumeRecovery(handle)
	release = false
	return nil
}

// pauseCheckpointPreservingPhase is a detached fallback for a runner whose
// context ended before its controller could write the pause. It preserves
// prepared/submitted so a later explicit recovery still performs the normal
// unknown-delivery reconciliation instead of silently cancelling/replaying.
func pauseCheckpointPreservingPhase(ctx context.Context, store automation.Store, handle, reason string) (automation.Checkpoint, error) {
	if store == nil || strings.TrimSpace(handle) == "" {
		return automation.Checkpoint{}, errAutomationRecoveryNotReady
	}
	persist, cancel := automationDetachedPersistenceContext(ctx)
	defer cancel()
	checkpoint, err := store.Load(persist, handle)
	if err != nil {
		return checkpoint, err
	}
	if checkpoint.Status != automation.Running {
		return checkpoint, nil
	}
	old := checkpoint.Revision
	checkpoint.Revision++
	checkpoint.Status = automation.Paused
	if strings.TrimSpace(reason) != "" {
		checkpoint.Reason = reason
	}
	checkpoint.UpdatedAt = time.Now().UTC()
	if err := store.Save(persist, checkpoint, old); err != nil {
		return checkpoint, err
	}
	return checkpoint, nil
}

func automationDetachedPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
}

func waitAutomationCheckpoint(ctx context.Context, store automation.Store, handle string) (automation.Checkpoint, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if store == nil || strings.TrimSpace(handle) == "" {
		return automation.Checkpoint{}, errAutomationRecoveryNotReady
	}
	poll := 10 * time.Millisecond
	for {
		checkpoint, err := store.Load(ctx, handle)
		if err != nil {
			return checkpoint, err
		}
		if checkpoint.Status != automation.Running {
			return checkpoint, nil
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return checkpoint, ctx.Err()
			}
			return checkpoint, ctx.Err()
		case <-timer.C:
		}
	}
}

func (handle *webAutomationHandle) drainDetachedRunner(ctx context.Context) error {
	if handle == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	handle.mu.Lock()
	mode, quest, levelDone := handle.mode, handle.questTasks, handle.levelDone
	handle.mu.Unlock()
	if mode == aicontrol.Quest && quest != nil {
		done := make(chan struct{})
		go func() {
			quest.Close()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if levelDone != nil {
		select {
		case <-levelDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// finishDetachedAfterDrain completes a handoff whose bounded drain timed
// out. It runs without a caller context so the pending marker remains owned
// until the old runner has actually stopped and the checkpoint is durable.
func (handle *webAutomationHandle) finishDetachedAfterDrain(reservation *automationRecoveryRecord, reserved bool, store automation.Store, id string) {
	_ = handle.drainDetachedRunner(context.Background())
	defer handle.closeBackend()
	if !reserved || store == nil || strings.TrimSpace(id) == "" {
		if reserved && handle.executor != nil {
			handle.executor.clearRecoveryRecord(reservation)
		}
		return
	}
	persist, cancel := automationDetachedPersistenceContext(context.Background())
	defer cancel()
	checkpoint, err := store.Load(persist, id)
	if err == nil && checkpoint.Status == automation.Running {
		checkpoint, err = pauseCheckpointPreservingPhase(persist, store, id, "控制租约已结束，任务已暂停")
	}
	if err != nil {
		handle.executor.clearRecoveryRecord(reservation)
		return
	}
	if checkpoint.Status == automation.Paused && checkpoint.Phase != "cancelled" && checkpoint.Status != automation.Completed {
		handle.executor.publishRecovery(reservation, checkpoint.Reason)
		return
	}
	handle.executor.clearRecoveryRecord(reservation)
}

// Detach is invoked only after the Web session's Gate has been closed. It
// waits for the lease-bound runner to persist a paused checkpoint, registers
// metadata for this identity, and then closes all live backend resources.
func (handle *webAutomationHandle) Detach(ctx context.Context) error {
	if handle == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	handle.mu.Lock()
	if handle.detached {
		handle.mu.Unlock()
		return nil
	}
	handle.detached = true
	mode, id := handle.mode, handle.taskHandle
	executor, store := handle.executor, handle.plans
	identity, config, generation := handle.identity, handle.config, handle.generation
	quest := handle.questTasks
	levelDone := handle.levelDone
	handle.mu.Unlock()

	// Occupy the identity before waiting for the old runner to finish its
	// durable pause. A newly connected session must not observe a free slot and
	// start a second run during this handoff window.
	var reservation *automationRecoveryRecord
	reserved := false
	if executor != nil && (mode == aicontrol.Quest || mode == aicontrol.Leveling) {
		reservation = &automationRecoveryRecord{
			identity: identity, handle: id, mode: mode, config: config,
			generation: generation, detachedAt: time.Now().UTC(), pending: true,
		}
		reserved = executor.reserveRecovery(reservation)
	}

	// The caller's context is generally a short cleanup timeout. Use its
	// deadline for the first wait, but do not let request cancellation suppress
	// the durable handoff or make us close a backend while its runner is live.
	waitCtx, waitCancel := context.WithTimeout(context.WithoutCancel(ctx), defaultWriteTimeout)
	var checkpoint automation.Checkpoint
	var waitErr error
	if (mode == aicontrol.Quest && quest != nil) || levelDone != nil {
		checkpoint, waitErr = waitAutomationCheckpoint(waitCtx, store, id)
	} else {
		// Handles assembled by tests/embedders may have no local runner. Do
		// not spend the full handoff timeout waiting for a status which no
		// goroutine can change.
		checkpoint, waitErr = store.Load(waitCtx, id)
	}
	waitCancel()
	if waitErr != nil && !errors.Is(waitErr, automation.ErrNotFound) && !errors.Is(waitErr, context.DeadlineExceeded) {
		// A storage read error says nothing about whether the old runner has
		// stopped. Retain the handoff marker until cleanup has drained it.
		go handle.finishDetachedAfterDrain(reservation, reserved, store, id)
		return waitErr
	}
	drainCtx, drainCancel := context.WithTimeout(context.Background(), defaultWriteTimeout)
	drainErr := handle.drainDetachedRunner(drainCtx)
	drainCancel()
	if drainErr != nil {
		// The runner may still own the old backend after the bounded drain
		// wait. Keep the pending marker busy and finish the handoff in the
		// background; closing GameTasks synchronously here can itself wait for
		// that runner and would otherwise expose a free identity too early.
		go handle.finishDetachedAfterDrain(reservation, reserved, store, id)
		return drainErr
	}
	// Reload after the runner is drained. If its own cancellation path did not
	// get a chance to persist the pause, this detached CAS fallback preserves
	// the current phase and supplies a reason without turning it into cancelled.
	persistCtx, persistCancel := automationDetachedPersistenceContext(context.Background())
	if waitErr == nil || errors.Is(waitErr, context.DeadlineExceeded) {
		checkpoint, waitErr = store.Load(persistCtx, id)
	}
	if errors.Is(waitErr, automation.ErrNotFound) {
		persistCancel()
		if reserved {
			executor.clearRecoveryRecord(reservation)
		}
		handle.closeBackend()
		return nil
	}
	if waitErr != nil {
		persistCancel()
		if reserved {
			executor.clearRecoveryRecord(reservation)
		}
		handle.closeBackend()
		return waitErr
	}
	if checkpoint.Status == automation.Running {
		checkpoint, waitErr = pauseCheckpointPreservingPhase(persistCtx, store, id, "控制租约已结束，任务已暂停")
	}
	persistCancel()
	if waitErr != nil {
		if reserved {
			executor.clearRecoveryRecord(reservation)
		}
	} else if checkpoint.Status == automation.Paused && checkpoint.Phase != "cancelled" && checkpoint.Status != automation.Completed {
		if reserved {
			executor.publishRecovery(reservation, checkpoint.Reason)
		}
	} else if checkpoint.Status == automation.Completed {
		if reserved {
			executor.clearRecoveryRecord(reservation)
		}
	} else if reserved {
		// A cancelled or otherwise terminal checkpoint cannot be recovered;
		// release the pending marker once the durable state says so.
		executor.clearRecoveryRecord(reservation)
	}
	handle.closeBackend()
	return waitErr
}

var _ automationRecoveryProvider = (*AutomationExecutor)(nil)
var _ automationDetacher = (*webAutomationHandle)(nil)
