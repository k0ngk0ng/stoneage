package aiservice

// This file is the composition boundary between the authenticated game
// service and the process-level Codex adapter.  It deliberately does not
// know how a game account is logged in or how a character is created.  A
// SessionProvider supplies an already authenticated character session (or a
// backend that was built around one); this factory only binds that session to
// a short-lived agent capability and provisions the isolated Codex files.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aimodels"
	"github.com/k0ngk0ng/stoneage/internal/aiprovision"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/aisupervisor"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
)

var (
	ErrFactoryConfig      = errors.New("aiservice: invalid Codex factory configuration")
	ErrFactoryBusy        = errors.New("aiservice: profile already has an agent session")
	ErrFactorySession     = errors.New("aiservice: authenticated game session is unavailable")
	ErrFactoryModel       = errors.New("aiservice: AI model configuration is unavailable")
	ErrFactoryRuntime     = errors.New("aiservice: Codex runtime is unavailable")
	ErrFactoryProvision   = errors.New("aiservice: agent runtime provisioning failed")
	ErrFactoryCredentials = errors.New("aiservice: AI model credential is unavailable")
)

var factoryProfileIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ModelConfigSource is the small model-store surface needed by the factory.
// *airuntime.Store implements it. Keeping the interface here also lets a
// process compose the factory around a read-only model registry.
type ModelConfigSource interface {
	GetModelConfig(context.Context, string) (airuntime.ModelConfig, error)
	GetDefaultModelConfigID(context.Context) (string, error)
}

// SessionLease is returned by SessionProvider after it has selected and
// authenticated one concrete game character. Backend may already be a fully
// composed GameBackend (which is useful when the owner gate and task
// controller live in another service). If Backend is nil, Session is used to
// construct the default GameBackend below.
//
// Close is called synchronously before the Codex process is cancelled. It
// should revoke the provider's game lease and close any underlying socket.
// Wake is optional and is passed through to aisupervisor so game events or
// task completion can wake the next model turn without model output polling.
type SessionLease struct {
	Backend aimcp.Backend
	Session GameSession
	Gate    *aicontrol.Gate
	Tasks   TaskController
	Funding FundingLookup
	Wake    chan struct{}
	Close   func()
}

// SessionProvider supplies an already logged-in character. It must not
// accept account passwords from an AI profile or from Codex output.
type SessionProvider interface {
	Open(context.Context, airuntime.Profile) (SessionLease, error)
}

// SessionProviderFunc adapts a function to SessionProvider.
type SessionProviderFunc func(context.Context, airuntime.Profile) (SessionLease, error)

func (f SessionProviderFunc) Open(ctx context.Context, profile airuntime.Profile) (SessionLease, error) {
	if f == nil {
		return SessionLease{}, ErrFactorySession
	}
	return f(ctx, profile)
}

// BackendInput contains the server-owned binding and game lease selected by
// the factory. A custom builder can use this to add a task controller or a
// knowledge implementation while retaining the same fencing boundary.
type BackendInput struct {
	Profile airuntime.Profile
	// CharacterBuild is an explicit server-owned policy for deterministic
	// leveling. The Web leveling owner supplies this field; it must not infer a
	// policy from a model profile. Agent composition may leave it nil and use
	// Profile.Goal.CharacterBuild for compatibility.
	CharacterBuild *characterbuild.Policy
	Binding        aimcp.Binding
	Gate           *aicontrol.Gate
	Session        GameSession
	Tasks          TaskController
	Funding        FundingLookup
	Knowledge      *aiknowledge.Knowledge
	Receipts       *ReceiptStore
	// ScheduleStore is the server-owned profile database used for durable
	// model wake-ups. It is optional for custom backends that do not expose
	// scheduling.
	ScheduleStore *airuntime.Store
	// MemoryStore is the server-owned profile database used for private
	// self-authored agent notes. It is optional; when present the factory adds
	// the bound note capability to a backend that does not provide one.
	MemoryStore *airuntime.Store
	Lease       context.Context
	Wake        chan<- struct{}
}

// BackendBuilder builds the game backend after the character lease has been
// claimed for its authenticated owner. The returned backend is exposed only
// through the private Gateway capability.
type BackendBuilder func(context.Context, BackendInput) (aimcp.Backend, error)

// FactoryConfig contains only server-owned paths and dependencies. None of
// these values are decoded from an AI profile or a model prompt.
type FactoryConfig struct {
	Models  ModelConfigSource
	Secrets *airuntime.SecretStore

	Sessions SessionProvider
	Gateway  *Gateway
	// GatewayEndpoint is the already mounted private HTTP endpoint served by
	// Gateway, for example http://127.0.0.1:8081/v1/game. It is never sent
	// by an MCP tool; it is placed in the sidecar's private config.
	GatewayEndpoint string

	// SkillInstaller reads the checked-in, hash-pinned catalog. SkillRoot is
	// used only when SkillInstaller is nil.
	SkillInstaller *aimcp.SkillInstaller
	SkillRoot      string

	// RuntimeRoot is a private parent for state, workspaces, token files and
	// per-profile Codex homes. StateRoot/WorkspaceRoot can be supplied when a
	// service already owns those directories.
	RuntimeRoot      string
	StateRoot        string
	WorkspaceRoot    string
	CodexHomeRoot    string
	CodexBinary      string
	MCPBinary        string
	GitBinary        string
	Environment      map[string]string
	TerminationGrace time.Duration

	// ContainerBroker runs Codex in a profile-only container. When configured,
	// the factory never starts a local Codex executable or generates a local
	// model configuration. StateRoot then holds only transport checkpoints.
	ContainerBroker ContainerBroker

	// RequireCodexVersion defaults to true. Tests or a controlled bootstrap
	// may set it false while using a fake executable.
	RequireCodexVersion *bool

	// Backend is optional. When nil, Factory constructs GameBackend from the
	// fields in SessionLease and this config.
	Backend   BackendBuilder
	Gate      func(context.Context, airuntime.Profile) (*aicontrol.Gate, error)
	Knowledge *aiknowledge.Knowledge
	Receipts  *ReceiptStore

	// MemoryRecorder is optional. When present, Factory records the exact
	// server observation obtained for an AgentSession before composing the
	// supervisor snapshot. MemoryStore is a convenience for callers that own
	// an airuntime.Store and do not need to construct the recorder themselves.
	MemoryRecorder *MemoryRecorder
	MemoryStore    *airuntime.Store
	// ScheduleStore is normally the same durable AI store as MemoryStore. It is
	// separate in the config so deployments can place timer state on an
	// explicitly chosen private volume.
	ScheduleStore *airuntime.Store

	pathGuard runtimepath.Guard
}

