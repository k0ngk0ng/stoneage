// Package aimcp exposes the small, game-specific MCP surface used by a
// StoneAge agent.  The package deliberately contains no account credentials,
// sockets, shell execution, or provider code.  A caller binds one server to
// one character and supplies a backend which enforces that binding at the
// game-action boundary.
package aimcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// ProtocolVersion is the MCP wire version implemented by this minimal
	// stdio server.  It is intentionally kept stable until the server adds a
	// capability which requires negotiating a newer protocol.
	ProtocolVersion = "2024-11-05"
	ServerName      = "stoneage-game-mcp"
	ServerVersion   = "0.1.0"
)

var (
	ErrInvalidRequest = errors.New("invalid MCP request")
	ErrInvalidParams  = errors.New("invalid MCP tool parameters")
	ErrNotInitialized = errors.New("MCP session is not initialized")
	ErrAlreadyStarted = errors.New("MCP session is already initialized")
	ErrUnknownTool    = errors.New("unknown MCP tool")
	ErrInvalidBinding = errors.New("invalid game character binding")
	ErrInvalidReceipt = errors.New("invalid game operation receipt")
	ErrSkillNotFound  = errors.New("skill is not in the fixed catalog")
	ErrSkillIntegrity = errors.New("skill integrity verification failed")
	ErrSkillConflict  = errors.New("installed skill conflicts with the catalog")
	ErrSkillPath      = errors.New("invalid skill path")
	ErrBackend        = errors.New("game backend unavailable")
)

// Binding is fixed when a Server is created.  It is never decoded from an
// MCP request.  Generation is a fencing token supplied by aicontrol; the
// backend must reject it after a human takeover or another ownership change.
type Binding struct {
	// AccountID is for backend lookup only and is never returned by this MCP
	// server.  It is deliberately not accepted by any tool argument.
	AccountID string `json:"-"`
	// ProfileID is an internal runtime scope. It is never sent over the game
	// gateway and is not accepted by an MCP request; server adapters use it to
	// keep durable schedules and memories isolated from other players.
	ProfileID     string `json:"-"`
	CharacterID   string `json:"-"`
	CharacterName string `json:"-"`
	Generation    uint64 `json:"-"`
}

func (b Binding) Validate() error {
	if strings.TrimSpace(b.CharacterID) == "" || len([]byte(b.CharacterID)) > maxCharacterIDBytes {
		return fmt.Errorf("%w: character identity is required", ErrInvalidBinding)
	}
	if b.Generation == 0 {
		return fmt.Errorf("%w: control generation is required", ErrInvalidBinding)
	}
	if len([]byte(b.CharacterName)) > maxTextBytes {
		return fmt.Errorf("%w: character name is too long", ErrInvalidBinding)
	}
	return nil
}

// Backend is the only game dependency of the MCP server.  Implementations
// normally adapt internal/aigame and the deterministic automation engines.
// Every method receives the immutable binding so it can verify both the
// character identity and the control generation before touching the game.
// No method accepts raw packets, a URL, a credential, or a shell command.
type Backend interface {
	Observe(context.Context, Binding) (Observation, error)
	QueryKnowledge(context.Context, Binding, KnowledgeQuery) (KnowledgeResult, error)
	StartTask(context.Context, Binding, TaskRequest) (TaskReceipt, error)
	StartLeveling(context.Context, Binding, LevelingRequest) (TaskReceipt, error)
	TaskStatus(context.Context, Binding, string) (TaskReceipt, error)
	Cancel(context.Context, Binding, CancelRequest) (TaskReceipt, error)
	GameAction(context.Context, Binding, TypedAction) (ActionReceipt, error)
}

// ScheduleBackend is an optional extension implemented by a server-owned
// backend when the bound AI player may create durable wake-ups. It is kept
// separate from Backend so older game adapters remain source compatible and
// cannot accidentally gain a scheduling capability they do not implement.
// Schedule completion is performed by the trusted runtime after delivery;
// the MCP model only creates, lists and cancels its own profile's entries.
type ScheduleBackend interface {
	CreateSchedule(context.Context, Binding, ScheduleRequest) (Schedule, error)
	ListSchedules(context.Context, Binding, ScheduleListRequest) (ScheduleList, error)
	CancelSchedule(context.Context, Binding, ScheduleCancelRequest) (Schedule, error)
}

