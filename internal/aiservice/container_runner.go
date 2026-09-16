package aiservice

// This file adapts the host-side container broker to aisupervisor.Runner.
// The supervisor deals in aicodex results, while the broker deals in an
// idempotent airunner request. A small profile-local journal bridges those
// shapes and, more importantly, records the request ID before any provider
// work can start.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
)

var containerRunnerProfileIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var containerRunnerRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

const (
	containerRunnerCheckpointName = "container-runner.json"
	containerRunnerLockName       = ".container-runner.lock"
	containerRunnerVersion        = 1
	containerRunnerMaxFileBytes   = 64 << 20
	containerRunnerMaxPrompt      = 1 << 20
)

var (
	// ErrContainerRunnerConfig means the server-owned runner configuration is
	// unusable. It never includes a path or credential value.
	ErrContainerRunnerConfig = errors.New("aiservice: invalid container runner configuration")
	// ErrContainerRunnerRecovery means an earlier request is still pending or
	// has an uncertain outcome. Callers must reconcile that request by its
	// persisted ID before submitting another intent.
	ErrContainerRunnerRecovery = errors.New("aiservice: container run requires recovery")
	// ErrContainerRunnerUnknown is returned when the broker cannot prove the
	// provider turn's outcome. It is intentionally separate from recovery so a
	// supervisor can retain the partial result and checkpoint.
	ErrContainerRunnerUnknown    = errors.New("aiservice: container run outcome is unknown")
	ErrContainerRunnerIncomplete = errors.New("aiservice: container runtime result is incomplete")
	ErrContainerRunnerCorrupt    = errors.New("aiservice: container runner checkpoint is corrupt")
	ErrContainerRunnerCredential = errors.New("aiservice: container runner credential is unavailable")
)

// Aliases keep the recovery/error names readable to callers that do not need
// to distinguish the adapter's implementation prefix.
var (
	ErrContainerRecovery = ErrContainerRunnerRecovery
	ErrContainerUnknown  = ErrContainerRunnerUnknown
)

// ContainerBroker is the host-side idempotent container seam. Lookup must
// never start a container; it only returns the durable result for a claimed
// profile/request pair.
type ContainerBroker interface {
	Run(context.Context, airunner.ExecuteRequest) (aibroker.RunResult, error)
	Lookup(context.Context, string, string) (aibroker.RunResult, error)
}

// ContainerRunnerConfig contains the fixed identity, private profile state
// directory, and request template selected by the server. RequestTemplate's
// Run and RequestID fields are deliberately empty: each model turn supplies
// only a prompt and an exact optional resume thread to Run.
type ContainerRunnerConfig struct {
	ProfileID       string
	StateRoot       string
	RequestTemplate airunner.ExecuteRequest
	Broker          ContainerBroker
	TurnTimeout     time.Duration
}

// ContainerRunner implements aisupervisor.Runner over one private container
// profile. It does not materialize Codex files on the host and does not copy
// the request template outside memory except for the credential-free local
// checkpoint.
type ContainerRunner struct {
	profileID   string
	stateRoot   string
	checkpoint  string
	lockPath    string
	template    airunner.ExecuteRequest
	broker      ContainerBroker
	guard       runtimepath.Guard
	turnTimeout time.Duration
}

type containerRunState string

const (
	containerRunPending   containerRunState = "pending"
	containerRunCompleted containerRunState = "completed"
	containerRunUnknown   containerRunState = "unknown"
)

type containerRunIntent struct {
	Prompt   string `json:"prompt"`
	Resume   bool   `json:"resume"`
	ThreadID string `json:"thread_id,omitempty"`
}

type containerRunCheckpoint struct {
	Reviewed  bool   `json:"reviewed,omitempty"`
	Version   int    `json:"version"`
	ProfileID string `json:"profile_id"`
	RequestID string `json:"request_id"`
	// CallerRequestID is the durable turn identity supplied by the caller.
	// RequestID remains the broker identity; they differ for requests created
	// by older callers that did not supply a turn ID.
	CallerRequestID string             `json:"caller_request_id,omitempty"`
	Intent          string             `json:"intent_sha256"`
	State           containerRunState  `json:"state"`
	Response        *airunner.Response `json:"response,omitempty"`
	UpdatedAt       time.Time          `json:"updated_at"`
}

