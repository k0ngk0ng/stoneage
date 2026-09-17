// Package websession connects the AI runtime to a character session owned by
// the Web bridge.  The bridge remains the only process which owns a game
// socket and the control gate; this package only carries a short lived lease
// over the bridge's private control endpoint.
package websession

import (
	"errors"
	"fmt"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

const (
	AttachPath  = "/internal/agent/attach"
	ObservePath = "/internal/agent/observe"
	ExecutePath = "/internal/agent/execute"
	DetachPath  = "/internal/agent/detach"
	WatchPath   = "/internal/agent/watch"
)

var (
	ErrInvalidConfig    = errors.New("websession: invalid configuration")
	ErrInvalidRequest   = errors.New("websession: invalid request")
	ErrUnauthorized     = errors.New("websession: agent lease is unauthorized")
	ErrLeaseConflict    = errors.New("websession: agent lease conflicts with current control")
	ErrLeaseRevoked     = errors.New("websession: agent lease was revoked")
	ErrLeaseUnavailable = errors.New("websession: agent lease is unavailable")
	// ErrWriteOutcomeUnknown means the Web bridge could not prove that a
	// typed write was rejected before submission. Callers must preserve their
	// durable pending receipt and must not retry the action automatically.
	ErrWriteOutcomeUnknown = errors.New("websession: typed write outcome is unknown")
	ErrProtocol            = errors.New("websession: invalid private API response")
	ErrClosed              = errors.New("websession: session is closed")
)

// Config contains process-owned endpoints. BaseURL is the normal Web HTTP
// origin used for the same /api/sessions, /send and /events protocol as a
// browser. SocketPath is the private Unix socket exposed only to the admin
// runtime for agent lease RPCs. A model or an MCP request never supplies any
// of these values.
type Config struct {
	BaseURL    string
	ServerID   string
	SocketPath string
}

// Identity is the complete Web-side character identity. Every component is
// compared during attach; an account name or display name alone is not a
// sufficient binding for a lease.
type Identity struct {
	AccountID             string `json:"account_id"`
	CharacterID           string `json:"character_id"`
	CharacterName         string `json:"character_name"`
	ServerID              string `json:"server_id"`
	PersistentCharacterID string `json:"persistent_character_id,omitempty"`
}

func (identity Identity) normalized() Identity {
	identity.AccountID = strings.TrimSpace(identity.AccountID)
	identity.CharacterID = strings.TrimSpace(identity.CharacterID)
	identity.CharacterName = strings.TrimSpace(identity.CharacterName)
	identity.ServerID = strings.TrimSpace(identity.ServerID)
	identity.PersistentCharacterID = strings.TrimSpace(identity.PersistentCharacterID)
	return identity
}

func (identity Identity) Validate() error {
	identity = identity.normalized()
	if identity.AccountID == "" || identity.CharacterID == "" || identity.CharacterName == "" || identity.ServerID == "" {
		return fmt.Errorf("%w: incomplete character identity", ErrInvalidRequest)
	}
	for name, value := range map[string]string{
		"account_id": identity.AccountID, "character_id": identity.CharacterID,
		"character_name": identity.CharacterName, "server_id": identity.ServerID,
		"persistent_character_id": identity.PersistentCharacterID,
	} {
		if len([]byte(value)) > 512 {
			return fmt.Errorf("%w: %s is too long", ErrInvalidRequest, name)
		}
	}
	return nil
}

// AttachRequest is sent only after the named-protocol session has entered a
// character. Generation is the Web Gate generation observed by the caller;
// attach is a compare-and-swap from manual ownership to agent ownership.
type AttachRequest struct {
	SessionID  string   `json:"session_id"`
	Generation uint64   `json:"generation"`
	Identity   Identity `json:"identity"`
}

func (request AttachRequest) Validate() error {
	if strings.TrimSpace(request.SessionID) == "" || len([]byte(request.SessionID)) > 256 || strings.ContainsAny(request.SessionID, "/\\\x00\r\n") {
		return fmt.Errorf("%w: session_id is invalid", ErrInvalidRequest)
	}
	if request.Generation == 0 {
		return fmt.Errorf("%w: generation is required", ErrInvalidRequest)
	}
	return request.Identity.Validate()
}

// AttachResponse is the opaque capability returned by Web. Token is never
// included in a durable profile, an MCP binding or a model prompt.
type AttachResponse struct {
	Token      string   `json:"token"`
	Generation uint64   `json:"generation"`
	Identity   Identity `json:"identity"`
}

func (response AttachResponse) Validate() error {
	if strings.TrimSpace(response.Token) == "" || len([]byte(response.Token)) > 4096 {
		return fmt.Errorf("%w: lease token is missing", ErrProtocol)
	}
	if response.Generation == 0 {
		return fmt.Errorf("%w: lease generation is missing", ErrProtocol)
	}
	return response.Identity.Validate()
}

// ObserveResponse carries the private session incarnation in addition to the
// public snapshot. Snapshot.SessionToken intentionally has json:"-" because
// it is local reconciliation metadata; restoring it here keeps the existing
// receipt/mail reconciliation logic intact without exposing it to MCP.
type ObserveResponse struct {
	Snapshot     aigame.Snapshot `json:"snapshot"`
	SessionToken string          `json:"session_token,omitempty"`
}

// ErrorResponse is the bounded machine-readable error returned by the
// private Web agent API. The execute endpoint uses codes so a client can
// distinguish a proven pre-submission rejection from an unknown write
// outcome without parsing an operator-facing message.
type ErrorResponse struct {
	Code string `json:"code"`
}

// ExecuteRequest is the typed write boundary. It contains an aigame.Action,
// never an arbitrary native packet or shell command.
type ExecuteRequest struct {
	Revision uint64        `json:"revision"`
	Action   aigame.Action `json:"action"`
}

func (request ExecuteRequest) Validate() error {
	if request.Revision == 0 {
		return fmt.Errorf("%w: revision is required", ErrInvalidRequest)
	}
	return nil
}
