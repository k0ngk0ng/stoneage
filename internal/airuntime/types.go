// Package airuntime contains the persistence and model-facing pieces of the
// StoneAge AI runtime.  It deliberately does not contain a game client,
// shell, or arbitrary network action executor.  A caller supplies the
// game-specific skills that an agent is allowed to invoke.
package airuntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	ProfileStatusActive  = "active"
	ProfileStatusPaused  = "paused"
	ProfileStatusStopped = "stopped"
	ProfileStatusDeleted = "deleted"

	EventProfileCreated    = "profile.created"
	EventProfileUpdated    = "profile.updated"
	EventProfileDeleted    = "profile.deleted"
	EventMemoryConfirmed   = "memory.confirmed"
	EventCheckpointSaved   = "checkpoint.saved"
	EventDecisionStarted   = "decision.started"
	EventDecisionFinished  = "decision.finished"
	EventDecisionFailed    = "decision.failed"
	EventBudgetExceeded    = "budget.exceeded"
	EventGameObservation   = "game.observation"
	EventScheduleCreated   = "schedule.created"
	EventScheduleClaimed   = "schedule.claimed"
	EventScheduleCompleted = "schedule.completed"
	EventScheduleCancelled = "schedule.cancelled"
)

var (
	ErrNotFound            = errors.New("airuntime: not found")
	ErrConflict            = errors.New("airuntime: compare-and-swap conflict")
	ErrInvalidProfile      = errors.New("airuntime: invalid profile")
	ErrInvalidSkill        = errors.New("airuntime: invalid skill")
	ErrInvalidArguments    = errors.New("airuntime: invalid skill arguments")
	ErrInvalidResponse     = errors.New("airuntime: invalid model response")
	ErrInvalidProvider     = errors.New("airuntime: invalid model configuration")
	ErrTokenBudgetExceeded = errors.New("airuntime: daily token budget exceeded")
	ErrAttemptSettled      = errors.New("airuntime: token attempt already settled")
	ErrAttemptConflict     = errors.New("airuntime: token attempt metadata conflicts")
	ErrAttemptPending      = errors.New("airuntime: an unsettled token attempt requires recovery")
	ErrScheduleConflict    = errors.New("airuntime: schedule conflict")
	ErrScheduleExpired     = errors.New("airuntime: schedule claim expired")
	ErrInvalidSchedule     = errors.New("airuntime: invalid schedule")
)

// AccountIdentity and CharacterIdentity are references to game identities.
// They intentionally contain no password field.  Authentication secrets stay
// in the normal game-account/authentication system and never enter an AI
// profile.
type AccountIdentity struct {
	ID       string `json:"id"`
	Username string `json:"username,omitempty"`
}

type CharacterIdentity struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

const (
	// ModelProviderResponses selects the OpenAI Responses wire format.
	ModelProviderResponses = "responses"
)

const ModelBackendCodex = "codex"

const (
	ReasoningEffortLow  = "low"
	ReasoningEffortHigh = "high"
	ReasoningEffortMax  = "max"
)

// ModelConfig contains only public runtime configuration.  API keys are
// intentionally absent; SecretStore resolves a key from a fixed server-side
// directory using the config ID.
type ModelConfig struct {
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	Backend          string        `json:"backend"`
	Provider         string        `json:"provider"`
	BaseURL          string        `json:"base_url"`
	Model            string        `json:"model"`
	WireAPI          string        `json:"wire_api"`
	ReasoningEffort  string        `json:"reasoning_effort"`
	Timeout          time.Duration `json:"timeout"`
	MaxOutputTokens  int           `json:"max_output_tokens"`
	DailyTokenBudget int64         `json:"daily_token_budget"`
	HasKey           bool          `json:"has_key"`
	Version          int64         `json:"version"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

type ModelConfigPatch struct {
	Name             *string        `json:"name,omitempty"`
	Backend          *string        `json:"backend,omitempty"`
	Provider         *string        `json:"provider,omitempty"`
	BaseURL          *string        `json:"base_url,omitempty"`
	Model            *string        `json:"model,omitempty"`
	WireAPI          *string        `json:"wire_api,omitempty"`
	ReasoningEffort  *string        `json:"reasoning_effort,omitempty"`
	Timeout          *time.Duration `json:"timeout,omitempty"`
	MaxOutputTokens  *int           `json:"max_output_tokens,omitempty"`
	DailyTokenBudget *int64         `json:"daily_token_budget,omitempty"`
	HasKey           *bool          `json:"has_key,omitempty"`
	Actor            string         `json:"-"`
}

// Personality is persisted as data and can be rendered into a user prompt by
// the runtime.  Prompt is a persona description, not a permission boundary.
type Personality struct {
	Name   string            `json:"name,omitempty"`
	Prompt string            `json:"prompt,omitempty"`
	Traits []string          `json:"traits,omitempty"`
	Values map[string]string `json:"values,omitempty"`
}

// Goal is intentionally extensible through Metadata.  Game actions still
// have to be supplied by an installed skill and accepted by the game layer.
type Goal struct {
	Kind              string            `json:"kind,omitempty"`
	Description       string            `json:"description,omitempty"`
	TargetLevel       int               `json:"target_level,omitempty"`
	TargetCharacterID string            `json:"target_character_id,omitempty"`
	StopWhenCompleted bool              `json:"stop_when_completed,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	CharacterBuild    *CharacterBuild   `json:"character_build,omitempty"`
	Life              *LifePolicy       `json:"life,omitempty"`
}