// NewContainerRunner validates the fixed profile boundary and creates its
// private state directory. It never contacts the broker and never starts a
// model turn.
func NewContainerRunner(config ContainerRunnerConfig) (*ContainerRunner, error) {
	if config.TurnTimeout < 0 || config.TurnTimeout > 24*time.Hour {
		return nil, ErrContainerRunnerConfig
	}
	profileID := strings.TrimSpace(config.ProfileID)
	if !containerRunnerProfileIDPattern.MatchString(profileID) {
		return nil, ErrContainerRunnerConfig
	}
	if config.Broker == nil {
		return nil, ErrContainerRunnerConfig
	}
	stateRoot := strings.TrimSpace(config.StateRoot)
	if stateRoot == "" {
		return nil, ErrContainerRunnerConfig
	}
	abs, err := filepath.Abs(filepath.Clean(stateRoot))
	if err != nil || abs == "" || abs == string(filepath.Separator) {
		return nil, ErrContainerRunnerConfig
	}
	guard, err := runtimepath.NewGuard()
	if err != nil || guard.Check(abs) != nil {
		return nil, ErrContainerRunnerConfig
	}
	if err := ensureContainerRunnerPrivateDir(abs); err != nil {
		return nil, ErrContainerRunnerConfig
	}
	checkpoint := filepath.Join(abs, containerRunnerCheckpointName)
	if guard.Check(checkpoint) != nil {
		return nil, ErrContainerRunnerConfig
	}
	lockPath := filepath.Join(abs, containerRunnerLockName)
	if guard.Check(lockPath) != nil {
		return nil, ErrContainerRunnerConfig
	}
	if err := ensureContainerRunnerLock(lockPath); err != nil {
		return nil, ErrContainerRunnerConfig
	}
	if info, statErr := os.Lstat(checkpoint); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > containerRunnerMaxFileBytes {
			return nil, ErrContainerRunnerConfig
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, ErrContainerRunnerConfig
	}

	template := cloneContainerTemplate(config.RequestTemplate)
	if template.ProfileID != "" && template.ProfileID != profileID {
		return nil, ErrContainerRunnerConfig
	}
	if template.RequestID != "" || template.Run.Prompt != "" || template.Run.Resume || template.Run.ThreadID != "" {
		return nil, ErrContainerRunnerConfig
	}
	if strings.TrimSpace(template.Model.APIKey) == "" {
		return nil, ErrContainerRunnerCredential
	}
	if strings.TrimSpace(template.MCP.Token) == "" {
		return nil, ErrContainerRunnerCredential
	}
	template.ProfileID = profileID
	template.RequestID = ""
	template.Run = airunner.RunRequest{}
	return &ContainerRunner{profileID: profileID, stateRoot: abs, checkpoint: checkpoint, lockPath: lockPath, template: template, broker: config.Broker, guard: guard, turnTimeout: config.TurnTimeout}, nil
}

// CheckpointPath exposes the credential-free local journal location for
// status/recovery adapters and tests.
func (runner *ContainerRunner) CheckpointPath() string {
	if runner == nil {
		return ""
	}
	return runner.checkpoint
}

// Close exists so a factory can treat process and container runners uniformly.
// The runner owns no goroutine; an in-flight Broker.Run is canceled by the
// context supplied to Run and stopped by the broker itself.
func (runner *ContainerRunner) Close() error { return nil }

// Recover exposes the broker's durable request identity to the supervisor.
// Unlike direct Run callers, recovery must supply the original reservation
// ID. Run reconciles existing checkpoints through Lookup and submits only
// that same ID when the broker proves that no claim exists.
func (runner *ContainerRunner) Recover(ctx context.Context, request aicodex.RunRequest) (aicodex.Result, error) {
	if strings.TrimSpace(request.RequestID) == "" {
		return aicodex.Result{}, ErrContainerRunnerRecovery
	}
	return runner.Run(ctx, request)
}