// UnavailableBackend is useful for a process that is started before its game
// session is attached.  It keeps the MCP protocol available while ensuring
// that no request can silently mutate a game.
type UnavailableBackend struct{}

func (UnavailableBackend) unavailable() error { return ErrBackend }
func (UnavailableBackend) Observe(context.Context, Binding) (Observation, error) {
	return Observation{}, ErrBackend
}
func (UnavailableBackend) QueryKnowledge(context.Context, Binding, KnowledgeQuery) (KnowledgeResult, error) {
	return KnowledgeResult{}, ErrBackend
}
func (UnavailableBackend) StartTask(context.Context, Binding, TaskRequest) (TaskReceipt, error) {
	return TaskReceipt{}, ErrBackend
}
func (UnavailableBackend) StartLeveling(context.Context, Binding, LevelingRequest) (TaskReceipt, error) {
	return TaskReceipt{}, ErrBackend
}
func (UnavailableBackend) TaskStatus(context.Context, Binding, string) (TaskReceipt, error) {
	return TaskReceipt{}, ErrBackend
}
func (UnavailableBackend) Cancel(context.Context, Binding, CancelRequest) (TaskReceipt, error) {
	return TaskReceipt{}, ErrBackend
}
func (UnavailableBackend) GameAction(context.Context, Binding, TypedAction) (ActionReceipt, error) {
	return ActionReceipt{}, ErrBackend
}

type Entity struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Level   int      `json:"level"`
	HP      int      `json:"hp"`
	MaxHP   int      `json:"max_hp"`
	Alive   bool     `json:"alive"`
	Skills  []string `json:"skills,omitempty"`
	UseFlag int      `json:"use_flag,omitempty"`
}

// Observation contains only server-observed game state useful for planning.
// It intentionally omits account credentials and opaque protocol packets.
// PetSelection keeps an explicitly empty role (slot -1) distinct from unknown
// status and from a selected slot whose pet details have not arrived.
type PetSelection struct {
	// Local continuity is for in-process recovery only, never a durable/model ID.
	LocalIdentity string  `json:"-"`
	LocalEpoch    uint64  `json:"-"`
	Known         bool    `json:"known"`
	Slot          int     `json:"slot"`
	Pet           *Entity `json:"pet,omitempty"`
}

type Observation struct {
	RidingPet PetSelection `json:"riding_pet"`
	BattlePet PetSelection `json:"battle_pet"`
	// Persisted native identity is separate from the account/slot execution binding.
	PersistentCharacterID string             `json:"persistent_character_id,omitempty"`
	Revision              uint64             `json:"revision"`
	CharacterID           string             `json:"character_id"`
	CharacterName         string             `json:"character_name,omitempty"`
	Connected             bool               `json:"connected"`
	Ready                 bool               `json:"ready"`
	Phase                 string             `json:"phase,omitempty"`
	Floor                 int                `json:"floor"`
	X                     int                `json:"x"`
	Y                     int                `json:"y"`
	Character             Entity             `json:"character"`
	Pets                  []Entity           `json:"pets,omitempty"`
	Party                 []PartyMember      `json:"party,omitempty"`
	Actors                []VisibleActor     `json:"actors,omitempty"`
	InventoryItems        []InventoryItem    `json:"inventory_items,omitempty"`
	ActiveWindow          *WindowState       `json:"active_window,omitempty"`
	Windows               []WindowState      `json:"windows,omitempty"`
	Chat                  []ChatMessage      `json:"chat,omitempty"`
	AddressBookKnown      bool               `json:"address_book_known"`
	AddressBook           []AddressBookEntry `json:"address_book,omitempty"`
	// AddressBookRevision advances only after a complete native AB table. It
	// lets clients distinguish a fresh list response from an ABI update while
	// keeping the connection-local session token private to the backend.
	AddressBookRevision uint64          `json:"address_book_revision,omitempty"`
	Battle              BattleState     `json:"battle"`
	Trade               *TradeState     `json:"trade,omitempty"`
	Gold                int64           `json:"gold"`
	UnlimitedFunds      bool            `json:"unlimited_funds"`
	Spent               int64           `json:"spent,omitempty"`
	SpendingKnown       bool            `json:"spending_known"`
	Inventory           map[string]int  `json:"inventory,omitempty"`
	Flags               map[string]bool `json:"flags,omitempty"`
	// Skills and OwnProgress are populated only when the corresponding
	// server-owned status stream has been observed. Numeric skill IDs use
	// their decimal text as keys; progress keys identify end/now event groups.
	Skills      map[string]int `json:"skills,omitempty"`
	OwnProgress map[string]int `json:"own_progress,omitempty"`
}

