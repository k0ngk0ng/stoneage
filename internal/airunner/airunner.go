// Package airunner is the process boundary for one isolated StoneAge AI
// runtime.  It accepts a small, server-shaped execution request, materializes
// Codex state below one profile-owned directory, and delegates the model turn
// to the official Codex CLI through internal/aicodex.
//
// The package deliberately does not accept executable paths, filesystem paths,
// approval policies, sandbox policies, or a model wire API from a request.
// Those values belong to Config, which is constructed by the server/container
// launcher.  A future HTTP or Docker broker can use Execute and the DTOs here
// without reimplementing the Codex loop.
package airunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aimodels"
	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
)

const (
	// MaxRequestBytes bounds the complete stdin JSON document.  The bound is
	// intentionally independent of aicodex's prompt limit so a broker cannot
	// hide unbounded metadata beside a prompt.
	MaxRequestBytes = 1 << 20

	maxRequestIDBytes = 128
	maxModelKeyBytes  = 4096
	maxSkillCount     = 64
	maxSkillNameBytes = 128
	maxSkillVerBytes  = 128
	maxDigestBytes    = 128
	maxOwnerBytes     = 4096

	ownerFileName = ".stoneage-ai-owner.json"
	tokenFileName = "game-capability.token"
)

var (
	ErrInvalidConfig   = errors.New("airunner: invalid configuration")
	ErrInvalidRequest  = errors.New("airunner: invalid request")
	ErrRequestTooLarge = errors.New("airunner: request is too large")
	ErrProfileMismatch = errors.New("airunner: request profile does not match this runtime")
	ErrOwnerMismatch   = errors.New("airunner: profile volume owner does not match")
	ErrOwnerCorrupt    = errors.New("airunner: profile volume owner marker is corrupt")
	ErrExecution       = errors.New("airunner: execution failed")
	ErrOutput          = errors.New("airunner: response output failed")
	ErrSkillMismatch   = errors.New("airunner: skill is not allowlisted")
	ErrMCPConfig       = errors.New("airunner: invalid game capability")
	ErrModelConfig     = errors.New("airunner: invalid model configuration")
	ErrProfileBusy     = aicodex.ErrProfileBusy
)

var (
	profileIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	providerPattern  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
)

// Config contains only fixed, server-owned values.  StateRoot is the root of
// one profile's persistent volume.  A launcher must give each profile a
// separate volume and set ProfileID to the identity bound to that container.
// No field in ExecuteRequest can replace any of these paths or binaries.
type Config struct {
	ProfileID   string
	StateRoot   string
	CodexBinary string
	MCPBinary   string
	SkillRoot   string
	GitBinary   string
	Environment map[string]string

	Limits           aicodex.Limits
	TerminationGrace time.Duration
	Clock            func() time.Time
}

// RunRequest is the request-shaped subset of aicodex.RunRequest.  The profile
// ID is supplied by ExecuteRequest and cannot be smuggled into this object.
// Resume always carries an exact thread ID; the executor never uses Codex's
// --last selector.
type RunRequest struct {
	Prompt   string `json:"prompt"`
	Resume   bool   `json:"resume,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
}

// Model is the only model material passed into a profile runtime. APIKey is
// accepted on the private stdin boundary and is never copied into a response.
// The wire API is fixed to the Responses protocol by Execute.
type Model struct {
	Provider        string `json:"provider"`
	BaseURL         string `json:"base_url"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	ContextWindow   int    `json:"context_window,omitempty"`
	APIKey          string `json:"api_key"`
}

// Skill is deliberately smaller than airuntime.SkillVersion: paths,
// parameters, and arbitrary skill metadata are not accepted at this process
// boundary. Name/version/digest are checked against aimcp's built-in catalog.
type Skill struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

// MCP contains the fixed game capability for this profile. The token is
// written to a private file and only its path enters the Codex MCP config.
type MCP struct {
	Endpoint      string `json:"endpoint"`
	Token         string `json:"token"`
	CharacterID   string `json:"character_id"`
	CharacterName string `json:"character_name,omitempty"`
	Generation    uint64 `json:"generation"`
}

// ExecuteRequest is the finite DTO intended for a future HTTP/Docker broker.
// The broker should obtain it from the authenticated admin/control plane; a
// model prompt cannot manufacture a second profile or path.
type ExecuteRequest struct {
	ProfileID string `json:"profile_id"`
	RequestID string `json:"request_id"`
	// ReviewedRequestID proves that an operator explicitly reviewed the
	// earlier unknown broker request. The first request using it starts a
	// fresh game conversation; later exact-thread resumes carry the same ID.
	// The executor uses it to select an isolated Codex checkpoint root while
	// keeping the profile's workspace, Codex home and game capability unchanged.
	ReviewedRequestID string `json:"reviewed_request_id,omitempty"`
	// Probe selects the model-only connection-test path. Probe requests are
	// accepted only by the server-side broker and must not carry a game
	// capability or skills. Keeping this on the existing finite DTO lets the
	// container retain one audited stdin protocol without making the probe
	// impersonate a game session.
	Probe  bool       `json:"probe,omitempty"`
	Run    RunRequest `json:"run_request"`
	Model  Model      `json:"model"`
	Skills []Skill    `json:"skills,omitempty"`
	MCP    MCP        `json:"mcp"`
}