// Run adapts one exact supervisor turn to an idempotent broker request.
// Every new request ID is persisted before Broker.Run. A pending/unknown
// checkpoint can only be reconciled through Broker.Lookup for that same ID.
func (runner *ContainerRunner) Run(ctx context.Context, request aicodex.RunRequest) (aicodex.Result, error) {
	if runner == nil || runner.broker == nil || runner.lockPath == "" {
		return aicodex.Result{}, ErrContainerRunnerConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if runner.turnTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, runner.turnTimeout)
		defer cancel()
	}
	if err := validateContainerIntent(runner.profileID, request); err != nil {
		return aicodex.Result{ProfileID: runner.profileID}, err
	}
	if err := ctx.Err(); err != nil {
		return aicodex.Result{ProfileID: runner.profileID}, err
	}
	intentHash := hashContainerIntent(request)
	if runner.guard.Check(runner.lockPath) != nil {
		return aicodex.Result{ProfileID: runner.profileID}, ErrContainerRunnerConfig
	}
	release, err := acquireContainerRunnerLock(ctx, runner.lockPath)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return aicodex.Result{ProfileID: runner.profileID}, err
		}
		return aicodex.Result{ProfileID: runner.profileID}, ErrContainerRunnerConfig
	}
	defer release()

	checkpoint, exists, err := runner.loadCheckpoint()
	if err != nil {
		return aicodex.Result{ProfileID: runner.profileID}, err
	}
	if exists {
		switch checkpoint.State {
		case containerRunCompleted:
			if checkpoint.Intent == intentHash && checkpoint.CallerRequestID == request.RequestID {
				return runner.restoreCheckpoint(checkpoint)
			}
			// A caller ID identifies one durable model turn. Reusing it for a
			// different intent is a conflict, even when the old thread could
			// technically be resumed. Callers without an ID retain the older
			// direct-run behavior: a new intent must explicitly resume the exact
			// completed thread.
			if checkpoint.CallerRequestID != "" || request.RequestID != "" {
				if checkpoint.CallerRequestID == request.RequestID {
					return aicodex.Result{ProfileID: runner.profileID}, ErrContainerRunnerRecovery
				}
			}
			oldThreadID := completedContainerThreadID(checkpoint)
			if !request.Resume || request.ThreadID == "" || oldThreadID == "" || request.ThreadID != oldThreadID {
				return aicodex.Result{ProfileID: runner.profileID}, ErrContainerRunnerRecovery
			}
			// A completed turn is a safe boundary only when the caller has
			// explicitly selected its exact thread for the next turn.
		case containerRunPending, containerRunUnknown:
			if checkpoint.Reviewed {
				// An acknowledged unknown turn can only be followed by a
				// fresh caller and fresh conversation; its old ID is never replayed.
				if request.RequestID == "" || request.RequestID == checkpoint.CallerRequestID || request.RequestID == checkpoint.RequestID || request.Resume || request.ThreadID != "" {
					return aicodex.Result{ProfileID: runner.profileID}, ErrContainerRunnerRecovery
				}
				break
			}
			if checkpoint.Intent != intentHash {
				return aicodex.Result{ProfileID: runner.profileID}, ErrContainerRunnerRecovery
			}
			return runner.reconcile(ctx, checkpoint, request)
		default:
			return aicodex.Result{ProfileID: runner.profileID}, ErrContainerRunnerCorrupt
		}
	}

	if err := ctx.Err(); err != nil {
		return aicodex.Result{ProfileID: runner.profileID}, err
	}
	requestID := request.RequestID
	if requestID == "" {
		requestID, err = newContainerRequestID()
		if err != nil {
			return aicodex.Result{ProfileID: runner.profileID}, ErrContainerRunnerConfig
		}
	}
	checkpoint = containerRunCheckpoint{Version: containerRunnerVersion, ProfileID: runner.profileID, RequestID: requestID, CallerRequestID: request.RequestID, Intent: intentHash, State: containerRunPending, UpdatedAt: time.Now().UTC()}
	if err := runner.saveCheckpoint(checkpoint); err != nil {
		return aicodex.Result{ProfileID: runner.profileID}, err
	}
	return runner.callBroker(ctx, checkpoint, request)
}