const SkillKindNative = "native"

// SkillVersion is metadata for an installed Codex skill.  Codex owns the
// skill definition and execution; this package stores its identity and
// provenance only.  Parameters remains optional for compatibility with old
// catalog entries, but it is opaque JSON and is never required to be a
// function-tool schema here.
type SkillVersion struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description,omitempty"`
	Kind        string          `json:"kind,omitempty"`
	Path        string          `json:"path,omitempty"`
	Digest      string          `json:"digest,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type Profile struct {
	ID                 string            `json:"id"`
	Account            AccountIdentity   `json:"account"`
	Character          CharacterIdentity `json:"character"`
	ModelConfigID      string            `json:"model_config_id,omitempty"`
	Personality        Personality       `json:"personality"`
	Goal               Goal              `json:"goal"`
	Skills             []SkillVersion    `json:"skills"`
	UnlimitedFunds     bool              `json:"unlimited_funds"`
	DailyTokenBudget   int64             `json:"daily_token_budget"`
	ExternalSpendLimit int64             `json:"external_spend_limit"`
	Status             string            `json:"status"`
	Version            int64             `json:"version"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

// ProfilePatch is applied atomically with UpdateProfileCAS.  A nil field is
// left unchanged.  Actor is recorded in the audit event and is not model
// controlled; callers handling admin changes should pass an administrator or
// service identity here.
type ProfilePatch struct {
	Account            *AccountIdentity   `json:"account,omitempty"`
	Character          *CharacterIdentity `json:"character,omitempty"`
	ModelConfigID      *string            `json:"model_config_id,omitempty"`
	Personality        *Personality       `json:"personality,omitempty"`
	Goal               *Goal              `json:"goal,omitempty"`
	Skills             *[]SkillVersion    `json:"skills,omitempty"`
	UnlimitedFunds     *bool              `json:"unlimited_funds,omitempty"`
	DailyTokenBudget   *int64             `json:"daily_token_budget,omitempty"`
	ExternalSpendLimit *int64             `json:"external_spend_limit,omitempty"`
	Status             *string            `json:"status,omitempty"`
	Actor              string             `json:"-"`
}

type Memory struct {
	ID            int64           `json:"id"`
	ProfileID     string          `json:"profile_id"`
	Kind          string          `json:"kind"`
	Subject       string          `json:"subject,omitempty"`
	Content       json.RawMessage `json:"content"`
	SourceEventID int64           `json:"source_event_id"`
	Confirmed     bool            `json:"confirmed"`
	CreatedAt     time.Time       `json:"created_at"`
}

type MemoryInput struct {
	Kind          string
	Subject       string
	Content       json.RawMessage
	SourceEventID int64
	Confirmed     bool
	Actor         string
}

type Checkpoint struct {
	ProfileID string          `json:"profile_id"`
	Version   int64           `json:"version"`
	State     json.RawMessage `json:"state"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type AuditEvent struct {
	ID        int64           `json:"id"`
	ProfileID string          `json:"profile_id"`
	Kind      string          `json:"kind"`
	Actor     string          `json:"actor"`
	Detail    json.RawMessage `json:"detail,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type TokenUsage struct {
	Date               string `json:"date"`
	ChargedTokens      int64  `json:"charged_tokens"`
	ReservedTokens     int64  `json:"reserved_tokens"`
	InputTokens        int64  `json:"input_tokens"`
	OutputTokens       int64  `json:"output_tokens"`
	TotalTokens        int64  `json:"total_tokens"`
	Attempts           int64  `json:"attempts"`
	FailedAttempts     int64  `json:"failed_attempts"`
	OverBudgetAttempts int64  `json:"over_budget_attempts"`
}

// TokenReservation is an internal accounting handle returned by
// BeginTokenAttempt.  It is exported so a runtime implementation can defer
// settlement, but callers should treat it as opaque.
type TokenReservation struct {
	ID        string
	ProfileID string
	Date      string
	Charge    int64
}

// TokenAttemptState is the durable lifecycle of one model turn. A row that
// has not reached settled is never silently reused as a new turn: after a
// process restart the supervisor either consumes a recorded outcome or
// marks the attempt unknown.
type TokenAttemptState string