// Factory implements aisupervisor.Factory. One instance can own many
// profiles, but it never opens two sessions for the same profile.
type Factory struct {
	cfg FactoryConfig

	mu     sync.Mutex
	closed bool
	active map[string]*factoryLease
}

type factoryLease struct {
	profileID string
	mu        sync.Mutex
	closed    bool
	close     func()
}

// factoryCleanupState is separate from factoryLease because Open publishes
// resources incrementally. Factory.Close may invoke the callback between two
// provisioning steps; every resource assignment therefore has to be fenced
// against a cleanup that has already begun.
type factoryCleanupState struct {
	mu sync.Mutex

	closed         bool
	gate           *aicontrol.Gate
	backend        aimcp.Backend
	tasks          TaskController
	revoke         func()
	tokenPath      string
	providerClose  func()
	release        func()
	forgetMemory   func()
	stopLeaseWatch func()
	once           sync.Once
}

func (state *factoryCleanupState) isClosed() bool {
	if state == nil {
		return true
	}
	state.mu.Lock()
	closed := state.closed
	state.mu.Unlock()
	return closed
}

func (state *factoryCleanupState) setGate(gate *aicontrol.Gate) bool {
	if state == nil {
		return false
	}
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		if gate != nil {
			_, _ = gate.Takeover("AI agent session closed")
		}
		return false
	}
	state.gate = gate
	state.mu.Unlock()
	return true
}

func (state *factoryCleanupState) setBackend(backend aimcp.Backend) bool {
	if state == nil {
		closeGameRuntime(backend, nil)
		return false
	}
	state.mu.Lock()
	if state.closed {
		alreadyOwned := state.backend != nil
		ownedTasks := state.tasks
		state.mu.Unlock()
		if !alreadyOwned {
			// The provider's task controller may already have been closed by
			// the first cleanup pass. A late default GameBackend wraps that
			// same controller, so close only a backend-owned closer here.
			if !isNilRuntimeValue(ownedTasks) {
				if closer, ok := backend.(gameRuntimeCloser); ok && !isNilRuntimeValue(backend) {
					closer.Close()
				}
			} else {
				closeGameRuntime(backend, nil)
			}
		}
		return false
	}
	state.backend = backend
	state.mu.Unlock()
	return true
}

func (state *factoryCleanupState) setRevoke(revoke func()) bool {
	if state == nil {
		if revoke != nil {
			revoke()
		}
		return false
	}
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		if revoke != nil {
			revoke()
		}
		return false
	}
	state.revoke = revoke
	state.mu.Unlock()
	return true
}

func (state *factoryCleanupState) setTokenPath(path string) bool {
	if state == nil {
		if path != "" {
			_ = os.Remove(path)
		}
		return false
	}
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		if path != "" {
			_ = os.Remove(path)
		}
		return false
	}
	state.tokenPath = path
	state.mu.Unlock()
	return true
}

func (state *factoryCleanupState) setLeaseWatch(stop func()) bool {
	if state == nil {
		if stop != nil {
			stop()
		}
		return false
	}
	state.mu.Lock()
	if state.closed {
		state.mu.Unlock()
		if stop != nil {
			stop()
		}
		return false
	}
	state.stopLeaseWatch = stop
	state.mu.Unlock()
	return true
}

func (state *factoryCleanupState) cleanup() {
	if state == nil {
		return
	}
	state.once.Do(func() {
		state.mu.Lock()
		state.closed = true
		gate, backend, tasks, revoke, tokenPath := state.gate, state.backend, state.tasks, state.revoke, state.tokenPath
		stopLeaseWatch := state.stopLeaseWatch
		providerClose, release, forgetMemory := state.providerClose, state.release, state.forgetMemory
		state.mu.Unlock()

		// Fencing first cancels any in-flight game/task work before the
		// provider tears down the authenticated socket.
		if stopLeaseWatch != nil {
			stopLeaseWatch()
		}
		if gate != nil {
			_, _ = gate.Takeover("AI agent session closed")
		}
		closeGameRuntime(backend, tasks)
		if revoke != nil {
			revoke()
		}
		if tokenPath != "" {
			_ = os.Remove(tokenPath)
		}
		if providerClose != nil {
			providerClose()
		}
		if release != nil {
			release()
		}
		if forgetMemory != nil {
			forgetMemory()
		}
	})
}

type gameRuntimeCloser interface {
	Close()
}

// closeGameRuntime waits for deterministic task workers before the provider
// closes the authenticated socket and any parent-owned stores. A pre-bound
// GameBackend may keep its task controller internally, so recover that
// server-owned field when the lease did not repeat it explicitly.
func closeGameRuntime(backend aimcp.Backend, tasks TaskController) {
	if isNilRuntimeValue(tasks) {
		if game, ok := backend.(*GameBackend); ok {
			tasks = game.Tasks
		}
	}
	if isNilRuntimeValue(tasks) {
		tasks = nil
	}
	if closer, ok := tasks.(gameRuntimeCloser); ok {
		closer.Close()
		return
	}
	if !isNilRuntimeValue(backend) {
		if closer, ok := backend.(gameRuntimeCloser); ok {
			closer.Close()
		}
	}
}