func (runner *ContainerRunner) reconcile(ctx context.Context, checkpoint containerRunCheckpoint, request aicodex.RunRequest) (aicodex.Result, error) {
	if err := ctx.Err(); err != nil {
		return partialResultFromResponse(checkpoint.Response, runner.profileID), err
	}
	differentCaller := checkpoint.CallerRequestID != request.RequestID
	outcome, lookupErr := runner.broker.Lookup(ctx, runner.profileID, checkpoint.RequestID)
	if errors.Is(lookupErr, context.Canceled) || errors.Is(lookupErr, context.DeadlineExceeded) {
		// Lookup did not prove an outcome. Keep the pending/unknown journal
		// untouched and let the caller retain both the recovery category and
		// the cancellation cause.
		return partialResultFromResponse(checkpoint.Response, runner.profileID), errors.Join(ErrContainerRunnerRecovery, lookupErr)
	}
	if lookupErr == nil || outcome.State == aibroker.RunCompleted {
		if outcome.State == aibroker.RunCompleted && isCompleteContainerResponse(outcome.Response) {
			result, finishErr := runner.finishCompleted(checkpoint, outcome.Response)
			if differentCaller && finishErr == nil {
				// The lookup may have proved that the old caller's turn
				// completed, but that result cannot be reported as a successful
				// turn for a different caller reservation. Keep the old outcome
				// durable and make the new caller reconcile it explicitly.
				return result, ErrContainerRunnerRecovery
			}
			return result, finishErr
		}
		result, finishErr := runner.finishUnknown(checkpoint, outcome.Response, ErrContainerRunnerIncomplete)
		if differentCaller && finishErr != nil {
			return result, errors.Join(ErrContainerRunnerRecovery, finishErr)
		}
		return result, finishErr
	}
	if errors.Is(lookupErr, aibroker.ErrJournalNotFound) {
		if checkpoint.State == containerRunPending {
			// The local record proves this ID was reserved. Reusing that same
			// ID is safe only after Lookup proved no broker claim exists.
			result, runErr := runner.callBroker(ctx, checkpoint, request)
			if differentCaller && runErr == nil {
				return result, ErrContainerRunnerRecovery
			}
			if differentCaller && runErr != nil {
				return result, errors.Join(ErrContainerRunnerRecovery, runErr)
			}
			return result, runErr
		}
		return partialResultFromResponse(checkpoint.Response, runner.profileID), ErrContainerRunnerRecovery
	}
	if errors.Is(lookupErr, aibroker.ErrRunUnknown) || outcome.State == aibroker.RunUnknown {
		result, finishErr := runner.finishUnknown(checkpoint, outcome.Response, ErrContainerRunnerUnknown)
		if differentCaller && finishErr != nil {
			return result, errors.Join(ErrContainerRunnerRecovery, finishErr)
		}
		return result, finishErr
	}
	// Running, backend, and cancellation errors are all unresolved. Do not
	// issue Broker.Run or manufacture a new request ID.
	return partialResultFromResponse(checkpoint.Response, runner.profileID), ErrContainerRunnerRecovery
}

func (runner *ContainerRunner) callBroker(ctx context.Context, checkpoint containerRunCheckpoint, request aicodex.RunRequest) (aicodex.Result, error) {
	payload := cloneContainerTemplate(runner.template)
	payload.ProfileID = runner.profileID
	payload.RequestID = checkpoint.RequestID
	payload.Run = airunner.RunRequest{Prompt: request.Prompt, Resume: request.Resume, ThreadID: request.ThreadID}
	outcome, brokerErr := runner.broker.Run(ctx, payload)
	response := sanitizeContainerResponse(outcome.Response, runner.template.Model.APIKey, runner.template.MCP.Token)
	if brokerErr == nil && outcome.State == aibroker.RunCompleted && isCompleteContainerResponse(response) {
		return runner.finishCompleted(checkpoint, response)
	}
	if brokerErr == nil && outcome.State == aibroker.RunCompleted {
		return runner.finishUnknown(checkpoint, response, ErrContainerRunnerIncomplete)
	}
	if errors.Is(brokerErr, context.Canceled) || errors.Is(brokerErr, context.DeadlineExceeded) {
		result := partialResultFromResponse(&response, runner.profileID)
		return runner.finishUnknownWithResult(checkpoint, response, result, errors.Join(ErrContainerRunnerUnknown, brokerErr))
	}
	if outcome.State == aibroker.RunRunning || errors.Is(brokerErr, aibroker.ErrRunRunning) {
		return runner.finishUnknown(checkpoint, response, ErrContainerRunnerRecovery)
	}
	if outcome.State == aibroker.RunUnknown || errors.Is(brokerErr, aibroker.ErrRunUnknown) || brokerErr != nil {
		return runner.finishUnknown(checkpoint, response, ErrContainerRunnerUnknown)
	}
	return runner.finishUnknown(checkpoint, response, ErrContainerRunnerUnknown)
}