// TradeState separates locally submitted offers from peer observations.
// Closing the window does not establish that any asset transfer succeeded.
type TradeState struct {
	Active            bool          `json:"active"`
	Pending           bool          `json:"pending"`
	Closed            bool          `json:"closed"`
	Uncertain         bool          `json:"uncertain"`
	Manual            bool          `json:"manual"`
	Phase             string        `json:"phase"`
	PeerID            int32         `json:"peer_id"`
	PeerName          string        `json:"peer_name"`
	OwnOffers         [2]TradeOffer `json:"own_offers"`
	PeerOffers        [2]TradeOffer `json:"peer_offers"`
	OwnPet            *TradeOffer   `json:"own_pet,omitempty"`
	PeerPet           *TradeOffer   `json:"peer_pet,omitempty"`
	OwnLockSubmitted  bool          `json:"own_lock_submitted"`
	OwnFinalSubmitted bool          `json:"own_final_submitted"`
	PeerLocked        bool          `json:"peer_locked"`
	PeerFinal         bool          `json:"peer_final"`
}

type TradeOffer struct {
	Kind           string `json:"kind"`
	ItemIndex      int32  `json:"item_index"`
	PetSlot        int32  `json:"pet_slot"`
	Amount         int32  `json:"amount"`
	Name           string `json:"name,omitempty"`
	Graphic        int32  `json:"graphic,omitempty"`
	Effect         string `json:"effect,omitempty"`
	Damage         string `json:"damage,omitempty"`
	Level          int32  `json:"level,omitempty"`
	Attack         int32  `json:"attack,omitempty"`
	Defense        int32  `json:"defense,omitempty"`
	Quick          int32  `json:"quick,omitempty"`
	Transmigration int32  `json:"transmigration,omitempty"`
	MaxHP          int32  `json:"max_hp,omitempty"`
	Submitted      bool   `json:"submitted"`
	Confirmed      bool   `json:"confirmed"`
}

// VisibleActor and WindowState are restricted to information the bound
// character can observe in the current field/window. They are not a hidden
// world database and contain no account credentials.
type VisibleActor struct {
	PersistentCharacterID string `json:"persistent_character_id,omitempty"`
	ID                    int    `json:"id"`
	Kind                  string `json:"kind,omitempty"`
	CharType              int    `json:"char_type,omitempty"`
	Name                  string `json:"name,omitempty"`
	FreeName              string `json:"free_name,omitempty"`
	Title                 string `json:"title,omitempty"`
	X                     int    `json:"x"`
	Y                     int    `json:"y"`
	Direction             int    `json:"direction,omitempty"`
	Level                 int    `json:"level,omitempty"`
	PetName               string `json:"pet_name,omitempty"`
	PetLevel              int    `json:"pet_level,omitempty"`
}

type WindowState struct {
	Type       int    `json:"type"`
	ButtonType int    `json:"button_type,omitempty"`
	Sequence   int    `json:"sequence"`
	ObjectID   int    `json:"object_id"`
	Data       string `json:"data,omitempty"`
	Open       bool   `json:"open"`
	Submitted  bool   `json:"submitted"`
}

type ChatMessage struct {
	// SpeakerCharacterID is attributed by the server at chat time.
	SpeakerCharacterID string `json:"speaker_character_id,omitempty"`
	// ContextID scopes the observation; it is not a persistent speaker ID.
	ContextID string `json:"context_id,omitempty"`
	At        string `json:"at,omitempty"`
	// Channel is P/TK public chat, msg ordinary game mail, or pmsg pet mail.
	Channel string `json:"channel,omitempty"`
	FromID  int    `json:"from_id,omitempty"`
	Color   int    `json:"color,omitempty"`
	Text    string `json:"text"`
}