// Usage is a JSON-safe copy of the bounded Codex token counters.
type Usage = aicodex.Usage

type Event struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id,omitempty"`
	TurnID   string          `json:"turn_id,omitempty"`
	ItemType string          `json:"item_type,omitempty"`
	Item     json.RawMessage `json:"item,omitempty"`
	Usage    Usage           `json:"usage,omitempty"`
	HasUsage bool            `json:"has_usage,omitempty"`
	Error    string          `json:"error,omitempty"`
	Raw      json.RawMessage `json:"raw,omitempty"`
}

type Turn struct {
	Status string `json:"status"`
	ID     string `json:"id,omitempty"`
	Usage  Usage  `json:"usage,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Process struct {
	Status   string `json:"status"`
	ExitCode int    `json:"exit_code"`
	Signal   string `json:"signal,omitempty"`
}

type Checkpoint struct {
	Version       int       `json:"version"`
	ProfileID     string    `json:"profile_id"`
	ThreadID      string    `json:"thread_id,omitempty"`
	State         string    `json:"state"`
	TurnStatus    string    `json:"turn_status"`
	LastTurnID    string    `json:"last_turn_id,omitempty"`
	LastEventType string    `json:"last_event_type,omitempty"`
	TurnCompleted bool      `json:"turn_completed"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Result is a redacted, JSON-safe projection of aicodex.Result.  It omits
// request credentials and does not expose the underlying error string.
type Result struct {
	ProfileID   string     `json:"profile_id"`
	ThreadID    string     `json:"thread_id,omitempty"`
	Events      []Event    `json:"events,omitempty"`
	LastMessage string     `json:"last_message,omitempty"`
	Usage       Usage      `json:"usage,omitempty"`
	Turn        Turn       `json:"turn"`
	Process     Process    `json:"process"`
	Stderr      string     `json:"stderr,omitempty"`
	Checkpoint  Checkpoint `json:"checkpoint"`
}

// Response is the only value the command-line entry point writes to stdout.
// Error is a stable category, never an arbitrary wrapped Go diagnostic.
type Response struct {
	OK        bool    `json:"ok"`
	ProfileID string  `json:"profile_id,omitempty"`
	RequestID string  `json:"request_id,omitempty"`
	Error     string  `json:"error,omitempty"`
	Result    *Result `json:"result,omitempty"`
}

// Executor owns one profile volume. It serializes materialization and Codex
// execution so a model update cannot race a live turn. aicodex also keeps its
// process-wide profile lock, covering other Runner instances in this process.
type Executor struct {
	cfg       Config
	guard     runtimepath.Guard
	profiles  profilePaths
	installer *aimcp.SkillInstaller
	mu        sync.Mutex
}

type profilePaths struct {
	root      string
	stateRoot string
	stateDir  string
	workRoot  string
	workspace string
	codexHome string
	token     string
	owner     string
}

type ownerMarker struct {
	Version     int       `json:"version"`
	Owner       string    `json:"owner"`
	ProfileID   string    `json:"profile_id"`
	CharacterID string    `json:"character_id"`
	CreatedAt   time.Time `json:"created_at"`
}

// New validates fixed paths and does not read any model key or start Codex.
// StateRoot is created lazily by Execute so configuration errors do not touch
// a persistent volume.
func New(config Config) (*Executor, error) {
	config.ProfileID = strings.TrimSpace(config.ProfileID)
	config.StateRoot = filepath.Clean(strings.TrimSpace(config.StateRoot))
	config.CodexBinary = filepath.Clean(strings.TrimSpace(config.CodexBinary))
	config.MCPBinary = filepath.Clean(strings.TrimSpace(config.MCPBinary))
	config.SkillRoot = filepath.Clean(strings.TrimSpace(config.SkillRoot))
	config.GitBinary = strings.TrimSpace(config.GitBinary)
	if config.GitBinary != "" {
		config.GitBinary = filepath.Clean(config.GitBinary)
	}
	if !profileIDPattern.MatchString(config.ProfileID) {
		return nil, fmt.Errorf("%w: ProfileID is invalid", ErrInvalidConfig)
	}
	if err := validatePrivateRootPath(config.StateRoot, "StateRoot"); err != nil {
		return nil, err
	}
	if err := validateExecutable(config.CodexBinary, "CodexBinary"); err != nil {
		return nil, err
	}
	if err := validateExecutable(config.MCPBinary, "MCPBinary"); err != nil {
		return nil, err
	}
	if config.GitBinary != "" {
		if err := validateExecutable(config.GitBinary, "GitBinary"); err != nil {
			return nil, err
		}
	}
	installer, err := aimcp.NewSkillInstaller(config.SkillRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: SkillRoot: %v", ErrInvalidConfig, err)
	}
	guard, err := runtimepath.NewGuard()
	if err != nil {
		return nil, fmt.Errorf("%w: initialize runtime path guard", ErrInvalidConfig)
	}
	if err := guard.Check(config.StateRoot); err != nil {
		return nil, fmt.Errorf("%w: StateRoot is not isolated", ErrInvalidConfig)
	}
	if config.TerminationGrace <= 0 {
		config.TerminationGrace = 2 * time.Second
	}
	if config.TerminationGrace > 30*time.Second {
		return nil, fmt.Errorf("%w: TerminationGrace is too long", ErrInvalidConfig)
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	config.Environment = cloneEnvironment(config.Environment)
	paths := makeProfilePaths(config.StateRoot, config.ProfileID)
	return &Executor{cfg: config, guard: guard, profiles: paths, installer: installer}, nil
}

func makeProfilePaths(root, profileID string) profilePaths {
	stateRoot := filepath.Join(root, "state")
	return profilePaths{
		root: root, stateRoot: stateRoot, stateDir: filepath.Join(stateRoot, profileID),
		workRoot: filepath.Join(root, "workspaces"), workspace: filepath.Join(root, "workspaces", profileID),
		codexHome: filepath.Join(root, "codex"), token: filepath.Join(stateRoot, profileID, tokenFileName),
		owner: filepath.Join(root, ownerFileName),
	}
}

// Config returns a copy without any request credential. Environment is cloned
// so a broker cannot mutate a live executor through a returned map.
func (executor *Executor) Config() Config {
	if executor == nil {
		return Config{}
	}
	config := executor.cfg
	config.Environment = cloneEnvironment(config.Environment)
	return config
}

// Execute provisions the profile and runs exactly one official Codex turn.
// Codex/model errors are returned alongside a populated redacted Result so a
// broker can persist both statuses. No raw diagnostic is needed at this API.
func (executor *Executor) Execute(ctx context.Context, request ExecuteRequest) (Response, error) {
	if executor == nil {
		return Response{Error: ErrorCode(ErrInvalidConfig)}, ErrInvalidConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request = normalizeRequest(request)
	if err := executor.validateRequest(request); err != nil {
		return responseForRequest(request, err), err
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return responseForRequest(request, err), err
	}
	if request.Probe {
		return executor.executeProbeLocked(ctx, request)
	}
	paths, err := executor.executionPaths(request)
	if err != nil {
		return responseForRequest(request, err), err
	}
	if err := executor.ensureProfile(request, paths); err != nil {
		return responseForRequest(request, err), err
	}
	if err := executor.installSkills(request); err != nil {
		return responseForRequest(request, err), err
	}
	if err := writePrivateToken(paths.token, request.MCP.Token); err != nil {
		return responseForRequest(request, err), err
	}
	runtimeFiles, err := aimodels.Materialize(paths.codexHome, aimodels.RuntimeSettings{
		Model: request.Model.Model, Provider: request.Model.Provider, BaseURL: request.Model.BaseURL,
		ReasoningEffort: request.Model.ReasoningEffort, ContextWindow: request.Model.ContextWindow,
		APIKey: request.Model.APIKey, MCPCommand: executor.cfg.MCPBinary, MCPArgs: []string{"serve"},
		MCPEnv: map[string]string{
			"STONEAGE_AI_ENDPOINT":           request.MCP.Endpoint,
			"STONEAGE_AI_TOKEN_FILE":         paths.token,
			"STONEAGE_AI_CHARACTER_ID":       request.MCP.CharacterID,
			"STONEAGE_AI_CHARACTER_NAME":     request.MCP.CharacterName,
			"STONEAGE_AI_CONTROL_GENERATION": fmt.Sprintf("%d", request.MCP.Generation),
		},
	})
	if err != nil {
		return responseForRequest(request, fmt.Errorf("%w: materialize model", ErrModelConfig)), fmt.Errorf("%w: materialize model", ErrModelConfig)
	}
	runner, err := aicodex.New(aicodex.Config{
		Binary: executor.cfg.CodexBinary, WorkspaceRoot: paths.workRoot, StateRoot: paths.stateRoot,
		CodexHome: paths.codexHome, ConfigFile: runtimeFiles.ConfigPath,
		Model:        request.Model.Model,
		Provider:     aicodex.ProviderConfig{Name: runtimeFiles.ProviderID, BaseURL: request.Model.BaseURL, WireAPI: "responses"},
		ModelCatalog: runtimeFiles.CatalogPath, ReasoningEffort: request.Model.ReasoningEffort,
		WebSearch: "disabled", GitBinary: executor.cfg.GitBinary,
		Environment: executor.cfg.Environment, TerminationGrace: executor.cfg.TerminationGrace,
		SecretEnv: "", Limits: executor.cfg.Limits, Clock: executor.cfg.Clock,
	})
	if err != nil {
		return responseForRequest(request, fmt.Errorf("%w: create Codex runner", ErrExecution)), fmt.Errorf("%w: create Codex runner", ErrExecution)
	}
	result, runErr := runner.Run(ctx, aicodex.RunRequest{
		ProfileID: request.ProfileID, Prompt: request.Run.Prompt, Resume: request.Run.Resume, ThreadID: request.Run.ThreadID,
	})
	response := Response{OK: runErr == nil, ProfileID: request.ProfileID, RequestID: request.RequestID,
		Result: redactResult(result, request.Model.APIKey, request.MCP.Token)}
	if runErr != nil {
		response.Error = ErrorCode(runErr)
	}
	return response, runErr
}

// executeProbeLocked executes one model-only request in the same isolated
// runtime boundary as a game turn. It deliberately skips the owner marker,
// game-capability token, skill installation and MCP configuration. The
// caller must hold executor.mu; the probe volume is disposable and is owned
// by the container broker rather than by a game profile.
func (executor *Executor) executeProbeLocked(ctx context.Context, request ExecuteRequest) (Response, error) {
	if err := executor.ensureProbeProfile(); err != nil {
		return responseForRequest(request, err), err
	}
	paths := executor.profiles
	runtimeFiles, err := aimodels.Materialize(paths.codexHome, aimodels.RuntimeSettings{
		Model: request.Model.Model, Provider: request.Model.Provider, BaseURL: request.Model.BaseURL,
		ReasoningEffort: request.Model.ReasoningEffort, ContextWindow: request.Model.ContextWindow,
		APIKey: request.Model.APIKey,
	})
	if err != nil {
		wrapped := fmt.Errorf("%w: materialize model", ErrModelConfig)
		return responseForRequest(request, wrapped), wrapped
	}
	runner, err := aicodex.New(aicodex.Config{
		Binary: executor.cfg.CodexBinary, WorkspaceRoot: paths.workRoot, StateRoot: paths.stateRoot,
		CodexHome: paths.codexHome, ConfigFile: runtimeFiles.ConfigPath,
		Model:        request.Model.Model,
		Provider:     aicodex.ProviderConfig{Name: runtimeFiles.ProviderID, BaseURL: request.Model.BaseURL, WireAPI: "responses"},
		ModelCatalog: runtimeFiles.CatalogPath, ReasoningEffort: request.Model.ReasoningEffort,
		WebSearch: "disabled", GitBinary: executor.cfg.GitBinary,
		Environment: executor.cfg.Environment, TerminationGrace: executor.cfg.TerminationGrace,
		SecretEnv: "", Limits: executor.cfg.Limits, Clock: executor.cfg.Clock,
	})
	if err != nil {
		wrapped := fmt.Errorf("%w: create Codex runner", ErrExecution)
		return responseForRequest(request, wrapped), wrapped
	}
	result, runErr := runner.Run(ctx, aicodex.RunRequest{ProfileID: request.ProfileID, Prompt: request.Run.Prompt})
	response := Response{OK: runErr == nil, ProfileID: request.ProfileID, RequestID: request.RequestID,
		Result: redactResult(result, request.Model.APIKey)}
	if runErr != nil {
		response.Error = ErrorCode(runErr)
	}
	return response, runErr
}

// ExecuteJSON decodes one bounded stdin document and executes it. It does not
// write output; callers can encode the returned Response exactly once.
func (executor *Executor) ExecuteJSON(ctx context.Context, reader io.Reader) (Response, error) {
	request, err := DecodeRequest(reader)
	if err != nil {
		return Response{Error: ErrorCode(err)}, err
	}
	return executor.Execute(ctx, request)
}

// DecodeRequest reads at most MaxRequestBytes+1 bytes, rejects duplicate keys,
// unknown fields, trailing JSON values, and malformed nested objects.
func DecodeRequest(reader io.Reader) (ExecuteRequest, error) {
	if reader == nil {
		return ExecuteRequest{}, fmt.Errorf("%w: input is nil", ErrInvalidRequest)
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxRequestBytes+1))
	if err != nil {
		return ExecuteRequest{}, fmt.Errorf("%w: cannot read input", ErrInvalidRequest)
	}
	if len(data) > MaxRequestBytes {
		return ExecuteRequest{}, ErrRequestTooLarge
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return ExecuteRequest{}, fmt.Errorf("%w: input is empty", ErrInvalidRequest)
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return ExecuteRequest{}, fmt.Errorf("%w: malformed JSON", ErrInvalidRequest)
	}
	var request ExecuteRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return ExecuteRequest{}, fmt.Errorf("%w: malformed JSON", ErrInvalidRequest)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ExecuteRequest{}, fmt.Errorf("%w: trailing JSON", ErrInvalidRequest)
	}
	// Keep the session selector unambiguous at the stdin boundary. The
	// executor repeats this check after normalizing the request, but rejecting
	// it here ensures a broker cannot accidentally enqueue a resume without an
	// exact checkpoint thread.
	if request.Run.Resume && strings.TrimSpace(request.Run.ThreadID) == "" {
		return ExecuteRequest{}, fmt.Errorf("%w: resume requires exact thread_id", ErrInvalidRequest)
	}
	if !request.Run.Resume && request.Run.ThreadID != "" {
		return ExecuteRequest{}, fmt.Errorf("%w: thread_id requires resume", ErrInvalidRequest)
	}
	if hasControl(request.Run.ThreadID) {
		return ExecuteRequest{}, fmt.Errorf("%w: thread_id contains control characters", ErrInvalidRequest)
	}
	return request, nil
}

// WriteResponse emits one JSON object. It is intentionally separate from the
// CLI so an HTTP/Docker broker can use the same finite output DTO.
func WriteResponse(writer io.Writer, response Response) error {
	if writer == nil {
		return ErrOutput
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(response); err != nil {
		return fmt.Errorf("%w: encode response", ErrOutput)
	}
	return nil
}

// ErrorCode converts internal errors to a stable, secret-free wire category.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrRequestTooLarge):
		return "request_too_large"
	case errors.Is(err, ErrInvalidConfig):
		return "invalid_config"
	case errors.Is(err, ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, ErrProfileMismatch):
		return "profile_mismatch"
	case errors.Is(err, ErrOwnerMismatch), errors.Is(err, ErrOwnerCorrupt):
		return "owner_mismatch"
	case errors.Is(err, ErrSkillMismatch), errors.Is(err, aimcp.ErrSkillNotFound), errors.Is(err, aimcp.ErrSkillIntegrity), errors.Is(err, aimcp.ErrSkillConflict):
		return "skill_rejected"
	case errors.Is(err, ErrMCPConfig), errors.Is(err, aimcp.ErrBackend):
		return "game_capability_invalid"
	case errors.Is(err, ErrModelConfig):
		return "model_config_invalid"
	case errors.Is(err, aicodex.ErrProfileBusy), errors.Is(err, ErrProfileBusy):
		return "profile_busy"
	case errors.Is(err, aicodex.ErrCheckpointMissing):
		return "checkpoint_missing"
	case errors.Is(err, aicodex.ErrCheckpointRecovery):
		return "checkpoint_recovery_required"
	case errors.Is(err, aicodex.ErrThreadMismatch):
		return "thread_mismatch"
	case errors.Is(err, aicodex.ErrCheckpointCorrupt):
		return "checkpoint_corrupt"
	case errors.Is(err, aicodex.ErrTurnIncomplete):
		return "turn_incomplete"
	case errors.Is(err, aicodex.ErrTurnFailed):
		return "turn_failed"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "execution_failed"
	}
}