func (runner *ContainerRunner) finishCompleted(checkpoint containerRunCheckpoint, response airunner.Response) (aicodex.Result, error) {
	response = sanitizeContainerResponse(response, runner.template.Model.APIKey, runner.template.MCP.Token)
	result, err := responseToCodexResult(response, runner.profileID)
	if err != nil {
		return runner.finishUnknownWithResult(checkpoint, response, result, err)
	}
	checkpoint.State = containerRunCompleted
	checkpoint.Response = &response
	checkpoint.UpdatedAt = time.Now().UTC()
	if err := runner.saveCheckpoint(checkpoint); err != nil {
		return result, errors.Join(ErrContainerRunnerUnknown, err)
	}
	return result, nil
}

func (runner *ContainerRunner) finishUnknown(checkpoint containerRunCheckpoint, response airunner.Response, reason error) (aicodex.Result, error) {
	return runner.finishUnknownWithResult(checkpoint, response, partialResultFromResponse(&response, runner.profileID), reason)
}

func (runner *ContainerRunner) finishUnknownWithResult(checkpoint containerRunCheckpoint, response airunner.Response, result aicodex.Result, reason error) (aicodex.Result, error) {
	response = sanitizeContainerResponse(response, runner.template.Model.APIKey, runner.template.MCP.Token)
	checkpoint.State = containerRunUnknown
	if response.Result != nil {
		checkpoint.Response = &response
	}
	checkpoint.UpdatedAt = time.Now().UTC()
	if err := runner.saveCheckpoint(checkpoint); err != nil {
		return result, errors.Join(ErrContainerRunnerUnknown, err)
	}
	return result, reason
}

func (runner *ContainerRunner) restoreCheckpoint(checkpoint containerRunCheckpoint) (aicodex.Result, error) {
	if checkpoint.Response == nil || !isCompleteContainerResponse(*checkpoint.Response) {
		return aicodex.Result{ProfileID: runner.profileID}, ErrContainerRunnerCorrupt
	}
	return responseToCodexResult(*checkpoint.Response, runner.profileID)
}

