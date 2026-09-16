// Package aicodex runs the real Codex CLI for a StoneAge AI player.
//
// This package is deliberately a process adapter.  It does not implement an
// agent loop, imitate a model provider, or execute model output itself.  A
// caller starts one Codex turn, receives the JSONL events emitted by
// `codex exec --json`, and gives the game runtime responsibility for acting on
// the result.
//
// A Runner is configured by the server with a fixed executable, roots for
// isolated workspaces and Codex state, and (optionally) a generated config
// file.  A profile ID is never treated as a path or interpolated into a shell
// command.  Every invocation uses os/exec with an argument slice and sends
// the prompt through stdin.
package aicodex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
)

const (
	// CodexBackend identifies this adapter in a model/runtime configuration.
	CodexBackend = "codex"

	// Turn statuses are deliberately separate from process statuses.  A
	// zero-exit process without turn.completed is not a successful turn.
	TurnUnknown   TurnStatus = "unknown"
	TurnCompleted TurnStatus = "completed"
	TurnFailed    TurnStatus = "failed"

	ProcessNotStarted ProcessStatus = "not_started"
	ProcessExited     ProcessStatus = "exited"
	ProcessSignaled   ProcessStatus = "signaled"

	CheckpointRunning   CheckpointState = "running"
	CheckpointUnknown   CheckpointState = "unknown"
	CheckpointCompleted CheckpointState = "completed"
)

var (
	ErrInvalidConfig          = errors.New("aicodex: invalid configuration")
	ErrInvalidRequest         = errors.New("aicodex: invalid run request")
	ErrProfileBusy            = errors.New("aicodex: profile is already running")
	ErrCheckpointMissing      = errors.New("aicodex: thread checkpoint is missing")
	ErrCheckpointRecovery     = errors.New("aicodex: unfinished thread requires explicit recovery")
	ErrCheckpointCorrupt      = errors.New("aicodex: thread checkpoint is corrupt")
	ErrCheckpointWrite        = errors.New("aicodex: cannot persist thread checkpoint")
	ErrThreadMismatch         = errors.New("aicodex: Codex returned a different thread ID")
	ErrMissingThread          = errors.New("aicodex: Codex did not emit a thread ID")
	ErrTurnIncomplete         = errors.New("aicodex: process exited without turn.completed")
	ErrTurnFailed             = errors.New("aicodex: Codex turn failed")
	ErrProcessStart           = errors.New("aicodex: cannot start Codex process")
	ErrProcessExit            = errors.New("aicodex: Codex process exited unsuccessfully")
	ErrEventMalformed         = errors.New("aicodex: malformed Codex JSONL event")
	ErrEventTooLarge          = errors.New("aicodex: Codex JSONL event is too large")
	ErrEventLimit             = errors.New("aicodex: Codex JSONL event limit exceeded")
	ErrOutputLimit            = errors.New("aicodex: Codex output limit exceeded")
	ErrSecretUnavailable      = errors.New("aicodex: Codex credential is unavailable")
	ErrConfigFilePrivate      = errors.New("aicodex: Codex config file must be private")
	ErrRecoveryThreadRequired = errors.New("aicodex: recovery requires an exact thread ID")
	ErrWorkspaceIsolation     = errors.New("aicodex: workspace is not isolated")
)

var profileIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var providerNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// SecretProvider obtains a server-side credential for one profile.  The
// credential is used only to construct the child process environment and is
// never included in a Request, prompt, Result, event, error, or log capture.
type SecretProvider interface {
	Lookup(context.Context, string) (string, error)
}

// SecretProviderFunc adapts a function to SecretProvider.
type SecretProviderFunc func(context.Context, string) (string, error)

func (f SecretProviderFunc) Lookup(ctx context.Context, profileID string) (string, error) {
	if f == nil {
		return "", nil
	}
	return f(ctx, profileID)
}

// ProviderConfig contains non-secret Codex provider settings.  A secret must
// be supplied through SecretProvider or a private generated config file; it
// is never accepted here because this value is commonly serialized by an
// admin API.
type ProviderConfig struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url,omitempty"`
	WireAPI string `json:"wire_api,omitempty"`
}

// Limits bounds data accepted from a child process.  The limits protect the
// server even if a skill or a broken executable emits an unbounded stream.
type Limits struct {
	MaxEventBytes  int
	MaxEvents      int
	MaxStdoutBytes int64
	MaxPromptBytes int
	MaxStderrBytes int
}

func (l Limits) withDefaults() Limits {
	if l.MaxEventBytes <= 0 {
		l.MaxEventBytes = 1 << 20
	}
	if l.MaxEvents <= 0 {
		l.MaxEvents = 4096
	}
	if l.MaxStdoutBytes <= 0 {
		l.MaxStdoutBytes = 32 << 20
	}
	if l.MaxPromptBytes <= 0 {
		l.MaxPromptBytes = 512 << 10
	}
	if l.MaxStderrBytes <= 0 {
		l.MaxStderrBytes = 64 << 10
	}
	return l
}

// Config is server-owned configuration.  Binary, CWD/WorkspaceRoot, state
// roots, and config paths cannot be supplied by a profile or a model prompt.
//
// If WorkspaceRoot is set, the command CWD is WorkspaceRoot/<profile-id> so
// each agent has a separate workspace.  If it is empty, CWD is used exactly
// as configured.  CodexHomeRoot is split by profile ID and defaults to
// StateRoot. CodexHome, when supplied, is an already-isolated dedicated home
// for this Runner and is used exactly as configured; this is useful when the
// model catalog materializer has already created a per-agent home.
//
// ConfigFile is a private TOML file generated by the server.  It is copied to
// the profile's CodexHome as config.toml.  When ConfigFile is present,
// --ignore-user-config is intentionally omitted so Codex can read that file;
// the child still cannot see the operator's CODEX_HOME because the runner
// replaces it with the profile-specific directory.
type Config struct {
	Binary          string
	CWD             string
	WorkspaceRoot   string
	StateRoot       string
	CodexHomeRoot   string
	CodexHome       string // exact dedicated home; mutually exclusive with CodexHomeRoot
	ConfigFile      string
	ConfigPath      string // alias for ConfigFile, useful with aimodels.RuntimeFiles
	Model           string
	Provider        ProviderConfig
	ModelCatalog    string
	CatalogPath     string // alias for ModelCatalog
	ReasoningEffort string
	WebSearch       string

	// These are fixed product defaults.  They are passed as per-process
	// config overrides, leaving the user's machine configuration untouched.
	ApprovalPolicy string
	SandboxMode    string

	SecretProvider SecretProvider
	// SecretEnv controls the name used for a server-provided credential.  An
	// empty value disables environment credential injection, which is useful
	// when ConfigFile contains the provider's private token field.
	SecretEnv string
	// Environment contains server-supplied non-secret variables needed by a
	// game MCP service. Inherited variables are otherwise reduced to a small
	// allowlist of PATH and locale/terminal settings.
	Environment map[string]string
	// GitBinary is an optional fixed path used to establish an independent
	// repository boundary in each workspace.
	GitBinary string
	// DisableWorkspaceGit is intended only for a caller that already
	// provisioned a dedicated .git boundary.
	DisableWorkspaceGit bool

	Limits           Limits
	TerminationGrace time.Duration
	// IgnoreUserConfig is retained for callers that build Config values from
	// older settings. Isolation always implies this behavior when ConfigFile
	// is empty; it is intentionally ignored when ConfigFile is present.
	IgnoreUserConfig bool
	Clock            func() time.Time

	// pathGuard is captured when New validates the server-owned roots. It is
	// intentionally private: callers cannot replace the protected operator
	// roots through Config or an admin payload.
	pathGuard runtimepath.Guard
}