func responseForRequest(request ExecuteRequest, err error) Response {
	return Response{ProfileID: safeID(request.ProfileID), RequestID: safeID(request.RequestID), Error: ErrorCode(err)}
}

func safeID(value string) string {
	if profileIDPattern.MatchString(value) || requestIDPattern.MatchString(value) {
		return value
	}
	return ""
}

func (executor *Executor) validateRequest(request ExecuteRequest) error {
	if !profileIDPattern.MatchString(request.ProfileID) {
		return fmt.Errorf("%w: profile_id is invalid", ErrInvalidRequest)
	}
	if request.ProfileID != executor.cfg.ProfileID {
		return ErrProfileMismatch
	}
	if !requestIDPattern.MatchString(request.RequestID) || len([]byte(request.RequestID)) > maxRequestIDBytes {
		return fmt.Errorf("%w: request_id is invalid", ErrInvalidRequest)
	}
	if request.ReviewedRequestID != "" {
		if !requestIDPattern.MatchString(request.ReviewedRequestID) || len([]byte(request.ReviewedRequestID)) > maxRequestIDBytes {
			return fmt.Errorf("%w: reviewed_request_id is invalid", ErrInvalidRequest)
		}
		if request.ReviewedRequestID == request.RequestID {
			return fmt.Errorf("%w: reviewed_request_id must identify an earlier request", ErrInvalidRequest)
		}
	}
	if request.Run.Resume && strings.TrimSpace(request.Run.ThreadID) == "" {
		return fmt.Errorf("%w: resume requires exact thread_id", ErrInvalidRequest)
	}
	if !request.Run.Resume && request.Run.ThreadID != "" {
		return fmt.Errorf("%w: thread_id requires resume", ErrInvalidRequest)
	}
	if hasControl(request.Run.ThreadID) {
		return fmt.Errorf("%w: thread_id contains control characters", ErrInvalidRequest)
	}
	if len([]byte(request.Run.ThreadID)) > 256 {
		return fmt.Errorf("%w: thread_id is too long", ErrInvalidRequest)
	}
	if len([]byte(request.Model.APIKey)) == 0 || len([]byte(request.Model.APIKey)) > maxModelKeyBytes || hasControl(request.Model.APIKey) {
		return ErrModelConfig
	}
	if !providerPattern.MatchString(strings.TrimSpace(request.Model.Provider)) {
		return ErrModelConfig
	}
	if strings.TrimSpace(request.Model.Model) == "" || len([]byte(request.Model.Model)) > 256 || hasControl(request.Model.Model) {
		return ErrModelConfig
	}
	if request.Model.ContextWindow < 0 || request.Model.ContextWindow > 16*1024*1024 {
		return ErrModelConfig
	}
	if hasControl(request.Model.ReasoningEffort) || len([]byte(request.Model.ReasoningEffort)) > 64 {
		return ErrModelConfig
	}
	if err := validateHTTPURL(request.Model.BaseURL); err != nil {
		return fmt.Errorf("%w: model base URL", ErrModelConfig)
	}
	if request.Probe {
		// A model probe has no game identity or tool surface. Reject rather
		// than silently ignore either field so a caller cannot accidentally
		// turn a connection check into a capability-bearing request.
		if request.Run.Resume || request.Run.ThreadID != "" || len(request.Skills) != 0 || request.MCP != (MCP{}) || request.ReviewedRequestID != "" {
			return fmt.Errorf("%w: model probe cannot carry game state", ErrInvalidRequest)
		}
	} else {
		if len(request.Skills) > maxSkillCount {
			return fmt.Errorf("%w: too many skills", ErrInvalidRequest)
		}
		seenSkills := make(map[string]struct{}, len(request.Skills))
		for _, skill := range request.Skills {
			name := strings.TrimSpace(skill.Name)
			if name == "" || len([]byte(name)) > maxSkillNameBytes || hasControl(name) {
				return ErrSkillMismatch
			}
			if _, exists := seenSkills[name]; exists {
				return ErrSkillMismatch
			}
			seenSkills[name] = struct{}{}
			if len([]byte(skill.Version)) > maxSkillVerBytes || hasControl(skill.Version) || len([]byte(skill.Digest)) > maxDigestBytes || hasControl(skill.Digest) {
				return ErrSkillMismatch
			}
		}
		if err := validateMCP(request.MCP); err != nil {
			return err
		}
	}
	promptLimit := executor.cfg.Limits.MaxPromptBytes
	if promptLimit <= 0 {
		promptLimit = 512 << 10
	}
	if len([]byte(request.Run.Prompt)) > promptLimit {
		return fmt.Errorf("%w: prompt is too large", ErrInvalidRequest)
	}
	return nil
}