const (
	TokenAttemptReserved   TokenAttemptState = "reserved"
	TokenAttemptPrepared   TokenAttemptState = "prepared"
	TokenAttemptDispatched TokenAttemptState = "dispatched"
	TokenAttemptRecorded   TokenAttemptState = "recorded"
	TokenAttemptUnknown    TokenAttemptState = "unknown"
	TokenAttemptSettled    TokenAttemptState = "settled"
)

// TokenAttemptMetadata is the exact request identity needed to reconcile a
// model turn after a supervisor restart. Prompt is server-built input and is
// persisted in the private AI database; credentials are never part of this
// type.
type TokenAttemptMetadata struct {
	Prompt   string
	Resume   bool
	ThreadID string
}

// TokenAttemptOutcome is a bounded, credential-free projection of a runner
// result. It deliberately excludes events, stderr and arbitrary provider
// diagnostics while retaining enough information to apply a completed turn
// after a crash between result delivery and token settlement/checkpointing.
type TokenAttemptOutcome struct {
	ThreadID        string
	LastMessage     string
	TurnID          string
	TurnStatus      string
	TurnError       string
	ProcessStatus   string
	ProcessExitCode int
	ProcessSignal   string
	CheckpointState string
	TurnCompleted   bool
	Usage           TokenUsage
	Error           string
}

// TokenAttempt is the durable state of one token reservation. Result is nil
// until RecordTokenAttemptResult has committed. Prompt and resume/thread
// metadata remain available for an explicit recovery or audit decision.
type TokenAttempt struct {
	TokenReservation
	State     TokenAttemptState
	Prompt    string
	Resume    bool
	ThreadID  string
	Outcome   *TokenAttemptOutcome
	Failed    bool
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
	SettledAt time.Time
}

// SkillInvocation is the only action a model decision can produce.  The
// runtime never executes it; a game executor must look up the installed skill
// and apply its own authorization and state checks.
type SkillInvocation struct {
	Skill     string          `json:"skill"`
	Arguments json.RawMessage `json:"arguments"`
	Reason    string          `json:"reason,omitempty"`
}

var skillNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func validateStatus(status string) error {
	switch status {
	case ProfileStatusActive, ProfileStatusPaused, ProfileStatusStopped, ProfileStatusDeleted:
		return nil
	default:
		return fmt.Errorf("%w: unknown status %q", ErrInvalidProfile, status)
	}
}

func validateProfile(profile Profile) error {
	if err := profile.Goal.ValidateLife(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	if err := profile.Goal.CharacterBuild.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	if strings.TrimSpace(profile.ID) == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidProfile)
	}
	if strings.TrimSpace(profile.Account.ID) == "" {
		return fmt.Errorf("%w: account id is required", ErrInvalidProfile)
	}
	if strings.TrimSpace(profile.Character.ID) == "" {
		return fmt.Errorf("%w: character id is required", ErrInvalidProfile)
	}
	if profile.DailyTokenBudget < 0 {
		return fmt.Errorf("%w: daily token budget must be non-negative", ErrInvalidProfile)
	}
	if profile.ExternalSpendLimit < 0 {
		return fmt.Errorf("%w: external spend limit must be non-negative", ErrInvalidProfile)
	}
	if profile.Status == "" {
		profile.Status = ProfileStatusStopped
	}
	if err := validateStatus(profile.Status); err != nil {
		return err
	}
	if err := validateSkills(profile.Skills); err != nil {
		return err
	}
	return nil
}

func validateSkills(skills []SkillVersion) error {
	seen := make(map[string]struct{}, len(skills))
	for _, skill := range skills {
		if !skillNamePattern.MatchString(skill.Name) {
			return fmt.Errorf("%w: name %q must match %s", ErrInvalidSkill, skill.Name, skillNamePattern.String())
		}
		if strings.TrimSpace(skill.Version) == "" {
			return fmt.Errorf("%w: %s has no version", ErrInvalidSkill, skill.Name)
		}
		if skill.Kind != "" && skill.Kind != SkillKindNative {
			return fmt.Errorf("%w: %s has unsupported kind %q", ErrInvalidSkill, skill.Name, skill.Kind)
		}
		if len(bytesTrim(skill.Parameters)) > 0 && !json.Valid(skill.Parameters) {
			return fmt.Errorf("%w: %s parameters are not JSON", ErrInvalidSkill, skill.Name)
		}
		if _, ok := seen[skill.Name]; ok {
			return fmt.Errorf("%w: duplicate skill %q", ErrInvalidSkill, skill.Name)
		}
		seen[skill.Name] = struct{}{}
	}
	return nil
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}

func cloneSkills(skills []SkillVersion) []SkillVersion {
	if skills == nil {
		return nil
	}
	result := make([]SkillVersion, len(skills))
	copy(result, skills)
	for i := range result {
		if result[i].Kind == "" {
			result[i].Kind = SkillKindNative
		}
		result[i].Parameters = cloneJSON(result[i].Parameters)
	}
	return result
}