func (c Config) normalized() (Config, error) {
	if c.Environment != nil {
		cloned := make(map[string]string, len(c.Environment))
		for name, value := range c.Environment {
			cloned[name] = value
		}
		c.Environment = cloned
	}
	c.Binary = strings.TrimSpace(c.Binary)
	c.CWD = strings.TrimSpace(c.CWD)
	c.WorkspaceRoot = strings.TrimSpace(c.WorkspaceRoot)
	c.StateRoot = strings.TrimSpace(c.StateRoot)
	c.CodexHomeRoot = strings.TrimSpace(c.CodexHomeRoot)
	c.CodexHome = strings.TrimSpace(c.CodexHome)
	c.ConfigFile = strings.TrimSpace(c.ConfigFile)
	c.ConfigPath = strings.TrimSpace(c.ConfigPath)
	c.Model = strings.TrimSpace(c.Model)
	c.Provider.Name = strings.TrimSpace(c.Provider.Name)
	c.Provider.BaseURL = strings.TrimRight(strings.TrimSpace(c.Provider.BaseURL), "/")
	c.Provider.WireAPI = strings.TrimSpace(c.Provider.WireAPI)
	c.ModelCatalog = strings.TrimSpace(c.ModelCatalog)
	c.CatalogPath = strings.TrimSpace(c.CatalogPath)
	c.ReasoningEffort = strings.TrimSpace(c.ReasoningEffort)
	c.WebSearch = strings.TrimSpace(c.WebSearch)
	c.ApprovalPolicy = strings.TrimSpace(c.ApprovalPolicy)
	c.SandboxMode = strings.TrimSpace(c.SandboxMode)
	c.SecretEnv = strings.TrimSpace(c.SecretEnv)

	if c.Binary == "" || !filepath.IsAbs(c.Binary) {
		return Config{}, fmt.Errorf("%w: Binary must be an absolute server path", ErrInvalidConfig)
	}
	if c.CWD == "" && c.WorkspaceRoot == "" {
		return Config{}, fmt.Errorf("%w: CWD or WorkspaceRoot is required", ErrInvalidConfig)
	}
	for name, value := range map[string]string{
		"CWD": c.CWD, "WorkspaceRoot": c.WorkspaceRoot, "StateRoot": c.StateRoot,
		"CodexHomeRoot": c.CodexHomeRoot, "CodexHome": c.CodexHome, "ConfigFile": c.ConfigFile,
	} {
		if value != "" && !filepath.IsAbs(value) {
			return Config{}, fmt.Errorf("%w: %s must be an absolute path", ErrInvalidConfig, name)
		}
	}
	if c.StateRoot == "" {
		return Config{}, fmt.Errorf("%w: StateRoot is required", ErrInvalidConfig)
	}
	if c.CodexHomeRoot != "" && c.CodexHome != "" {
		return Config{}, fmt.Errorf("%w: CodexHome and CodexHomeRoot are mutually exclusive", ErrInvalidConfig)
	}
	if c.ConfigFile != "" && c.ConfigPath != "" && filepath.Clean(c.ConfigFile) != filepath.Clean(c.ConfigPath) {
		return Config{}, fmt.Errorf("%w: ConfigFile and ConfigPath disagree", ErrInvalidConfig)
	}
	if c.ConfigFile == "" {
		c.ConfigFile = c.ConfigPath
	}
	if c.ConfigFile != "" && !filepath.IsAbs(c.ConfigFile) {
		return Config{}, fmt.Errorf("%w: ConfigFile must be absolute", ErrInvalidConfig)
	}
	if c.ModelCatalog != "" && c.CatalogPath != "" && filepath.Clean(c.ModelCatalog) != filepath.Clean(c.CatalogPath) {
		return Config{}, fmt.Errorf("%w: ModelCatalog and CatalogPath disagree", ErrInvalidConfig)
	}
	if c.ModelCatalog == "" {
		c.ModelCatalog = c.CatalogPath
	}
	if c.ModelCatalog != "" && !filepath.IsAbs(c.ModelCatalog) {
		return Config{}, fmt.Errorf("%w: ModelCatalog must be absolute", ErrInvalidConfig)
	}
	if c.CodexHomeRoot == "" && c.CodexHome == "" {
		c.CodexHomeRoot = c.StateRoot
	}
	if c.Provider.Name != "" && !providerNamePattern.MatchString(c.Provider.Name) {
		return Config{}, fmt.Errorf("%w: invalid provider name", ErrInvalidConfig)
	}
	if c.Provider.BaseURL != "" {
		if c.Provider.Name == "" {
			return Config{}, fmt.Errorf("%w: provider name is required with BaseURL", ErrInvalidConfig)
		}
		if err := validateHTTPURL(c.Provider.BaseURL); err != nil {
			return Config{}, fmt.Errorf("%w: provider BaseURL: %v", ErrInvalidConfig, err)
		}
	}
	if c.Provider.WireAPI != "" && c.Provider.WireAPI != "responses" {
		return Config{}, fmt.Errorf("%w: only the Responses wire API is supported", ErrInvalidConfig)
	}
	if c.Model != "" && hasControl(c.Model) {
		return Config{}, fmt.Errorf("%w: model contains control characters", ErrInvalidConfig)
	}
	if c.ModelCatalog != "" && !filepath.IsAbs(c.ModelCatalog) {
		return Config{}, fmt.Errorf("%w: ModelCatalog must be absolute", ErrInvalidConfig)
	}
	for name, value := range map[string]string{
		"ReasoningEffort": c.ReasoningEffort,
		"WebSearch":       c.WebSearch,
	} {
		if hasControl(value) {
			return Config{}, fmt.Errorf("%w: %s contains control characters", ErrInvalidConfig, name)
		}
	}
	if c.ApprovalPolicy == "" {
		c.ApprovalPolicy = "never"
	}
	if c.SandboxMode == "" {
		c.SandboxMode = "danger-full-access"
	}
	if c.ApprovalPolicy != "never" {
		return Config{}, fmt.Errorf("%w: ApprovalPolicy must be never", ErrInvalidConfig)
	}
	if c.SandboxMode != "danger-full-access" {
		return Config{}, fmt.Errorf("%w: SandboxMode must be danger-full-access", ErrInvalidConfig)
	}
	if c.SecretEnv == "" {
		c.SecretEnv = "CODEX_API_KEY"
	}
	if !envNamePattern.MatchString(c.SecretEnv) {
		return Config{}, fmt.Errorf("%w: invalid SecretEnv", ErrInvalidConfig)
	}
	if c.GitBinary != "" && !filepath.IsAbs(c.GitBinary) {
		return Config{}, fmt.Errorf("%w: GitBinary must be an absolute server path", ErrInvalidConfig)
	}
	if isRuntimeIsolationEnvironment(c.SecretEnv) {
		return Config{}, fmt.Errorf("%w: SecretEnv cannot replace an isolated process variable", ErrInvalidConfig)
	}
	for name, value := range c.Environment {
		if !envNamePattern.MatchString(name) || hasControl(value) || isProtectedEnvironment(name) {
			return Config{}, fmt.Errorf("%w: invalid or protected Environment key %q", ErrInvalidConfig, name)
		}
	}
	pathGuard, err := runtimepath.NewGuard()
	if err != nil {
		return Config{}, fmt.Errorf("%w: initialize runtime path guard", ErrInvalidConfig)
	}
	if err := pathGuard.CheckAll(c.CWD, c.WorkspaceRoot, c.StateRoot, c.CodexHomeRoot, c.CodexHome, c.ConfigFile, c.ModelCatalog); err != nil {
		return Config{}, fmt.Errorf("%w: runtime path is not isolated", ErrInvalidConfig)
	}
	c.pathGuard = pathGuard
	c.Limits = c.Limits.withDefaults()
	if c.TerminationGrace <= 0 {
		c.TerminationGrace = 2 * time.Second
	}
	if c.TerminationGrace > 30*time.Second {
		return Config{}, fmt.Errorf("%w: TerminationGrace is too long", ErrInvalidConfig)
	}
	return c, nil
}