// Interfaces carrying a typed nil are common when an optional task
// controller is assembled by a provider. Treat them as absent before calling
// Close; invoking a method on the typed nil would panic during session
// cleanup.
func isNilRuntimeValue(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

// setClose and closeNow serialize the callback handoff with Factory.Close.
// Open can take long enough for process shutdown to race this assignment.
func (lease *factoryLease) setClose(callback func()) bool {
	if lease == nil {
		return false
	}
	lease.mu.Lock()
	if lease.closed {
		lease.mu.Unlock()
		if callback != nil {
			callback()
		}
		return false
	}
	lease.close = callback
	lease.mu.Unlock()
	return true
}

func (lease *factoryLease) closeNow() {
	if lease == nil {
		return
	}
	lease.mu.Lock()
	if lease.closed {
		lease.mu.Unlock()
		return
	}
	lease.closed = true
	callback := lease.close
	lease.mu.Unlock()
	if callback != nil {
		callback()
	}
}

var _ aisupervisor.Factory = (*Factory)(nil)
var _ aisupervisor.FactoryCloser = (*Factory)(nil)

// NewFactory validates the fixed server configuration. It does not start a
// Codex process and does not log or read any model key.
func NewFactory(config FactoryConfig) (*Factory, error) {
	normalized, err := normalizeFactoryConfig(config)
	if err != nil {
		return nil, err
	}
	return &Factory{cfg: normalized, active: make(map[string]*factoryLease)}, nil
}

// NewCodexFactory is a descriptive alias used by callers that have more than
// one kind of runtime factory in the process.
func NewCodexFactory(config FactoryConfig) (*Factory, error) { return NewFactory(config) }

// PrepareExecutorChange fences the container transport journal during an
// executor migration. The supervisor already owns the lifecycle fence; the
// factory keeps its own active-session guard so this method cannot mark a
// checkpoint while a profile session is being provisioned or used. Local
// Codex factories have no container journal and therefore treat the hook as a
// no-op.
func (factory *Factory) PrepareExecutorChange(ctx context.Context, profileID string) error {
	if factory == nil {
		return ErrFactoryConfig
	}
	profileID = strings.TrimSpace(profileID)
	if !factoryProfileIDPattern.MatchString(profileID) {
		return ErrFactoryConfig
	}
	if factory.cfg.ContainerBroker == nil {
		return nil
	}
	factory.mu.Lock()
	defer factory.mu.Unlock()
	if factory.closed {
		return aisupervisor.ErrClosed
	}
	if factory.active[profileID] != nil {
		return ErrFactoryBusy
	}
	return resetContainerRunnerForExecutorChange(ctx, profileID, filepath.Join(factory.cfg.StateRoot, profileID))
}

func normalizeFactoryConfig(config FactoryConfig) (FactoryConfig, error) {
	config.GatewayEndpoint = strings.TrimSpace(config.GatewayEndpoint)
	config.RuntimeRoot = strings.TrimSpace(config.RuntimeRoot)
	config.StateRoot = strings.TrimSpace(config.StateRoot)
	config.WorkspaceRoot = strings.TrimSpace(config.WorkspaceRoot)
	config.CodexHomeRoot = strings.TrimSpace(config.CodexHomeRoot)
	config.CodexBinary = strings.TrimSpace(config.CodexBinary)
	config.MCPBinary = strings.TrimSpace(config.MCPBinary)
	config.GitBinary = strings.TrimSpace(config.GitBinary)
	config.SkillRoot = strings.TrimSpace(config.SkillRoot)
	config.Environment = cloneEnvironment(config.Environment)

	if config.Models == nil {
		return FactoryConfig{}, fmt.Errorf("%w: Models is required", ErrFactoryConfig)
	}
	if config.Sessions == nil {
		return FactoryConfig{}, fmt.Errorf("%w: Sessions is required", ErrFactoryConfig)
	}
	if config.Gateway == nil {
		return FactoryConfig{}, fmt.Errorf("%w: Gateway is required", ErrFactoryConfig)
	}
	if err := validateEndpoint(config.GatewayEndpoint); err != nil {
		return FactoryConfig{}, fmt.Errorf("%w: GatewayEndpoint: %v", ErrFactoryConfig, err)
	}
	if config.Secrets == nil {
		return FactoryConfig{}, fmt.Errorf("%w: Secrets is required", ErrFactoryConfig)
	}
	if config.ContainerBroker == nil && (config.CodexBinary == "" || !filepath.IsAbs(config.CodexBinary) || !executable(config.CodexBinary)) {
		return FactoryConfig{}, fmt.Errorf("%w: CodexBinary must be an executable absolute path", ErrFactoryConfig)
	}
	if config.ContainerBroker == nil && (config.MCPBinary == "" || !filepath.IsAbs(config.MCPBinary) || !executable(config.MCPBinary)) {
		return FactoryConfig{}, fmt.Errorf("%w: MCPBinary must be an executable absolute path", ErrFactoryConfig)
	}
	for name, value := range map[string]string{
		"RuntimeRoot": config.RuntimeRoot, "StateRoot": config.StateRoot,
		"WorkspaceRoot": config.WorkspaceRoot, "CodexHomeRoot": config.CodexHomeRoot,
	} {
		if value != "" && (!filepath.IsAbs(value) || value == string(filepath.Separator)) {
			return FactoryConfig{}, fmt.Errorf("%w: %s must be a non-root absolute path", ErrFactoryConfig, name)
		}
	}
	if config.RuntimeRoot == "" {
		return FactoryConfig{}, fmt.Errorf("%w: RuntimeRoot is required", ErrFactoryConfig)
	}
	if config.StateRoot == "" {
		config.StateRoot = filepath.Join(config.RuntimeRoot, "state")
	}
	if config.WorkspaceRoot == "" {
		config.WorkspaceRoot = filepath.Join(config.RuntimeRoot, "workspaces")
	}
	if config.CodexHomeRoot == "" {
		config.CodexHomeRoot = filepath.Join(config.RuntimeRoot, "codex")
	}
	for name, value := range map[string]string{
		"StateRoot": config.StateRoot, "WorkspaceRoot": config.WorkspaceRoot,
		"CodexHomeRoot": config.CodexHomeRoot,
	} {
		if !filepath.IsAbs(value) || value == string(filepath.Separator) {
			return FactoryConfig{}, fmt.Errorf("%w: %s must be a non-root absolute path", ErrFactoryConfig, name)
		}
	}
	pathGuard, err := runtimepath.NewGuard()
	if err != nil {
		return FactoryConfig{}, fmt.Errorf("%w: initialize runtime path guard", ErrFactoryConfig)
	}
	if err := pathGuard.CheckAll(config.RuntimeRoot, config.StateRoot, config.WorkspaceRoot, config.CodexHomeRoot); err != nil {
		return FactoryConfig{}, fmt.Errorf("%w: runtime paths are not isolated", ErrFactoryConfig)
	}
	config.pathGuard = pathGuard
	if config.SkillInstaller == nil {
		if config.SkillRoot == "" || !filepath.IsAbs(config.SkillRoot) {
			return FactoryConfig{}, fmt.Errorf("%w: SkillRoot is required when SkillInstaller is nil", ErrFactoryConfig)
		}
		installer, err := aimcp.NewSkillInstaller(config.SkillRoot)
		if err != nil {
			return FactoryConfig{}, fmt.Errorf("%w: initialize skill installer: %v", ErrFactoryConfig, err)
		}
		config.SkillInstaller = installer
	}
	if config.TerminationGrace <= 0 {
		config.TerminationGrace = 2 * time.Second
	}
	if config.TerminationGrace > 30*time.Second {
		return FactoryConfig{}, fmt.Errorf("%w: TerminationGrace is too long", ErrFactoryConfig)
	}
	for name, value := range config.Environment {
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "=\x00\r\n") || strings.ContainsAny(value, "\x00\r\n") {
			return FactoryConfig{}, fmt.Errorf("%w: invalid environment value %q", ErrFactoryConfig, name)
		}
	}
	if config.GitBinary != "" && (!filepath.IsAbs(config.GitBinary) || !executable(config.GitBinary)) {
		return FactoryConfig{}, fmt.Errorf("%w: GitBinary must be executable and absolute", ErrFactoryConfig)
	}
	if config.MemoryRecorder == nil && config.MemoryStore != nil {
		config.MemoryRecorder = NewMemoryRecorder(config.MemoryStore)
	}
	return config, nil
}

