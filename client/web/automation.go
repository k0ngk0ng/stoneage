package main

import (
	"context"
	"errors"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiservice"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
)

// Automation is the bridge boundary for a real deterministic task/leveling
// executor (or a future agent runtime). The HTTP layer supplies only an
// authenticated character session, a validated control generation and a
// user-selected configuration. It deliberately does not accept arbitrary
// wire steps or actions from the browser.
//
// Start must return an error when it cannot create a real run. A nil Handler
// executor therefore produces an honest 503 from the start endpoint rather
// than a successful-looking, idle automation state.
type Automation interface {
	Start(context.Context, *AutomationSession, AutomationStartRequest) (AutomationHandle, error)
}

// automationTaskDirectoryProvider is optional so existing test adapters and
// other automation implementations only need to support starting a run. The
// directory endpoint exposes reviewed task metadata; it never accepts a
// browser-supplied action or precompiled plan.
type automationTaskDirectoryProvider interface {
	TaskDirectory(context.Context) (AutomationTaskDirectory, error)
}

// AutomationTaskDirectory is the safe task catalog returned to the Web UI.
// It intentionally contains no source paths, evidence bytes, protocol
// actions, or server-internal configuration.
type AutomationTaskDirectory struct {
	Supplies          []aiservice.StockOfferSummary `json:"supplies"`
	KnowledgeRevision string                        `json:"knowledge_revision"`
	Tasks             []AutomationTaskSummary       `json:"tasks"`
}

type AutomationTaskSummary struct {
	ID               string                     `json:"id"`
	Name             string                     `json:"name"`
	Description      string                     `json:"description,omitempty"`
	PreparationNotes string                     `json:"preparation_notes,omitempty"`
	ReviewBlockers   []string                   `json:"review_blockers"`
	Requirements     []string                   `json:"requirements"`
	Dependencies     []AutomationTaskDependency `json:"dependencies"`
	RequiresPet      bool                       `json:"requires_pet"`
}

type AutomationTaskDependency struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// AutomationHandle is intentionally open so the deterministic runner can
// expose only the lifecycle operations it supports. The bridge discovers the
// optional interfaces below and still uses Gate cancellation as the common
// stop mechanism.
type AutomationHandle interface{}

// AutomationRecovery is an offer, not a running task. Recovery is explicit
// and is scoped by the executor to the newly authenticated character/line.
type AutomationRecovery struct {
	Handle string         `json:"handle"`
	Mode   aicontrol.Mode `json:"mode"`
	Reason string         `json:"reason,omitempty"`
}
type automationRecoveryProvider interface {
	Recovery(context.Context, *AutomationSession) (*AutomationRecovery, error)
	Recover(context.Context, *AutomationSession, string) (AutomationHandle, error)
	DiscardRecovery(context.Context, *AutomationSession, string) error
}
type automationDetacher interface{ Detach(context.Context) error }

type automationStopper interface {
	Stop(context.Context) error
}

type automationPauser interface {
	Pause(context.Context) error
}

type automationResumer interface {
	Resume(context.Context) error
}

type automationActivator interface {
	Activate()
}

// AutomationStartRequest is the only user-controlled input that reaches the
// executor. Config is an opaque, bounded JSON document containing selectors
// such as a task ID, level targets and budget limits; it must be compiled and
// validated by the injected executor against its knowledge revision.
type AutomationStartRequest struct {
	SessionID  string
	Mode       aicontrol.Mode
	Generation uint64
	Reason     string
	Config     AutomationConfig
}

// AutomationConfig contains selectors and limits only. A browser cannot
// provide a protocol step, skill argument or precompiled plan; the executor
// resolves TaskID/Targets against its server-side knowledge revision.
type AutomationSupply struct {
	Item         string `json:"item"`
	TargetCount  int    `json:"target_count"`
	ReorderCount int    `json:"reorder_count"`
}

type AutomationConfig struct {
	Supply              *AutomationSupply      `json:"supply,omitempty"`
	TaskID              string                 `json:"task_id,omitempty"`
	IncludeDependencies bool                   `json:"include_dependencies,omitempty"`
	SelectedPetID       string                 `json:"selected_pet_id,omitempty"`
	CharacterBuild      *characterbuild.Policy `json:"character_build,omitempty"`
	Targets             []AutomationTarget     `json:"targets,omitempty"`
	TargetPolicy        string                 `json:"target_policy,omitempty"`
	Budget              AutomationBudget       `json:"budget"`
	MaximumSeconds      int                    `json:"maximum_seconds"`
	MaximumDeaths       int                    `json:"maximum_deaths"`
	OfflineContinue     bool                   `json:"offline_continue"`
}

type AutomationTarget struct {
	Kind  string `json:"kind"`
	ID    string `json:"id,omitempty"`
	Level int    `json:"level"`
}

type AutomationBudget struct {
	Minimum      int64 `json:"minimum"`
	ExpectedLow  int64 `json:"expected_low"`
	ExpectedHigh int64 `json:"expected_high"`
	Reserve      int64 `json:"reserve"`
	MaximumSpend int64 `json:"maximum_spend"`
}

// AutomationPreview is optional. A configured executor can use it to return
// a knowledge-backed cost/condition preview; absence is reported as
// unavailable instead of fabricating an estimate in the browser.
type AutomationPreview struct {
	TaskOrder       []string         `json:"task_order,omitempty"`
	Ready           bool             `json:"ready"`
	Problems        []string         `json:"problems,omitempty"`
	Budget          AutomationBudget `json:"budget"`
	AlreadyComplete bool             `json:"already_complete"`
}

type automationPreviewer interface {
	Preview(context.Context, *AutomationSession, AutomationStartRequest) (AutomationPreview, error)
}