// RunRequest describes one initial or resumed Codex turn.  ThreadID is an
// exact checkpoint value; it is never inferred from --last or from the newest
// session in a CodexHome.
type RunRequest struct {
	ProfileID string
	// RequestID is the supervisor's durable turn identity. The local CLI
	// adapter does not forward it as a Codex argument; container transports
	// use it to distinguish a retry from a new turn with an identical prompt.
	RequestID string
	Prompt    string
	Resume    bool
	ThreadID  string
	SecretID  string
}

// Request is a short compatibility alias for callers that prefer that name.
type Request = RunRequest

// Event is one sanitized JSONL event emitted by Codex.  Raw is bounded by
// Limits.MaxEventBytes and has known secrets redacted before being exposed.
type Event struct {
	Type     string
	ThreadID string
	TurnID   string
	ItemType string
	Item     json.RawMessage
	Usage    Usage
	HasUsage bool
	Error    string
	Raw      json.RawMessage
}

// Usage contains token counters from turn.completed when Codex supplies them.
type Usage struct {
	InputTokens       int64 `json:"input_tokens,omitempty"`
	CachedInputTokens int64 `json:"cached_input_tokens,omitempty"`
	OutputTokens      int64 `json:"output_tokens,omitempty"`
	TotalTokens       int64 `json:"total_tokens,omitempty"`
}

type TurnStatus string

type TurnResult struct {
	Status TurnStatus
	ID     string
	Usage  Usage
	Error  string
}

type ProcessStatus string

type ProcessResult struct {
	Status   ProcessStatus
	ExitCode int
	Signal   string
}

type CheckpointState string