func (runner *ContainerRunner) loadCheckpoint() (containerRunCheckpoint, bool, error) {
	if runner.guard.Check(runner.checkpoint) != nil {
		return containerRunCheckpoint{}, false, ErrContainerRunnerConfig
	}
	info, err := os.Lstat(runner.checkpoint)
	if errors.Is(err, os.ErrNotExist) {
		return containerRunCheckpoint{}, false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > containerRunnerMaxFileBytes {
		return containerRunCheckpoint{}, false, ErrContainerRunnerCorrupt
	}
	data, err := os.ReadFile(runner.checkpoint)
	if err != nil {
		return containerRunCheckpoint{}, false, ErrContainerRunnerCorrupt
	}
	var checkpoint containerRunCheckpoint
	if err := decodeContainerJSON(data, &checkpoint); err != nil || checkpoint.Version != containerRunnerVersion || checkpoint.ProfileID != runner.profileID || !containerRunnerRequestIDPattern.MatchString(checkpoint.RequestID) || (checkpoint.CallerRequestID != "" && !containerRunnerRequestIDPattern.MatchString(checkpoint.CallerRequestID)) || len(checkpoint.Intent) != 64 || !isLowerHexContainer(checkpoint.Intent) {
		return containerRunCheckpoint{}, false, ErrContainerRunnerCorrupt
	}
	if checkpoint.State != containerRunPending && checkpoint.State != containerRunUnknown && checkpoint.State != containerRunCompleted {
		return containerRunCheckpoint{}, false, ErrContainerRunnerCorrupt
	}
	if checkpoint.Reviewed && checkpoint.State != containerRunUnknown {
		return containerRunCheckpoint{}, false, ErrContainerRunnerCorrupt
	}
	if checkpoint.Response != nil {
		if checkpoint.Response.ProfileID != "" && checkpoint.Response.ProfileID != runner.profileID {
			return containerRunCheckpoint{}, false, ErrContainerRunnerCorrupt
		}
		if checkpoint.Response.RequestID != "" && checkpoint.Response.RequestID != checkpoint.RequestID {
			return containerRunCheckpoint{}, false, ErrContainerRunnerCorrupt
		}
		checkpoint.Response = ptrSanitizedResponse(*checkpoint.Response, runner.template.Model.APIKey, runner.template.MCP.Token)
	}
	return checkpoint, true, nil
}

func (runner *ContainerRunner) saveCheckpoint(checkpoint containerRunCheckpoint) error {
	if runner.guard.Check(runner.checkpoint) != nil || checkpoint.Version != containerRunnerVersion || checkpoint.ProfileID != runner.profileID || !containerRunnerRequestIDPattern.MatchString(checkpoint.RequestID) || (checkpoint.CallerRequestID != "" && !containerRunnerRequestIDPattern.MatchString(checkpoint.CallerRequestID)) || len(checkpoint.Intent) != 64 || !isLowerHexContainer(checkpoint.Intent) {
		return ErrContainerRunnerConfig
	}
	if checkpoint.Response != nil {
		response := sanitizeContainerResponse(*checkpoint.Response, runner.template.Model.APIKey, runner.template.MCP.Token)
		checkpoint.Response = &response
	}
	data, err := json.Marshal(checkpoint)
	if err != nil || len(data) > containerRunnerMaxFileBytes {
		return ErrContainerRunnerUnknown
	}
	temporary, err := os.CreateTemp(runner.stateRoot, ".container-runner-*.tmp")
	if err != nil {
		return ErrContainerRunnerUnknown
	}
	temporaryName := temporary.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporaryName)
		}
	}()
	if err = temporary.Chmod(0600); err == nil {
		written, writeErr := temporary.Write(data)
		if writeErr != nil {
			err = writeErr
		} else if written != len(data) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return ErrContainerRunnerUnknown
	}
	if err := os.Rename(temporaryName, runner.checkpoint); err != nil {
		return ErrContainerRunnerUnknown
	}
	keep = true
	directory, err := os.Open(runner.stateRoot)
	if err != nil {
		return ErrContainerRunnerUnknown
	}
	err = directory.Sync()
	_ = directory.Close()
	if err != nil {
		return ErrContainerRunnerUnknown
	}
	return nil
}

func ensureContainerRunnerPrivateDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsDir() || info.Mode().Perm()&0077 != 0 {
		return ErrContainerRunnerConfig
	}
	return os.Chmod(path, 0700)
}

func cloneContainerTemplate(template airunner.ExecuteRequest) airunner.ExecuteRequest {
	template.Skills = append([]airunner.Skill(nil), template.Skills...)
	return template
}

func validateContainerIntent(profileID string, request aicodex.RunRequest) error {
	if request.ProfileID != profileID || !containerRunnerProfileIDPattern.MatchString(request.ProfileID) {
		return ErrContainerRunnerConfig
	}
	if request.RequestID != "" && !containerRunnerRequestIDPattern.MatchString(request.RequestID) {
		return ErrContainerRunnerConfig
	}
	if len([]byte(request.Prompt)) > containerRunnerMaxPrompt || strings.ContainsRune(request.Prompt, '\x00') {
		return ErrContainerRunnerConfig
	}
	if request.Resume {
		if strings.TrimSpace(request.ThreadID) == "" || len([]byte(request.ThreadID)) > 256 || strings.ContainsAny(request.ThreadID, "\x00\r\n") {
			return ErrContainerRunnerConfig
		}
	} else if request.ThreadID != "" {
		return ErrContainerRunnerConfig
	}
	return nil
}

func hashContainerIntent(request aicodex.RunRequest) string {
	intent := containerRunIntent{Prompt: request.Prompt, Resume: request.Resume, ThreadID: request.ThreadID}
	data, _ := json.Marshal(intent)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func newContainerRequestID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "container-" + hex.EncodeToString(random[:]), nil
}

