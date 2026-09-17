// Package aisupervisor owns the lifecycle around one real Codex session per
// StoneAge AI profile.  It supervises turns and game observations; it is not a
// model provider and it does not implement a second reasoning loop.
package aisupervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

const (
	StateStarting   = "starting"
	StateRunning    = "running"
	StateWaiting    = "waiting"
	StateDiagnosing = "diagnosing"
	StatePaused     = "paused"
	StateStopped    = "stopped"
	StateCompleted  = "completed"
	StateError      = "error"

	TaskStatusPending   = "pending"
	TaskStatusRunning   = "running"
	TaskStatusCompleted = "completed"
	TaskStatusFailed    = "failed"
)

var (
	ErrClosed              = errors.New("aisupervisor: supervisor is closed")
	ErrFactoryUnavailable  = errors.New("aisupervisor: agent session factory is not configured")
	ErrAlreadyRunning      = errors.New("aisupervisor: profile is already running")
	ErrInvalidSession      = errors.New("aisupervisor: factory returned an invalid session")
	ErrProfileDeleted      = errors.New("aisupervisor: profile is deleted")
	ErrProfileChanged      = errors.New("aisupervisor: profile changed while a turn was pending")
	ErrNoProgress          = errors.New("aisupervisor: profile paused after making no progress")
	ErrRunLimit            = errors.New("aisupervisor: profile paused after repeated turn failures")
	ErrObserveLimit        = errors.New("aisupervisor: profile paused after repeated observation failures")
	ErrGoalComplete        = errors.New("aisupervisor: game goal is complete")
	ErrBudgetBoundary      = errors.New("aisupervisor: token budget reached after turn")
	ErrInvalidSnapshot     = errors.New("aisupervisor: invalid game snapshot")
	ErrAttemptRecovery     = errors.New("aisupervisor: unresolved model turn requires recovery")
	ErrAttemptStillRunning = errors.New("aisupervisor: previous model turn is still running")
)

// StartFailure stages and codes are deliberately small, stable and
// secret-free.  They are safe to expose at the remote-worker/admin boundary;
// the underlying error is retained only for errors.Is compatibility and is
// never included in Error or in the public details methods.
const (
	StartStageProfile  = "profile"
	StartStageRecovery = "recovery"
	StartStageFactory  = "factory"
	StartStageSession  = "session"
	StartStageRuntime  = "runtime"
)

const (
	StartCodeCanceled           = "canceled"
	StartCodeTimeout            = "timeout"
	StartCodeClosed             = "closed"
	StartCodeProfileNotFound    = "profile_not_found"
	StartCodeProfileChanged     = "profile_changed"
	StartCodeProfileDeleted     = "profile_deleted"
	StartCodeProfileInvalid     = "profile_invalid"
	StartCodeRecoveryRequired   = "recovery_required"
	StartCodeRecoveryFailed     = "recovery_failed"
	StartCodeFactoryUnavailable = "factory_unavailable"
	StartCodeFactoryOpenFailed  = "factory_open_failed"
	StartCodeSessionInvalid     = "session_invalid"
	StartCodeRuntimeFailed      = "runtime_failed"
	StartCodeAttemptPending     = "attempt_pending"
	StartCodeUnknown            = "unknown"
)

// StartFailure is returned when a profile cannot be started before a managed
// session is published.  Error deliberately contains no provider, account,
// model, network or credential detail.  Callers may still use errors.Is to
// inspect the original package sentinel.
type StartFailure struct {
	ProfileID string
	Stage     string
	Code      string
	Duration  time.Duration

	cause error
}

func (failure *StartFailure) Error() string {
	if failure == nil {
		return "aisupervisor: profile start failed"
	}
	return "aisupervisor: profile start failed"
}