// AddressBookEntry is the server-owned recipient directory used by ordinary
// game mail. Index is a reusable slot and is only safe to use while the
// accompanying observation reports AddressBookKnown=true.
type AddressBookEntry struct {
	Index          int    `json:"index"`
	Use            bool   `json:"use"`
	Online         bool   `json:"online"`
	Level          int    `json:"level"`
	DuelPoint      int    `json:"duel_point"`
	Graphic        int    `json:"graphic"`
	Name           string `json:"name,omitempty"`
	Transmigration int    `json:"transmigration"`
}

type PartyMember struct {
	PersistentCharacterID string `json:"persistent_character_id,omitempty"`
	ID                    string `json:"id"`
	Name                  string `json:"name,omitempty"`
	Level                 int    `json:"level,omitempty"`
	HP                    int    `json:"hp,omitempty"`
	MaxHP                 int    `json:"max_hp,omitempty"`
}

type BattleState struct {
	MyNo               int                 `json:"my_no"`
	MyNoKnown          bool                `json:"my_no_known"`
	BPFlags            int                 `json:"bp_flags"`
	PlayerCommandReady bool                `json:"player_command_ready"`
	PetCommandReady    bool                `json:"pet_command_ready"`
	Active             bool                `json:"active"`
	Turn               int                 `json:"turn,omitempty"`
	CommandReady       bool                `json:"command_ready"`
	Result             string              `json:"result,omitempty"`
	Participants       []BattleParticipant `json:"participants,omitempty"`
}

type BattleParticipant struct {
	ID     int    `json:"id"`
	Name   string `json:"name,omitempty"`
	Level  int    `json:"level,omitempty"`
	HP     int    `json:"hp"`
	MaxHP  int    `json:"max_hp"`
	Player bool   `json:"player,omitempty"`
	Dead   bool   `json:"dead,omitempty"`
}

type InventoryItem struct {
	Index   int    `json:"index"`
	Name    string `json:"name,omitempty"`
	Name2   string `json:"name2,omitempty"`
	Memo    string `json:"memo,omitempty"`
	Graphic int    `json:"graphic,omitempty"`
	Count   int    `json:"count,omitempty"`
}

type KnowledgeQuery struct {
	// Kind is one of facts, task, leveling, route, or rule.  The backend
	// resolves it against the versioned local StoneAge knowledge base.
	Kind  string `json:"kind"`
	ID    string `json:"id,omitempty"`
	Text  string `json:"text,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type KnowledgeEntry struct {
	ID       string          `json:"id"`
	Title    string          `json:"title,omitempty"`
	Summary  string          `json:"summary,omitempty"`
	Verified bool            `json:"verified"`
	Source   string          `json:"source,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
}

type KnowledgeResult struct {
	Revision string           `json:"revision"`
	Kind     string           `json:"kind"`
	Entries  []KnowledgeEntry `json:"entries"`
}

type TaskRequest struct {
	TaskID     string                     `json:"task_id"`
	Parameters map[string]json.RawMessage `json:"parameters,omitempty"`
}

type LevelingRequest struct {
	TargetKind     string                     `json:"target_kind"`
	TargetID       string                     `json:"target_id,omitempty"`
	TargetLevel    int                        `json:"target_level"`
	TargetPolicy   string                     `json:"target_policy,omitempty"`
	MaximumSeconds int                        `json:"maximum_seconds,omitempty"`
	MaximumDeaths  int                        `json:"maximum_deaths,omitempty"`
	Parameters     map[string]json.RawMessage `json:"parameters,omitempty"`
}

type CancelRequest struct {
	Handle string `json:"handle"`
	Reason string `json:"reason,omitempty"`
}