// Open provisions one profile. The session provider is called before any
// token or model process is started; on every error its lease is released.
func (factory *Factory) Open(ctx context.Context, profile airuntime.Profile) (result aisupervisor.AgentSession, resultErr error) {
	started := time.Now()
	stage := "validate_factory"
	log.Printf("event=ai_factory_open_started profile=%q", profile.ID)
	defer func() {
		if resultErr != nil {
			var failure *aiprovision.OpenFailure
			if !errors.As(resultErr, &failure) {
				resultErr = &factoryOpenFailure{profile.ID, stage, factoryErrorCode(resultErr), time.Since(started), resultErr}
			}
		}
		code := factoryErrorCode(resultErr)
		outcome := "completed"
		if resultErr != nil {
			outcome = "failed"
		}
		log.Printf("event=ai_factory_open_%s profile=%q stage=%s duration_ms=%d error_code=%s error_type=%T", outcome, profile.ID, stage, time.Since(started).Milliseconds(), code, resultErr)
	}()

	if factory == nil {
		return aisupervisor.AgentSession{}, ErrFactoryConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return aisupervisor.AgentSession{}, err
	}
	if err := validateFactoryProfile(profile); err != nil {
		return aisupervisor.AgentSession{}, err
	}
	factory.mu.Lock()
	if factory.closed {
		factory.mu.Unlock()
		return aisupervisor.AgentSession{}, aisupervisor.ErrClosed
	}
	if factory.active[profile.ID] != nil {
		factory.mu.Unlock()
		return aisupervisor.AgentSession{}, ErrFactoryBusy
	}
	// Reserve the profile before invoking external providers. This closes the
	// duplicate-open race even when the provider takes a long time.
	reservation := &factoryLease{profileID: profile.ID}
	factory.active[profile.ID] = reservation
	factory.mu.Unlock()

	stage = "open_game_session"
	lease, err := factory.cfg.Sessions.Open(ctx, profile)
	if err != nil {
		factory.releaseReservation(profile.ID, reservation)
		// Provider diagnostics are intentionally not returned: a provider may
		// include account, password or upstream protocol material in its error.
		var details *aiprovision.OpenFailure
		if errors.As(err, &details) {
			return aisupervisor.AgentSession{}, errors.Join(ErrFactorySession, details)
		}
		return aisupervisor.AgentSession{}, ErrFactorySession
	}
	var gate *aicontrol.Gate
	var leaseContext context.Context
	cleanupState := &factoryCleanupState{
		gate:          lease.Gate,
		backend:       lease.Backend,
		tasks:         lease.Tasks,
		providerClose: lease.Close,
		release:       func() { factory.releaseReservation(profile.ID, reservation) },
		forgetMemory: func() {
			if factory.cfg.MemoryRecorder != nil {
				factory.cfg.MemoryRecorder.Forget(profile.ID)
			}
		},
	}
	cleanup := cleanupState.cleanup
	if !reservation.setClose(cleanup) {
		// Factory.Close won the handoff while the provider was opening. The
		// callback above has already released the provider lease and removed
		// the reservation; do not continue provisioning after shutdown.
		return aisupervisor.AgentSession{}, aisupervisor.ErrClosed
	}
	failed := true
	defer func() {
		if failed {
			cleanup()
		}
	}()

	if cleanupState.isClosed() {
		return aisupervisor.AgentSession{}, aisupervisor.ErrClosed
	}
	if lease.Close == nil && lease.Backend == nil && lease.Session == nil {
		return aisupervisor.AgentSession{}, ErrFactorySession
	}
	stage = "claim_local_gate"
	gate, leaseContext, err = factory.claimGate(ctx, profile, lease.Gate, lease.Backend != nil)
	if err != nil {
		return aisupervisor.AgentSession{}, err
	}
	if !cleanupState.setGate(gate) {
		return aisupervisor.AgentSession{}, aisupervisor.ErrClosed
	}
	if !cleanupState.setLeaseWatch(watchSessionLeaseGate(gate, lease.Session)) {
		return aisupervisor.AgentSession{}, aisupervisor.ErrClosed
	}
	// The protocol session observes the login name, while Account.ID is an
	// auth-database reference. Bind the gateway to the former so GameBackend's
	// authoritative snapshot check cannot reject or accidentally cross-bind a
	// character.
	stage = "bind_game_identity"
	binding := aimcp.Binding{AccountID: profile.Account.Username, ProfileID: profile.ID, CharacterID: profile.Character.ID,
		CharacterName: profile.Character.Name, Generation: gate.State().Generation}
	if err := binding.Validate(); err != nil {
		return aisupervisor.AgentSession{}, err
	}

	stage = "build_game_backend"
	backend := lease.Backend
	if backend == nil {
		if lease.Session == nil {
			return aisupervisor.AgentSession{}, fmt.Errorf("%w: provider returned neither Backend nor Session", ErrFactorySession)
		}
		scheduleStore := factory.cfg.ScheduleStore
		if scheduleStore == nil {
			scheduleStore = factory.cfg.MemoryStore
		}
		input := BackendInput{Profile: profile, Binding: binding, Gate: gate, Session: lease.Session,
			Tasks: lease.Tasks, Funding: lease.Funding, Knowledge: factory.cfg.Knowledge, Receipts: factory.cfg.Receipts,
			ScheduleStore: scheduleStore, MemoryStore: factory.cfg.MemoryStore, Lease: leaseContext, Wake: lease.Wake}
		if factory.cfg.Backend != nil {
			backend, err = factory.cfg.Backend(ctx, input)
		} else {
			if err := profile.Goal.CharacterBuild.Validate(); err != nil {
				return aisupervisor.AgentSession{}, err
			}
			backend = &GameBackend{Binding: binding, Gate: gate, Owner: aicontrol.Agent,
				Session: lease.Session, Funding: lease.Funding, Knowledge: factory.cfg.Knowledge,
				Tasks: lease.Tasks, Receipts: factory.cfg.Receipts, Schedules: scheduleStore, AgentNotes: factory.cfg.MemoryStore, CharacterBuild: profile.Goal.CharacterBuild.Clone(),
				OwnStateRefresh: &OwnStateRefresher{}}
		}
		if err != nil {
			// A custom builder may allocate a closable backend before
			// returning its diagnostic. Publish it to cleanup before the
			// error is sanitized so the authenticated lease cannot leak.
			if !isNilRuntimeValue(backend) && !cleanupState.setBackend(backend) {
				return aisupervisor.AgentSession{}, aisupervisor.ErrClosed
			}
			// Backend builders are supplied by the game service and may wrap
			// protocol/authentication errors. Keep them out of durable runtime
			// status and audit records.
			return aisupervisor.AgentSession{}, fmt.Errorf("%w: build game backend failed", ErrFactoryProvision)
		}
	}
	if backend == nil {
		return aisupervisor.AgentSession{}, fmt.Errorf("%w: backend is nil", ErrFactoryProvision)
	}
	if game, isGameBackend := backend.(*GameBackend); isGameBackend && game.AgentNotes == nil {
		game.AgentNotes = factory.cfg.MemoryStore
	}
	if factory.cfg.MemoryStore != nil {
		if _, supportsMemory := backend.(aimcp.AgentMemoryBackend); !supportsMemory {
			bound, bindErr := NewBoundAgentMemoryBackend(backend, factory.cfg.MemoryStore, binding, gate, aicontrol.Agent)
			if bindErr != nil {
				return aisupervisor.AgentSession{}, fmt.Errorf("%w: bind agent memory capability", ErrFactoryProvision)
			}
			backend = bound
		}
	}
	if !cleanupState.setBackend(backend) {
		return aisupervisor.AgentSession{}, aisupervisor.ErrClosed
	}
	if cleanupState.isClosed() {
		return aisupervisor.AgentSession{}, aisupervisor.ErrClosed
	}

	stage = "load_model"
	model, key, err := factory.loadModel(ctx, profile)
	if err != nil {
		return aisupervisor.AgentSession{}, err
	}
	if cleanupState.isClosed() {
		return aisupervisor.AgentSession{}, aisupervisor.ErrClosed
	}
	stage = "register_game_capability"
	token, revoke, err := factory.cfg.Gateway.Register(binding, backend)
	if err != nil {
		return aisupervisor.AgentSession{}, fmt.Errorf("%w: register game capability failed", ErrFactoryProvision)
	}
	if !cleanupState.setRevoke(revoke) {
		return aisupervisor.AgentSession{}, aisupervisor.ErrClosed
	}
	stage = "create_model_runner"
	var runner aisupervisor.Runner
	if factory.cfg.ContainerBroker != nil {
		runner, err = factory.newContainerRunner(profile, binding, model, key, token)
	} else {
		runner, err = factory.newLocalRunner(ctx, profile, binding, model, key, token, cleanupState)
	}
	if err != nil {
		return aisupervisor.AgentSession{}, err
	}
	runner = bindRunnerToSessionLease(runner, leaseContext, lease.Session)

	failed = false
	factory.mu.Lock()
	if factory.active[profile.ID] != reservation || factory.closed {
		factory.mu.Unlock()
		cleanup()
		return aisupervisor.AgentSession{}, aisupervisor.ErrClosed
	}
	factory.mu.Unlock()

	return aisupervisor.AgentSession{
		Runner: runner,
		Observe: func(observeCtx context.Context) (aisupervisor.Snapshot, error) {
			activeSource := any(lease.Tasks)
			if activeSource == nil {
				activeSource = backend
			}
			return supervisorSnapshotWithMemory(observeCtx, backend, binding, profile, activeSource, factory.cfg.MemoryRecorder)
		},
		Close: cleanup,
		Wake:  lease.Wake,
	}, nil
}