func validateMCP(mcp MCP) error {
	if strings.TrimSpace(mcp.Endpoint) == "" || !validToken(mcp.Token) {
		return ErrMCPConfig
	}
	// Character identity is copied into the generated MCP process
	// configuration. Reject control characters before it reaches TOML or the
	// child environment, even though aimcp's binding validator only needs a
	// bounded non-empty identifier.
	if hasControl(mcp.CharacterID) || len([]byte(mcp.CharacterID)) > 128 {
		return ErrMCPConfig
	}
	if _, err := aimcp.NewRemoteBackend(aimcp.RemoteBackendConfig{Endpoint: strings.TrimSpace(mcp.Endpoint), Token: strings.TrimSpace(mcp.Token)}); err != nil {
		return ErrMCPConfig
	}
	if err := (aimcp.Binding{CharacterID: strings.TrimSpace(mcp.CharacterID), CharacterName: mcp.CharacterName, Generation: mcp.Generation}).Validate(); err != nil {
		return ErrMCPConfig
	}
	if hasControl(mcp.CharacterName) || len([]byte(mcp.CharacterName)) > 4096 {
		return ErrMCPConfig
	}
	return nil
}

func validToken(token string) bool {
	token = strings.TrimSpace(token)
	if len(token) != 43 {
		return false
	}
	for _, character := range token {
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func normalizeRequest(request ExecuteRequest) ExecuteRequest {
	request.ReviewedRequestID = strings.TrimSpace(request.ReviewedRequestID)
	request.Model.Provider = strings.TrimSpace(request.Model.Provider)
	request.Model.BaseURL = strings.TrimSpace(request.Model.BaseURL)
	request.Model.Model = strings.TrimSpace(request.Model.Model)
	request.Model.ReasoningEffort = strings.TrimSpace(request.Model.ReasoningEffort)
	request.Model.APIKey = strings.TrimSpace(request.Model.APIKey)
	request.MCP.Endpoint = strings.TrimSpace(request.MCP.Endpoint)
	request.MCP.Token = strings.TrimSpace(request.MCP.Token)
	request.MCP.CharacterID = strings.TrimSpace(request.MCP.CharacterID)
	request.MCP.CharacterName = strings.TrimSpace(request.MCP.CharacterName)
	for index := range request.Skills {
		request.Skills[index].Name = strings.TrimSpace(request.Skills[index].Name)
		request.Skills[index].Version = strings.TrimSpace(request.Skills[index].Version)
		request.Skills[index].Digest = strings.TrimSpace(request.Skills[index].Digest)
	}
	return request
}

// executionPaths selects the Codex process/checkpoint root for one request.
// A reviewed recovery gets a fresh namespace below state/reviewed while the
// profile's workspace, Codex home and game token stay on their normal paths.
func (executor *Executor) executionPaths(request ExecuteRequest) (profilePaths, error) {
	paths := executor.profiles
	if request.ReviewedRequestID != "" {
		paths.stateRoot = filepath.Join(paths.stateRoot, "reviewed", request.ReviewedRequestID)
		paths.stateDir = filepath.Join(paths.stateRoot, request.ProfileID)
	}
	if err := executor.guard.CheckAll(paths.stateRoot, paths.stateDir); err != nil {
		return profilePaths{}, fmt.Errorf("%w: reviewed state path is not isolated", ErrInvalidConfig)
	}
	return paths, nil
}

func (executor *Executor) ensureProfile(request ExecuteRequest, paths profilePaths) error {
	base := executor.profiles
	if err := executor.guard.CheckAll(paths.root, base.stateRoot, base.stateDir, paths.stateRoot, paths.stateDir, paths.workRoot, paths.workspace, paths.codexHome, paths.owner, paths.token); err != nil {
		return fmt.Errorf("%w: profile path is not isolated", ErrInvalidConfig)
	}
	if err := ensurePrivateDir(paths.root); err != nil {
		return fmt.Errorf("%w: state root: %v", ErrInvalidConfig, err)
	}
	if err := ensureOwner(paths.owner, request.ProfileID, request.MCP.CharacterID, executor.cfg.Clock); err != nil {
		return err
	}
	for _, path := range []string{base.stateRoot, base.stateDir, paths.stateRoot, paths.stateDir, paths.workRoot, paths.workspace, paths.codexHome} {
		if err := ensurePrivateDir(path); err != nil {
			return fmt.Errorf("%w: profile directory: %v", ErrInvalidConfig, err)
		}
	}
	return nil
}

// ensureProbeProfile prepares only the private directories required by the
// Codex model probe. In particular it does not create an owner marker or a
// game-capability token, so a probe cannot be mistaken for a logged-in game
// profile even if its disposable volume is inspected while the container is
// running.
func (executor *Executor) ensureProbeProfile() error {
	paths := executor.profiles
	if err := executor.guard.CheckAll(paths.root, paths.stateRoot, paths.stateDir, paths.workRoot, paths.workspace, paths.codexHome); err != nil {
		return fmt.Errorf("%w: probe path is not isolated", ErrInvalidConfig)
	}
	if err := ensurePrivateDir(paths.root); err != nil {
		return fmt.Errorf("%w: probe state root", ErrInvalidConfig)
	}
	for _, path := range []string{paths.stateRoot, paths.stateDir, paths.workRoot, paths.workspace, paths.codexHome} {
		if err := ensurePrivateDir(path); err != nil {
			return fmt.Errorf("%w: probe directory", ErrInvalidConfig)
		}
	}
	return nil
}

func (executor *Executor) installSkills(request ExecuteRequest) error {
	names := make([]string, 0, len(request.Skills))
	for _, requested := range request.Skills {
		name := strings.TrimSpace(requested.Name)
		spec, err := executor.installer.Verify(name)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrSkillMismatch, name)
		}
		if requested.Version != "" && requested.Version != spec.Version {
			return fmt.Errorf("%w: %s version", ErrSkillMismatch, name)
		}
		if requested.Digest != "" && !digestMatches(requested.Digest, spec.SHA256) {
			return fmt.Errorf("%w: %s digest", ErrSkillMismatch, name)
		}
		names = append(names, name)
	}
	if err := executor.installer.Reconcile(names, executor.profiles.workspace); err != nil {
		return fmt.Errorf("%w: reconcile catalog skills", ErrSkillMismatch)
	}
	return nil
}

