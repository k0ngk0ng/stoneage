package aibroker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

var (
	profileIDPattern   = mustNamePattern(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	requestIDPattern   = mustNamePattern(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	namePrefixPattern  = mustNamePattern(`^[a-z0-9][a-z0-9_.-]{0,24}$`)
	memoryLimitPattern = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)(b|k|kb|m|mb|g|gb|t|tb|p|pb)?$`)
)

const (
	runtimeUser      = "10001"
	runtimeStateRoot = "/var/lib/stoneage-ai"
	runtimeCodex     = "/usr/local/bin/codex"
	runtimeMCP       = "/usr/local/bin/stoneage-game-mcp"
	runtimeSkillRoot = "/opt/stoneage/ai/skills"
	runtimeHome      = runtimeStateRoot
	profileArg       = "-profile"
)

// Broker materializes one fixed RunSpec per request and owns the request
// journal. A Broker can safely be copied only through its pointer; its
// dependencies may maintain their own synchronization.
type Broker struct {
	config      Config
	docker      Docker
	journal     Journal
	mu          sync.Mutex
	closed      bool
	active      map[string]context.CancelFunc
	recovery    map[string]struct{}
	wg          sync.WaitGroup
	ownJournal  bool
	journalLock func()
}

// New validates all host-owned values and constructs missing process/file
// adapters. The normal production call supplies DockerBinary and JournalPath;
// tests supply Docker and NewMemoryJournal so no real Docker daemon is used.
func New(config Config) (*Broker, error) {
	config.Image = strings.TrimSpace(config.Image)
	if config.Image == "" {
		return nil, fmt.Errorf("%w: Image is required", ErrInvalidConfig)
	}
	config.Network = strings.TrimSpace(config.Network)
	if config.Network == "" {
		return nil, fmt.Errorf("%w: Network is required", ErrInvalidConfig)
	}
	if hasControlOrSpace(config.Image) || strings.HasPrefix(config.Image, "-") {
		return nil, fmt.Errorf("%w: image is invalid", ErrInvalidConfig)
	}
	if hasControlOrSpace(config.Network) || strings.HasPrefix(config.Network, "-") {
		return nil, fmt.Errorf("%w: network is invalid", ErrInvalidConfig)
	}
	if config.CPUs == 0 {
		config.CPUs = DefaultCPUs
	}
	if config.Memory == "" {
		config.Memory = DefaultMemory
	}
	if config.MemorySwap == "" {
		config.MemorySwap = DefaultMemorySwap
	}
	if config.PidsLimit == 0 {
		config.PidsLimit = DefaultPidsLimit
	}
	if err := validateResourceLimits(config.CPUs, config.Memory, config.MemorySwap, config.PidsLimit); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	config.NamePrefix = strings.ToLower(strings.TrimSpace(config.NamePrefix))
	if config.NamePrefix == "" {
		config.NamePrefix = DefaultNamePrefix
	}
	if !namePrefixPattern.MatchString(config.NamePrefix) {
		return nil, fmt.Errorf("%w: name prefix is invalid", ErrInvalidConfig)
	}
	if config.MaxRequestBytes <= 0 {
		config.MaxRequestBytes = DefaultMaxRequestBytes
	}
	if config.MaxRequestBytes > airunner.MaxRequestBytes {
		return nil, fmt.Errorf("%w: request limit exceeds runtime limit", ErrInvalidConfig)
	}
	if config.MaxStdoutBytes <= 0 {
		config.MaxStdoutBytes = DefaultMaxStdoutBytes
	}
	if config.MaxStderrBytes <= 0 {
		config.MaxStderrBytes = DefaultMaxStderrBytes
	}
	if config.MaxStdoutBytes < 1024 || config.MaxStderrBytes < 1024 {
		return nil, fmt.Errorf("%w: output limits are too small", ErrInvalidConfig)
	}
	if config.StopTimeout <= 0 {
		config.StopTimeout = DefaultStopTimeout
	}
	if config.StopTimeout > 30*time.Second {
		return nil, fmt.Errorf("%w: stop timeout is too long", ErrInvalidConfig)
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if err := validateEnvironment(config.Environment); err != nil {
		return nil, err
	}
	config.Environment = cloneEnvironment(config.Environment)
	config.RunnerCommand = append([]string(nil), config.RunnerCommand...)
	for _, arg := range config.RunnerCommand {
		if arg == "" || strings.ContainsAny(arg, "\x00\r\n") {
			return nil, fmt.Errorf("%w: runner command is invalid", ErrInvalidConfig)
		}
	}

	docker := config.Docker
	if docker == nil {
		var err error
		docker, err = NewDocker(config.DockerBinary)
		if err != nil {
			return nil, err
		}
		if cli, ok := docker.(*DockerCLI); ok {
			cli.MaxStdoutBytes = config.MaxStdoutBytes
			cli.MaxStderrBytes = config.MaxStderrBytes
			cli.StopTimeout = config.StopTimeout
		}
	}
	journal := config.Journal
	ownJournal := false
	var journalLock func()
	if journal == nil && strings.TrimSpace(config.JournalPath) != "" {
		var err error
		var sqliteJournal *SQLiteJournal
		sqliteJournal, err = OpenSQLiteJournal(config.JournalPath)
		if err != nil {
			return nil, err
		}
		journal = sqliteJournal
		journalLock, err = tryAcquireJournalLock(sqliteJournal.Path() + ".lock")
		if err != nil {
			_ = sqliteJournal.Close()
			if errors.Is(err, ErrJournalBusy) {
				return nil, fmt.Errorf("%w: acquire journal lock", ErrJournalBusy)
			}
			return nil, fmt.Errorf("%w: acquire journal lock", ErrInvalidConfig)
		}
		ownJournal = true
	}
	if journal == nil {
		if journalLock != nil {
			journalLock()
		}
		return nil, fmt.Errorf("%w: persistent Journal or JournalPath is required", ErrInvalidConfig)
	}
	config.Docker = docker
	config.Journal = journal
	broker := &Broker{config: config, docker: docker, journal: journal, active: make(map[string]context.CancelFunc), recovery: make(map[string]struct{}), ownJournal: ownJournal, journalLock: journalLock}
	if err := broker.loadRecovery(); err != nil {
		_ = broker.Close()
		return nil, err
	}
	return broker, nil
}

// loadRecovery snapshots only the running rows that existed before this
// Broker was constructed. Those keys are kept separate from claims made by
// this Broker so later status calls can re-check old containers without
// reconciling a request that is currently being launched.
func (broker *Broker) loadRecovery() error {
	listing, ok := broker.journal.(JournalRecovery)
	if !ok {
		return nil
	}
	entries, err := listing.ListRunning(context.Background())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		key, keyErr := entryKey(entry.ProfileID, entry.RequestID)
		if keyErr != nil {
			return fmt.Errorf("%w: recovery entry key", ErrDocker)
		}
		updated, err := broker.reconcileRecovered(context.Background(), entry)
		if err != nil {
			return err
		}
		if updated.State == RunRunning {
			broker.mu.Lock()
			broker.recovery[key] = struct{}{}
			broker.mu.Unlock()
		}
	}
	return nil
}

func (broker *Broker) isRecovered(key string) bool {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	_, ok := broker.recovery[key]
	return ok
}

func (broker *Broker) forgetRecovered(key string) {
	broker.mu.Lock()
	delete(broker.recovery, key)
	broker.mu.Unlock()
}

// reconcileRecovered performs one read-only lifecycle check for a startup
// recovery row. Docker/daemon errors and temporary result-read failures leave
// the row running so the broker never guesses that a provider turn failed.
// An exited container is completed only after its bounded output contains a
// strict response for this exact journal pair.
func (broker *Broker) reconcileRecovered(ctx context.Context, entry JournalEntry) (JournalEntry, error) {
	if entry.State != RunRunning {
		key, _ := entryKey(entry.ProfileID, entry.RequestID)
		broker.forgetRecovered(key)
		return entry, nil
	}
	inspector, ok := broker.docker.(DockerInspector)
	if !ok {
		return entry, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	inspectCtx, cancel := context.WithTimeout(ctx, broker.config.StopTimeout)
	state, inspectErr := inspector.Inspect(inspectCtx, entry.ContainerName)
	cancel()
	if inspectErr != nil {
		if !errors.Is(inspectErr, ErrContainerNotFound) {
			return entry, nil
		}
		return broker.markRecoveredUnknown(entry)
	}
	if state == DockerContainerExited {
		reader, ok := broker.docker.(DockerResultReader)
		if !ok {
			return broker.markRecoveredUnknown(entry)
		}
		readCtx, readCancel := context.WithTimeout(ctx, broker.config.StopTimeout)
		result, readErr := reader.ReadResult(readCtx, entry.ContainerName)
		readCancel()
		if readErr != nil {
			// A vanished container is definitive. An output limit is also
			// definitive because the bounded reader cannot establish a valid
			// response. Daemon/log query errors remain retryable.
			if errors.Is(readErr, ErrContainerNotFound) || errors.Is(readErr, ErrOutputLimit) {
				return broker.markRecoveredUnknown(entry)
			}
			return entry, nil
		}
		response, responseErr := broker.decodeRecoveredResponse(entry, result)
		if responseErr != nil {
			return broker.markRecoveredUnknown(entry)
		}
		return broker.markRecoveredCompleted(entry, response)
	}
	if state == DockerContainerDead {
		return broker.markRecoveredUnknown(entry)
	}
	if !terminalContainerState(state) {
		return entry, nil
	}
	return broker.markRecoveredUnknown(entry)
}

func terminalContainerState(state DockerContainerState) bool {
	switch state {
	case DockerContainerExited, DockerContainerDead:
		return true
	default:
		return false
	}
}

func (broker *Broker) markRecoveredUnknown(entry JournalEntry) (JournalEntry, error) {
	unknown := entry
	unknown.State = RunUnknown
	unknown.ErrorCode = string(RunUnknown)
	unknown.Response = marshalUnknownResponse(entry.ProfileID, entry.RequestID)
	unknown.UpdatedAt = broker.config.Clock()
	return broker.publishRecovered(entry, unknown)
}

// markRecoveredCompleted publishes an observed terminal result using the
// same state CAS as unknown recovery. The response is durable before cleanup
// is attempted, so a cleanup failure cannot turn a successful provider turn
// back into an unknown one.
func (broker *Broker) markRecoveredCompleted(entry JournalEntry, response airunner.Response) (JournalEntry, error) {
	completed := entry
	completed.State = RunCompleted
	completed.ErrorCode = ""
	completed.Response = marshalResponse(response)
	completed.UpdatedAt = broker.config.Clock()
	updated, err := broker.publishRecovered(entry, completed)
	if err != nil {
		return updated, err
	}
	if updated.State == RunCompleted {
		broker.cleanupCompleted(updated)
	}
	return updated, nil
}

// publishRecovered is the common compare-and-publish path for startup rows.
// A Journal implementation that can list rows but cannot CAS them is not safe
// for recovery transitions and therefore leaves the row running.
func (broker *Broker) publishRecovered(entry, candidate JournalEntry) (JournalEntry, error) {
	cas, ok := broker.journal.(JournalCAS)
	if !ok {
		return entry, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), broker.config.StopTimeout)
	err := cas.UpdateIfState(ctx, RunRunning, candidate)
	cancel()
	if err == nil {
		key, _ := entryKey(entry.ProfileID, entry.RequestID)
		broker.forgetRecovered(key)
		return candidate, nil
	}
	if errors.Is(err, ErrJournalState) {
		current, getErr := broker.journal.Get(context.Background(), entry.ProfileID, entry.RequestID)
		if getErr != nil {
			return entry, getErr
		}
		key, _ := entryKey(entry.ProfileID, entry.RequestID)
		if current.State != RunRunning {
			broker.forgetRecovered(key)
		}
		return current, nil
	}
	return entry, err
}

func (broker *Broker) decodeRecoveredResponse(entry JournalEntry, result DockerResult) (airunner.Response, error) {
	if result.ExitCode != 0 {
		return airunner.Response{}, ErrRunUnknown
	}
	if len(result.Stdout) == 0 || len(result.Stdout) > broker.config.MaxStdoutBytes || len(result.Stderr) > broker.config.MaxStderrBytes {
		return airunner.Response{}, ErrOutputLimit
	}
	response, err := decodeStrictResponse(result.Stdout)
	if err != nil {
		return airunner.Response{}, fmt.Errorf("%w: recovered response", ErrResponse)
	}
	if !response.OK || response.ProfileID != entry.ProfileID || response.RequestID != entry.RequestID {
		return airunner.Response{}, ErrResponse
	}
	if response.Result != nil && response.Result.ProfileID != "" && response.Result.ProfileID != entry.ProfileID {
		return airunner.Response{}, ErrResponse
	}
	if !runtimeTurnComplete(response) {
		return airunner.Response{}, ErrResponse
	}
	return response, nil
}

func marshalResponse(response airunner.Response) []byte {
	raw, err := json.Marshal(response)
	if err != nil {
		return nil
	}
	return raw
}

func marshalUnknownResponse(profileID, requestID string) []byte {
	response, err := json.Marshal(airunner.Response{OK: false, ProfileID: profileID, RequestID: requestID, Error: string(RunUnknown)})
	if err != nil {
		return nil
	}
	return response
}

// Config returns a copy of broker configuration without mutable maps/slices.
func (broker *Broker) Config() Config {
	if broker == nil {
		return Config{}
	}
	config := broker.config
	config.Environment = cloneEnvironment(config.Environment)
	config.RunnerCommand = append([]string(nil), config.RunnerCommand...)
	return config
}

// VolumeName returns the deterministic profile volume name used by this
// broker. It is safe to call for a validated profile ID only.
func (broker *Broker) VolumeName(profileID string) string {
	prefix := DefaultNamePrefix
	if broker != nil && broker.config.NamePrefix != "" {
		prefix = broker.config.NamePrefix
	}
	return deterministicName(prefix, "profile", profileID)
}

// ContainerName returns the deterministic name for one profile/request pair.
// Hashing both identifiers prevents names from disclosing profile metadata and
// keeps cancellation able to address the exact container.
func (broker *Broker) ContainerName(profileID, requestID string) string {
	prefix := DefaultNamePrefix
	if broker != nil && broker.config.NamePrefix != "" {
		prefix = broker.config.NamePrefix
	}
	return deterministicName(prefix, "run", profileID, requestID)
}

// CleanupProbe removes the disposable container and profile volume belonging
// to one exact model-only probe. The request must still carry Probe=true and
// its complete payload hash must match the journal row created by Run; this
// prevents an administrator cleanup call from addressing another profile or
// from deleting a volume after its request credentials have changed.
//
// Cleanup is fail-closed. A Docker inspector is required to prove the
// container is absent or terminal. Running, transitional, inspection-error,
// and removal-error cases leave both the container and volume in place. Game
// requests and their unknown outcomes never use this method.
func (broker *Broker) CleanupProbe(ctx context.Context, request airunner.ExecuteRequest) error {
	if broker == nil || broker.docker == nil || broker.journal == nil {
		return fmt.Errorf("%w: broker is not configured", ErrInvalidConfig)
	}
	if !request.Probe {
		return fmt.Errorf("%w: probe cleanup requires a model-only request", ErrInvalidRequest)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRequest(request); err != nil {
		return err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("%w: encode probe request", ErrInvalidRequest)
	}
	if len(payload) > broker.config.MaxRequestBytes {
		return ErrRequestTooLarge
	}
	hash := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(hash[:])
	entry, err := broker.journal.Get(ctx, request.ProfileID, request.RequestID)
	if err != nil {
		return err
	}
	if entry.PayloadHash != payloadHash || entry.ContainerName != broker.ContainerName(request.ProfileID, request.RequestID) || entry.VolumeName != broker.VolumeName(request.ProfileID) {
		return ErrJournalConflict
	}
	if entry.State == RunRunning {
		return ErrRunRunning
	}
	if entry.State != RunCompleted && entry.State != RunUnknown {
		return ErrResponse
	}

	// Keep a locally active run from being removed between the journal read and
	// lifecycle inspection. A broker process cannot normally have an active
	// run for a completed/unknown row, but this fence also covers cancellation
	// and concurrent cleanup calls.
	broker.mu.Lock()
	if broker.closed {
		broker.mu.Unlock()
		return ErrClosed
	}
	if _, active := broker.active[entry.ContainerName]; active {
		broker.mu.Unlock()
		return ErrProfileBusy
	}
	broker.mu.Unlock()

	inspector, ok := broker.docker.(DockerInspector)
	if !ok {
		return fmt.Errorf("%w: probe container inspector is required", ErrDocker)
	}
	inspectCtx, inspectCancel := context.WithTimeout(ctx, broker.config.StopTimeout)
	state, inspectErr := inspector.Inspect(inspectCtx, entry.ContainerName)
	inspectCancel()
	containerAbsent := errors.Is(inspectErr, ErrContainerNotFound)
	if inspectErr != nil && !containerAbsent {
		return fmt.Errorf("%w: inspect probe container", ErrDocker)
	}
	if !containerAbsent {
		if !terminalContainerState(state) {
			return ErrRunRunning
		}
		remover, removable := broker.docker.(DockerRemover)
		if !removable {
			return fmt.Errorf("%w: probe container remover is required", ErrDocker)
		}
		removeCtx, removeCancel := context.WithTimeout(ctx, broker.config.StopTimeout)
		removeErr := remover.Remove(removeCtx, entry.ContainerName)
		removeCancel()
		if removeErr != nil && !errors.Is(removeErr, ErrContainerNotFound) {
			return removeErr
		}
	}

	volumeRemover, removable := broker.docker.(DockerVolumeRemover)
	if !removable {
		return fmt.Errorf("%w: probe volume remover is required", ErrDocker)
	}
	volumeCtx, volumeCancel := context.WithTimeout(ctx, broker.config.StopTimeout)
	volumeErr := volumeRemover.RemoveVolume(volumeCtx, entry.VolumeName)
	volumeCancel()
	if volumeErr != nil {
		return volumeErr
	}
	return nil
}

// ProfileVolumeName is the default-prefix helper for schedulers that need to
// display the volume selected before constructing a Broker.
func ProfileVolumeName(profileID string) string {
	return deterministicName(DefaultNamePrefix, "profile", profileID)
}

// ContainerName is the default-prefix helper corresponding to
// ProfileVolumeName.
func ContainerName(profileID, requestID string) string {
	return deterministicName(DefaultNamePrefix, "run", profileID, requestID)
}

// Run accepts an airunner request, starts at most one container for its
// request ID, and records the outcome before returning. Existing completed,
// running, and unknown entries are replayed/returned without a fresh model
// turn. Request payloads are hashed but never persisted.
func (broker *Broker) Run(ctx context.Context, request airunner.ExecuteRequest) (RunResult, error) {
	if broker == nil || broker.docker == nil || broker.journal == nil {
		return RunResult{}, fmt.Errorf("%w: broker is not configured", ErrInvalidConfig)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	broker.mu.Lock()
	closed := broker.closed
	broker.mu.Unlock()
	if closed {
		return RunResult{Response: requestErrorResponse(request, ErrClosed)}, ErrClosed
	}
	if err := validateRequest(request); err != nil {
		return RunResult{Response: requestErrorResponse(request, err)}, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return RunResult{Response: requestErrorResponse(request, ErrInvalidRequest)}, fmt.Errorf("%w: encode request", ErrInvalidRequest)
	}
	if len(payload) > broker.config.MaxRequestBytes {
		return RunResult{Response: requestErrorResponse(request, ErrRequestTooLarge)}, ErrRequestTooLarge
	}
	// Decode the exact bytes through the runtime boundary as a final check.
	// This keeps future additions to ExecuteRequest from silently accepting
	// fields that the process boundary would reject.
	if _, err := airunner.DecodeRequest(bytes.NewReader(payload)); err != nil {
		return RunResult{Response: requestErrorResponse(request, err)}, err
	}
	hash := sha256.Sum256(payload)
	payloadHash := hex.EncodeToString(hash[:])
	volumeName := broker.VolumeName(request.ProfileID)
	containerName := broker.ContainerName(request.ProfileID, request.RequestID)
	existing, getErr := broker.journal.Get(ctx, request.ProfileID, request.RequestID)
	if getErr == nil {
		if existing.PayloadHash != payloadHash {
			return RunResult{Entry: existing, Response: requestErrorResponse(request, ErrJournalConflict)}, ErrJournalConflict
		}
		key, _ := entryKey(request.ProfileID, request.RequestID)
		if broker.isRecovered(key) {
			if existing, err = broker.reconcileRecovered(ctx, existing); err != nil {
				return RunResult{Entry: existing, Response: requestErrorResponse(request, err)}, err
			}
		}
		return broker.replay(existing)
	}
	if !errors.Is(getErr, ErrJournalNotFound) {
		return RunResult{Response: requestErrorResponse(request, getErr)}, getErr
	}
	// A fresh claim after an operator-reviewed unknown run must carry the
	// exact reviewed request ID. This check happens only after replay lookup so
	// an old request can still be read idempotently without a proof field.
	if err := broker.validateReviewedRequest(ctx, request); err != nil {
		return RunResult{Response: requestErrorResponse(request, err)}, err
	}
	entry := JournalEntry{ProfileID: request.ProfileID, RequestID: request.RequestID, PayloadHash: payloadHash,
		State: RunRunning, ContainerName: containerName, VolumeName: volumeName, UpdatedAt: broker.config.Clock()}
	created, createErr := broker.journal.Create(ctx, entry)
	if createErr != nil {
		return RunResult{Response: requestErrorResponse(request, createErr)}, createErr
	}
	if !created {
		existing, getErr = broker.journal.Get(ctx, request.ProfileID, request.RequestID)
		if getErr != nil {
			return RunResult{Response: requestErrorResponse(request, getErr)}, getErr
		}
		if existing.PayloadHash != payloadHash {
			return RunResult{Entry: existing, Response: requestErrorResponse(request, ErrJournalConflict)}, ErrJournalConflict
		}
		return broker.replay(existing)
	}
	return broker.executeNew(ctx, request, payload, entry)
}

// Lookup reads a previously claimed request without accepting a payload or
// starting Docker. It is intended for reconnect/status paths where the
// caller no longer has the original model key or game token.
func (broker *Broker) Lookup(ctx context.Context, profileID, requestID string) (RunResult, error) {
	if broker == nil || broker.journal == nil {
		return RunResult{}, fmt.Errorf("%w: broker is not configured", ErrInvalidConfig)
	}
	if !profileIDPattern.MatchString(profileID) || !requestIDPattern.MatchString(requestID) {
		return RunResult{}, fmt.Errorf("%w: identifiers are invalid", ErrInvalidRequest)
	}
	entry, err := broker.journal.Get(ctx, profileID, requestID)
	if err != nil {
		return RunResult{}, err
	}
	key, _ := entryKey(profileID, requestID)
	if broker.isRecovered(key) {
		entry, err = broker.reconcileRecovered(ctx, entry)
		if err != nil {
			return RunResult{Entry: entry}, err
		}
	}
	return broker.replay(entry)
}

// UnknownReviewReady is a read-only preflight for the operator UI. It neither
// removes a container nor releases a profile claim. ReviewUnknown must still
// inspect again because readiness is only a snapshot.
func (broker *Broker) UnknownReviewReady(ctx context.Context, profileID, requestID string, expectedUpdatedAt time.Time) (bool, error) {
	if broker == nil || broker.docker == nil || broker.journal == nil {
		return false, ErrInvalidConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !profileIDPattern.MatchString(profileID) || !requestIDPattern.MatchString(requestID) || expectedUpdatedAt.IsZero() {
		return false, ErrInvalidRequest
	}
	entry, err := broker.journal.Get(ctx, profileID, requestID)
	if err != nil {
		return false, err
	}
	if entry.State != RunUnknown || !entry.UpdatedAt.Equal(expectedUpdatedAt) {
		return false, ErrJournalState
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.closed {
		return false, ErrClosed
	}
	if _, active := broker.active[entry.ContainerName]; active {
		return false, nil
	}
	inspector, ok := broker.docker.(DockerInspector)
	if !ok {
		return false, ErrDocker
	}
	bounded, cancel := context.WithTimeout(ctx, broker.config.StopTimeout)
	defer cancel()
	state, err := inspector.Inspect(bounded, entry.ContainerName)
	if errors.Is(err, ErrContainerNotFound) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: inspect unknown container readiness", ErrDocker)
	}
	return terminalContainerState(state), nil
}

// ReviewUnknown records an explicit operator disposition for an unknown run.
// It never changes the request outcome and therefore old Run/Lookup calls
// continue to replay ErrRunUnknown. Before releasing the durable profile
// claim, the broker verifies that no local execution is active and that the
// exact container is absent or terminated.
func (broker *Broker) ReviewUnknown(ctx context.Context, profileID, requestID string, expectedUpdatedAt time.Time, actor, reason string) (JournalEntry, error) {
	if broker == nil || broker.docker == nil || broker.journal == nil {
		return JournalEntry{}, fmt.Errorf("%w: broker is not configured", ErrInvalidConfig)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !profileIDPattern.MatchString(profileID) || !requestIDPattern.MatchString(requestID) {
		return JournalEntry{}, fmt.Errorf("%w: identifiers are invalid", ErrInvalidRequest)
	}
	if err := validateReviewInput(expectedUpdatedAt, actor, reason); err != nil {
		return JournalEntry{}, err
	}
	reviewer, ok := broker.journal.(JournalReviewer)
	if !ok {
		return JournalEntry{}, fmt.Errorf("%w: journal does not support unknown review", ErrInvalidConfig)
	}
	entry, err := broker.journal.Get(ctx, profileID, requestID)
	if err != nil {
		return JournalEntry{}, err
	}
	if entry.Review != nil {
		if entry.Review.Actor == actor && entry.Review.Reason == reason {
			return entry, nil
		}
		return JournalEntry{}, ErrJournalState
	}
	if entry.State != RunUnknown || !entry.UpdatedAt.Equal(expectedUpdatedAt) {
		return JournalEntry{}, ErrJournalState
	}

	// Keep the broker lock through inspection and journal publication so a
	// local run cannot be registered between the active check and the claim
	// release. Cross-process callers are fenced by the journal's CAS/index.
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.closed {
		return JournalEntry{}, ErrClosed
	}
	if _, active := broker.active[entry.ContainerName]; active {
		return JournalEntry{}, ErrProfileBusy
	}
	inspector, ok := broker.docker.(DockerInspector)
	if !ok {
		return JournalEntry{}, fmt.Errorf("%w: container inspector is required for unknown review", ErrDocker)
	}
	inspectCtx, cancel := context.WithTimeout(ctx, broker.config.StopTimeout)
	state, inspectErr := inspector.Inspect(inspectCtx, entry.ContainerName)
	cancel()
	if inspectErr != nil {
		if !errors.Is(inspectErr, ErrContainerNotFound) {
			return JournalEntry{}, fmt.Errorf("%w: inspect unknown container", ErrDocker)
		}
	} else if !terminalContainerState(state) {
		return JournalEntry{}, ErrRunRunning
	} else if remover, removable := broker.docker.(DockerRemover); removable {
		removeCtx, removeCancel := context.WithTimeout(ctx, broker.config.StopTimeout)
		// Removal is best effort after Inspect proved that the container is
		// terminal. A transient cleanup failure cannot make a stopped provider
		// turn live again; the durable review should still release the claim.
		_ = remover.Remove(removeCtx, entry.ContainerName)
		removeCancel()
	}
	return reviewer.ReviewUnknown(ctx, profileID, requestID, expectedUpdatedAt, actor, reason)
}

// Close cancels active runs, asks Docker to stop each deterministic container,
// waits for the run transitions to be journaled, and closes a journal opened
// by this Broker. A caller-supplied Journal remains owned by that caller.
func (broker *Broker) Close() error {
	if broker == nil {
		return nil
	}
	broker.mu.Lock()
	if broker.closed {
		broker.mu.Unlock()
		return nil
	}
	broker.closed = true
	active := make(map[string]context.CancelFunc, len(broker.active))
	for name, cancel := range broker.active {
		active[name] = cancel
	}
	journalLock := broker.journalLock
	broker.journalLock = nil
	broker.mu.Unlock()
	var stopErrs []error
	for name, cancel := range active {
		if cancel != nil {
			cancel()
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), broker.config.StopTimeout)
		if err := broker.docker.Stop(stopCtx, name); err != nil {
			stopErrs = append(stopErrs, err)
		}
		stopCancel()
	}
	broker.wg.Wait()
	if broker.ownJournal && broker.journal != nil {
		if closer, ok := broker.journal.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				stopErrs = append(stopErrs, err)
			}
		}
	}
	if journalLock != nil {
		journalLock()
	}
	return errors.Join(stopErrs...)
}

// Execute is the response-shaped convenience method used by service
// adapters. The richer Run method is available when the durable state is
// needed by a scheduler.
func (broker *Broker) Execute(ctx context.Context, request airunner.ExecuteRequest) (airunner.Response, error) {
	result, err := broker.Run(ctx, request)
	return result.Response, err
}

// Submit is an alias for Execute, useful for callers that name the broker
// operation after its idempotent request submission semantics.
func (broker *Broker) Submit(ctx context.Context, request airunner.ExecuteRequest) (airunner.Response, error) {
	return broker.Execute(ctx, request)
}

// ExecuteJSON decodes one strict airunner request and submits it. The reader
// is consumed once and no request bytes are copied to the journal.
func (broker *Broker) ExecuteJSON(ctx context.Context, reader io.Reader) (airunner.Response, error) {
	request, err := airunner.DecodeRequest(reader)
	if err != nil {
		return airunner.Response{OK: false, Error: airunner.ErrorCode(err)}, err
	}
	return broker.Execute(ctx, request)
}

func (broker *Broker) executeNew(ctx context.Context, request airunner.ExecuteRequest, payload []byte, entry JournalEntry) (RunResult, error) {
	spec := broker.runSpec(request.ProfileID, entry.VolumeName, entry.ContainerName)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	if !broker.registerActive(entry.ContainerName, cancel) {
		cancel()
		return broker.finishUnknown(request, entry, unknownResponse(request), ErrClosed)
	}
	defer broker.unregisterActive(entry.ContainerName)
	call := make(chan dockerCall, 1)
	go func() {
		result, err := broker.docker.Run(runCtx, spec, append([]byte(nil), payload...))
		call <- dockerCall{result: result, err: err}
	}()
	var dockerResult DockerResult
	var runErr error
	containerStopped := false
	select {
	case outcome := <-call:
		dockerResult, runErr = outcome.result, outcome.err
	case <-runCtx.Done():
		// Stop uses an independent bounded context because the caller's
		// context is already canceled. The actual container name is derived
		// from the journal entry, never from request-controlled Docker args.
		stopCtx, cancel := context.WithTimeout(context.Background(), broker.config.StopTimeout)
		_ = broker.docker.Stop(stopCtx, entry.ContainerName)
		cancel()
		containerStopped = true
		select {
		case outcome := <-call:
			dockerResult, runErr = outcome.result, outcome.err
		case <-time.After(broker.config.StopTimeout):
			return broker.finishUnknown(request, entry, airunner.Response{OK: false, ProfileID: request.ProfileID, RequestID: request.RequestID, Error: string(RunUnknown)}, errors.Join(ErrRunUnknown, ctx.Err()))
		}
		if runErr == nil {
			runErr = runCtx.Err()
		}
	}

	response, state, responseErr := broker.decodeDockerResponse(request, dockerResult, runErr)
	if responseErr != nil {
		if !containerStopped {
			broker.stopContainer(entry.ContainerName)
		}
		return broker.finishUnknown(request, entry, response, responseErr)
	}
	entry.State = state
	entry.Response = mustRedactedResponse(response, request)
	entry.ErrorCode = ""
	if state == RunUnknown {
		entry.ErrorCode = string(RunUnknown)
	}
	entry.UpdatedAt = broker.config.Clock()
	if err := broker.persistOutcome(entry); err != nil {
		return RunResult{Entry: entry, Response: response}, errors.Join(ErrRunUnknown, err)
	}
	if state == RunUnknown {
		response.Error = string(RunUnknown)
		return RunResult{State: state, Entry: entry, Response: response}, ErrRunUnknown
	}
	broker.cleanupCompleted(entry)
	response.OK = true
	return RunResult{State: state, Entry: entry, Response: response}, nil
}

func (broker *Broker) registerActive(containerName string, cancel context.CancelFunc) bool {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.closed {
		return false
	}
	if broker.active == nil {
		broker.active = make(map[string]context.CancelFunc)
	}
	broker.active[containerName] = cancel
	broker.wg.Add(1)
	return true
}

func (broker *Broker) unregisterActive(containerName string) {
	broker.mu.Lock()
	delete(broker.active, containerName)
	broker.mu.Unlock()
	broker.wg.Done()
}

func (broker *Broker) stopContainer(containerName string) {
	if broker == nil || broker.docker == nil {
		return
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), broker.config.StopTimeout)
	defer cancel()
	// Stop errors intentionally do not alter the unknown state. A failed
	// stop cannot prove that the container has stopped or that a provider turn
	// was not already accepted.
	_ = broker.docker.Stop(stopCtx, containerName)
}

func (broker *Broker) cleanupCompleted(entry JournalEntry) {
	if broker == nil || broker.docker == nil || entry.ContainerName == "" {
		return
	}
	remover, ok := broker.docker.(DockerRemover)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), broker.config.StopTimeout)
	defer cancel()
	// Cleanup is deliberately best effort. The completed journal row remains
	// authoritative when Docker is unavailable, and replay will retry this
	// exact non-forced removal on the next status/request lookup.
	_ = remover.Remove(ctx, entry.ContainerName)
}

func (broker *Broker) finishUnknown(request airunner.ExecuteRequest, entry JournalEntry, response airunner.Response, reason error) (RunResult, error) {
	response.OK = false
	response.ProfileID = request.ProfileID
	response.RequestID = request.RequestID
	response.Error = string(RunUnknown)
	entry.State = RunUnknown
	entry.ErrorCode = string(RunUnknown)
	entry.Response = mustRedactedResponse(response, request)
	entry.UpdatedAt = broker.config.Clock()
	if err := broker.persistOutcome(entry); err != nil {
		return RunResult{State: RunUnknown, Entry: entry, Response: response}, errors.Join(ErrRunUnknown, reason, err)
	}
	return RunResult{State: RunUnknown, Entry: entry, Response: response}, errors.Join(ErrRunUnknown, reason)
}

func (broker *Broker) persistOutcome(entry JournalEntry) error {
	// Client cancellation must not discard a result already observed from the
	// runtime. Keep the durable write independent, with its own finite timeout.
	ctx, cancel := context.WithTimeout(context.Background(), broker.config.StopTimeout)
	defer cancel()
	if cas, ok := broker.journal.(JournalCAS); ok {
		return cas.UpdateIfState(ctx, RunRunning, entry)
	}
	return broker.journal.Update(ctx, entry)
}

func (broker *Broker) replay(entry JournalEntry) (RunResult, error) {
	response := airunner.Response{OK: false, ProfileID: entry.ProfileID, RequestID: entry.RequestID}
	if len(entry.Response) > 0 {
		decoded, err := decodeResponse(entry.Response)
		if err != nil {
			return RunResult{State: entry.State, Entry: entry, Response: response}, fmt.Errorf("%w: journal response", ErrResponse)
		}
		response = decoded
	}
	response.ProfileID = entry.ProfileID
	response.RequestID = entry.RequestID
	switch entry.State {
	case RunCompleted:
		broker.cleanupCompleted(entry)
		response.OK = true
		return RunResult{State: entry.State, Entry: entry, Response: response}, nil
	case RunRunning:
		response.OK = false
		response.Error = string(RunRunning)
		return RunResult{State: entry.State, Entry: entry, Response: response}, ErrRunRunning
	case RunUnknown:
		response.OK = false
		response.Error = string(RunUnknown)
		return RunResult{State: entry.State, Entry: entry, Response: response}, ErrRunUnknown
	default:
		return RunResult{State: entry.State, Entry: entry, Response: response}, ErrResponse
	}
}

func (broker *Broker) runSpec(profileID, volumeName, containerName string) RunSpec {
	environment := cloneEnvironment(broker.config.Environment)
	if environment == nil {
		environment = make(map[string]string, 6)
	}
	for key, value := range map[string]string{
		"HOME":                     runtimeHome,
		"STONEAGE_AI_PROFILE_ID":   profileID,
		"STONEAGE_AI_STATE_ROOT":   runtimeStateRoot,
		"STONEAGE_AI_CODEX_BINARY": runtimeCodex,
		"STONEAGE_AI_MCP_BINARY":   runtimeMCP,
		"STONEAGE_AI_SKILL_ROOT":   runtimeSkillRoot,
	} {
		environment[key] = value
	}
	command := append([]string(nil), broker.config.RunnerCommand...)
	command = append(command, profileArg, profileID)
	mount := VolumeMount{Name: volumeName, Target: runtimeStateRoot, ReadOnly: false}
	return RunSpec{Image: broker.config.Image, Network: broker.config.Network, CPUs: broker.config.CPUs,
		Memory: broker.config.Memory, MemorySwap: broker.config.MemorySwap, PidsLimit: broker.config.PidsLimit, ContainerName: containerName,
		VolumeName: volumeName, Mounts: []VolumeMount{mount}, Volumes: []VolumeMount{mount}, User: runtimeUser,
		ReadOnlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"},
		NoNewPrivileges: true, Tmpfs: map[string]string{"/run": "rw,noexec,nosuid,nodev,size=" + DefaultRunTmpfsSize,
			"/tmp": "rw,noexec,nosuid,nodev,size=" + DefaultTmpfsSize}, Environment: environment, Command: command}
}

func (broker *Broker) decodeDockerResponse(request airunner.ExecuteRequest, result DockerResult, runErr error) (airunner.Response, RunState, error) {
	if len(result.Stdout) > broker.config.MaxStdoutBytes {
		return unknownResponse(request), RunUnknown, ErrOutputLimit
	}
	if runErr != nil {
		response, decodeErr := broker.safeDecodeResponse(request, result.Stdout)
		if decodeErr != nil {
			return unknownResponse(request), RunUnknown, errors.Join(ErrRunUnknown, runErr)
		}
		return response, RunUnknown, errors.Join(ErrRunUnknown, runErr)
	}
	response, err := broker.safeDecodeResponse(request, result.Stdout)
	if err != nil {
		return unknownResponse(request), RunUnknown, errors.Join(ErrResponse, err)
	}
	if result.ExitCode != 0 {
		return response, RunUnknown, ErrRunUnknown
	}
	if !response.OK || !runtimeTurnComplete(response) {
		return response, RunUnknown, ErrRunUnknown
	}
	return response, RunCompleted, nil
}

func runtimeTurnComplete(response airunner.Response) bool {
	if response.Result == nil || response.Result.ThreadID == "" {
		return false
	}
	if response.Result.Turn.Status != "completed" {
		return false
	}
	return response.Result.Process.Status == "exited" && response.Result.Process.ExitCode == 0
}

func (broker *Broker) safeDecodeResponse(request airunner.ExecuteRequest, raw []byte) (airunner.Response, error) {
	if len(raw) == 0 {
		return airunner.Response{}, ErrResponse
	}
	safe, err := redactJSON(raw, request.Model.APIKey, request.MCP.Token)
	if err != nil {
		return airunner.Response{}, err
	}
	response, err := decodeResponse(safe)
	if err != nil {
		return airunner.Response{}, err
	}
	if response.ProfileID != "" && response.ProfileID != request.ProfileID {
		return airunner.Response{}, ErrResponse
	}
	if response.RequestID != "" && response.RequestID != request.RequestID {
		return airunner.Response{}, ErrResponse
	}
	if response.Result != nil {
		if response.Result.ProfileID != "" && response.Result.ProfileID != request.ProfileID {
			return airunner.Response{}, ErrResponse
		}
		if request.Run.Resume && response.Result.ThreadID != "" && response.Result.ThreadID != request.Run.ThreadID {
			return airunner.Response{}, ErrResponse
		}
	}
	response.ProfileID = request.ProfileID
	response.RequestID = request.RequestID
	return response, nil
}

func decodeResponse(raw []byte) (airunner.Response, error) {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	var response airunner.Response
	if err := decoder.Decode(&response); err != nil {
		return airunner.Response{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return airunner.Response{}, errors.New("trailing JSON")
		}
		return airunner.Response{}, err
	}
	return response, nil
}

func mustRedactedResponse(response airunner.Response, request airunner.ExecuteRequest) []byte {
	raw, err := json.Marshal(response)
	if err != nil {
		return nil
	}
	safe, err := redactJSON(raw, request.Model.APIKey, request.MCP.Token)
	if err != nil {
		return nil
	}
	return safe
}

func redactJSON(raw []byte, secrets ...string) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("trailing JSON")
		}
		return nil, err
	}
	cleanSecrets := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			cleanSecrets = append(cleanSecrets, secret)
		}
	}
	redactValue(value, cleanSecrets)
	return json.Marshal(value)
}

func redactValue(value any, secrets []string) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if strings.EqualFold(key, "api_key") || strings.EqualFold(key, "token") || strings.Contains(strings.ToLower(key), "secret") {
				if _, ok := child.(string); ok {
					current[key] = "[REDACTED]"
					continue
				}
			}
			redactValue(child, secrets)
		}
	case []any:
		for _, child := range current {
			redactValue(child, secrets)
		}
	case string:
		// Strings are immutable in an interface; callers handle replacement
		// while traversing map/slice values through redactString below.
		_ = redactString(current, secrets)
	}
	redactContainers(value, secrets)
}

func redactContainers(value any, secrets []string) any {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			if text, ok := child.(string); ok {
				current[key] = redactString(text, secrets)
			} else {
				current[key] = redactContainers(child, secrets)
			}
		}
	case []any:
		for index, child := range current {
			if text, ok := child.(string); ok {
				current[index] = redactString(text, secrets)
			} else {
				current[index] = redactContainers(child, secrets)
			}
		}
	}
	return value
}

func redactString(value string, secrets []string) string {
	for _, secret := range secrets {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

func validateRequest(request airunner.ExecuteRequest) error {
	if !profileIDPattern.MatchString(request.ProfileID) {
		return fmt.Errorf("%w: profile ID is invalid", ErrInvalidRequest)
	}
	if !requestIDPattern.MatchString(request.RequestID) {
		return fmt.Errorf("%w: request ID is invalid", ErrInvalidRequest)
	}
	if request.ReviewedRequestID != "" {
		if !requestIDPattern.MatchString(request.ReviewedRequestID) {
			return fmt.Errorf("%w: reviewed request ID is invalid", ErrInvalidRequest)
		}
		if request.ReviewedRequestID == request.RequestID {
			return fmt.Errorf("%w: reviewed request ID must identify an earlier request", ErrInvalidRequest)
		}
	}
	if request.Run.Resume != (request.Run.ThreadID != "") {
		return fmt.Errorf("%w: resume and exact thread ID must be supplied together", ErrInvalidRequest)
	}
	if strings.ContainsAny(request.Run.Prompt, "\x00") || len([]byte(request.Run.Prompt)) > 512<<10 {
		return fmt.Errorf("%w: prompt is invalid", ErrInvalidRequest)
	}
	if request.Model.Provider == "" || !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`).MatchString(request.Model.Provider) || request.Model.Model == "" || len([]byte(request.Model.Model)) > 256 || strings.ContainsAny(request.Model.Model, "\x00\r\n") {
		return fmt.Errorf("%w: model is invalid", ErrInvalidRequest)
	}
	if request.Model.APIKey == "" || len([]byte(request.Model.APIKey)) > 4096 || strings.ContainsAny(request.Model.APIKey, "\x00\r\n") {
		return fmt.Errorf("%w: model credential is invalid", ErrInvalidRequest)
	}
	if request.Model.BaseURL == "" {
		return fmt.Errorf("%w: model base URL is required", ErrInvalidRequest)
	}
	modelURL, err := url.Parse(request.Model.BaseURL)
	if err != nil || modelURL.Host == "" || (modelURL.Scheme != "http" && modelURL.Scheme != "https") || modelURL.User != nil || modelURL.RawQuery != "" || modelURL.Fragment != "" || strings.ContainsAny(request.Model.BaseURL, "\x00\r\n") {
		return fmt.Errorf("%w: model base URL is invalid", ErrInvalidRequest)
	}
	if request.Model.ContextWindow < 0 || request.Model.ContextWindow > 16*1024*1024 || len([]byte(request.Model.ReasoningEffort)) > 64 || strings.ContainsAny(request.Model.ReasoningEffort, "\x00\r\n") {
		return fmt.Errorf("%w: model limits are invalid", ErrInvalidRequest)
	}
	if request.Probe {
		// Probe requests are model-only. The runtime performs the same strict
		// check, but keep it at the broker boundary so a malformed request is
		// rejected before it can claim a container/profile volume.
		if len(request.Skills) != 0 || request.MCP != (airunner.MCP{}) || request.Run.Resume || request.Run.ThreadID != "" || request.ReviewedRequestID != "" {
			return fmt.Errorf("%w: model probe cannot carry game capability", ErrInvalidRequest)
		}
	} else if request.MCP.Endpoint == "" || !validHTTPURL(request.MCP.Endpoint) || !validGameToken(request.MCP.Token) || request.MCP.CharacterID == "" || len([]byte(request.MCP.CharacterID)) > 128 || strings.ContainsAny(request.MCP.CharacterID, "\x00\r\n") || len([]byte(request.MCP.CharacterName)) > 4096 || strings.ContainsAny(request.MCP.CharacterName, "\x00\r\n") {
		return fmt.Errorf("%w: game capability is incomplete", ErrInvalidRequest)
	}
	seenSkills := make(map[string]struct{}, len(request.Skills))
	if len(request.Skills) > 64 {
		return fmt.Errorf("%w: too many skills", ErrInvalidRequest)
	}
	for _, skill := range request.Skills {
		if skill.Name == "" || len([]byte(skill.Name)) > 128 || strings.ContainsAny(skill.Name, "\x00\r\n") || len([]byte(skill.Version)) > 128 || strings.ContainsAny(skill.Version, "\x00\r\n") || len([]byte(skill.Digest)) > 128 || strings.ContainsAny(skill.Digest, "\x00\r\n") {
			return fmt.Errorf("%w: skill is invalid", ErrInvalidRequest)
		}
		if _, exists := seenSkills[skill.Name]; exists {
			return fmt.Errorf("%w: duplicate skill", ErrInvalidRequest)
		}
		seenSkills[skill.Name] = struct{}{}
	}
	return nil
}