// newLocalRunner is the explicit local-development path. Container mode never
// calls it and therefore never materializes provider credentials on the host.
func (factory *Factory) newLocalRunner(ctx context.Context, profile airuntime.Profile, binding aimcp.Binding, model airuntime.ModelConfig, key, token string, cleanupState *factoryCleanupState) (aisupervisor.Runner, error) {
	workspace := filepath.Join(factory.cfg.WorkspaceRoot, profile.ID)
	codexHome := filepath.Join(factory.cfg.CodexHomeRoot, profile.ID)
	stateDir := filepath.Join(factory.cfg.StateRoot, profile.ID)
	if err := ensurePrivateTree(factory.cfg.pathGuard, factory.cfg.RuntimeRoot, factory.cfg.StateRoot, factory.cfg.WorkspaceRoot, factory.cfg.CodexHomeRoot, stateDir, codexHome, workspace); err != nil {
		return nil, fmt.Errorf("%w: runtime directories: %v", ErrFactoryProvision, err)
	}
	if cleanupState.isClosed() {
		return nil, aisupervisor.ErrClosed
	}
	if err := factory.installSkills(profile, workspace); err != nil {
		return nil, err
	}
	if cleanupState.isClosed() {
		return nil, aisupervisor.ErrClosed
	}
	tokenPath, err := prepareCapabilityToken(factory.cfg.pathGuard, stateDir)
	if err != nil {
		return nil, fmt.Errorf("%w: token file: %v", ErrFactoryProvision, err)
	}
	if !cleanupState.setTokenPath(tokenPath) {
		return nil, aisupervisor.ErrClosed
	}
	if err := replaceCapabilityToken(tokenPath, token); err != nil {
		return nil, fmt.Errorf("%w: persist game capability: %v", ErrFactoryProvision, err)
	}
	if cleanupState.isClosed() {
		_ = os.Remove(tokenPath)
		return nil, aisupervisor.ErrClosed
	}

	runtimeFiles, err := aimodels.Materialize(codexHome, aimodels.RuntimeSettings{
		Model: model.Model, Provider: model.Provider, BaseURL: model.BaseURL,
		ReasoningEffort: model.ReasoningEffort, APIKey: key, MCPCommand: factory.cfg.MCPBinary,
		MCPArgs: []string{"serve"}, MCPEnv: map[string]string{
			"STONEAGE_AI_ENDPOINT":           factory.cfg.GatewayEndpoint,
			"STONEAGE_AI_TOKEN_FILE":         tokenPath,
			"STONEAGE_AI_CHARACTER_ID":       profile.Character.ID,
			"STONEAGE_AI_CHARACTER_NAME":     profile.Character.Name,
			"STONEAGE_AI_CONTROL_GENERATION": strconv.FormatUint(binding.Generation, 10),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: Codex config generation failed", ErrFactoryProvision)
	}
	if cleanupState.isClosed() {
		return nil, aisupervisor.ErrClosed
	}

	requireVersion := true
	if factory.cfg.RequireCodexVersion != nil {
		requireVersion = *factory.cfg.RequireCodexVersion
	}
	if requireVersion {
		if err := factory.checkCodexVersion(ctx); err != nil {
			return nil, err
		}
	}
	if cleanupState.isClosed() {
		return nil, aisupervisor.ErrClosed
	}
	runner, err := aicodex.New(aicodex.Config{
		Binary: factory.cfg.CodexBinary, WorkspaceRoot: factory.cfg.WorkspaceRoot,
		StateRoot: factory.cfg.StateRoot, CodexHome: codexHome,
		ConfigFile: runtimeFiles.ConfigPath, Model: model.Model,
		Provider:     aicodex.ProviderConfig{Name: runtimeFiles.ProviderID, BaseURL: model.BaseURL, WireAPI: model.WireAPI},
		ModelCatalog: runtimeFiles.CatalogPath, ReasoningEffort: model.ReasoningEffort,
		WebSearch: "disabled", GitBinary: factory.cfg.GitBinary,
		Environment: factory.cfg.Environment, TerminationGrace: factory.cfg.TerminationGrace,
		TurnTimeout: model.Timeout,
		SecretEnv:   "", Limits: aicodex.Limits{MaxStdoutBytes: maxCodexOutput(model.MaxOutputTokens)},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: create Codex runner failed", ErrFactoryProvision)
	}

	return runner, nil
}

func (factory *Factory) newContainerRunner(profile airuntime.Profile, binding aimcp.Binding, model airuntime.ModelConfig, key, token string) (aisupervisor.Runner, error) {
	skills := make([]airunner.Skill, 0, len(profile.Skills))
	for _, skill := range profile.Skills {
		if skill.Kind != "" && skill.Kind != airuntime.SkillKindNative {
			return nil, fmt.Errorf("%w: skill is not native", ErrFactoryProvision)
		}
		spec, err := factory.cfg.SkillInstaller.Verify(skill.Name)
		if err != nil || (skill.Version != "" && skill.Version != spec.Version) || !digestMatches(skill.Digest, spec.SHA256) {
			return nil, fmt.Errorf("%w: skill does not match catalog", ErrFactoryProvision)
		}
		skills = append(skills, airunner.Skill{Name: spec.Name, Version: spec.Version, Digest: "sha256:" + spec.SHA256})
	}
	stateDir := filepath.Join(factory.cfg.StateRoot, profile.ID)
	if err := ensurePrivateTree(factory.cfg.pathGuard, factory.cfg.RuntimeRoot, factory.cfg.StateRoot, stateDir); err != nil {
		return nil, fmt.Errorf("%w: container transport state is unavailable", ErrFactoryProvision)
	}
	return NewContainerRunner(ContainerRunnerConfig{
		ProfileID: profile.ID, StateRoot: stateDir, Broker: factory.cfg.ContainerBroker,
		TurnTimeout: model.Timeout, TurnTimeoutGrace: aibroker.DefaultStopTimeout,
		RequestTemplate: airunner.ExecuteRequest{
			ProfileID: profile.ID,
			Model: airunner.Model{Provider: model.Provider, BaseURL: model.BaseURL, Model: model.Model,
				ReasoningEffort: model.ReasoningEffort, APIKey: key},
			Skills: skills,
			MCP: airunner.MCP{Endpoint: factory.cfg.GatewayEndpoint, Token: token,
				CharacterID: binding.CharacterID, CharacterName: binding.CharacterName, Generation: binding.Generation},
		},
	})
}

func (factory *Factory) claimGate(ctx context.Context, profile airuntime.Profile, supplied *aicontrol.Gate, backendAlreadyBound bool) (*aicontrol.Gate, context.Context, error) {
	gate := supplied
	var err error
	if gate == nil && factory.cfg.Gate != nil {
		gate, err = factory.cfg.Gate(ctx, profile)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: gate provider failed", ErrFactoryProvision)
		}
	}
	if gate == nil {
		gate = aicontrol.New()
	}
	state := gate.State()
	// A provider that returns a fully composed backend may already have
	// claimed Agent ownership while it was attaching the game session. The
	// backend's binding then uses this exact generation, so do not rotate it.
	if state.Mode == aicontrol.Agent && backendAlreadyBound {
		return gate, context.Background(), nil
	}
	if state.Mode != aicontrol.Manual && state.Mode != aicontrol.Paused {
		return nil, nil, fmt.Errorf("%w: character is controlled by %s", ErrFactoryProvision, state.Mode)
	}
	started, leaseContext, err := gate.Switch(state.Generation, aicontrol.Agent, "AI agent session")
	if err != nil {
		return nil, nil, fmt.Errorf("%w: claim agent control failed", ErrFactoryProvision)
	}
	if started.Generation == 0 || leaseContext == nil {
		return nil, nil, fmt.Errorf("%w: invalid agent lease", ErrFactoryProvision)
	}
	return gate, leaseContext, nil
}

func (factory *Factory) loadModel(ctx context.Context, profile airuntime.Profile) (airuntime.ModelConfig, string, error) {
	id := strings.TrimSpace(profile.ModelConfigID)
	var err error
	if id == "" {
		id, err = factory.cfg.Models.GetDefaultModelConfigID(ctx)
		if err != nil || id == "" {
			return airuntime.ModelConfig{}, "", fmt.Errorf("%w: no default model configuration", ErrFactoryModel)
		}
	}
	model, err := factory.cfg.Models.GetModelConfig(ctx, id)
	if err != nil {
		return airuntime.ModelConfig{}, "", fmt.Errorf("%w: read model configuration failed", ErrFactoryModel)
	}
	if model.Backend != airuntime.ModelBackendCodex || strings.TrimSpace(model.Provider) == "" || strings.TrimSpace(model.Model) == "" || strings.TrimSpace(model.BaseURL) == "" {
		return airuntime.ModelConfig{}, "", fmt.Errorf("%w: unsupported Codex model configuration", ErrFactoryModel)
	}
	wireAPI := strings.TrimSpace(model.WireAPI)
	if wireAPI == "" {
		wireAPI = airuntime.ModelProviderResponses
	}
	// Codex and the game MCP integration use the Responses protocol.
	if wireAPI != airuntime.ModelProviderResponses {
		return airuntime.ModelConfig{}, "", fmt.Errorf("%w: unsupported Codex wire API", ErrFactoryModel)
	}
	model.WireAPI = wireAPI
	key, err := factory.cfg.Secrets.ReadKey(model.ID)
	if err != nil || strings.TrimSpace(key) == "" {
		return airuntime.ModelConfig{}, "", fmt.Errorf("%w: model %q", ErrFactoryCredentials, model.ID)
	}
	return model, key, nil
}

func (factory *Factory) installSkills(profile airuntime.Profile, workspace string) error {
	names := make([]string, 0, len(profile.Skills))
	for _, skill := range profile.Skills {
		if skill.Kind != "" && skill.Kind != airuntime.SkillKindNative {
			return fmt.Errorf("%w: skill %q is not a native Codex skill", ErrFactoryProvision, skill.Name)
		}
		spec, err := factory.cfg.SkillInstaller.Verify(skill.Name)
		if err != nil {
			return fmt.Errorf("%w: install skill %q: %v", ErrFactoryProvision, skill.Name, err)
		}
		if skill.Version != "" && skill.Version != spec.Version {
			return fmt.Errorf("%w: skill %q version is not allowlisted", ErrFactoryProvision, skill.Name)
		}
		if !digestMatches(skill.Digest, spec.SHA256) {
			return fmt.Errorf("%w: skill %q digest does not match catalog", ErrFactoryProvision, skill.Name)
		}
		names = append(names, spec.Name)
	}
	return factory.cfg.SkillInstaller.Reconcile(names, workspace)
}

func digestMatches(value, hash string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	value = strings.TrimPrefix(strings.ToLower(value), "sha256:")
	return value == strings.ToLower(hash)
}

func (factory *Factory) checkCodexVersion(ctx context.Context) error {
	versionHome := filepath.Join(factory.cfg.RuntimeRoot, "version-home")
	if err := ensurePrivateTree(factory.cfg.pathGuard, versionHome); err != nil {
		return fmt.Errorf("%w: version home is not isolated", ErrFactoryRuntime)
	}
	command := exec.CommandContext(ctx, factory.cfg.CodexBinary, "--version")
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin", "HOME=" + versionHome}
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("%w: check Codex version failed", ErrFactoryRuntime)
	}
	if err := aimodels.CheckCodexVersion(string(output)); err != nil {
		return fmt.Errorf("%w: %v", ErrFactoryRuntime, err)
	}
	return nil
}

func supervisorSnapshot(ctx context.Context, backend aimcp.Backend, binding aimcp.Binding, profile airuntime.Profile, tasks any) (aisupervisor.Snapshot, error) {
	return supervisorSnapshotWithMemory(ctx, backend, binding, profile, tasks, nil)
}

func supervisorSnapshotWithMemory(ctx context.Context, backend aimcp.Backend, binding aimcp.Binding, profile airuntime.Profile, tasks any, recorder *MemoryRecorder) (aisupervisor.Snapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	observation, err := backend.Observe(ctx, binding)
	if err != nil {
		return aisupervisor.Snapshot{}, err
	}
	if recorder != nil {
		if err := recorder.Observe(ctx, profile.ID, observation); err != nil {
			return aisupervisor.Snapshot{}, fmt.Errorf("record game observation: %w", err)
		}
	}
	data, err := jsonMarshalSupervisorContext(ctx, backend, observation)
	if err != nil {
		return aisupervisor.Snapshot{}, err
	}
	active, err := activeTaskHandles(ctx, tasks)
	if err != nil {
		return aisupervisor.Snapshot{}, err
	}
	goalComplete := goalReached(observation, profile.Goal)
	if profile.Goal.CharacterBuild != nil && len(active) != 0 {
		goalComplete = false
	}
	return aisupervisor.Snapshot{
		GameReady:    observation.Connected && observation.Ready,
		GoalComplete: goalComplete,
		ProgressKey:  progressKey(observation),
		ActiveTasks:  active,
		Context:      data,
	}, nil
}

func progressKey(observation aimcp.Observation) string {
	parts := []string{
		observation.Phase,
		strconv.Itoa(observation.Floor), strconv.Itoa(observation.X), strconv.Itoa(observation.Y),
		strconv.Itoa(observation.Character.Level), strconv.Itoa(observation.Character.HP),
		strconv.FormatBool(observation.Battle.Active), strconv.Itoa(observation.Battle.Turn),
		strconv.FormatBool(observation.Battle.CommandReady),
		observation.Battle.Result,
	}
	for _, pet := range observation.Pets {
		parts = append(parts, pet.ID, strconv.Itoa(pet.Level), strconv.Itoa(pet.HP), strconv.Itoa(pet.MaxHP), strconv.Itoa(pet.UseFlag))
	}
	if observation.ActiveWindow != nil {
		parts = append(parts, strconv.Itoa(observation.ActiveWindow.Type), strconv.Itoa(observation.ActiveWindow.Sequence), strconv.Itoa(observation.ActiveWindow.ObjectID), strconv.FormatBool(observation.ActiveWindow.Open))
	}
	// Social participation, inventory changes and attribute preparation are
	// real gameplay progress even when position/HP/level do not change. Exclude
	// revision counters and chat traffic, which can advance without any result.
	state, _ := json.Marshal(struct {
		Gold        int64
		Inventory   map[string]int
		Flags       map[string]bool
		OwnProgress map[string]int
		Party       []aimcp.PartyMember
	}{observation.Gold, observation.Inventory, observation.Flags, observation.OwnProgress, observation.Party})
	parts = append(parts, fmt.Sprintf("%x", sha256.Sum256(state)))
	if trade := observation.Trade; trade != nil {
		// Only server-observed stages count as progress. Local submission
		// counters must not keep a stalled agent alive indefinitely.
		peerState, _ := json.Marshal(struct {
			Active, Closed, PeerLocked, PeerFinal bool
			PeerID                                int32
			PeerName                              string
			Offers                                [2]aimcp.TradeOffer
			Pet                                   *aimcp.TradeOffer
		}{trade.Active, trade.Closed, trade.PeerLocked, trade.PeerFinal,
			trade.PeerID, trade.PeerName, trade.PeerOffers, trade.PeerPet})
		parts = append(parts, fmt.Sprintf("%x", sha256.Sum256(peerState)))
	}
	return strings.Join(parts, "|")
}

// activeTaskSource is intentionally optional so existing TaskController
// implementations remain valid. Tasks added by the deterministic executor
// implement this method; an absent method means there is no task controller
// attached to this character.
type activeTaskSource interface {
	Active(context.Context) ([]aimcp.TaskReceipt, error)
}

// uncertainActionSource exposes durable, unknown action receipts to a new
// Codex turn. They are deliberately separate from ActiveTasks: an unknown
// write must be reconciled by observing the game and must never keep the
// supervisor waiting forever as if it were a running deterministic task.
type uncertainActionSource interface {
	PendingActions(context.Context) ([]aimcp.TaskReceipt, error)
}

func activeTaskHandles(ctx context.Context, tasks any) ([]aisupervisor.TaskHandle, error) {
	source, ok := tasks.(activeTaskSource)
	if !ok {
		return nil, nil
	}
	receipts, err := source.Active(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]aisupervisor.TaskHandle, 0, len(receipts))
	for _, receipt := range receipts {
		if receipt.Handle == "" {
			continue
		}
		contextData, _ := json.Marshal(map[string]string{"state": receipt.State, "reason": receipt.Reason})
		result = append(result, aisupervisor.TaskHandle{
			Handle: receipt.Handle, Status: receipt.Status,
			Evidence: append([]byte(nil), receipt.Evidence...), Context: contextData,
		})
	}
	return result, nil
}