// ThreadCheckpoint is the only durable state owned by this adapter.  A
// checkpoint is marked completed only after both turn.completed and a
// successful process exit have been observed.  Interrupted or ambiguous
// executions remain unknown and require explicit resume/reconciliation.
type ThreadCheckpoint struct {
	Version       int             `json:"version"`
	ProfileID     string          `json:"profile_id"`
	ThreadID      string          `json:"thread_id"`
	State         CheckpointState `json:"state"`
	TurnStatus    TurnStatus      `json:"turn_status"`
	LastTurnID    string          `json:"last_turn_id,omitempty"`
	LastEventType string          `json:"last_event_type,omitempty"`
	TurnCompleted bool            `json:"turn_completed"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type Result struct {
	ProfileID   string
	ThreadID    string
	Events      []Event
	LastMessage string
	Usage       Usage
	Turn        TurnResult
	Process     ProcessResult
	Stderr      string
	Checkpoint  ThreadCheckpoint
}

// Runner owns process invocation and per-profile serialization.
type Runner struct {
	cfg Config
}

// New validates server-owned configuration without starting a process.
func New(cfg Config) (*Runner, error) {
	normalized, err := cfg.normalized()
	if err != nil {
		return nil, err
	}
	return &Runner{cfg: normalized}, nil
}

// Config returns a copy of the normalized server configuration.  It contains
// no secret material.
func (r *Runner) Config() Config {
	if r == nil {
		return Config{}
	}
	return r.cfg
}

// Run starts one real `codex exec` turn.  It serializes calls for the same
// profile, persists thread checkpoints, and returns a Result even when the
// process or turn failed so callers can inspect both statuses independently.
func (r *Runner) Run(ctx context.Context, req RunRequest) (Result, error) {
	if r == nil {
		return Result{}, fmt.Errorf("%w: runner is nil", ErrInvalidConfig)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRequest(req, r.cfg.Limits); err != nil {
		return Result{}, err
	}
	stateDir := filepath.Join(r.cfg.StateRoot, req.ProfileID)
	if err := r.cfg.pathGuard.Check(stateDir); err != nil {
		return Result{ProfileID: req.ProfileID}, fmt.Errorf("%w: state path is not isolated", ErrInvalidConfig)
	}
	if err := acquireProfile(ctx, stateDir); err != nil {
		return Result{}, err
	}
	defer releaseProfile(stateDir)
	return r.runLocked(ctx, req, stateDir)
}

// TryRun is the non-blocking variant for schedulers that would rather skip a
// profile already owned by another turn than queue a potentially stale
// prompt. It uses the same checkpoint and process verification as Run.
func (r *Runner) TryRun(ctx context.Context, req RunRequest) (Result, error) {
	if r == nil {
		return Result{}, fmt.Errorf("%w: runner is nil", ErrInvalidConfig)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRequest(req, r.cfg.Limits); err != nil {
		return Result{}, err
	}
	stateDir := filepath.Join(r.cfg.StateRoot, req.ProfileID)
	if err := r.cfg.pathGuard.Check(stateDir); err != nil {
		return Result{ProfileID: req.ProfileID}, fmt.Errorf("%w: state path is not isolated", ErrInvalidConfig)
	}
	if !tryAcquireProfile(stateDir) {
		return Result{ProfileID: req.ProfileID}, ErrProfileBusy
	}
	defer releaseProfile(stateDir)
	return r.runLocked(ctx, req, stateDir)
}

// NewTurn is a convenience wrapper for an initial turn.
func (r *Runner) NewTurn(ctx context.Context, profileID, prompt string) (Result, error) {
	return r.Run(ctx, RunRequest{ProfileID: profileID, Prompt: prompt})
}

// ResumeTurn resumes the exact thread recorded for profileID.  It never uses
// Codex's --last selection.
func (r *Runner) ResumeTurn(ctx context.Context, profileID, prompt string) (Result, error) {
	return r.Run(ctx, RunRequest{ProfileID: profileID, Prompt: prompt, Resume: true})
}

// LoadCheckpoint reads a profile's checkpoint without starting Codex.
func (r *Runner) LoadCheckpoint(profileID string) (ThreadCheckpoint, error) {
	if r == nil {
		return ThreadCheckpoint{}, fmt.Errorf("%w: runner is nil", ErrInvalidConfig)
	}
	if err := validateProfileID(profileID); err != nil {
		return ThreadCheckpoint{}, err
	}
	stateDir := filepath.Join(r.cfg.StateRoot, profileID)
	if err := r.cfg.pathGuard.Check(stateDir); err != nil {
		return ThreadCheckpoint{}, fmt.Errorf("%w: state path is not isolated", ErrInvalidConfig)
	}
	return loadCheckpoint(filepath.Join(stateDir, "thread.json"), profileID)
}

func validateRequest(req RunRequest, limits Limits) error {
	if err := validateProfileID(req.ProfileID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if len(req.Prompt) > limits.MaxPromptBytes {
		return fmt.Errorf("%w: prompt exceeds %d bytes", ErrInvalidRequest, limits.MaxPromptBytes)
	}
	if hasControl(req.ThreadID) || hasControl(req.SecretID) {
		return fmt.Errorf("%w: request contains control characters", ErrInvalidRequest)
	}
	if req.Resume {
		if req.ThreadID != "" && hasControl(req.ThreadID) {
			return fmt.Errorf("%w: invalid thread ID", ErrInvalidRequest)
		}
	} else if req.ThreadID != "" {
		return fmt.Errorf("%w: ThreadID requires Resume", ErrInvalidRequest)
	}
	return nil
}

func validateProfileID(profileID string) error {
	if !profileIDPattern.MatchString(profileID) {
		return fmt.Errorf("invalid profile ID")
	}
	return nil
}

func (r *Runner) runLocked(ctx context.Context, req RunRequest, stateDir string) (Result, error) {
	workspace, home, processHome, processTemp, configSecrets, err := r.prepareProfile(req.ProfileID, stateDir)
	if err != nil {
		return Result{ProfileID: req.ProfileID}, err
	}
	cpPath := filepath.Join(stateDir, "thread.json")
	oldCP, cpErr := loadCheckpoint(cpPath, req.ProfileID)
	if cpErr != nil && !errors.Is(cpErr, ErrCheckpointMissing) {
		return Result{ProfileID: req.ProfileID}, cpErr
	}
	hasCP := cpErr == nil
	if req.Resume {
		if !hasCP {
			return Result{ProfileID: req.ProfileID}, ErrCheckpointMissing
		}
		if oldCP.ThreadID == "" {
			return Result{ProfileID: req.ProfileID}, ErrRecoveryThreadRequired
		}
		if req.ThreadID != "" && req.ThreadID != oldCP.ThreadID {
			return Result{ProfileID: req.ProfileID}, ErrThreadMismatch
		}
		req.ThreadID = oldCP.ThreadID
	} else if hasCP && oldCP.State != CheckpointCompleted {
		return Result{ProfileID: req.ProfileID}, ErrCheckpointRecovery
	}

	secret, err := r.lookupSecret(ctx, req)
	if err != nil {
		return Result{ProfileID: req.ProfileID, ThreadID: req.ThreadID, Checkpoint: oldCP}, err
	}
	secrets := append([]string{}, configSecrets...)
	if secret != "" {
		secrets = append(secrets, secret)
	}
	redactor := newRedactor(secrets)

	args := r.buildArgs(req)
	cmd := exec.Command(r.cfg.Binary, args...)
	cmd.Dir = workspace
	cmd.Env = buildEnvironment(home, processHome, processTemp, r.cfg.SecretEnv, secret, r.cfg.Environment)
	configureProcessGroup(cmd)

	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		return Result{ProfileID: req.ProfileID, ThreadID: req.ThreadID, Checkpoint: oldCP}, fmt.Errorf("%w: stdout pipe", ErrProcessStart)
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		return Result{ProfileID: req.ProfileID, ThreadID: req.ThreadID, Checkpoint: oldCP}, fmt.Errorf("%w: stderr pipe", ErrProcessStart)
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
		return Result{ProfileID: req.ProfileID, ThreadID: req.ThreadID, Checkpoint: oldCP}, fmt.Errorf("%w: stdin pipe", ErrProcessStart)
	}
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		_ = stderr.Close()
		_ = stderrWriter.Close()
		_ = stdin.Close()
		return Result{ProfileID: req.ProfileID, ThreadID: req.ThreadID, Checkpoint: oldCP}, fmt.Errorf("%w: %s", ErrProcessStart, sanitizeError(err.Error(), redactor))
	}
	// The child owns the inherited write ends. Closing the parent's copies is
	// essential: readers then see EOF as soon as the child exits, while Wait
	// cannot truncate events that were already written to the pipe.
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()

	events := make(chan Event, 32)
	parseErrors := make(chan error, 1)
	stderrDone := make(chan struct {
		text string
	}, 1)
	waitDone := make(chan waitResult, 1)
	finished := make(chan struct{})
	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()

	go func() {
		defer stdout.Close()
		err := parseJSONL(runCtx, stdout, events, r.cfg.Limits, redactor)
		parseErrors <- err
	}()
	go func() {
		defer stderr.Close()
		capture := newBoundedCapture(r.cfg.Limits.MaxStderrBytes, redactor)
		_, _ = io.Copy(capture, stderr)
		capture.Close()
		stderrDone <- struct{ text string }{capture.String()}
	}()
	go func() {
		err := cmd.Wait()
		waitDone <- waitResult{err: err}
		close(finished)
	}()
	go func() {
		_, _ = io.Copy(stdin, strings.NewReader(req.Prompt))
		_ = stdin.Close()
	}()

	checkpoint := oldCP
	if !req.Resume {
		// A new turn must not expose a previous completed thread as the
		// result of this attempt if Codex exits before emitting a new ID.
		checkpoint = ThreadCheckpoint{
			Version: 1, ProfileID: req.ProfileID, State: CheckpointUnknown, TurnStatus: TurnUnknown,
		}
	}
	result := Result{
		ProfileID: req.ProfileID, ThreadID: req.ThreadID, Checkpoint: checkpoint,
		Process: ProcessResult{Status: ProcessExited, ExitCode: 0},
		Turn:    TurnResult{Status: TurnUnknown},
	}
	var processErr error
	var parseErr error
	var wait waitResult
	var stderrText string
	var eventsClosed, parseReported, waitReported, stderrReported bool
	var stopOnce sync.Once
	requestStop := func(reason error) {
		if reason != nil && processErr == nil {
			// A context may be canceled just after Wait completed but before
			// its buffered result was selected. Do not turn a verified process
			// exit into a spurious cancellation error.
			contextReason := errors.Is(reason, context.Canceled) || errors.Is(reason, context.DeadlineExceeded)
			finishedAlready := false
			select {
			case <-finished:
				finishedAlready = true
			default:
			}
			if !contextReason || !finishedAlready {
				processErr = reason
			}
		}
		stopOnce.Do(func() {
			stopRun()
			terminateProcess(cmd, finished, r.cfg.TerminationGrace)
		})
	}

	for !(eventsClosed && parseReported && waitReported && stderrReported) {
		select {
		case event, ok := <-events:
			if !ok {
				eventsClosed = true
				continue
			}
			result.Events = append(result.Events, event)
			if event.Type == "item.completed" && event.ItemType == "agent_message" {
				if text := itemText(event.Item); text != "" {
					result.LastMessage = text
				}
			}
			if event.Type == "thread.started" && event.ThreadID != "" {
				if result.ThreadID != "" && result.ThreadID != event.ThreadID {
					requestStop(ErrThreadMismatch)
					continue
				}
				result.ThreadID = event.ThreadID
				result.Checkpoint = ThreadCheckpoint{
					Version: 1, ProfileID: req.ProfileID, ThreadID: result.ThreadID,
					State: CheckpointRunning, TurnStatus: TurnUnknown,
					LastEventType: event.Type, UpdatedAt: r.now(),
				}
				if err := saveCheckpoint(cpPath, result.Checkpoint); err != nil {
					requestStop(fmt.Errorf("%w: %v", ErrCheckpointWrite, err))
				}
			} else if result.ThreadID != "" {
				result.Checkpoint.ThreadID = result.ThreadID
				result.Checkpoint.ProfileID = req.ProfileID
				result.Checkpoint.Version = 1
				result.Checkpoint.LastEventType = event.Type
				if event.TurnID != "" {
					result.Checkpoint.LastTurnID = event.TurnID
				}
				if event.Type == "turn.completed" {
					result.Checkpoint.TurnCompleted = true
					result.Checkpoint.TurnStatus = TurnCompleted
					result.Checkpoint.State = CheckpointRunning
				}
				// Persist event progress. A turn.completed event is still only
				// a fact observed from stdout; completion is decided after Wait.
				if err := saveCheckpoint(cpPath, result.Checkpoint); err != nil {
					requestStop(fmt.Errorf("%w: %v", ErrCheckpointWrite, err))
				}
			}
			if event.Type == "turn.started" && event.TurnID != "" {
				result.Turn.ID = event.TurnID
			}
			if event.Type == "turn.completed" {
				result.Turn.Status = TurnCompleted
				if event.HasUsage {
					result.Turn.Usage = event.Usage
					result.Usage = event.Usage
				}
			}
			if event.Type == "turn.failed" || event.Type == "error" {
				result.Turn.Status = TurnFailed
				result.Turn.Error = event.Error
			}
		case err := <-parseErrors:
			parseReported = true
			parseErr = err
			if err != nil {
				requestStop(err)
			}
		case wr := <-waitDone:
			waitReported = true
			wait = wr
		case output := <-stderrDone:
			stderrReported = true
			stderrText = output.text
		case <-ctx.Done():
			if !waitReported {
				requestStop(ctx.Err())
			}
		}
	}
	result.Stderr = stderrText
	result.Process = makeProcessResult(wait.err)
	if parseErr != nil && processErr == nil {
		processErr = parseErr
	}

	// Resolve the two independent statuses only after all output and process
	// exit data are available. Never infer a successful turn from exit code.
	if processErr == nil {
		switch {
		case result.Process.Status == ProcessExited && result.Process.ExitCode != 0:
			processErr = processExitError(result.Process, result.Stderr)
		case result.Turn.Status == TurnFailed:
			processErr = fmt.Errorf("%w: %s", ErrTurnFailed, result.Turn.Error)
		case result.Process.Status != ProcessExited:
			processErr = fmt.Errorf("%w: process did not exit normally", ErrProcessExit)
		case result.Turn.Status != TurnCompleted:
			processErr = ErrTurnIncomplete
		case result.ThreadID == "":
			processErr = ErrMissingThread
		}
	}

	if result.ThreadID != "" {
		result.Checkpoint.ThreadID = result.ThreadID
		result.Checkpoint.ProfileID = req.ProfileID
		result.Checkpoint.Version = 1
		result.Checkpoint.UpdatedAt = r.now()
		if processErr == nil && result.Process.Status == ProcessExited && result.Process.ExitCode == 0 && result.Turn.Status == TurnCompleted {
			result.Checkpoint.State = CheckpointCompleted
			result.Checkpoint.TurnCompleted = true
			result.Checkpoint.TurnStatus = TurnCompleted
		} else {
			result.Checkpoint.State = CheckpointUnknown
			if result.Turn.Status == TurnCompleted {
				result.Checkpoint.TurnCompleted = true
				result.Checkpoint.TurnStatus = TurnCompleted
			}
		}
		if err := saveCheckpoint(cpPath, result.Checkpoint); err != nil && processErr == nil {
			processErr = fmt.Errorf("%w: %v", ErrCheckpointWrite, err)
		}
	}
	return result, processErr
}

func (r *Runner) lookupSecret(ctx context.Context, req RunRequest) (string, error) {
	if r.cfg.SecretProvider == nil {
		return "", nil
	}
	id := req.SecretID
	if id == "" {
		id = req.ProfileID
	}
	secret, err := r.cfg.SecretProvider.Lookup(ctx, id)
	if err != nil || strings.TrimSpace(secret) == "" {
		return "", ErrSecretUnavailable
	}
	return secret, nil
}

func (r *Runner) now() time.Time {
	if r.cfg.Clock != nil {
		return r.cfg.Clock().UTC()
	}
	return time.Now().UTC()
}

func (r *Runner) prepareProfile(profileID, stateDir string) (workspace, home, processHome, processTemp string, configSecrets []string, err error) {
	if err = r.cfg.pathGuard.CheckAll(r.cfg.StateRoot, stateDir, r.cfg.ConfigFile, r.cfg.ModelCatalog); err != nil {
		return "", "", "", "", nil, fmt.Errorf("%w: runtime path is not isolated", ErrInvalidConfig)
	}
	if err = ensurePrivateDir(r.cfg.StateRoot); err != nil {
		return "", "", "", "", nil, err
	}
	if err = ensurePrivateDir(stateDir); err != nil {
		return "", "", "", "", nil, err
	}
	processHome = filepath.Join(stateDir, "process-home")
	processTemp = filepath.Join(stateDir, "tmp")
	if err = ensurePrivateDir(processHome); err != nil {
		return "", "", "", "", nil, err
	}
	if err = ensurePrivateDir(processTemp); err != nil {
		return "", "", "", "", nil, err
	}
	if err = ensurePrivateDir(filepath.Join(processHome, "cache")); err != nil {
		return "", "", "", "", nil, err
	}
	if err = ensurePrivateDir(filepath.Join(processHome, "xdg-config")); err != nil {
		return "", "", "", "", nil, err
	}
	for _, directory := range []string{"xdg-data", "xdg-state", "xdg-runtime"} {
		if err = ensurePrivateDir(filepath.Join(processHome, directory)); err != nil {
			return "", "", "", "", nil, err
		}
	}
	if r.cfg.CodexHome != "" {
		home = r.cfg.CodexHome
	} else {
		homeRoot := r.cfg.CodexHomeRoot
		if err = ensurePrivateDir(homeRoot); err != nil {
			return "", "", "", "", nil, err
		}
		home = filepath.Join(homeRoot, profileID)
	}
	if err = r.cfg.pathGuard.Check(home); err != nil {
		return "", "", "", "", nil, fmt.Errorf("%w: Codex home is not isolated", ErrInvalidConfig)
	}
	if err = ensurePrivateDir(home); err != nil {
		return "", "", "", "", nil, err
	}
	if r.cfg.ConfigFile != "" {
		if err = r.cfg.pathGuard.Check(r.cfg.ConfigFile); err != nil {
			return "", "", "", "", nil, fmt.Errorf("%w: config source is not isolated", ErrInvalidConfig)
		}
		if err = r.cfg.pathGuard.Check(filepath.Join(home, "config.toml")); err != nil {
			return "", "", "", "", nil, fmt.Errorf("%w: config destination is not isolated", ErrInvalidConfig)
		}
		configSecrets, err = installConfigFile(r.cfg.ConfigFile, filepath.Join(home, "config.toml"))
		if err != nil {
			return "", "", "", "", nil, err
		}
	}
	if r.cfg.WorkspaceRoot != "" {
		if err = r.cfg.pathGuard.Check(r.cfg.WorkspaceRoot); err != nil {
			return "", "", "", "", nil, fmt.Errorf("%w: workspace root is not isolated", ErrInvalidConfig)
		}
		if err = ensureWorkspaceRoot(r.cfg.WorkspaceRoot); err != nil {
			return "", "", "", "", nil, err
		}
		workspace = filepath.Join(r.cfg.WorkspaceRoot, profileID)
		if err = r.cfg.pathGuard.Check(workspace); err != nil {
			return "", "", "", "", nil, fmt.Errorf("%w: workspace is not isolated", ErrInvalidConfig)
		}
		if err = ensureWorkspaceDir(workspace); err != nil {
			return "", "", "", "", nil, err
		}
	} else {
		workspace = r.cfg.CWD
		if err = r.cfg.pathGuard.Check(workspace); err != nil {
			return "", "", "", "", nil, fmt.Errorf("%w: workspace is not isolated", ErrInvalidConfig)
		}
		if err = ensureWorkspaceDir(workspace); err != nil {
			return "", "", "", "", nil, err
		}
	}
	if !r.cfg.DisableWorkspaceGit {
		if err = ensureWorkspaceGit(workspace, r.cfg.GitBinary); err != nil {
			return "", "", "", "", nil, err
		}
	}
	return workspace, home, processHome, processTemp, configSecrets, nil
}

func (r *Runner) buildArgs(req RunRequest) []string {
	args := []string{"exec"}
	if req.Resume {
		args = append(args, "resume")
	}
	args = append(args, "--json", "--skip-git-repo-check")
	// resume's CLI subcommand does not expose --color in the supported
	// version, while initial exec does. Keep JSON output deterministic where
	// the flag is accepted.
	if !req.Resume {
		args = append(args, "--color", "never")
	}
	// An isolated CODEX_HOME plus a generated ConfigFile is the normal path.
	// If there is no generated file, ignore any operator user config. Never
	// pass this flag alongside ConfigFile because it would ignore that file.
	if r.cfg.ConfigFile == "" {
		args = append(args, "--ignore-user-config")
	}
	args = append(args,
		"-c", "approval_policy="+tomlString(r.cfg.ApprovalPolicy),
		"-c", "sandbox_mode="+tomlString(r.cfg.SandboxMode),
	)
	if r.cfg.Model != "" {
		args = append(args, "-m", r.cfg.Model)
	}
	if r.cfg.Provider.Name != "" {
		args = append(args, "-c", "model_provider="+tomlString(r.cfg.Provider.Name))
		if r.cfg.Provider.BaseURL != "" {
			args = append(args, "-c", "model_providers."+r.cfg.Provider.Name+".base_url="+tomlString(r.cfg.Provider.BaseURL))
		}
		wireAPI := r.cfg.Provider.WireAPI
		if wireAPI == "" {
			wireAPI = "responses"
		}
		args = append(args, "-c", "model_providers."+r.cfg.Provider.Name+".wire_api="+tomlString(wireAPI))
	}
	if r.cfg.ModelCatalog != "" {
		args = append(args, "-c", "model_catalog_json="+tomlString(r.cfg.ModelCatalog))
	}
	if r.cfg.ReasoningEffort != "" {
		args = append(args, "-c", "model_reasoning_effort="+tomlString(r.cfg.ReasoningEffort))
	}
	if r.cfg.WebSearch != "" {
		args = append(args, "-c", "web_search="+tomlString(r.cfg.WebSearch))
	}
	if req.Resume {
		args = append(args, req.ThreadID, "-")
	} else {
		args = append(args, "-")
	}
	return args
}

func tomlString(value string) string {
	// Configuration values are validated for control characters before this
	// function is called. strconv.Quote produces a TOML-compatible basic
	// string for ordinary UTF-8 values and avoids shell interpretation.
	return strconv.Quote(value)
}

func parseJSONL(ctx context.Context, reader io.Reader, events chan<- Event, limits Limits, redactor *secretRedactor) error {
	defer close(events)
	buffer := bufio.NewReaderSize(reader, 64<<10)
	var total int64
	count := 0
	for {
		line, err := readLineLimited(buffer, limits.MaxEventBytes)
		if len(line) > 0 {
			total += int64(len(line))
			if total > limits.MaxStdoutBytes {
				return ErrOutputLimit
			}
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) != 0 {
				count++
				if count > limits.MaxEvents {
					return ErrEventLimit
				}
				sanitized := []byte(redactor.RedactJSON(string(trimmed)))
				event, decodeErr := decodeEvent(sanitized)
				if decodeErr != nil {
					return fmt.Errorf("%w: %v", ErrEventMalformed, decodeErr)
				}
				select {
				case events <- event:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		// Wait closes a StdoutPipe after the child exits. Depending on the
		// scheduler the reader can observe os.ErrClosed instead of EOF; the
		// process/turn verification below still rejects an incomplete turn.
		if errors.Is(err, os.ErrClosed) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func readLineLimited(reader *bufio.Reader, max int) ([]byte, error) {
	if max <= 0 {
		max = 1 << 20
	}
	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > max+1 {
			return nil, ErrEventTooLarge
		}
		if err == nil {
			if len(line) > max {
				return nil, ErrEventTooLarge
			}
			return line, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) > max {
				return nil, ErrEventTooLarge
			}
			return line, io.EOF
		}
		return line, err
	}
}

type rawEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id"`
	TurnID   string          `json:"turn_id"`
	Item     json.RawMessage `json:"item"`
	Usage    json.RawMessage `json:"usage"`
	Error    json.RawMessage `json:"error"`
}