// validateReviewedRequest proves that a request selecting the fresh
// checkpoint namespace is backed by the same profile's explicitly reviewed
// unknown journal row. The proof is intentionally checked against the
// durable row instead of trusting the request payload alone.
func (broker *Broker) validateReviewedRequest(ctx context.Context, request airunner.ExecuteRequest) error {
	if request.ReviewedRequestID != "" {
		entry, err := broker.journal.Get(ctx, request.ProfileID, request.ReviewedRequestID)
		if err != nil {
			if errors.Is(err, ErrJournalNotFound) {
				return fmt.Errorf("%w: reviewed request was not found", ErrReviewedRequest)
			}
			return err
		}
		if entry.ProfileID != request.ProfileID || entry.RequestID == request.RequestID || entry.State != RunUnknown || entry.Review == nil {
			return fmt.Errorf("%w: reviewed request is not an acknowledged unknown run", ErrReviewedRequest)
		}
		if entry.VolumeName != broker.VolumeName(request.ProfileID) || entry.ContainerName != broker.ContainerName(request.ProfileID, entry.RequestID) {
			return fmt.Errorf("%w: reviewed request belongs to another profile volume", ErrReviewedRequest)
		}
		return nil
	}
	if lookup, ok := broker.journal.(JournalReviewedLookup); ok {
		hasReviewed, err := lookup.HasReviewedUnknown(ctx, request.ProfileID)
		if err != nil {
			return err
		}
		if hasReviewed {
			return fmt.Errorf("%w: reviewed request ID is required", ErrReviewedRequest)
		}
	}
	return nil
}

func validHTTPURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && !strings.ContainsAny(value, "\x00\r\n")
}

func validGameToken(value string) bool {
	if len(value) != 43 {
		return false
	}
	for _, character := range value {
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func requestErrorResponse(request airunner.ExecuteRequest, err error) airunner.Response {
	return airunner.Response{OK: false, ProfileID: safeIdentifier(request.ProfileID), RequestID: safeIdentifier(request.RequestID), Error: errorCode(err)}
}

func unknownResponse(request airunner.ExecuteRequest) airunner.Response {
	return airunner.Response{OK: false, ProfileID: request.ProfileID, RequestID: request.RequestID, Error: string(RunUnknown)}
}

func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrRequestTooLarge):
		return "request_too_large"
	case errors.Is(err, ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, ErrClosed):
		return "closed"
	case errors.Is(err, ErrJournalConflict):
		return "request_conflict"
	case errors.Is(err, ErrReviewedRequest):
		return "reviewed_request_required"
	case errors.Is(err, ErrProfileBusy):
		return "profile_busy"
	case errors.Is(err, ErrJournalBusy):
		return "journal_busy"
	case errors.Is(err, ErrRunRunning):
		return string(RunRunning)
	case errors.Is(err, ErrRunUnknown):
		return string(RunUnknown)
	case errors.Is(err, ErrResponse):
		return "invalid_response"
	case errors.Is(err, ErrOutputLimit):
		return "output_limit"
	default:
		return "broker_error"
	}
}