func jsonMarshalSupervisorContext(ctx context.Context, backend aimcp.Backend, observation aimcp.Observation) ([]byte, error) {
	data, err := jsonMarshalObservation(observation)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	uncertain := make([]aimcp.TaskReceipt, 0)
	if source, ok := backend.(uncertainActionSource); ok {
		receipts, err := source.PendingActions(ctx)
		if err != nil {
			return nil, err
		}
		uncertain = append(uncertain, receipts...)
	}
	uncertainJSON, err := json.Marshal(uncertain)
	if err != nil {
		return nil, err
	}
	fields["uncertain_actions"] = uncertainJSON
	return json.Marshal(fields)
}

func goalReached(observation aimcp.Observation, goal airuntime.Goal) bool {
	if goal.Kind == "life" || !observation.Connected || !observation.Ready || !goal.StopWhenCompleted || goal.TargetLevel <= 0 {
		return false
	}
	if goal.CharacterBuild != nil && !observation.Flags["build:settled"] {
		return false
	}
	targetKind := strings.ToLower(strings.TrimSpace(goal.Metadata["target_kind"]))
	if targetKind == "" {
		targetKind = strings.ToLower(strings.TrimSpace(goal.Metadata["target_type"]))
	}
	if targetKind == "" {
		switch strings.ToLower(strings.TrimSpace(goal.Kind)) {
		case "", "level", "character", "character_level":
			targetKind = "character"
		case "pet", "pet_level":
			targetKind = "pet"
		}
	}
	switch targetKind {
	case "pet":
		if goal.TargetCharacterID == "" {
			return false
		}
		for _, pet := range observation.Pets {
			if pet.ID == goal.TargetCharacterID {
				return pet.Level >= goal.TargetLevel
			}
		}
		return false
	case "", "character":
		// Character goals are the default. A configured target character ID
		// must match the bound character before a level can complete.
	default:
		return false
	}
	// Character goals are the default. A configured target character ID must
	// match the bound character before a level can complete the profile.
	if goal.TargetCharacterID != "" && goal.TargetCharacterID != observation.CharacterID {
		return false
	}
	return observation.Character.Level >= goal.TargetLevel
}