func decodeEvent(raw []byte) (Event, error) {
	var envelope rawEvent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&envelope); err != nil {
		return Event{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Event{}, errors.New("multiple JSON values")
		}
		return Event{}, fmt.Errorf("trailing JSON: %w", err)
	}
	if strings.TrimSpace(envelope.Type) == "" {
		return Event{}, errors.New("event type is required")
	}
	event := Event{
		Type: envelope.Type, ThreadID: envelope.ThreadID, TurnID: envelope.TurnID,
		Raw: append(json.RawMessage(nil), raw...),
	}
	if len(envelope.Item) != 0 && string(envelope.Item) != "null" {
		var item map[string]json.RawMessage
		if err := json.Unmarshal(envelope.Item, &item); err == nil {
			event.Item = append(json.RawMessage(nil), envelope.Item...)
			_ = json.Unmarshal(item["type"], &event.ItemType)
		}
	}
	if len(envelope.Usage) != 0 && string(envelope.Usage) != "null" {
		if err := json.Unmarshal(envelope.Usage, &event.Usage); err == nil {
			event.HasUsage = true
		}
	}
	if len(envelope.Error) != 0 && string(envelope.Error) != "null" {
		var text string
		if json.Unmarshal(envelope.Error, &text) == nil {
			event.Error = text
		} else {
			event.Error = string(envelope.Error)
		}
		event.Error = limitText(event.Error, 4096)
	}
	return event, nil
}