func safeIdentifier(value string) string {
	if profileIDPattern.MatchString(value) || requestIDPattern.MatchString(value) {
		return value
	}
	return ""
}

func deterministicName(prefix, kind string, values ...string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(kind))
	for _, value := range values {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(value))
	}
	digest := hex.EncodeToString(hash.Sum(nil))[:32]
	return prefix + "-" + kind + "-" + digest
}

func validateEnvironment(environment map[string]string) error {
	for key, value := range environment {
		if !validEnvKey(key) || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: environment contains invalid %q", ErrInvalidConfig, key)
		}
		switch key {
		case "HOME", "CODEX_HOME", "STONEAGE_AI_PROFILE_ID", "STONEAGE_AI_STATE_ROOT", "STONEAGE_AI_CODEX_BINARY", "STONEAGE_AI_MCP_BINARY", "STONEAGE_AI_SKILL_ROOT":
			return fmt.Errorf("%w: protected runtime environment %q cannot be overridden", ErrInvalidConfig, key)
		}
		if strings.Contains(strings.ToLower(key), "token") || strings.Contains(strings.ToLower(key), "secret") || strings.Contains(strings.ToLower(key), "api_key") {
			return fmt.Errorf("%w: credential environment %q is not allowed", ErrInvalidConfig, key)
		}
	}
	return nil
}