func digestMatches(value, expected string) bool {
	value = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(value), "sha256:"))
	return value == "" || value == strings.ToLower(expected)
}

func ensureOwner(path, profileID, characterID string, clock func() time.Time) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxOwnerBytes {
			return ErrOwnerCorrupt
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return ErrOwnerCorrupt
		}
		var marker ownerMarker
		if err := decodeStrict(data, &marker); err != nil || marker.Version != 1 || marker.Owner != "stoneage-ai-runner" || marker.ProfileID != profileID || marker.CharacterID != characterID {
			return ErrOwnerMismatch
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrOwnerCorrupt
	}
	if clock == nil {
		clock = time.Now
	}
	marker := ownerMarker{Version: 1, Owner: "stoneage-ai-runner", ProfileID: profileID, CharacterID: characterID, CreatedAt: clock().UTC()}
	data, err := json.Marshal(marker)
	if err != nil {
		return ErrOwnerCorrupt
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return ensureOwner(path, profileID, characterID, clock)
	}
	if err != nil {
		return ErrOwnerCorrupt
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(path)
		return ErrOwnerCorrupt
	}
	return nil
}

func writePrivateToken(path, token string) error {
	token = strings.TrimSpace(token)
	if !validToken(token) {
		return ErrMCPConfig
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return ErrMCPConfig
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".game-capability.tmp-*")
	if err != nil {
		return ErrMCPConfig
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.WriteString(token + "\n")
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil || os.Rename(tmpName, path) != nil {
		return ErrMCPConfig
	}
	return nil
}

func ensurePrivateDir(path string) error {
	if path == "" || !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return ErrInvalidConfig
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return ErrInvalidConfig
	}
	return nil
}