func (factory *Factory) releaseReservation(profileID string, reservation *factoryLease) {
	factory.mu.Lock()
	if factory.active[profileID] == reservation {
		delete(factory.active, profileID)
	}
	factory.mu.Unlock()
}

// Close releases every provider lease. The supervisor normally calls each
// AgentSession.Close first; this method also makes process shutdown safe when
// a provider was opened but never handed to a supervisor.
func (factory *Factory) Close() error {
	if factory == nil {
		return nil
	}
	factory.mu.Lock()
	if factory.closed {
		factory.mu.Unlock()
		return nil
	}
	factory.closed = true
	leases := make([]*factoryLease, 0, len(factory.active))
	for _, lease := range factory.active {
		leases = append(leases, lease)
	}
	factory.mu.Unlock()
	for _, lease := range leases {
		lease.closeNow()
	}
	return nil
}

func validateFactoryProfile(profile airuntime.Profile) error {
	if !factoryProfileIDPattern.MatchString(profile.ID) {
		return fmt.Errorf("%w: invalid profile ID", ErrFactoryProvision)
	}
	if strings.TrimSpace(profile.Account.ID) == "" || strings.TrimSpace(profile.Account.Username) == "" || strings.TrimSpace(profile.Character.ID) == "" {
		return fmt.Errorf("%w: account id, login name and character identity are required", ErrFactoryProvision)
	}
	return nil
}