func itemText(raw json.RawMessage) string {
	var item struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &item) != nil {
		return ""
	}
	return limitText(item.Text, 1<<20)
}

func makeProcessResult(err error) ProcessResult {
	if err == nil {
		return ProcessResult{Status: ProcessExited, ExitCode: 0}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		if code >= 0 {
			return ProcessResult{Status: ProcessExited, ExitCode: code}
		}
		return ProcessResult{Status: ProcessSignaled, ExitCode: code, Signal: processSignal(err)}
	}
	return ProcessResult{Status: ProcessSignaled, ExitCode: -1}
}

func processExitError(process ProcessResult, stderr string) error {
	if stderr == "" {
		return fmt.Errorf("%w: exit code %d", ErrProcessExit, process.ExitCode)
	}
	return fmt.Errorf("%w: exit code %d: %s", ErrProcessExit, process.ExitCode, limitText(stderr, 4096))
}

type waitResult struct{ err error }

func hasControl(value string) bool {
	for _, r := range value {
		if r == '\x00' || r == '\n' || r == '\r' {
			return true
		}
	}
	return false
}

func validateHTTPURL(value string) error {
	if hasControl(value) {
		return errors.New("URL contains control characters")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("URL must use http or https")
	}
	if strings.ContainsAny(value, "\r\n") {
		return errors.New("URL contains a newline")
	}
	return nil
}