func validatePrivateRootPath(path, name string) error {
	if path == "" || !filepath.IsAbs(path) || path == string(filepath.Separator) || path == "." {
		return fmt.Errorf("%w: %s must be a non-root absolute path", ErrInvalidConfig, name)
	}
	return nil
}

func validateExecutable(path, name string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%w: %s must be an absolute path", ErrInvalidConfig, name)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("%w: %s is not executable", ErrInvalidConfig, name)
	}
	return nil
}

func cloneEnvironment(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func hasControl(value string) bool {
	return strings.ContainsAny(value, "\x00\r\n")
}

func validateHTTPURL(value string) error {
	value = strings.TrimSpace(value)
	if hasControl(value) {
		return errors.New("URL contains control characters")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("URL must use http or https")
	}
	return nil
}

func redactResult(result aicodex.Result, secrets ...string) *Result {
	redact := func(value string) string {
		for _, secret := range secrets {
			secret = strings.TrimSpace(secret)
			if secret != "" {
				value = strings.ReplaceAll(value, secret, "<redacted>")
			}
		}
		return value
	}
	events := make([]Event, 0, len(result.Events))
	for _, event := range result.Events {
		item := redactJSON(event.Item, redact)
		raw := redactJSON(event.Raw, redact)
		events = append(events, Event{Type: event.Type, ThreadID: event.ThreadID, TurnID: event.TurnID, ItemType: event.ItemType, Item: item, Usage: event.Usage, HasUsage: event.HasUsage, Error: redact(event.Error), Raw: raw})
	}
	return &Result{
		ProfileID: result.ProfileID, ThreadID: result.ThreadID, Events: events,
		LastMessage: redact(result.LastMessage), Usage: result.Usage,
		Turn:    Turn{Status: string(result.Turn.Status), ID: result.Turn.ID, Usage: result.Turn.Usage, Error: redact(result.Turn.Error)},
		Process: Process{Status: string(result.Process.Status), ExitCode: result.Process.ExitCode, Signal: redact(result.Process.Signal)},
		Stderr:  redact(result.Stderr), Checkpoint: Checkpoint{Version: result.Checkpoint.Version, ProfileID: result.Checkpoint.ProfileID, ThreadID: result.Checkpoint.ThreadID, State: string(result.Checkpoint.State), TurnStatus: string(result.Checkpoint.TurnStatus), LastTurnID: result.Checkpoint.LastTurnID, LastEventType: result.Checkpoint.LastEventType, TurnCompleted: result.Checkpoint.TurnCompleted, UpdatedAt: result.Checkpoint.UpdatedAt},
	}
}

func redactJSON(raw json.RawMessage, redact func(string) string) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	value := []byte(redact(string(raw)))
	if !json.Valid(value) {
		return json.RawMessage(`{"redacted":true}`)
	}
	return json.RawMessage(value)
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
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

// rejectDuplicateKeys walks nested JSON before a struct decoder applies its
// last-value-wins behavior. This keeps the stdin contract unambiguous.
func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := walkJSON(decoder, 0); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func walkJSON(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("JSON nesting is too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := seen[name]; exists {
				return errors.New("duplicate object key")
			}
			seen[name] = struct{}{}
			if err := walkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := walkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return errors.New("invalid JSON delimiter")
	}
}