func responseToCodexResult(response airunner.Response, profileID string) (aicodex.Result, error) {
	if response.Result == nil || response.Result.ProfileID != "" && response.Result.ProfileID != profileID {
		return aicodex.Result{ProfileID: profileID}, ErrContainerRunnerIncomplete
	}
	value := response.Result
	result := aicodex.Result{ProfileID: profileID, ThreadID: value.ThreadID, LastMessage: value.LastMessage, Usage: value.Usage, Turn: aicodex.TurnResult{Status: aicodex.TurnStatus(value.Turn.Status), ID: value.Turn.ID, Usage: value.Turn.Usage, Error: value.Turn.Error}, Process: aicodex.ProcessResult{Status: aicodex.ProcessStatus(value.Process.Status), ExitCode: value.Process.ExitCode, Signal: value.Process.Signal}, Stderr: value.Stderr, Checkpoint: aicodex.ThreadCheckpoint{Version: value.Checkpoint.Version, ProfileID: value.Checkpoint.ProfileID, ThreadID: value.Checkpoint.ThreadID, State: aicodex.CheckpointState(value.Checkpoint.State), TurnStatus: aicodex.TurnStatus(value.Checkpoint.TurnStatus), LastTurnID: value.Checkpoint.LastTurnID, LastEventType: value.Checkpoint.LastEventType, TurnCompleted: value.Checkpoint.TurnCompleted, UpdatedAt: value.Checkpoint.UpdatedAt}}
	result.Events = make([]aicodex.Event, 0, len(value.Events))
	for _, event := range value.Events {
		result.Events = append(result.Events, aicodex.Event{Type: event.Type, ThreadID: event.ThreadID, TurnID: event.TurnID, ItemType: event.ItemType, Item: append(json.RawMessage(nil), event.Item...), Usage: event.Usage, HasUsage: event.HasUsage, Error: event.Error, Raw: append(json.RawMessage(nil), event.Raw...)})
	}
	return result, nil
}

func isCompleteContainerResponse(response airunner.Response) bool {
	if !response.OK || response.Result == nil || response.Result.ThreadID == "" {
		return false
	}
	result := response.Result
	return result.Turn.Status == string(aicodex.TurnCompleted) && result.Process.Status == string(aicodex.ProcessExited) && result.Process.ExitCode == 0
}

func partialResultFromResponse(response *airunner.Response, profileID string) aicodex.Result {
	if response == nil {
		return aicodex.Result{ProfileID: profileID}
	}
	result, _ := responseToCodexResult(*response, profileID)
	return result
}

func completedContainerThreadID(checkpoint containerRunCheckpoint) string {
	if checkpoint.Response == nil || checkpoint.Response.Result == nil {
		return ""
	}
	return checkpoint.Response.Result.ThreadID
}

func sanitizeContainerResponse(response airunner.Response, secrets ...string) airunner.Response {
	data, err := json.Marshal(response)
	if err != nil {
		return airunner.Response{OK: false, ProfileID: response.ProfileID, RequestID: response.RequestID}
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&value) != nil {
		return airunner.Response{OK: false, ProfileID: response.ProfileID, RequestID: response.RequestID}
	}
	redactContainerValue(value, secrets)
	data, err = json.Marshal(value)
	if err != nil {
		return airunner.Response{OK: false, ProfileID: response.ProfileID, RequestID: response.RequestID}
	}
	var sanitized airunner.Response
	if decodeContainerJSON(data, &sanitized) != nil {
		return airunner.Response{OK: false, ProfileID: response.ProfileID, RequestID: response.RequestID}
	}
	return sanitized
}

func ptrSanitizedResponse(response airunner.Response, secrets ...string) *airunner.Response {
	sanitized := sanitizeContainerResponse(response, secrets...)
	return &sanitized
}

func redactContainerValue(value any, secrets []string) {
	switch current := value.(type) {
	case map[string]any:
		for key, child := range current {
			lower := strings.ToLower(key)
			if lower == "api_key" || lower == "token" || strings.Contains(lower, "secret") {
				if _, ok := child.(string); ok {
					current[key] = "[REDACTED]"
					continue
				}
			}
			if text, ok := child.(string); ok {
				current[key] = redactContainerString(text, secrets)
			} else {
				redactContainerValue(child, secrets)
			}
		}
	case []any:
		for index, child := range current {
			if text, ok := child.(string); ok {
				current[index] = redactContainerString(text, secrets)
			} else {
				redactContainerValue(child, secrets)
			}
		}
	}
}

func redactContainerString(value string, secrets []string) string {
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func decodeContainerJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func isLowerHexContainer(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

var _ interface {
	Run(context.Context, aicodex.RunRequest) (aicodex.Result, error)
} = (*ContainerRunner)(nil)