func limitText(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max] + "…"
}

func sanitizeError(value string, redactor *secretRedactor) string {
	if redactor == nil {
		return limitText(value, 4096)
	}
	return limitText(redactor.Redact(value), 4096)
}

// Build a child environment from a deliberately small inherited allowlist.
// HOME, all temporary directories, and CODEX_HOME point inside the profile's
// private state. The only model credential added is the one resolved for this
// profile; credentials inherited from the server process are discarded.
func buildEnvironment(codexHome, processHome, processTemp, secretEnv, secret string, extra map[string]string) []string {
	result := make([]string, 0, 16+len(extra))
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || !allowedInheritedEnvironment(name) {
			continue
		}
		result = append(result, entry)
	}
	if !hasEnvironment(result, "PATH") {
		result = append(result, "PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin")
	}
	result = append(result,
		"CODEX_HOME="+codexHome,
		"HOME="+processHome,
		"USERPROFILE="+processHome,
		"TMPDIR="+processTemp,
		"TMP="+processTemp,
		"TEMP="+processTemp,
		"XDG_CACHE_HOME="+filepath.Join(processHome, "cache"),
		"XDG_CONFIG_HOME="+filepath.Join(processHome, "xdg-config"),
		"XDG_DATA_HOME="+filepath.Join(processHome, "xdg-data"),
		"XDG_STATE_HOME="+filepath.Join(processHome, "xdg-state"),
		"XDG_RUNTIME_DIR="+filepath.Join(processHome, "xdg-runtime"),
	)
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if isProtectedEnvironment(key) {
			continue
		}
		result = append(result, key+"="+extra[key])
	}
	if secretEnv != "" && secret != "" {
		result = append(result, secretEnv+"="+secret)
	}
	return result
}

func allowedInheritedEnvironment(name string) bool {
	upper := strings.ToUpper(name)
	switch upper {
	case "PATH", "LANG", "LC_ALL", "LC_CTYPE", "LC_MESSAGES", "TERM", "COLORTERM", "NO_COLOR":
		return true
	case "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT": // Windows process essentials.
		return true
	}
	return false
}

func isProtectedEnvironment(name string) bool {
	upper := strings.ToUpper(name)
	if isRuntimeIsolationEnvironment(upper) {
		return true
	}
	return strings.HasSuffix(upper, "_API_KEY") || strings.HasSuffix(upper, "_ACCESS_TOKEN") || upper == "CODEX_API_KEY" || upper == "OPENAI_API_KEY" || upper == "DEEPSEEK_API_KEY" || upper == "ANTHROPIC_API_KEY"
}

func isRuntimeIsolationEnvironment(name string) bool {
	switch strings.ToUpper(name) {
	case "CODEX_HOME", "HOME", "USERPROFILE", "TMPDIR", "TMP", "TEMP", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR":
		return true
	default:
		return false
	}
}

func hasEnvironment(environment []string, name string) bool {
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if ok && key == name {
			return true
		}
	}
	return false
}

func ensurePrivateDir(path string) error {
	if path == "" {
		return fmt.Errorf("%w: empty private directory", ErrInvalidConfig)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: private path is not a directory", ErrInvalidConfig)
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: private directory %q is accessible by group or others", ErrInvalidConfig, filepath.Base(path))
	}
	return nil
}

func ensureWorkspaceRoot(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0750); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: WorkspaceRoot is not a directory", ErrInvalidConfig)
	}
	return nil
}

func ensureWorkspaceDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0750); err != nil {
			return err
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: workspace is not a directory", ErrInvalidConfig)
	}
	return nil
}