// AutomationSession exposes the character connection without exposing the
// mutable tcpSession itself. Executors can observe the current ownership
// state and submit a packet through the same generation-fenced path as the
// browser. Game semantics remain the executor's responsibility.
type AutomationSession struct {
	ID string

	session *tcpSession
	// mode/generation identify the owner captured when this bound session was
	// created. They are immutable fencing tokens: a takeover or a later run
	// cannot upgrade an old executor reference into a new lease.
	mode       aicontrol.Mode
	generation uint64
}

func (s *AutomationSession) State() aicontrol.State {
	if s == nil || s.session == nil || s.session.gate == nil {
		return aicontrol.State{}
	}
	return s.session.gate.State()
}

func (s *AutomationSession) Dispatch(ctx context.Context, generation uint64, owner aicontrol.Mode, send func(context.Context) error) error {
	if s == nil || s.session == nil || s.session.gate == nil {
		return errors.New("automation session is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return s.session.dispatch(ctx, generation, owner, send)
}

// UsesControlGate marks this adapter as already fencing every outbound action
// through the Web session's Gate. Services which add their own Gate wrapper
// must avoid nesting it around ExecuteExpected, because Gate.Dispatch holds
// its mutex while invoking the callback.
func (s *AutomationSession) UsesControlGate() {}

func (s *AutomationSession) Send(ctx context.Context, generation uint64, owner aicontrol.Mode, packet []byte) error {
	if s == nil || s.session == nil || s.session.gate == nil {
		return errors.New("automation session is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	copyPacket := append([]byte(nil), packet...)
	return s.session.dispatch(ctx, generation, owner, func(context.Context) error {
		return s.session.write(copyPacket)
	})
}

// Observe returns state parsed from server packets on the bound Web TCP
// session. It never consults browser-side app state or opens another login.
func (s *AutomationSession) Observe(ctx context.Context) (aigame.Snapshot, error) {
	if s == nil || s.session == nil {
		return aigame.Snapshot{}, errors.New("automation session is unavailable")
	}
	return s.session.observeAuthoritative(ctx)
}

// ExecuteExpected validates and encodes a typed action with aigame, then
// writes it through the existing generation-fenced Web session. The owner
// and generation are the immutable lease captured by this session; an old
// executor reference cannot borrow a later run's token after takeover.
func (s *AutomationSession) ExecuteExpected(ctx context.Context, expectedRevision uint64, action aigame.Action) error {
	if s == nil || s.session == nil || s.session.gate == nil {
		return errors.New("automation session is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	owner, generation := s.mode, s.generation
	if generation == 0 {
		return aicontrol.ErrOwner
	}
	if owner != aicontrol.Quest && owner != aicontrol.Leveling && owner != aicontrol.Agent && owner != aicontrol.Battle {
		return aicontrol.ErrOwner
	}
	return s.session.dispatch(ctx, generation, owner, func(sendCtx context.Context) error {
		s.session.authoritativeMu.RLock()
		observer := s.session.authoritative
		observerErr := s.session.authoritativeErr
		s.session.authoritativeMu.RUnlock()
		if observerErr != nil {
			return observerErr
		}
		if observer == nil {
			return errors.New("authoritative observer is unavailable")
		}
		return observer.ExecuteExpected(sendCtx, expectedRevision, action)
	})
}

var errAutomationUnavailable = errors.New("automation executor unavailable")

// SetAutomation installs the real executor used by task/leveling start
// requests. It is safe to call during handler setup or while other sessions
// are being observed; existing runs retain their handle.
func (h *Handler) SetAutomation(executor Automation) {
	if h == nil {
		return
	}
	h.automationMu.Lock()
	h.automation = executor
	h.automationMu.Unlock()
}

func (h *Handler) automationExecutor() Automation {
	if h == nil {
		return nil
	}
	h.automationMu.RLock()
	executor := h.automation
	h.automationMu.RUnlock()
	return executor
}

func (s *tcpSession) setAutomation(handle AutomationHandle, mode aicontrol.Mode, generation uint64) bool {
	s.automationMu.Lock()
	defer s.automationMu.Unlock()
	if s.gate == nil || s.gate.State().Generation != generation || s.gate.State().Mode != mode {
		return false
	}
	s.automationHandle = handle
	s.automationMode = mode
	s.automationGen = generation
	return true
}

func (s *tcpSession) automationStatus() (AutomationHandle, aicontrol.Mode, uint64) {
	s.automationMu.Lock()
	handle, mode, generation := s.automationHandle, s.automationMode, s.automationGen
	s.automationMu.Unlock()
	return handle, mode, generation
}

func (s *tcpSession) clearAutomation(generation uint64) AutomationHandle {
	s.automationMu.Lock()
	defer s.automationMu.Unlock()
	if generation != 0 && s.automationGen != generation {
		return nil
	}
	handle := s.automationHandle
	s.automationHandle = nil
	s.automationMode = ""
	s.automationGen = 0
	return handle
}

func stopAutomationHandle(handle AutomationHandle) {
	stopper, ok := handle.(automationStopper)
	if !ok || stopper == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultWriteTimeout)
	defer cancel()
	_ = stopper.Stop(ctx)
}

func activateAutomationHandle(handle AutomationHandle) {
	if activator, ok := handle.(automationActivator); ok && activator != nil {
		activator.Activate()
	}
}

// Disconnect revokes the Gate before this hook. An uncertain detach must not
// fall through to Stop, which would destroy the checkpoint being preserved.
func detachAutomationHandle(handle AutomationHandle) {
	if detacher, ok := handle.(automationDetacher); ok {
		ctx, cancel := context.WithTimeout(context.Background(), defaultWriteTimeout)
		defer cancel()
		_ = detacher.Detach(ctx)
		return
	}
	stopAutomationHandle(handle)
}