func validateEndpoint(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/v1/game" {
		return errors.New("must be an http(s) URL ending in /v1/game")
	}
	return nil
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}

func cloneEnvironment(environment map[string]string) map[string]string {
	if environment == nil {
		return nil
	}
	result := make(map[string]string, len(environment))
	for key, value := range environment {
		result[key] = value
	}
	return result
}

func ensurePrivateTree(guard runtimepath.Guard, paths ...string) error {
	for _, path := range paths {
		if path == "" {
			continue
		}
		if err := guard.Check(path); err != nil {
			return fmt.Errorf("private runtime path is unsafe: %w", err)
		}
		if err := os.MkdirAll(path, 0700); err != nil {
			return err
		}
		if err := guard.Check(path); err != nil {
			return fmt.Errorf("private runtime path is unsafe: %w", err)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("private runtime path is unsafe")
		}
		if err := os.Chmod(path, 0700); err != nil {
			return err
		}
		if err := guard.Check(path); err != nil {
			return fmt.Errorf("private runtime path is unsafe: %w", err)
		}
	}
	return nil
}

func prepareCapabilityToken(guard runtimepath.Guard, directory string) (string, error) {
	if err := ensurePrivateTree(guard, directory); err != nil {
		return "", err
	}
	path := filepath.Join(directory, "game-capability.token")
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", errors.New("capability token target is unsafe")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, nil
}

func replaceCapabilityToken(path, token string) error {
	if len(token) == 0 {
		return errors.New("empty capability token")
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".game-capability-*-")
	if err != nil {
		return err
	}
	tmpName := temporary.Name()
	defer os.Remove(tmpName)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(token + "\n"); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func maxCodexOutput(value int) int64 {
	if value <= 0 {
		return 32 << 20
	}
	// JSONL framing, tool traces and UTF-8 expansion make a token-to-byte
	// conversion inherently approximate. Keep a conservative bounded cap.
	const max = int64(256 << 20)
	bytes := int64(value) * 8
	if bytes < 1<<20 {
		bytes = 1 << 20
	}
	if bytes > max {
		bytes = max
	}
	return bytes
}

// jsonMarshalObservation is kept as a small seam so supervisor snapshots
// always contain a copy and never expose mutable backend-owned buffers.
func jsonMarshalObservation(value aimcp.Observation) ([]byte, error) {
	return json.Marshal(value)
}