// ScheduleRequest contains only a bounded reminder/activity prompt. RunAt is
// an RFC3339 timestamp; DelaySeconds is a convenience for relative timers.
// The server resolves the latter using its trusted clock. No game operation,
// account selector or credential can be embedded in a schedule.
type ScheduleRequest struct {
	Kind           string `json:"kind"`
	Title          string `json:"title,omitempty"`
	Prompt         string `json:"prompt"`
	RunAt          string `json:"run_at,omitempty"`
	DelaySeconds   int64  `json:"delay_seconds,omitempty"`
	RepeatSeconds  int64  `json:"repeat_seconds,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type ScheduleListRequest struct {
	Status string `json:"status,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type ScheduleCancelRequest struct {
	ScheduleID string `json:"schedule_id"`
	Reason     string `json:"reason,omitempty"`
}

// Schedule is the credential-free MCP projection of an AI runtime schedule.
// Claim and profile internals are deliberately absent from this type.
type Schedule struct {
	ID            string          `json:"id"`
	Kind          string          `json:"kind"`
	Title         string          `json:"title,omitempty"`
	Prompt        string          `json:"prompt"`
	RunAt         string          `json:"run_at"`
	RepeatSeconds int64           `json:"repeat_seconds,omitempty"`
	Status        string          `json:"status"`
	Occurrences   int             `json:"occurrences"`
	LastOutcome   json.RawMessage `json:"last_outcome,omitempty"`
}

type ScheduleList struct {
	Schedules []Schedule `json:"schedules"`
}

const (
	maxScheduleKindBytes           = 64
	maxScheduleTitleBytes          = 160
	maxSchedulePromptBytes         = 8 * 1024
	maxScheduleIdempotencyKeyBytes = 128
	maxScheduleHorizonSeconds      = 365 * 24 * 60 * 60
)

func validateScheduleRequest(request ScheduleRequest) error {
	if !validText(request.Kind, maxScheduleKindBytes) || strings.TrimSpace(request.Kind) == "" {
		return ErrInvalidParams
	}
	if !validText(request.Title, maxScheduleTitleBytes) || !validText(request.Prompt, maxSchedulePromptBytes) || strings.TrimSpace(request.Prompt) == "" {
		return ErrInvalidParams
	}
	if request.RunAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, request.RunAt); err != nil {
			return ErrInvalidParams
		}
	}
	if request.RunAt == "" && (request.DelaySeconds < 1 || request.DelaySeconds > maxScheduleHorizonSeconds) {
		return ErrInvalidParams
	}
	if request.RunAt != "" && request.DelaySeconds != 0 {
		return ErrInvalidParams
	}
	if request.RepeatSeconds < 0 || (request.RepeatSeconds > 0 && request.RepeatSeconds < 60) || request.RepeatSeconds > maxScheduleHorizonSeconds {
		return ErrInvalidParams
	}
	if len([]byte(request.IdempotencyKey)) > maxScheduleIdempotencyKeyBytes || !validText(request.IdempotencyKey, maxScheduleIdempotencyKeyBytes) {
		return ErrInvalidParams
	}
	return nil
}

func validateScheduleListRequest(request ScheduleListRequest) error {
	switch request.Status {
	case "", "pending", "delivering", "delivered", "cancelled":
	default:
		return ErrInvalidParams
	}
	if request.Limit == 0 {
		request.Limit = 50
	}
	if request.Limit < 1 || request.Limit > 100 {
		return ErrInvalidParams
	}
	return nil
}

func validateScheduleCancelRequest(request ScheduleCancelRequest) error {
	if err := validateHandle(request.ScheduleID); err != nil {
		return ErrInvalidParams
	}
	if !validText(request.Reason, maxTextBytes) {
		return ErrInvalidParams
	}
	return nil
}

func validateSchedule(schedule Schedule) error {
	if err := validateHandle(schedule.ID); err != nil || !validText(schedule.Kind, maxScheduleKindBytes) ||
		!validText(schedule.Title, maxScheduleTitleBytes) || !validText(schedule.Prompt, maxSchedulePromptBytes) ||
		len([]byte(schedule.RunAt)) > 64 {
		return ErrInvalidParams
	}
	switch schedule.Status {
	case "pending", "delivering", "delivered", "cancelled":
	default:
		return ErrInvalidParams
	}
	if schedule.RepeatSeconds < 0 || schedule.RepeatSeconds > maxScheduleHorizonSeconds || schedule.Occurrences < 0 {
		return ErrInvalidParams
	}
	if schedule.LastOutcome != nil && (len(schedule.LastOutcome) > maxSchedulePromptBytes || !json.Valid(schedule.LastOutcome)) {
		return ErrInvalidParams
	}
	return nil
}