// ensureWorkspaceGit establishes a repository root at the exact workspace
// directory. Codex uses the repository root when discovering project
// instructions; allowing the StoneAge source repository to become the root
// would expose its coding AGENTS.md to a game agent.
func ensureWorkspaceGit(workspace, configuredBinary string) error {
	agents := filepath.Join(workspace, "AGENTS.md")
	if info, err := os.Lstat(agents); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().IsRegular() {
			return ErrWorkspaceIsolation
		}
		return ErrWorkspaceIsolation
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrWorkspaceIsolation
	}

	gitDir := filepath.Join(workspace, ".git")
	info, err := os.Lstat(gitDir)
	if errors.Is(err, os.ErrNotExist) {
		binary, findErr := findGitBinary(configuredBinary)
		if findErr != nil {
			return fmt.Errorf("%w: git is unavailable", ErrWorkspaceIsolation)
		}
		cmd := exec.Command(binary, "-c", "init.templateDir=", "init", "--quiet", workspace)
		cmd.Dir = workspace
		cmd.Env = []string{
			"HOME=" + filepath.Join(workspace, ".git-home"),
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=" + os.DevNull,
			"GIT_CONFIG_SYSTEM=" + os.DevNull,
			"PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin",
		}
		if outputErr := cmd.Run(); outputErr != nil {
			return fmt.Errorf("%w: cannot initialize repository", ErrWorkspaceIsolation)
		}
		info, err = os.Lstat(gitDir)
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrWorkspaceIsolation
	}
	// Verify that .git points at this directory. A pre-existing git worktree
	// whose .git file redirects into the StoneAge checkout is not isolated.
	binary, findErr := findGitBinary(configuredBinary)
	if findErr != nil {
		return fmt.Errorf("%w: git is unavailable", ErrWorkspaceIsolation)
	}
	cmd := exec.Command(binary, "-C", workspace, "rev-parse", "--show-toplevel")
	cmd.Env = []string{
		"HOME=" + filepath.Join(workspace, ".git-home"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin",
	}
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%w: git root cannot be verified", ErrWorkspaceIsolation)
	}
	root, err := canonicalPathForComparison(strings.TrimSpace(string(output)))
	workspaceRoot, workspaceErr := canonicalPathForComparison(workspace)
	if err != nil || workspaceErr != nil || root != workspaceRoot {
		return ErrWorkspaceIsolation
	}
	return nil
}

// canonicalPathForComparison resolves harmless platform aliases such as
// macOS's /var -> /private/var while still comparing the actual repository
// roots. This keeps the isolation check strict without rejecting a valid
// temporary workspace reached through an OS-maintained alias.
func canonicalPathForComparison(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func findGitBinary(configured string) (string, error) {
	if configured != "" {
		info, err := os.Stat(configured)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			return "", errors.New("configured git executable is unavailable")
		}
		return configured, nil
	}
	for _, candidate := range []string{"/usr/bin/git", "/opt/homebrew/bin/git", "/usr/local/bin/git"} {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return candidate, nil
		}
	}
	if candidate, err := exec.LookPath("git"); err == nil {
		return candidate, nil
	}
	return "", errors.New("system git executable is unavailable")
}

func installConfigFile(source, destination string) ([]string, error) {
	info, err := os.Lstat(source)
	if err != nil {
		return nil, fmt.Errorf("%w: config source unavailable", ErrConfigFilePrivate)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: config source must be a regular file", ErrConfigFilePrivate)
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, ErrConfigFilePrivate
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return nil, fmt.Errorf("%w: read config source", ErrConfigFilePrivate)
	}
	secrets := extractConfigSecrets(data)
	if filepath.Clean(source) == filepath.Clean(destination) {
		return secrets, nil
	}
	if existing, statErr := os.Lstat(destination); statErr == nil && existing.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: destination is a symlink", ErrConfigFilePrivate)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".config.toml.tmp-*")
	if err != nil {
		return nil, fmt.Errorf("%w: create private config", ErrConfigFilePrivate)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("%w: chmod config", ErrConfigFilePrivate)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("%w: write config", ErrConfigFilePrivate)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("%w: sync config", ErrConfigFilePrivate)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("%w: close config", ErrConfigFilePrivate)
	}
	if err := os.Rename(tmpName, destination); err != nil {
		return nil, fmt.Errorf("%w: install config", ErrConfigFilePrivate)
	}
	return secrets, nil
}

var configSecretPattern = regexp.MustCompile(`(?im)(?:experimental_bearer_token|api[_-]?key|access[_-]?token|bearer[_-]?token)\s*=\s*["']([^"']+)["']`)

func extractConfigSecrets(data []byte) []string {
	matches := configSecretPattern.FindAllSubmatch(data, -1)
	result := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) == 2 && len(match[1]) > 0 {
			result = append(result, string(match[1]))
		}
	}
	return result
}

// The process-wide lock map intentionally remains for the lifetime of the
// server.  This avoids a remove-and-recreate race between two Runners that
// share a state root.
var profileLocks = struct {
	sync.Mutex
	items map[string]chan struct{}
}{items: make(map[string]chan struct{})}

func acquireProfile(ctx context.Context, key string) error {
	profileLocks.Lock()
	lock, ok := profileLocks.items[key]
	if !ok {
		lock = make(chan struct{}, 1)
		lock <- struct{}{}
		profileLocks.items[key] = lock
	}
	profileLocks.Unlock()
	select {
	case <-lock:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func tryAcquireProfile(key string) bool {
	profileLocks.Lock()
	lock, ok := profileLocks.items[key]
	if !ok {
		lock = make(chan struct{}, 1)
		lock <- struct{}{}
		profileLocks.items[key] = lock
	}
	profileLocks.Unlock()
	select {
	case <-lock:
		return true
	default:
		return false
	}
}

func releaseProfile(key string) {
	profileLocks.Lock()
	lock := profileLocks.items[key]
	profileLocks.Unlock()
	if lock != nil {
		lock <- struct{}{}
	}
}

func checkpointPathValid(path string) bool {
	return filepath.Base(path) == "thread.json"
}

func loadCheckpoint(path, profileID string) (ThreadCheckpoint, error) {
	if !checkpointPathValid(path) {
		return ThreadCheckpoint{}, ErrCheckpointCorrupt
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ThreadCheckpoint{}, ErrCheckpointMissing
	}
	if err != nil {
		return ThreadCheckpoint{}, fmt.Errorf("%w: read checkpoint", ErrCheckpointCorrupt)
	}
	var checkpoint ThreadCheckpoint
	if json.Unmarshal(data, &checkpoint) != nil || checkpoint.Version != 1 || checkpoint.ProfileID != profileID || checkpoint.ThreadID == "" {
		return ThreadCheckpoint{}, ErrCheckpointCorrupt
	}
	if checkpoint.State != CheckpointRunning && checkpoint.State != CheckpointUnknown && checkpoint.State != CheckpointCompleted {
		return ThreadCheckpoint{}, ErrCheckpointCorrupt
	}
	return checkpoint, nil
}

func saveCheckpoint(path string, checkpoint ThreadCheckpoint) error {
	if checkpoint.Version == 0 {
		checkpoint.Version = 1
	}
	if checkpoint.ProfileID == "" || checkpoint.ThreadID == "" {
		return ErrCheckpointWrite
	}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".thread.json.tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
