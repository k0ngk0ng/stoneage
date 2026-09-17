// Package airemote connects the production AI supervisor to a profile-owned
// worker running outside the production host.  The server keeps the durable
// request journal; the worker only receives one bounded runtime request at a
// time and reports its result over an authenticated HTTPS control channel.
package airemote

import (
	"context"
	"encoding/json"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
)

const (
	ProtocolVersion = 1
	KindRun         = "run"
	KindInspect     = "inspect"
	KindReadResult  = "read_result"
	KindStop        = "stop"
	KindRemove      = "remove"
	KindRemoveVol   = "remove_volume"
)

// ConnectRequest is sent by a worker on its first enrollment or when it has
// a previously issued session credential. EnrollmentToken is intentionally
// never included in a server response and is not persisted by Hub.
type ConnectRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	WorkerID        string `json:"worker_id"`
	ProfileID       string `json:"profile_id"`
	EnrollmentToken string `json:"enrollment_token,omitempty"`
	SessionToken    string `json:"session_token,omitempty"`
	Epoch           uint64 `json:"epoch,omitempty"`
	Start           bool   `json:"start,omitempty"`
}

type ConnectResponse struct {
	ProtocolVersion int       `json:"protocol_version"`
	WorkerID        string    `json:"worker_id"`
	ProfileID       string    `json:"profile_id"`
	SessionToken    string    `json:"session_token"`
	Epoch           uint64    `json:"epoch"`
	LeaseSeconds    int       `json:"lease_seconds"`
	ConnectedAt     time.Time `json:"connected_at"`
	StartRequested  bool      `json:"start_requested,omitempty"`
	StartError      string    `json:"start_error,omitempty"`
	StartPhase      string    `json:"start_phase,omitempty"`
	StartStage      string    `json:"start_stage,omitempty"`
	StartCode       string    `json:"start_code,omitempty"`
	StartDurationMS int64     `json:"start_duration_ms,omitempty"`
	Error           string    `json:"error,omitempty"`
}

type PollRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	WorkerID        string `json:"worker_id"`
	ProfileID       string `json:"profile_id"`
	Epoch           uint64 `json:"epoch"`
	WaitSeconds     int    `json:"wait_seconds,omitempty"`
}

type PollResponse struct {
	ProtocolVersion int       `json:"protocol_version"`
	Command         *Command  `json:"command,omitempty"`
	LeaseUntil      time.Time `json:"lease_until"`
	StartError      string    `json:"start_error,omitempty"`
	StartPhase      string    `json:"start_phase,omitempty"`
	StartStage      string    `json:"start_stage,omitempty"`
	StartCode       string    `json:"start_code,omitempty"`
	StartDurationMS int64     `json:"start_duration_ms,omitempty"`
}

// Command contains no persisted model key. Payload lives only in Hub memory
// until the worker acknowledges the command, then is discarded. Transport is
// expected to be HTTPS; the endpoint may additionally be protected by mTLS.
type Command struct {
	ProtocolVersion int             `json:"protocol_version"`
	CommandID       string          `json:"command_id"`
	Kind            string          `json:"kind"`
	WorkerID        string          `json:"worker_id"`
	WorkerEpoch     uint64          `json:"worker_epoch"`
	ProfileID       string          `json:"profile_id"`
	RequestID       string          `json:"request_id,omitempty"`
	ContainerName   string          `json:"container_name"`
	VolumeName      string          `json:"volume_name,omitempty"`
	PayloadHash     string          `json:"payload_hash,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	GameEndpoint    string          `json:"game_endpoint,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}

type ResultRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	CommandID       string `json:"command_id"`
	WorkerID        string `json:"worker_id"`
	WorkerEpoch     uint64 `json:"worker_epoch"`
	ProfileID       string `json:"profile_id"`
	RequestID       string `json:"request_id,omitempty"`
	ContainerName   string `json:"container_name"`
	PayloadHash     string `json:"payload_hash,omitempty"`
	State           string `json:"state"`
	NotFound        bool   `json:"not_found,omitempty"`
	ExitCode        int    `json:"exit_code,omitempty"`
	Stdout          []byte `json:"stdout,omitempty"`
	Stderr          []byte `json:"stderr,omitempty"`
	Error           string `json:"error,omitempty"`
}

type HeartbeatRequest struct {
	ProtocolVersion int    `json:"protocol_version"`
	WorkerID        string `json:"worker_id"`
	ProfileID       string `json:"profile_id"`
	Epoch           uint64 `json:"epoch"`
}

type HeartbeatResponse struct {
	ProtocolVersion int       `json:"protocol_version"`
	LeaseUntil      time.Time `json:"lease_until"`
}

// Status is safe to expose in the admin UI. It contains no credential or
// runtime payload.
type Status struct {
	ProfileID       string    `json:"profile_id"`
	WorkerID        string    `json:"worker_id,omitempty"`
	Online          bool      `json:"online"`
	Epoch           uint64    `json:"epoch,omitempty"`
	ConnectedAt     time.Time `json:"connected_at,omitempty"`
	LastSeenAt      time.Time `json:"last_seen_at,omitempty"`
	LeaseUntil      time.Time `json:"lease_until,omitempty"`
	Container       string    `json:"container,omitempty"`
	RequestID       string    `json:"request_id,omitempty"`
	StartRequested  bool      `json:"start_requested,omitempty"`
	StartError      string    `json:"start_error,omitempty"`
	StartPhase      string    `json:"start_phase,omitempty"`
	StartStage      string    `json:"start_stage,omitempty"`
	StartCode       string    `json:"start_code,omitempty"`
	StartDurationMS int64     `json:"start_duration_ms,omitempty"`
}

// RouteRecord is the only routing metadata Hub persists. It is deliberately
// free of payloads, API keys, game tokens and raw runtime output.
type RouteRecord struct {
	ProfileID     string    `json:"profile_id"`
	WorkerID      string    `json:"worker_id"`
	WorkerEpoch   uint64    `json:"worker_epoch"`
	RequestID     string    `json:"request_id"`
	ContainerName string    `json:"container_name"`
	VolumeName    string    `json:"volume_name,omitempty"`
	PayloadHash   string    `json:"payload_hash"`
	State         string    `json:"state"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type ProfileRecord struct {
	ProfileID     string    `json:"profile_id"`
	TokenHash     string    `json:"token_hash,omitempty"`
	Consumed      bool      `json:"consumed"`
	WorkerID      string    `json:"worker_id,omitempty"`
	SessionHash   string    `json:"session_hash,omitempty"`
	Epoch         uint64    `json:"epoch,omitempty"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	PublicBaseURL string    `json:"public_base_url,omitempty"`
}

type Snapshot struct {
	Routes   []RouteRecord   `json:"routes,omitempty"`
	Profiles []ProfileRecord `json:"profiles,omitempty"`
}

type Store interface {
	Load(context.Context) (Snapshot, error)
	Save(context.Context, Snapshot) error
}

// Compile-time documentation for the server-side seam.
var _ aibroker.Docker = (*Hub)(nil)