func (failure *StartFailure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

// StartFailureDetails returns only the allowlisted fields intended for
// transport/UI use.  It avoids exposing the wrapped error through a generic
// formatter at boundaries that handle startup diagnostics.
func (failure *StartFailure) StartFailureDetails() (profileID, stage, code string, duration time.Duration) {
	if failure == nil {
		return "", "", "", 0
	}
	return failure.ProfileID, failure.Stage, failure.Code, failure.Duration
}

// Runner is the small part of aicodex.Runner needed by the supervisor.  It
// keeps the supervisor testable and prevents it from depending on process
// details or a model API.
type Runner interface {
	Run(context.Context, aicodex.RunRequest) (aicodex.Result, error)
}

// RecoveryRunner is implemented by transports that can reconcile an already
// dispatched request by its durable request ID without issuing a second model
// turn. A plain local CLI Runner does not satisfy this interface: when a
// dispatch result is lost, the supervisor must leave the attempt unknown and
// pause rather than blindly invoke the CLI again.
type RecoveryRunner interface {
	Recover(context.Context, aicodex.RunRequest) (aicodex.Result, error)
}

// AgentSession is supplied by the game service for one profile.  Close must
// revoke the profile's game-control lease synchronously.  The supervisor
// calls Close before cancelling a Codex turn.  Observe is an observation
// callback, not a model call.  Wake receives game events or skill-task
// completion notifications; it is never populated by model output.
type AgentSession struct {
	Runner  Runner
	Observe func(context.Context) (Snapshot, error)
	Close   func()
	Wake    chan struct{}
}

// Factory opens an isolated session for a profile.  Binary paths, Codex
// homes, MCP servers, game leases and credentials remain server-owned by the
// factory; none are accepted from a profile or a prompt.
type Factory interface {
	Open(context.Context, airuntime.Profile) (AgentSession, error)
}

// FactoryCloser is optional.  A service may implement it to release global
// resources after all profile sessions have been closed.
type FactoryCloser interface {
	Close() error
}

type TaskHandle struct {
	Handle   string          `json:"handle"`
	Status   string          `json:"status"`
	Evidence json.RawMessage `json:"evidence,omitempty"`
	Context  json.RawMessage `json:"context,omitempty"`
}

type Snapshot struct {
	GameReady    bool            `json:"game_ready"`
	GoalComplete bool            `json:"goal_complete"`
	ProgressKey  string          `json:"progress_key,omitempty"`
	ActiveTasks  []TaskHandle    `json:"active_tasks,omitempty"`
	Context      json.RawMessage `json:"context,omitempty"`
}

// Status is intentionally independent of internal/admin.  An adapter can
// map it to an HTTP view without importing the admin package into the runtime.
type LifeActivity struct {
	Kind      string    `json:"kind,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
	Until     time.Time `json:"until,omitempty"`
}

type Status struct {
	Activity        LifeActivity `json:"activity"`
	ProfileID       string       `json:"profile_id"`
	State           string       `json:"state"`
	Message         string       `json:"message,omitempty"`
	LastError       string       `json:"last_error,omitempty"`
	ProfileVersion  int64        `json:"profile_version,omitempty"`
	Checkpoint      int64        `json:"checkpoint_version,omitempty"`
	ThreadID        string       `json:"thread_id,omitempty"`
	ModelTurnDone   bool         `json:"model_turn_done"`
	NextDecisionAt  time.Time    `json:"next_decision_at,omitempty"`
	GameReady       bool         `json:"game_ready"`
	GoalComplete    bool         `json:"goal_complete"`
	ProgressKey     string       `json:"progress_key,omitempty"`
	ActiveTasks     []TaskHandle `json:"active_tasks,omitempty"`
	RunFailures     int          `json:"run_failures,omitempty"`
	NoProgressCount int          `json:"no_progress_count,omitempty"`
	UpdatedAt       time.Time    `json:"updated_at"`
}

// Config bounds observation, turn and retry work.  Values are normalized at
// construction; a service can choose tighter limits for production.
type Config struct {
	PollInterval           time.Duration
	ObserveTimeout         time.Duration
	TurnTimeout            time.Duration
	FailureBackoff         time.Duration
	MaxFailureBackoff      time.Duration
	MaxRunFailures         int
	MaxObservationFailures int
	// NoProgressLimit bounds diagnosis episodes. A poll of an unchanged
	// snapshot never increments this value; one episode can be recorded only
	// after NoProgressWindow has elapsed since progress (or the prior
	// diagnosis).
	NoProgressLimit  int
	NoProgressWindow time.Duration
	TokenCharge      int64
	Clock            func() time.Time
	Prompt           func(airuntime.Profile, Snapshot) (string, error)
}

func (config Config) normalized() (Config, error) {
	if config.PollInterval <= 0 {
		config.PollInterval = time.Second
	}
	if config.PollInterval > 24*time.Hour {
		return Config{}, errors.New("aisupervisor: poll interval is too long")
	}
	if config.ObserveTimeout <= 0 {
		config.ObserveTimeout = 30 * time.Second
	}
	if config.ObserveTimeout > 10*time.Minute {
		return Config{}, errors.New("aisupervisor: observation timeout is too long")
	}
	if config.TurnTimeout <= 0 {
		config.TurnTimeout = 30 * time.Minute
	}
	if config.TurnTimeout > 24*time.Hour {
		return Config{}, errors.New("aisupervisor: turn timeout is too long")
	}
	if config.FailureBackoff <= 0 {
		config.FailureBackoff = time.Second
	}
	if config.MaxFailureBackoff <= 0 {
		config.MaxFailureBackoff = time.Minute
	}
	if config.MaxFailureBackoff < config.FailureBackoff || config.MaxFailureBackoff > time.Hour {
		return Config{}, errors.New("aisupervisor: failure backoff bounds are invalid")
	}
	if config.MaxRunFailures <= 0 {
		config.MaxRunFailures = 3
	}
	if config.MaxRunFailures > 32 {
		return Config{}, errors.New("aisupervisor: max run failures is too high")
	}
	if config.MaxObservationFailures <= 0 {
		config.MaxObservationFailures = 3
	}
	if config.MaxObservationFailures > 32 {
		return Config{}, errors.New("aisupervisor: max observation failures is too high")
	}
	if config.NoProgressLimit <= 0 {
		config.NoProgressLimit = 3
	}
	if config.NoProgressLimit > 32 {
		return Config{}, errors.New("aisupervisor: no-progress limit is too high")
	}
	if config.NoProgressWindow <= 0 {
		config.NoProgressWindow = 5 * time.Minute
	}
	if config.NoProgressWindow > 24*time.Hour {
		return Config{}, errors.New("aisupervisor: no-progress window is too long")
	}
	if config.TokenCharge <= 0 {
		// This is a conservative accounting reservation, not an API hard
		// limit. The Codex result's actual usage is recorded separately.
		config.TokenCharge = 1
	}
	if config.TokenCharge > 2_000_000_000 {
		return Config{}, errors.New("aisupervisor: token charge is too high")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return config, nil
}

type persistedState struct {
	Activity       LifeActivity `json:"activity"`
	ProfileVersion int64        `json:"profile_version"`
	ThreadID       string       `json:"thread_id,omitempty"`
	// NextDecisionAt is the next autonomous life-mode decision boundary. It is
	// persisted so a supervisor restart does not replay every missed interval;
	// a due timestamp simply permits one decision, after which the next
	// boundary is scheduled from the current clock.
	NextDecisionAt time.Time `json:"next_decision_at,omitempty"`
	// PendingAttemptID is written before a model runner is dispatched. It
	// remains in the checkpoint until the recorded outcome has been settled
	// and applied, so a crash cannot turn one model turn into a new request.
	PendingAttemptID string    `json:"pending_attempt_id,omitempty"`
	ModelTurnDone    bool      `json:"model_turn_done"`
	WaitingForWake   bool      `json:"waiting_for_wake"`
	Snapshot         Snapshot  `json:"snapshot"`
	RunFailures      int       `json:"run_failures"`
	ObserveErrors    int       `json:"observe_errors"`
	NoProgress       int       `json:"no_progress"`
	LastProgressAt   time.Time `json:"last_progress_at,omitempty"`
	// LastNoProgressAt records the last bounded diagnosis in the current
	// no-progress episode. It prevents a fast poll loop from counting the same
	// episode repeatedly while preserving LastProgressAt as the real progress
	// timestamp.
	LastNoProgressAt time.Time `json:"last_no_progress_at,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
}

func validateSession(session AgentSession) error {
	if session.Runner == nil || session.Observe == nil || session.Close == nil {
		return ErrInvalidSession
	}
	return nil
}

func validateSnapshot(snapshot Snapshot) error {
	if len(snapshot.Context) > 0 && !json.Valid(snapshot.Context) {
		return fmt.Errorf("%w: snapshot context is not JSON", ErrInvalidSnapshot)
	}
	if len(snapshot.ActiveTasks) > 128 {
		return fmt.Errorf("%w: too many active tasks", ErrInvalidSnapshot)
	}
	for _, task := range snapshot.ActiveTasks {
		if strings.TrimSpace(task.Handle) == "" || len(task.Handle) > 256 {
			return fmt.Errorf("%w: task handle is invalid", ErrInvalidSnapshot)
		}
		if len(task.Status) > 64 || strings.ContainsAny(task.Status, "\x00\r\n") {
			return fmt.Errorf("%w: task status is invalid", ErrInvalidSnapshot)
		}
		if len(task.Evidence) > 1<<20 || (len(task.Evidence) > 0 && !json.Valid(task.Evidence)) {
			return fmt.Errorf("%w: task evidence is invalid", ErrInvalidSnapshot)
		}
		if len(task.Context) > 1<<20 || (len(task.Context) > 0 && !json.Valid(task.Context)) {
			return fmt.Errorf("%w: task context is invalid", ErrInvalidSnapshot)
		}
	}
	if len(snapshot.ProgressKey) > 1024 || strings.ContainsAny(snapshot.ProgressKey, "\x00\r\n") {
		return fmt.Errorf("%w: progress key is invalid", ErrInvalidSnapshot)
	}
	return nil
}