const maxRuntimePids = 1 << 20

func validateResourceLimits(cpus float64, memory, memorySwap string, pidsLimit int) error {
	if math.IsNaN(cpus) || math.IsInf(cpus, 0) || cpus <= 0 || cpus > 64 {
		return fmt.Errorf("CPU limit is invalid")
	}
	memoryBytes, ok := parseMemoryLimit(memory)
	if !ok {
		return fmt.Errorf("memory limit is invalid")
	}
	swapBytes, ok := parseMemoryLimit(memorySwap)
	if !ok || swapBytes < memoryBytes {
		return fmt.Errorf("memory-swap limit is invalid")
	}
	if pidsLimit <= 0 || pidsLimit > maxRuntimePids {
		return fmt.Errorf("pids limit is invalid")
	}
	return nil
}

func parseMemoryLimit(value string) (int64, bool) {
	matches := memoryLimitPattern.FindStringSubmatch(value)
	if len(matches) != 3 {
		return 0, false
	}
	amount, err := strconv.ParseFloat(matches[1], 64)
	if err != nil || math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return 0, false
	}
	multiplier := float64(1)
	switch strings.ToLower(matches[2]) {
	case "k", "kb":
		multiplier = 1 << 10
	case "m", "mb":
		multiplier = 1 << 20
	case "g", "gb":
		multiplier = 1 << 30
	case "t", "tb":
		multiplier = 1 << 40
	case "p", "pb":
		multiplier = 1 << 50
	}
	bytes := amount * multiplier
	maxInt64 := float64(int64(^uint64(0) >> 1))
	if bytes < 1 || bytes > maxInt64 {
		return 0, false
	}
	return int64(bytes), true
}

func cloneEnvironment(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func hasControlOrSpace(value string) bool {
	for _, r := range value {
		if r == 0 || r == '\r' || r == '\n' || r == '\t' || r == ' ' {
			return true
		}
	}
	return false
}

func isAbsolutePath(value string) bool {
	return strings.HasPrefix(value, "/")
}

func mustNamePattern(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}

type dockerCall struct {
	result DockerResult
	err    error
}