const (
	ReceiptPending   = "pending"
	ReceiptRunning   = "running"
	ReceiptConfirmed = "confirmed"
	ReceiptFailed    = "failed"
	ReceiptCancelled = "cancelled"
	ReceiptUnknown   = "unknown"
)

// TaskReceipt is a server-backed handle.  A pending/running receipt must be
// polled with game_task_status; callers must not infer success from model text.
// Confirmed results are expected to contain compact server evidence.
type TaskReceipt struct {
	Handle   string          `json:"handle"`
	Status   string          `json:"status"`
	State    string          `json:"state,omitempty"`
	Reason   string          `json:"reason,omitempty"`
	Evidence json.RawMessage `json:"evidence,omitempty"`
}

type ActionReceipt = TaskReceipt

// TypedAction is deliberately a closed vocabulary.  It is not a wire packet
// and has no raw fields; the backend translates it to legal game operations.
type TypedAction struct {
	Kind             string `json:"kind"`
	ExpectedRevision uint64 `json:"expected_revision"`
	X                int32  `json:"x,omitempty"`
	Y                int32  `json:"y,omitempty"`
	Direction        int32  `json:"direction,omitempty"`
	Route            string `json:"route,omitempty"`
	TargetID         int32  `json:"target_id,omitempty"`
	Index            int32  `json:"index,omitempty"`
	Value            int32  `json:"value,omitempty"`
	Value2           int32  `json:"value2,omitempty"`
	WindowType       int32  `json:"window_type,omitempty"`
	WindowButton     int32  `json:"window_button,omitempty"`
	WindowSequence   int32  `json:"window_sequence,omitempty"`
	WindowObjectID   int32  `json:"window_object_id,omitempty"`
	WindowSelect     int32  `json:"window_select,omitempty"`
	Command          string `json:"command,omitempty"`
	Text             string `json:"text,omitempty"`
	Color            int32  `json:"color,omitempty"`
	Range            int32  `json:"range,omitempty"`
	PartyRequest     int32  `json:"party_request,omitempty"`
	PetSlot          int32  `json:"pet_slot,omitempty"`
}

const (
	maxCharacterIDBytes = 128
	maxTextBytes        = 1024
	maxRouteBytes       = 512
	maxHandleBytes      = 128
	maxTaskIDBytes      = 128
	maxJSONValueBytes   = 64 * 1024
	maxEvidenceBytes    = 128 * 1024
)

func validReceipt(receipt TaskReceipt) error {
	if len([]byte(receipt.Handle)) > maxHandleBytes {
		return fmt.Errorf("%w: handle is too long", ErrInvalidReceipt)
	}
	switch receipt.Status {
	case ReceiptPending, ReceiptRunning:
		if strings.TrimSpace(receipt.Handle) == "" {
			return fmt.Errorf("%w: pending operation has no handle", ErrInvalidReceipt)
		}
	case ReceiptConfirmed, ReceiptFailed, ReceiptCancelled, ReceiptUnknown:
		if receipt.Status == ReceiptConfirmed && len(bytes.TrimSpace(receipt.Evidence)) == 0 {
			return fmt.Errorf("%w: confirmed operation has no server evidence", ErrInvalidReceipt)
		}
		if receipt.Status == ReceiptUnknown && strings.TrimSpace(receipt.Handle) == "" {
			return fmt.Errorf("%w: unknown operation has no handle", ErrInvalidReceipt)
		}
	default:
		return fmt.Errorf("%w: unsupported receipt status", ErrInvalidReceipt)
	}
	if len(receipt.Evidence) > maxEvidenceBytes {
		return fmt.Errorf("%w: evidence is too large", ErrInvalidReceipt)
	}
	if receipt.Evidence != nil && !json.Valid(receipt.Evidence) {
		return fmt.Errorf("%w: evidence is not JSON", ErrInvalidReceipt)
	}
	if len([]byte(receipt.Reason)) > maxTextBytes {
		return fmt.Errorf("%w: reason is too long", ErrInvalidReceipt)
	}
	return nil
}
