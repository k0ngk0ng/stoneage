// Package aibroker owns the host-side boundary that starts one isolated
// StoneAge AI runtime container for a profile.  Requests are passed to the
// runtime over stdin; the broker never interprets a prompt as a command or a
// Docker option.
package aibroker

import (
	"context"
	"errors"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

const (
	// The runtime image and network are deployment-owned and must be supplied
	// explicitly. The broker deliberately has no floating image tag default.
	DefaultNamePrefix = "stoneage-ai"

	DefaultMaxRequestBytes = 1 << 20
	DefaultMaxStdoutBytes  = 8 << 20
	DefaultMaxStderrBytes  = 256 << 10
	DefaultStopTimeout     = 10 * time.Second
	DefaultTmpfsSize       = "64m"
	DefaultRunTmpfsSize    = "8m"
)

var (
	ErrInvalidConfig     = errors.New("aibroker: invalid configuration")
	ErrClosed            = errors.New("aibroker: broker is closed")
	ErrInvalidRequest    = errors.New("aibroker: invalid request")
	ErrRequestTooLarge   = errors.New("aibroker: request is too large")
	ErrJournalNotFound   = errors.New("aibroker: journal entry not found")
	ErrJournalExists     = errors.New("aibroker: journal entry already exists")
	ErrJournalConflict   = errors.New("aibroker: request id conflicts with an existing run")
	ErrProfileBusy       = errors.New("aibroker: profile already has an unresolved run")
	ErrDocker            = errors.New("aibroker: Docker execution failed")
	ErrOutputLimit       = errors.New("aibroker: Docker output exceeded limit")
	ErrResponse          = errors.New("aibroker: runtime response is invalid")
	ErrRunRunning        = errors.New("aibroker: run is already running")
	ErrRunUnknown        = errors.New("aibroker: run outcome is unknown")
	ErrRunCompleted      = errors.New("aibroker: run has already completed")
	ErrJournalBusy       = errors.New("aibroker: journal is already owned by another broker")
	ErrJournalState      = errors.New("aibroker: journal state changed")
	ErrContainerNotFound = errors.New("aibroker: container was not found")
)

// ReviewReasonAcceptUncertainOutcome is the only operator disposition that
// can release an unknown request.  Keeping the reason as a fixed code avoids
// turning the broker journal into an operator-controlled free-text sink for
// credentials or provider diagnostics.
const ReviewReasonAcceptUncertainOutcome = "accept_uncertain_outcome"

// RunState is the durable outcome of one request ID.  A running entry is
// retained when the broker process goes away before it can observe a result;
// an unknown entry is never retried automatically because the model turn may
// already have reached the provider.
type RunState string

const (
	RunRunning   RunState = "running"
	RunCompleted RunState = "completed"
	RunUnknown   RunState = "unknown"
)

// Config contains server-owned Docker/runtime values.  Docker and Journal
// are dependency seams for tests and for services that already own these
// resources.  When Docker is nil, DockerBinary is used to construct the
// process-backed Docker client.  When Journal is nil, JournalPath is opened
// as a private file journal.
type Config struct {
	Docker       Docker
	DockerBinary string

	// Image and Network are fixed at construction time. They are not copied
	// from airunner.ExecuteRequest.
	Image   string
	Network string

	// RunnerCommand is appended after the image. The normal image has
	// /usr/local/bin/stoneage-ai-runner as its ENTRYPOINT, so this should be
	// left empty; the broker then passes only the fixed profile selector.
	// Supplying a command is useful for a controlled image whose entrypoint is
	// not the project runner, but it remains server-owned configuration.
	RunnerCommand []string

	// Environment contains fixed, non-secret environment values. Protected
	// runtime variables are rejected so a caller cannot redirect a container
	// to another state or executable path.
	Environment map[string]string

	Journal     Journal
	JournalPath string

	NamePrefix      string
	MaxRequestBytes int
	MaxStdoutBytes  int
	MaxStderrBytes  int
	StopTimeout     time.Duration
	Clock           func() time.Time
}

// VolumeMount is the only mount shape accepted by the broker. Source is a
// Docker named volume, never a host path. The runtime's profile state must be
// writable while all other host/admin/game trees remain outside its view.
type VolumeMount struct {
	Name     string
	Target   string
	ReadOnly bool
}

// Mount is retained as a short alias for callers that use the generic name.
type Mount = VolumeMount

// RunSpec is a fully materialized, server-owned Docker invocation. A fake
// Docker implementation can inspect it without starting a real container.
type RunSpec struct {
	Image         string
	Network       string
	ContainerName string
	VolumeName    string
	Mounts        []VolumeMount
	// Volumes mirrors Mounts for adapters that use Docker terminology. The
	// broker always sets both to the same single profile volume.
	Volumes         []VolumeMount
	User            string
	ReadOnlyRootfs  bool
	CapDrop         []string
	SecurityOpt     []string
	NoNewPrivileges bool
	Tmpfs           map[string]string
	Environment     map[string]string
	Command         []string
}

// DockerResult is the bounded result returned by a Docker adapter. The
// broker intentionally discards stderr and all diagnostics that could carry
// credentials when it builds its public response.
type DockerResult struct {
	ContainerID string
	ExitCode    int
	Stdout      []byte
	Stderr      []byte
}

// Docker is the only process/container seam used by Broker. Run receives the
// already encoded request as stdin; the adapter must not obtain a request
// payload from an environment variable, file mount, or shell command.
type Docker interface {
	Run(context.Context, RunSpec, []byte) (DockerResult, error)
	Stop(context.Context, string) error
}

// DockerInspector is an optional read-only seam used during broker recovery.
// Implementations must return ErrContainerNotFound only when the container is
// definitively absent. Other Docker/daemon errors are deliberately left
// opaque so recovery can fail closed and retain RunRunning.
type DockerInspector interface {
	Inspect(context.Context, string) (DockerContainerState, error)
}

// DockerResultReader is an optional seam used to recover a result after the
// broker process disappeared while the runtime container kept running. The
// implementation must read only a terminated container and bound both log
// streams before returning. A read error never proves that a turn failed.
type DockerResultReader interface {
	ReadResult(context.Context, string) (DockerResult, error)
}

// DockerRemover is an optional seam used to discard a terminated container
// after its response has been durably published. Implementations must refuse
// to force-remove an active container; cleanup is best effort and can be
// retried by a later Lookup.
type DockerRemover interface {
	Remove(context.Context, string) error
}

// DockerContainerState is the daemon-reported lifecycle state of a named
// container. Unknown values are treated as non-terminal by the broker.
type DockerContainerState string

const (
	DockerContainerCreated    DockerContainerState = "created"
	DockerContainerRunning    DockerContainerState = "running"
	DockerContainerPaused     DockerContainerState = "paused"
	DockerContainerRestarting DockerContainerState = "restarting"
	DockerContainerRemoving   DockerContainerState = "removing"
	DockerContainerExited     DockerContainerState = "exited"
	DockerContainerDead       DockerContainerState = "dead"
)

// JournalEntry is the durable, credential-free record for one profile/run
// pair. PayloadHash is SHA-256 of the exact stdin JSON. Response contains a
// redacted runtime response only; request payload, API keys and game tokens
// are never stored.
type JournalEntry struct {
	ProfileID     string    `json:"profile_id"`
	RequestID     string    `json:"request_id"`
	PayloadHash   string    `json:"payload_hash"`
	State         RunState  `json:"state"`
	ContainerName string    `json:"container_name"`
	VolumeName    string    `json:"volume_name"`
	Response      []byte    `json:"response,omitempty"`
	ErrorCode     string    `json:"error_code,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
	Review        *Review   `json:"review,omitempty"`
}

// Review is an immutable operator disposition attached to an unknown run.
// It deliberately leaves State and Response untouched so existing request
// replay keeps returning ErrRunUnknown after the profile claim is released.
type Review struct {
	Actor      string    `json:"actor"`
	Reason     string    `json:"reason"`
	ReviewedAt time.Time `json:"reviewed_at"`
}

// Journal is an atomic durable compare-and-publish seam. Create returns true
// only when it inserted a new key. Update replaces an existing key only when
// profile, request and payload hash still match.
type Journal interface {
	Get(context.Context, string, string) (JournalEntry, error)
	Create(context.Context, JournalEntry) (bool, error)
	Update(context.Context, JournalEntry) error
}

// JournalRecovery is an optional durable-journal extension. The SQLite
// implementation supplies it so New can inspect records left by a previous
// broker process without changing the Journal interface used by callers.
type JournalRecovery interface {
	ListRunning(context.Context) ([]JournalEntry, error)
}

// JournalCAS is an optional compare-and-publish extension. It prevents a
// recovery transition or a late runtime result from overwriting a newer state
// written by another owner. Implementations lacking this extension are used
// conservatively for recovery and retain unresolved records.
type JournalCAS interface {
	UpdateIfState(context.Context, RunState, JournalEntry) error
}

// JournalReviewer is an optional durable-journal extension for an explicit
// operator disposition. Implementations compare expectedUpdatedAt exactly,
// return the durable row for idempotent retries, and release the active
// profile claim without changing the request outcome.
type JournalReviewer interface {
	ReviewUnknown(context.Context, string, string, time.Time, string, string) (JournalEntry, error)
}

// RunResult gives callers the durable state alongside the runtime response.
// The convenience Execute method below exposes the airunner response shape.
type RunResult struct {
	State    RunState
	Entry    JournalEntry
	Response airunner.Response
}
