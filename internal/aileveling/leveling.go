// Package aileveling runs a deterministic, server-observed leveling loop.
//
// The package is deliberately small at the game boundary. Game is an
// authenticated aigame session and Navigator supplies a route that has
// already been checked by the navigation/knowledge layer. This controller
// never constructs tile routes, writes game state directly, or treats a
// submitted packet as a confirmed result.
package aileveling

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

var (
	ErrInvalidCoordinator = errors.New("aileveling: invalid coordinator")
	ErrInvalidRequest     = errors.New("aileveling: invalid leveling request")
	ErrTargetNotFound     = errors.New("aileveling: leveling target is not present")
	ErrAlreadyRunning     = errors.New("aileveling: leveling run is already active")
	ErrRunNotFound        = errors.New("aileveling: leveling run is not found")
	ErrNotRunning         = errors.New("aileveling: leveling run is not running")
	ErrNoNavigation       = errors.New("aileveling: verified navigation is unavailable")
	ErrNoRecovery         = errors.New("aileveling: verified recovery route is unavailable")
	ErrUnknownDelivery    = errors.New("aileveling: submitted operation has an unknown result")
)

// Game is the only mutable game dependency. ExecuteExpected must perform the
// revision check at the protocol submission boundary; the controller passes
// the revision from the immediately preceding observation. ErrStaleRevision
// must mean rejection before any packet write, never an uncertain delivery.
type Game interface {
	Observe(context.Context) (aigame.Snapshot, error)
	ExecuteExpected(context.Context, uint64, aigame.Action) error
}

// NavigationRequest is passed to Navigator for each world observation. The
// navigator owns area selection, map evidence and tile collision knowledge.
// Parameters are selectors only and are never interpreted as protocol data
// by this package.
type NavigationRequest struct {
	Targets      []Target
	TargetPolicy string
	AreaID       int
	Parameters   map[string]json.RawMessage
}

// Navigation is one result from a verified navigation provider.
//
// InArea means that no movement packet is needed and the controller should
// wait for a server encounter. When InArea is false, Route must be supplied
// by the navigator. The controller sends the route from the server-observed
// current coordinate; it does not calculate a route or claim tile support.
// When WarpDestinationKnown is true, the route enters a verified warp and the
// exact post-warp point is required before the movement is acknowledged.
type Navigation struct {
	Ready                bool
	InArea               bool
	Route                string
	Destination          aigame.Point
	WarpDestination      aigame.Point
	WarpDestinationKnown bool
	// WarpEventType requests one native EV after the current authoritative
	// position reaches the verified warp source. It is only valid with an empty
	// Route and Destination equal to the current source tile; the following
	// pending warp is acknowledged by WarpDestination alone.
	WarpEventType int32
	CostKnown     bool
	MaximumCost   int64
	Reason        string
}

// Navigator returns a verified movement route for the current observation.
// It may be backed by the project's navigation package. A zero Navigation
// with no error is treated as unavailable unless InArea or Route is set, so
// simple adapters can omit Ready when they have no separate readiness flag.
type Navigator interface {
	Next(context.Context, aigame.Snapshot, NavigationRequest) (Navigation, error)
}

// HealthRecovery uses one existing, reviewed supply and confirms its server
// outcome before returning nil. Implementations fence writes to the bound
// ownership generation. They must not purchase items or retry uncertain use.
type HealthRecovery interface {
	Heal(context.Context) error
}

// PetHealthRecovery is optional. The adapter must refresh and bind stable pet
// identity, consume at most one reviewed item, and confirm that same pet's HP
// increase plus item consumption before returning nil.
type PetHealthRecovery interface {
	HealPet(context.Context, aigame.PetSnapshot) error
}

// CharacterPreparation performs at most one server-observed character
// preparation step for the supplied snapshot. It returns true when the
// current snapshot has no eligible point left to allocate and leveling may
// continue (for example, reserve points may remain).
// A false result means the adapter is waiting for (or has submitted) one
// allocation; the coordinator must stop this tick and let the adapter's own
// receipt/reconciliation fence decide when another step is safe.
type CharacterPreparation interface {
	PrepareCharacter(context.Context, aigame.Snapshot) (bool, error)
}

// NavigatorFunc adapts a function to Navigator.
type NavigatorFunc func(context.Context, aigame.Snapshot, NavigationRequest) (Navigation, error)

func (f NavigatorFunc) Next(ctx context.Context, snapshot aigame.Snapshot, request NavigationRequest) (Navigation, error) {
	if f == nil {
		return Navigation{}, ErrNoNavigation
	}
	return f(ctx, snapshot, request)
}

// Target is the stable leveling target identity. Pet IDs must be stable
// server-side IDs; a session-local slot or list position is rejected.
type Target = automation.Target

// StartRequest contains selectors and execution limits. It deliberately has
// no funding or permission field. UnlimitedFunds is obtained from the
// authenticated server capability callback on Coordinator.
//
// Targets is the multi-target form. The singular TargetKind/TargetID/
// TargetLevel fields keep compatibility with aimcp.LevelingRequest; when
// Targets is empty they produce one target.
type StartRequest struct {
	Targets      []Target
	TargetKind   string
	TargetID     string
	TargetLevel  int
	TargetPolicy string

	AreaID     int
	Parameters map[string]json.RawMessage

	MaximumSeconds    int
	MaximumDeaths     int
	ReserveGold       int64
	MaximumSpend      int64
	NoProgressTimeout time.Duration
	// Generation fences the run to the ownership generation that selected
	// leveling. Zero uses the gate's current generation.
	Generation uint64
}

// Coordinator owns one deterministic leveling executor. Start only creates a
// durable checkpoint; callers should invoke Run with the lease context
// returned by aicontrol.Gate.Switch. If Lease is set, it is used for all
// preflight and action work, which prevents an HTTP request context from
// accidentally becoming the lifetime of a run.
type Coordinator struct {
	Game                 Game
	Navigator            Navigator
	HealthRecovery       HealthRecovery
	CharacterPreparation CharacterPreparation
	Supplies             Supplies
	Store                automation.Store
	Gate                 *aicontrol.Gate
	Now                  func() time.Time
	PollInterval         time.Duration
	NoProgressTimeout    time.Duration
	Lease                context.Context

	// CharacterID and CharacterName are supplied by the authenticated binding.
	// CharacterID is used for durable checkpoint scoping; CharacterName is an
	// optional additional observation fence.
	CharacterID   string
	CharacterName string
	// UnlimitedFunds is a server-owned capability lookup. A request cannot
	// enable it. The callback is evaluated for every observation.
	UnlimitedFunds func(context.Context) (bool, error)
	Owner          aicontrol.Mode

	mu       sync.Mutex
	active   map[string]*activeRun
	progress map[string]progressState
	settings map[string]runSettings
}

type activeRun struct {
	cancel     context.CancelFunc
	done       chan struct{}
	generation uint64
}

type runSettings struct {
	generation uint64
	noProgress time.Duration
	areaID     int
	parameters map[string]json.RawMessage
}

type progressState struct {
	stamp      progressStamp
	known      bool
	pending    pendingAction
	staleMoves int
}

type pendingAction struct {
	kind                 string
	turn                 int32
	position             aigame.Point
	warpSource           aigame.Point
	warpSourceKnown      bool
	warpDestination      aigame.Point
	warpDestinationKnown bool
}

type progressStamp struct {
	characterLevel int32
	petLevels      string
	statPoints     int32
	vital          int32
	strength       int32
	toughness      int32
	dexterity      int32
	floor, x, y    int32
	battle         bool
	turn           int32
	result         string
}

const (
	defaultMaximumSeconds    = 60 * 60
	defaultPollInterval      = 250 * time.Millisecond
	defaultNoProgressTimeout = 10 * time.Minute
	maxMaximumSeconds        = 30 * 24 * 60 * 60
	maxNoProgressTimeout     = 30 * 24 * time.Hour
	maxStaleMoveAttempts     = 3
	battlePlayerMenuNon      = 1 << 1
)

func (c *Coordinator) owner() aicontrol.Mode {
	if c == nil || c.Owner == "" {
		return aicontrol.Leveling
	}
	return c.Owner
}

func (c *Coordinator) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *Coordinator) baseContext(ctx context.Context) (context.Context, error) {
	if ctx == nil {
		if c != nil && c.Lease != nil {
			ctx = c.Lease
		} else {
			ctx = context.Background()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return ctx, nil
}

func (c *Coordinator) validateDependencies() error {
	if c == nil || c.Game == nil || c.Store == nil || c.Gate == nil {
		return ErrInvalidCoordinator
	}
	return nil
}

func (c *Coordinator) requestContext(ctx context.Context) (context.Context, error) {
	if err := c.validateDependencies(); err != nil {
		return nil, err
	}
	return c.baseContext(ctx)
}

// Start validates the authoritative current state and creates a durable
// leveling checkpoint. It does not start a goroutine; this makes ownership
// lifetime explicit and lets the service run it under its lease context.
func (c *Coordinator) Start(ctx context.Context, request StartRequest) (aimcp.TaskReceipt, error) {
	// Start is commonly called by an HTTP adapter. Prefer the configured
	// ownership lease so request cancellation cannot end a run during
	// preflight or checkpoint creation.
	if c != nil && c.Lease != nil {
		ctx = c.Lease
	}
	workCtx, err := c.requestContext(ctx)
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	targets, policy, err := normalizeRequest(request)
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	snapshot, unlimited, err := c.observe(workCtx)
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	characterID, err := c.characterID(snapshot)
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if err := c.checkOwnership(request.Generation); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if err := c.checkIdentity(snapshot); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if err := validateTargetsPresent(snapshot, targets, characterID, c.CharacterName); err != nil {
		return aimcp.TaskReceipt{}, err
	}

	maximumSeconds := request.MaximumSeconds
	if maximumSeconds == 0 {
		maximumSeconds = defaultMaximumSeconds
	}
	if maximumSeconds <= 0 || maximumSeconds > maxMaximumSeconds || request.MaximumDeaths < 0 || request.ReserveGold < 0 || request.MaximumSpend < 0 {
		return aimcp.TaskReceipt{}, fmt.Errorf("%w: invalid execution limits", ErrInvalidRequest)
	}
	noProgress := request.NoProgressTimeout
	if noProgress == 0 {
		noProgress = c.NoProgressTimeout
	}
	if noProgress == 0 {
		noProgress = defaultNoProgressTimeout
	}
	if noProgress < 0 || noProgress > maxNoProgressTimeout {
		return aimcp.TaskReceipt{}, fmt.Errorf("%w: invalid no-progress timeout", ErrInvalidRequest)
	}
	if err := validateFunding(snapshot, unlimited, request.ReserveGold); err != nil {
		return aimcp.TaskReceipt{}, err
	}

	generation := request.Generation
	if generation == 0 {
		generation = c.Gate.State().Generation
	}
	plan, err := newPlan(characterID, targets, policy, request, maximumSeconds)
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	now := c.now()
	checkpoint := automation.Checkpoint{
		Plan: plan, Revision: 1, Status: automation.Running, Phase: "ready",
		StartedAt: now, UpdatedAt: now, StepStartedAt: now,
		WasDead: isDead(snapshot),
	}
	// A configured character-preparation adapter must get the first chance to
	// observe and settle any unspent points, even when the leveling target is
	// already reached at start. Keep the checkpoint running so Tick can perform
	// that server-fenced work before recording completion.
	if c.CharacterPreparation == nil && targetsReached(snapshot, targets, policy, characterID, c.CharacterName) && !snapshot.Battle.Active {
		checkpoint.Status = automation.Completed
		checkpoint.Reason = "服务端已确认目标达成"
		confirmation := projectObservation(snapshot, characterID, unlimited)
		checkpoint.Confirmation = &confirmation
	}
	if checkpoint.Status == automation.Running && isDead(snapshot) {
		checkpoint.Status = automation.Paused
		checkpoint.Reason = noRecoveryReason()
	}
	if err := c.Store.Create(workCtx, checkpoint); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	c.mu.Lock()
	if c.active == nil {
		c.active = make(map[string]*activeRun)
	}
	if c.progress == nil {
		c.progress = make(map[string]progressState)
	}
	if c.settings == nil {
		c.settings = make(map[string]runSettings)
	}
	c.progress[plan.ID] = progressState{stamp: makeProgressStamp(snapshot), known: true}
	c.settings[plan.ID] = runSettings{generation: generation, noProgress: noProgress, areaID: request.AreaID, parameters: cloneParameters(request.Parameters)}
	c.mu.Unlock()
	return taskReceipt(checkpoint), nil
}

func normalizeRequest(request StartRequest) ([]Target, string, error) {
	policy := strings.ToLower(strings.TrimSpace(request.TargetPolicy))
	if policy == "" {
		policy = "all"
	}
	if policy != "all" && policy != "any" {
		return nil, "", fmt.Errorf("%w: target policy must be all or any", ErrInvalidRequest)
	}
	var targets []Target
	if len(request.Targets) > 0 {
		targets = append([]Target(nil), request.Targets...)
	} else {
		targets = []Target{{Kind: request.TargetKind, ID: request.TargetID, Level: request.TargetLevel}}
	}
	if len(targets) == 0 {
		return nil, "", fmt.Errorf("%w: at least one target is required", ErrInvalidRequest)
	}
	seen := make(map[string]struct{}, len(targets))
	for index := range targets {
		targets[index].Kind = normalizeTargetKind(targets[index].Kind)
		targets[index].ID = strings.TrimSpace(targets[index].ID)
		if targets[index].Kind != "character" && targets[index].Kind != "pet" {
			return nil, "", fmt.Errorf("%w: target %d has unsupported kind", ErrInvalidRequest, index+1)
		}
		if targets[index].Level <= 0 || targets[index].Level > 1000 {
			return nil, "", fmt.Errorf("%w: target %d has invalid level", ErrInvalidRequest, index+1)
		}
		if targets[index].Kind == "pet" && targets[index].ID == "" {
			return nil, "", fmt.Errorf("%w: pet target %d requires stable identity", ErrInvalidRequest, index+1)
		}
		key := targets[index].Kind + ":" + targets[index].ID
		if _, ok := seen[key]; ok {
			return nil, "", fmt.Errorf("%w: duplicate target %q", ErrInvalidRequest, key)
		}
		seen[key] = struct{}{}
	}
	return targets, policy, nil
}

func normalizeTargetKind(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "player", "character", "role", "self":
		return "character"
	case "pet":
		return "pet"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func newPlan(characterID string, targets []Target, policy string, request StartRequest, maximumSeconds int) (automation.Plan, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return automation.Plan{}, err
	}
	maximumSpend := request.MaximumSpend
	arguments, err := json.Marshal(planSettings{AreaID: request.AreaID, Parameters: cloneParameters(request.Parameters)})
	if err != nil {
		return automation.Plan{}, err
	}
	if maximumSpend == 0 {
		// Leveling itself has no paid protocol action. A verified navigator may
		// still return a cost, which is checked against this zero authorization.
		maximumSpend = 0
	}
	plan := automation.Plan{
		ID: "level-" + hex.EncodeToString(nonce[:]), CharacterID: characterID,
		Mode: "leveling", KnowledgeRevision: "aileveling-v1", Title: "deterministic leveling",
		Targets: append([]Target(nil), targets...), TargetPolicy: policy,
		Budget:         automation.Budget{Known: true, Reserve: request.ReserveGold, MaximumSpend: maximumSpend, Minimum: 0, ExpectedLow: 0, ExpectedHigh: maximumSpend},
		MaximumSeconds: maximumSeconds, MaximumDeaths: request.MaximumDeaths,
		Steps: []automation.Step{{ID: "encounter", Description: "server-observed encounter leveling", Action: automation.Action{Skill: "leveling.encounter", Arguments: arguments}, Success: []automation.Condition{{Kind: "not_battle"}}, TimeoutSeconds: 3600, MaximumCost: maximumSpend, CostKnown: true}},
	}
	if err := plan.Validate(); err != nil {
		return automation.Plan{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	return plan, nil
}

func (c *Coordinator) characterID(snapshot aigame.Snapshot) (string, error) {
	value := strings.TrimSpace(c.CharacterID)
	if value == "" {
		value = strings.TrimSpace(snapshot.Character)
	}
	if value == "" {
		return "", fmt.Errorf("%w: character identity is unavailable", ErrInvalidRequest)
	}
	return value, nil
}

func (c *Coordinator) checkIdentity(snapshot aigame.Snapshot) error {
	if c.CharacterName != "" && snapshot.Character != "" && c.CharacterName != snapshot.Character {
		return fmt.Errorf("%w: server character changed", ErrInvalidRequest)
	}
	return nil
}

func (c *Coordinator) checkOwnership(expected uint64) error {
	state := c.Gate.State()
	if state.Mode != c.owner() {
		return aicontrol.ErrOwner
	}
	if expected != 0 && state.Generation != expected {
		return aicontrol.ErrStale
	}
	return nil
}

// dispatchAction fences one leveling submission with the ownership gate. A
// raw protocol session needs the coordinator's outer dispatch. The Web game
// adapter already fences ExecuteExpected itself, so adding another dispatch
// would recursively acquire Gate.mu while it is held by the outer callback.
func (c *Coordinator) dispatchAction(ctx context.Context, generation uint64, send func(context.Context) error) error {
	if c == nil || c.Game == nil || c.Gate == nil {
		return ErrInvalidCoordinator
	}
	if send == nil {
		return errors.New("missing game action")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, alreadyFenced := c.Game.(interface{ UsesControlGate() }); alreadyFenced {
		if err := c.checkOwnership(generation); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return send(ctx)
	}
	return c.Gate.Dispatch(ctx, generation, c.owner(), send)
}

func validateTargetsPresent(snapshot aigame.Snapshot, targets []Target, characterID, characterName string) error {
	for _, target := range targets {
		if target.Kind == "character" {
			if target.ID == "" || target.ID == characterID || target.ID == characterName || target.ID == snapshot.Character || target.ID == strconv.FormatInt(int64(snapshot.Player.ID), 10) {
				continue
			}
			return fmt.Errorf("%w: character %q", ErrTargetNotFound, target.ID)
		}
		found := false
		for _, pet := range snapshot.Pets {
			if pet.IdentityKnown && pet.StableID != "" && pet.StableID == target.ID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: pet %q", ErrTargetNotFound, target.ID)
		}
	}
	return nil
}

func validateFunding(snapshot aigame.Snapshot, unlimited bool, reserve int64) error {
	if unlimited {
		return nil
	}
	if reserve > int64(snapshot.Player.Gold) {
		return fmt.Errorf("资金不足：低于保留金额，任务未启动")
	}
	return nil
}

func noRecoveryReason() string {
	return "角色已死亡，缺少已验证的疗伤/复活路线，任务已暂停"
}

func isDead(snapshot aigame.Snapshot) bool {
	return snapshot.Player.HasStatus && snapshot.Player.HP <= 0
}

func (c *Coordinator) observe(ctx context.Context) (aigame.Snapshot, bool, error) {
	snapshot, err := c.Game.Observe(ctx)
	if err != nil {
		return aigame.Snapshot{}, false, err
	}
	unlimited := false
	if c.UnlimitedFunds != nil {
		unlimited, err = c.UnlimitedFunds(ctx)
		if err != nil {
			return aigame.Snapshot{}, false, err
		}
	}
	return snapshot, unlimited, nil
}

func cloneParameters(input map[string]json.RawMessage) map[string]json.RawMessage {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]json.RawMessage, len(input))
	for key, value := range input {
		output[key] = append(json.RawMessage(nil), value...)
	}
	return output
}

// Status reads a durable checkpoint. It never infers completion from a
// running goroutine or from a model/client acknowledgement.
func (c *Coordinator) Status(ctx context.Context, handle string) (aimcp.TaskReceipt, error) {
	if err := c.validateDependencies(); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(handle) == "" {
		return aimcp.TaskReceipt{}, fmt.Errorf("%w: handle is required", ErrInvalidRequest)
	}
	checkpoint, err := c.Store.Load(ctx, handle)
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if c.CharacterID != "" && checkpoint.Plan.CharacterID != c.CharacterID {
		return aimcp.TaskReceipt{}, aimcp.ErrInvalidBinding
	}
	return taskReceipt(checkpoint), nil
}

// Active returns locally running leveling handles. The Store intentionally
// exposes no unscoped list operation, so a coordinator reports only runs it
// currently owns; a new coordinator must reconcile a durable handle explicitly
// before resuming it. Durable state is still read for every returned handle so
// a paused or completed run disappears immediately.
func (c *Coordinator) Active(ctx context.Context) ([]aimcp.TaskReceipt, error) {
	if err := c.validateDependencies(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	handles := make([]string, 0, len(c.active))
	for handle := range c.active {
		handles = append(handles, handle)
	}
	c.mu.Unlock()
	sort.Strings(handles)
	active := make([]aimcp.TaskReceipt, 0, len(handles))
	for _, handle := range handles {
		receipt, err := c.Status(ctx, handle)
		if err != nil {
			return nil, err
		}
		if receipt.Status == aimcp.ReceiptRunning || receipt.Status == aimcp.ReceiptPending {
			active = append(active, receipt)
		}
	}
	return active, nil
}

// Resume rebinds a paused leveling checkpoint to a newly issued ownership
// lease.  The paused checkpoint is reconciled against the current server
// observation first; an uncertain non-idempotent action is never replayed.
// Run remains a separate operation so callers can decide which goroutine owns
// the lease lifetime.
func (c *Coordinator) Resume(ctx context.Context, handle string, generation uint64) (aimcp.TaskReceipt, error) {
	if err := c.validateDependencies(); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if strings.TrimSpace(handle) == "" {
		return aimcp.TaskReceipt{}, fmt.Errorf("%w: handle is required", ErrInvalidRequest)
	}
	if ctx == nil {
		return aimcp.TaskReceipt{}, ErrInvalidCoordinator
	}
	if err := ctx.Err(); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	state := c.Gate.State()
	if state.Mode != c.owner() {
		return aimcp.TaskReceipt{}, aicontrol.ErrOwner
	}
	if generation == 0 {
		generation = state.Generation
	}
	if state.Generation != generation {
		return aimcp.TaskReceipt{}, aicontrol.ErrStale
	}

	checkpoint, err := c.Store.Load(ctx, handle)
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if c.CharacterID != "" && checkpoint.Plan.CharacterID != c.CharacterID {
		return aimcp.TaskReceipt{}, aimcp.ErrInvalidBinding
	}
	if checkpoint.Status == automation.Completed {
		return taskReceipt(checkpoint), nil
	}
	if checkpoint.Status != automation.Paused {
		return taskReceipt(checkpoint), ErrNotRunning
	}

	// The new lease becomes the coordinator's source for both Resume and the
	// subsequent Run.  Do this only after all preflight checks that can fail
	// without changing coordinator state have passed.
	workCtx, err := c.baseContext(ctx)
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	snapshot, unlimited, err := c.observe(workCtx)
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if err := c.checkOwnership(generation); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if err := c.checkIdentity(snapshot); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if !snapshot.Connected || !snapshot.Player.HasStatus || (snapshot.Phase != aigame.PhaseWorld && snapshot.Phase != aigame.PhaseBattle) {
		return aimcp.TaskReceipt{}, errors.New("game state is not synchronized")
	}
	characterID := checkpoint.Plan.CharacterID
	if err := validateTargetsPresent(snapshot, checkpointTargets(checkpoint.Plan), characterID, c.CharacterName); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if err := validateFunding(snapshot, unlimited, checkpoint.Plan.Budget.Reserve); err != nil {
		return aimcp.TaskReceipt{}, err
	}

	if checkpoint.Phase == "cancelled" {
		return aimcp.TaskReceipt{}, ErrUnknownDelivery
	}

	// A progress stamp is kept in memory while a coordinator remains alive. If
	// it is absent (for example after process restart), a prepared/submitted
	// step stays paused and therefore cannot be retried from guesswork.
	progress := c.progressState(handle, snapshot)
	if checkpoint.Phase == "prepared" || checkpoint.Phase == "submitted" {
		if err := c.ensureProgress(workCtx, handle, &checkpoint, snapshot, &progress); err != nil {
			return aimcp.TaskReceipt{}, err
		}
		if checkpoint.Phase == "prepared" || checkpoint.Phase == "submitted" {
			return aimcp.TaskReceipt{}, ErrUnknownDelivery
		}
	}
	// Reconcile a prepared/submitted action before evaluating completion. A
	// configured character-preparation adapter also needs to run on the next
	// Tick when the target was already reached, so it must not be completed in
	// Resume's preflight path.
	if c.CharacterPreparation == nil && targetsReached(snapshot, checkpointTargets(checkpoint.Plan), checkpoint.Plan.TargetPolicy, characterID, c.CharacterName) && !snapshot.Battle.Active {
		checkpoint.Status = automation.Completed
		checkpoint.Phase = "completed"
		checkpoint.Reason = "服务端已确认目标达成"
		confirmation := projectObservation(snapshot, characterID, unlimited)
		checkpoint.Confirmation = &confirmation
		if err := c.save(workCtx, &checkpoint); err != nil {
			return aimcp.TaskReceipt{}, err
		}
		return taskReceipt(checkpoint), nil
	}
	if isDead(snapshot) {
		return aimcp.TaskReceipt{}, errors.New(noRecoveryReason())
	}

	settings, err := c.settingsForPlan(checkpoint.Plan)
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if settings.noProgress <= 0 {
		settings.noProgress = c.NoProgressTimeout
	}
	if settings.noProgress <= 0 {
		settings.noProgress = defaultNoProgressTimeout
	}
	settings.generation = generation
	// Resume is the ownership handoff point.  Keep the new lease for the
	// following Run call; callers never reuse the cancelled pre-pause lease.
	c.Lease = ctx
	c.mu.Lock()
	if c.settings == nil {
		c.settings = make(map[string]runSettings)
	}
	c.settings[handle] = settings
	c.mu.Unlock()

	checkpoint.Status = automation.Running
	checkpoint.Reason = ""
	checkpoint.StepStartedAt = c.now()
	if checkpoint.Phase == "" {
		checkpoint.Phase = "ready"
	}
	if err := c.save(workCtx, &checkpoint); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	return taskReceipt(checkpoint), nil
}

// Cancel stops a local Run loop if one exists and durably pauses the
// checkpoint. An in-flight or uncertain game action is never replayed.
func (c *Coordinator) Cancel(ctx context.Context, handle, reason string) (aimcp.TaskReceipt, error) {
	if err := c.validateDependencies(); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if strings.TrimSpace(handle) == "" {
		return aimcp.TaskReceipt{}, fmt.Errorf("%w: handle is required", ErrInvalidRequest)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	run := c.active[handle]
	c.mu.Unlock()
	if run != nil && run.cancel != nil {
		run.cancel()
		select {
		case <-run.done:
		case <-ctx.Done():
			// Persist below with a detached context; the HTTP caller's context
			// cannot turn cancellation into an unrecorded operation.
		}
	}
	persist, stop := detachedPersistenceContext(ctx)
	defer stop()
	checkpoint, err := c.Store.Load(persist, handle)
	if err != nil {
		return aimcp.TaskReceipt{}, err
	}
	if checkpoint.Status == automation.Completed {
		return taskReceipt(checkpoint), nil
	}
	old := checkpoint.Revision
	checkpoint.Revision++
	checkpoint.Status = automation.Paused
	checkpoint.Phase = "cancelled"
	checkpoint.Reason = strings.TrimSpace(reason)
	if checkpoint.Reason == "" {
		checkpoint.Reason = "任务已取消；已提交的游戏操作仍需核验"
	}
	checkpoint.UpdatedAt = c.now()
	if err := c.Store.Save(persist, checkpoint, old); err != nil {
		return aimcp.TaskReceipt{}, err
	}
	return aimcp.TaskReceipt{Handle: checkpoint.Plan.ID, Status: aimcp.ReceiptCancelled, State: string(checkpoint.Status), Reason: checkpoint.Reason}, nil
}

func detachedPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	base := context.WithoutCancel(ctx)
	return context.WithTimeout(base, 3*time.Second)
}

func taskReceipt(checkpoint automation.Checkpoint) aimcp.TaskReceipt {
	receipt := aimcp.TaskReceipt{Handle: checkpoint.Plan.ID, Status: aimcp.ReceiptRunning, State: string(checkpoint.Status), Reason: checkpoint.Reason}
	switch checkpoint.Status {
	case automation.Completed:
		if checkpoint.Confirmation == nil || !checkpoint.Plan.Complete(*checkpoint.Confirmation) {
			receipt.Status = aimcp.ReceiptUnknown
			receipt.Reason = "完成检查点缺少权威观察，需要重新核验"
			return receipt
		}
		receipt.Status = aimcp.ReceiptConfirmed
		receipt.Evidence, _ = json.Marshal(map[string]any{
			"character_id":        checkpoint.Plan.CharacterID,
			"targets":             checkpoint.Plan.Targets,
			"target_policy":       checkpoint.Plan.TargetPolicy,
			"checkpoint_revision": checkpoint.Revision,
			"observation":         checkpoint.Confirmation,
			"confirmed_at":        checkpoint.UpdatedAt,
		})
	case automation.Paused:
		receipt.Status = aimcp.ReceiptFailed
	}
	return receipt
}

func projectObservation(snapshot aigame.Snapshot, characterID string, unlimited bool) automation.Observation {
	observation := automation.Observation{
		Revision: snapshot.Revision, CharacterID: characterID, Connected: snapshot.Connected,
		Ready:     snapshot.Player.HasStatus && (snapshot.Phase == aigame.PhaseWorld || snapshot.Phase == aigame.PhaseBattle),
		Character: automation.Entity{ID: characterID, Level: int(snapshot.Player.Level), HP: int(snapshot.Player.HP), MaxHP: int(snapshot.Player.MaxHP)},
		Floor:     int(snapshot.Position.Floor), X: int(snapshot.Position.X), Y: int(snapshot.Position.Y), Gold: int64(snapshot.Player.Gold), UnlimitedFunds: unlimited,
		Battle: snapshot.Battle.Active, Dead: isDead(snapshot), Inventory: map[string]int{}, Flags: map[string]bool{},
	}
	for _, pet := range snapshot.Pets {
		if pet.IdentityKnown && pet.StableID != "" {
			observation.Pets = append(observation.Pets, automation.Entity{ID: pet.StableID, Level: int(pet.Level), HP: int(pet.HP), MaxHP: int(pet.MaxHP)})
		}
	}
	return observation
}

func targetsReached(snapshot aigame.Snapshot, targets []Target, policy, characterID, characterName string) bool {
	matched := 0
	for _, target := range targets {
		level := -1
		if target.Kind == "character" {
			if target.ID == "" || target.ID == characterID || target.ID == characterName || target.ID == snapshot.Character || target.ID == strconv.FormatInt(int64(snapshot.Player.ID), 10) {
				level = int(snapshot.Player.Level)
			}
		} else {
			for _, pet := range snapshot.Pets {
				if pet.IdentityKnown && pet.StableID == target.ID {
					level = int(pet.Level)
					break
				}
			}
		}
		if level >= target.Level {
			matched++
		}
	}
	return len(targets) > 0 && ((policy == "any" && matched > 0) || (policy == "all" && matched == len(targets)))
}

func makeProgressStamp(snapshot aigame.Snapshot) progressStamp {
	var pets strings.Builder
	for _, pet := range snapshot.Pets {
		if !pet.IdentityKnown || pet.StableID == "" {
			continue
		}
		pets.WriteString(pet.StableID)
		pets.WriteByte('=')
		pets.WriteString(strconv.FormatInt(int64(pet.Level), 10))
		pets.WriteByte(';')
	}
	// Keep the values themselves in the stamp, but deliberately omit the
	// StatPointsKnown bit. A SKUP/S:AI invalidation or refresh changes only
	// knowledge state; it is progress only when the authoritative point count
	// or one of the four base attributes actually changes.
	return progressStamp{
		characterLevel: snapshot.Player.Level,
		petLevels:      pets.String(),
		statPoints:     snapshot.Player.UnspentStatPoints,
		vital:          snapshot.Player.Vital,
		strength:       snapshot.Player.Strength,
		toughness:      snapshot.Player.Toughness,
		dexterity:      snapshot.Player.Dexterity,
		floor:          snapshot.Position.Floor,
		x:              snapshot.Position.X,
		y:              snapshot.Position.Y,
		battle:         snapshot.Battle.Active,
		turn:           snapshot.Battle.Turn,
		result:         snapshot.Battle.Result,
	}
}

func (c *Coordinator) save(ctx context.Context, checkpoint *automation.Checkpoint) error {
	old := checkpoint.Revision
	checkpoint.Revision++
	checkpoint.UpdatedAt = c.now()
	if err := c.Store.Save(ctx, *checkpoint, old); err != nil {
		checkpoint.Revision = old
		return err
	}
	return nil
}

func (c *Coordinator) pause(ctx context.Context, checkpoint *automation.Checkpoint, reason string) error {
	checkpoint.Status = automation.Paused
	checkpoint.Reason = reason
	return c.save(ctx, checkpoint)
}

func (c *Coordinator) ensureProgress(ctx context.Context, handle string, checkpoint *automation.Checkpoint, snapshot aigame.Snapshot, state *progressState) error {
	stamp := makeProgressStamp(snapshot)
	if !state.known {
		state.stamp, state.known = stamp, true
		c.setProgressState(handle, *state)
		if checkpoint.StepStartedAt.IsZero() {
			checkpoint.StepStartedAt = c.now()
			return c.save(ctx, checkpoint)
		}
		return nil
	}
	if state.stamp == stamp {
		return nil
	}
	previous := state.stamp
	state.stamp = stamp
	confirmed := checkpoint.Phase == "submitted" && pendingConfirmed(state.pending, previous, stamp)
	if confirmed {
		checkpoint.Phase = "ready"
		state.pending = pendingAction{}
	}
	// Keep the original action deadline while its result is uncertain. This
	// stamp may change because a pet/character update or an intermediate tile
	// arrived, but none of those observations authorizes a retry or extends
	// the time allowed to reconcile the submitted packet.
	if checkpoint.Phase != "submitted" || confirmed || checkpoint.StepStartedAt.IsZero() {
		checkpoint.StepStartedAt = c.now()
	}
	c.setProgressState(handle, *state)
	return c.save(ctx, checkpoint)
}

// pendingConfirmed reports whether a server observation proves the outcome of
// the submitted action. A changed progress stamp by itself is not enough:
// level/HP updates can arrive independently of a movement or battle command,
// and an EO packet can be followed by several still-active battle snapshots.
func pendingConfirmed(pending pendingAction, previous, current progressStamp) bool {
	switch pending.kind {
	case "move":
		// A movement route can start an encounter before its requested endpoint
		// is observed. Entering battle is then the only available confirmation.
		if current.battle && !previous.battle {
			return true
		}
		if pending.warpDestinationKnown {
			if positionReached(current, pending.warpDestination) {
				return true
			}
			// A real 2.5 server leaves the player on the warp source after W;
			// the native EV must be submitted on the next coordinator tick.
			// Treating that source as completion lets navigation select EV
			// without ever replaying the W packet.
			return pending.warpSourceKnown && positionReached(current, pending.warpSource)
		}
		return positionReached(current, pending.position)

	case "warp":
		// EV is a separate non-idempotent action. Reaching the source again
		// cannot acknowledge it and must never trigger a duplicate EV.
		return pending.warpDestinationKnown && positionReached(current, pending.warpDestination)

	case "battle":
		// The attack was submitted during a live battle. A later turn, a
		// terminal result, or leaving battle proves that the command was
		// consumed. Unrelated character/pet progress does not.
		if previous.battle && !current.battle {
			return true
		}
		if current.result != "" && current.result != previous.result {
			return true
		}
		return current.battle && current.turn > pending.turn

	case "battle-end":
		// EO is sent after a terminal result/no-live-enemy observation, but the
		// server may keep reporting the battle for more than one snapshot.
		// Only the actual transition out of battle proves EO completed.
		return previous.battle && !current.battle

	default:
		// An unknown pending kind has no safe acknowledgement contract.
		return false
	}
}

func positionReached(stamp progressStamp, target aigame.Point) bool {
	if target.Floor != 0 && stamp.floor != target.Floor {
		return false
	}
	return stamp.x == target.X && stamp.y == target.Y
}

// Tick observes and advances at most one action. A failed submission is
// durably paused before returning; callers must reconcile the server before
// considering a new run.
func (c *Coordinator) Tick(ctx context.Context, handle string) (automation.Checkpoint, error) {
	if err := c.validateDependencies(); err != nil {
		return automation.Checkpoint{}, err
	}
	workCtx, err := c.requestContext(ctx)
	if err != nil {
		return automation.Checkpoint{}, err
	}
	checkpoint, err := c.Store.Load(workCtx, handle)
	if err != nil {
		return checkpoint, err
	}
	if checkpoint.Status != automation.Running {
		return checkpoint, nil
	}
	if err := c.checkOwnership(c.generationFor(handle)); err != nil {
		persist, stop := detachedPersistenceContext(workCtx)
		defer stop()
		return checkpoint, c.pause(persist, &checkpoint, "控制权已变化，任务已暂停")
	}
	snapshot, unlimited, err := c.observe(workCtx)
	if err != nil {
		persist, stop := detachedPersistenceContext(workCtx)
		defer stop()
		return checkpoint, errors.Join(err, c.pause(persist, &checkpoint, "服务端观察失败，任务已暂停；需要重新核验"))
	}
	characterID := checkpoint.Plan.CharacterID
	if err := c.checkIdentity(snapshot); err != nil || !snapshot.Connected || !snapshot.Player.HasStatus || (snapshot.Phase != aigame.PhaseWorld && snapshot.Phase != aigame.PhaseBattle) {
		persist, stop := detachedPersistenceContext(workCtx)
		defer stop()
		reason := "连接中断或角色状态未同步，任务已暂停"
		if err != nil {
			reason = "角色身份已变化，任务已暂停"
		}
		return checkpoint, c.pause(persist, &checkpoint, reason)
	}
	if err := validateTargetsPresent(snapshot, checkpointTargets(checkpoint.Plan), characterID, c.CharacterName); err != nil {
		persist, stop := detachedPersistenceContext(workCtx)
		defer stop()
		return checkpoint, c.pause(persist, &checkpoint, "目标宠物已不在当前角色，任务已暂停；需要重新确认")
	}
	state := c.progressState(handle, snapshot)
	if checkpoint.Phase == "prepared" {
		persist, stop := detachedPersistenceContext(workCtx)
		defer stop()
		return checkpoint, c.pause(persist, &checkpoint, "上次操作结果未确认，禁止重试")
	}
	if err := c.ensureProgress(workCtx, handle, &checkpoint, snapshot, &state); err != nil {
		return checkpoint, err
	}
	// A changed observation that did not satisfy the pending action's
	// acknowledgement contract is still uncertain. Do not let the battle or
	// target branches submit another command while that action is unresolved.
	submitted := checkpoint.Phase == "submitted"
	reached := targetsReached(snapshot, checkpointTargets(checkpoint.Plan), checkpoint.Plan.TargetPolicy, characterID, c.CharacterName)
	if !submitted && reached {
		if snapshot.Battle.Active {
			if snapshot.Battle.Result != "" {
				return c.submitEndBattle(workCtx, &checkpoint, snapshot, state)
			}
			// Reaching a level during an unfinished battle stops further
			// attacks. Wait for the terminal battle packet or the timeout.
			return checkpoint, nil
		} else if c.CharacterPreparation == nil {
			// Preserve the original no-hook completion path. A configured
			// preparation adapter must settle points below before this branch.
			checkpoint.Status = automation.Completed
			checkpoint.Phase = "completed"
			checkpoint.Reason = "服务端已确认目标达成"
			observation := projectObservation(snapshot, characterID, unlimited)
			checkpoint.Confirmation = &observation
			if err := c.save(workCtx, &checkpoint); err != nil {
				return checkpoint, err
			}
			return checkpoint, nil
		}
	}
	if isDead(snapshot) {
		persist, stop := detachedPersistenceContext(workCtx)
		defer stop()
		if !checkpoint.WasDead {
			checkpoint.Deaths++
		}
		checkpoint.WasDead = true
		if checkpoint.Deaths > checkpoint.Plan.MaximumDeaths {
			return checkpoint, c.pause(persist, &checkpoint, "达到死亡次数上限，任务已暂停")
		}
		return checkpoint, c.pause(persist, &checkpoint, noRecoveryReason())
	}
	checkpoint.WasDead = false
	if c.now().Sub(checkpoint.StartedAt) >= time.Duration(checkpoint.Plan.MaximumSeconds)*time.Second {
		persist, stop := detachedPersistenceContext(workCtx)
		defer stop()
		return checkpoint, c.pause(persist, &checkpoint, "达到时间上限，任务已暂停")
	}
	if checkpoint.StepStartedAt.IsZero() {
		checkpoint.StepStartedAt = c.now()
	}
	noProgress := c.noProgressFor(handle)
	if c.now().Sub(checkpoint.StepStartedAt) >= noProgress {
		persist, stop := detachedPersistenceContext(workCtx)
		defer stop()
		return checkpoint, c.pause(persist, &checkpoint, "达到无进度超时，任务已暂停")
	}
	if submitted {
		if state.pending.kind == "move" && snapshot.Phase == aigame.PhaseWorld && !snapshot.Battle.Active {
			// Native W does not echo the player's final position. Request the
			// server's coordinates without replaying W or acknowledging it from
			// a successful write. Only a later observation can clear pending.
			err := c.dispatchAction(workCtx, c.generationFor(handle), func(sendCtx context.Context) error {
				return c.Game.ExecuteExpected(sendCtx, snapshot.Revision, aigame.Action{Kind: aigame.ActionStatus, Command: "c"})
			})
			// This query is read-only: a concurrent observation merely requires
			// a fresh revision on the next tick. Keep the original deadline.
			if err != nil && !errors.Is(err, aigame.ErrStaleRevision) {
				return c.pauseUnavailable(workCtx, &checkpoint, err, "移动位置查询失败，任务已暂停；移动结果仍需核验")
			}
		}
		return checkpoint, nil
	}
	if snapshot.Battle.Active {
		return c.tickBattle(workCtx, &checkpoint, snapshot, state)
	}
	if c.CharacterPreparation != nil {
		if snapshot.Phase != aigame.PhaseWorld {
			// A battle phase without the active bit is still a transitional
			// server state. Do not let a preparation adapter spend in it.
			return checkpoint, nil
		}
		// The hook is deliberately outside dispatchAction/Gate.Dispatch. Its
		// adapter owns its own receipt and generation fence; holding the gate
		// here would deadlock adapters whose GameAction performs that fence.
		if err := c.checkOwnership(c.generationFor(handle)); err != nil {
			persist, stop := detachedPersistenceContext(workCtx)
			defer stop()
			return checkpoint, errors.Join(err, c.pause(persist, &checkpoint, "控制权已变化，任务已暂停"))
		}
		if snapshot.Trade.Active || snapshot.Trade.Pending {
			// A character in trade is not an eligible preparation state. Wait
			// for a later world observation without issuing another action.
			return checkpoint, nil
		}
		ready, err := c.CharacterPreparation.PrepareCharacter(workCtx, snapshot)
		if err != nil {
			return c.pauseUnavailable(workCtx, &checkpoint, err, "人物配点失败，任务已暂停；需要重新核验")
		}
		if !ready {
			// A false result means that one point is being waited on or was
			// submitted. Do not issue movement/battle work or refresh the
			// no-progress deadline in this tick.
			return checkpoint, nil
		}
		// The adapter may spend time observing or reconciling its own receipt.
		// Re-fence before using its true result to complete the task or submit
		// another action; the callback must not turn a takeover into a late
		// completion or write.
		if err := c.checkOwnership(c.generationFor(handle)); err != nil {
			persist, stop := detachedPersistenceContext(workCtx)
			defer stop()
			return checkpoint, errors.Join(err, c.pause(persist, &checkpoint, "控制权已变化，任务已暂停"))
		}
		if err := workCtx.Err(); err != nil {
			persist, stop := detachedPersistenceContext(workCtx)
			defer stop()
			return checkpoint, errors.Join(err, c.pause(persist, &checkpoint, "控制租约已结束，任务已暂停"))
		}
	}
	if reached {
		checkpoint.Status = automation.Completed
		checkpoint.Phase = "completed"
		checkpoint.Reason = "服务端已确认目标达成"
		observation := projectObservation(snapshot, characterID, unlimited)
		checkpoint.Confirmation = &observation
		if err := c.save(workCtx, &checkpoint); err != nil {
			return checkpoint, err
		}
		return checkpoint, nil
	}
	return c.tickWorld(workCtx, &checkpoint, snapshot, unlimited, state)
}

func checkpointTargets(plan automation.Plan) []Target {
	return append([]Target(nil), plan.Targets...)
}

func (c *Coordinator) generationFor(handle string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if run := c.active[handle]; run != nil {
		return run.generation
	}
	if settings := c.settings[handle]; settings.generation != 0 {
		return settings.generation
	}
	return c.Gate.State().Generation
}

func (c *Coordinator) noProgressFor(handle string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if settings := c.settings[handle]; settings.noProgress > 0 {
		return settings.noProgress
	}
	if c.NoProgressTimeout > 0 {
		return c.NoProgressTimeout
	}
	return defaultNoProgressTimeout
}

func (c *Coordinator) runSettings(handle string) runSettings {
	c.mu.Lock()
	defer c.mu.Unlock()
	if settings := c.settings[handle]; settings.noProgress > 0 || settings.generation != 0 || settings.areaID != 0 || len(settings.parameters) > 0 {
		return settings
	}
	return runSettings{}
}

func (c *Coordinator) progressState(handle string, snapshot aigame.Snapshot) progressState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.progress == nil {
		c.progress = make(map[string]progressState)
	}
	state := c.progress[handle]
	if !state.known {
		state.stamp, state.known = makeProgressStamp(snapshot), true
		c.progress[handle] = state
	}
	return state
}

func (c *Coordinator) setProgressState(handle string, state progressState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.progress == nil {
		c.progress = make(map[string]progressState)
	}
	c.progress[handle] = state
}

func (c *Coordinator) updatePending(handle string, pending pendingAction) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.progress[handle]
	state.pending = pending
	state.known = true
	state.staleMoves = 0
	c.progress[handle] = state
}

func (c *Coordinator) tickWorld(ctx context.Context, checkpoint *automation.Checkpoint, snapshot aigame.Snapshot, unlimited bool, state progressState) (automation.Checkpoint, error) {
	if !snapshot.Player.RidePetKnown || snapshot.Player.RidePet < -1 || snapshot.Player.RidePet >= 5 {
		return c.pauseUnavailable(ctx, checkpoint, ErrNoRecovery, "骑宠状态未确认，任务已暂停")
	}
	if snapshot.Player.RidePet >= 0 {
		var ridePet aigame.PetSnapshot
		found := false
		for _, pet := range snapshot.Pets {
			if pet.Slot == snapshot.Player.RidePet {
				ridePet, found = pet, true
				break
			}
		}
		if !found || ridePet.MaxHP <= 0 || ridePet.HP > ridePet.MaxHP || ridePet.HP <= 0 {
			return c.pauseUnavailable(ctx, checkpoint, ErrNoRecovery, "骑宠状态未确认或已死亡，任务已暂停")
		}
		if ridePet.HP < ridePet.MaxHP/2+ridePet.MaxHP%2 {
			return c.recoverWorldRidePetHealth(ctx, checkpoint, snapshot, ridePet)
		}
	}
	if snapshot.Player.MaxHP <= 0 || snapshot.Player.HP > snapshot.Player.MaxHP {
		return c.pauseUnavailable(ctx, checkpoint, ErrNoRecovery, "生命状态未确认，任务已暂停")
	}
	if snapshot.Player.HP < snapshot.Player.MaxHP/2+snapshot.Player.MaxHP%2 {
		return c.recoverWorldHealth(ctx, checkpoint, snapshot)
	}
	if snapshot.Player.BattlePetSlotKnown && snapshot.Player.BattlePetSlot >= 0 {
		for _, pet := range snapshot.Pets {
			if pet.Slot == snapshot.Player.BattlePetSlot && (pet.HP <= 0 || (pet.MaxHP > 0 && pet.HP < pet.MaxHP/2+pet.MaxHP%2)) {
				return c.recoverWorldPetHealth(ctx, checkpoint, snapshot, pet)
			}
		}
	}
	if c.Navigator == nil {
		return c.pauseUnavailable(ctx, checkpoint, ErrNoNavigation, "练级导航不可用，无法确认合法移动路线，任务已暂停")
	}
	settings, err := c.settingsForPlan(checkpoint.Plan)
	if err != nil {
		return c.pauseUnavailable(ctx, checkpoint, err, "练级配置未确认，任务已暂停")
	}
	if c.Supplies != nil {
		order, err := c.Supplies.Quote(ctx, snapshot, cloneParameters(settings.parameters))
		if err != nil {
			return c.pauseUnavailable(ctx, checkpoint, err, supplyPlanFailureReason(err))
		}
		if order != nil {
			return c.restock(ctx, checkpoint, *order, unlimited)
		}
	}
	navigation, err := c.Navigator.Next(ctx, snapshot, NavigationRequest{Targets: checkpointTargets(checkpoint.Plan), TargetPolicy: checkpoint.Plan.TargetPolicy, AreaID: settings.areaID, Parameters: cloneParameters(settings.parameters)})
	if err != nil {
		return c.pauseUnavailable(ctx, checkpoint, err, "练级导航失败，任务已暂停")
	}
	if strings.TrimSpace(navigation.Reason) != "" && !navigation.Ready {
		return c.pauseUnavailable(ctx, checkpoint, ErrNoNavigation, navigation.Reason)
	}
	if navigation.WarpEventType != 0 {
		return c.tickWarpEvent(ctx, checkpoint, snapshot, navigation, state, unlimited)
	}
	if navigation.InArea {
		return *checkpoint, nil
	}
	if strings.TrimSpace(navigation.Route) == "" {
		return c.pauseUnavailable(ctx, checkpoint, ErrNoNavigation, "练级区域没有已验证的移动路线，任务已暂停")
	}
	if navigation.MaximumCost < 0 || (!navigation.CostKnown && navigation.MaximumCost > 0) {
		return c.pauseUnavailable(ctx, checkpoint, errors.New("navigation cost is unknown"), "移动费用未验证，任务已暂停")
	}
	if !c.authorizeCost(snapshotGold(snapshot), checkpoint, navigation.MaximumCost, unlimited) {
		return c.pauseUnavailable(ctx, checkpoint, errors.New("insufficient leveling budget"), "预算不足，任务已暂停")
	}
	previousStepStart := checkpoint.StepStartedAt
	checkpoint.Phase = "prepared"
	checkpoint.StepStartedAt = c.now()
	if navigation.MaximumCost > 0 {
		checkpoint.ReservedSpend += navigation.MaximumCost
	}
	if err := c.save(ctx, checkpoint); err != nil {
		return *checkpoint, err
	}
	action := aigame.Move(snapshot.Position.X, snapshot.Position.Y, navigation.Route)
	if err := c.submit(ctx, checkpoint, snapshot, action); err != nil {
		if errors.Is(err, aigame.ErrStaleRevision) && !errors.Is(err, ErrUnknownDelivery) {
			// ExecuteExpected explicitly rejected W before writing. Release
			// only this unsent action's reservation and replan from a fresh
			// snapshot on the next tick, with a finite contention bound.
			checkpoint.ReservedSpend -= navigation.MaximumCost
			checkpoint.Phase = "ready"
			checkpoint.StepStartedAt = previousStepStart
			state.staleMoves++
			c.setProgressState(checkpoint.Plan.ID, state)
			if state.staleMoves >= maxStaleMoveAttempts {
				return c.pauseUnavailable(ctx, checkpoint, err, "移动前状态持续变化，未发送移动，任务已暂停")
			}
			err = c.save(ctx, checkpoint)
			return *checkpoint, err
		}
		return *checkpoint, err
	}
	state.pending = pendingAction{
		kind:                 "move",
		position:             navigation.Destination,
		warpSource:           navigation.Destination,
		warpSourceKnown:      navigation.WarpDestinationKnown,
		warpDestination:      navigation.WarpDestination,
		warpDestinationKnown: navigation.WarpDestinationKnown,
	}
	c.updatePending(checkpoint.Plan.ID, state.pending)
	checkpoint.Phase = "submitted"
	if err := c.save(ctx, checkpoint); err != nil {
		return *checkpoint, errors.Join(err, ErrUnknownDelivery)
	}
	return *checkpoint, nil
}

func (c *Coordinator) tickWarpEvent(ctx context.Context, checkpoint *automation.Checkpoint, snapshot aigame.Snapshot, navigation Navigation, state progressState, unlimited bool) (automation.Checkpoint, error) {
	if !navigation.Ready || navigation.InArea || strings.TrimSpace(navigation.Route) != "" || !navigation.WarpDestinationKnown || !validWarpEventType(navigation.WarpEventType) {
		return c.pauseUnavailable(ctx, checkpoint, ErrNoNavigation, "传送事件缺少已验证的来源或目标，任务已暂停")
	}
	if !sameGridPoint(snapshot.Position, navigation.Destination) {
		return c.pauseUnavailable(ctx, checkpoint, ErrNoNavigation, "传送事件未在服务端确认的来源格，任务已暂停")
	}
	if navigation.WarpDestination.Floor < 0 || navigation.WarpDestination.X < 0 || navigation.WarpDestination.Y < 0 {
		return c.pauseUnavailable(ctx, checkpoint, ErrNoNavigation, "传送目标坐标未验证，任务已暂停")
	}
	if sameGridPoint(navigation.Destination, navigation.WarpDestination) {
		return c.pauseUnavailable(ctx, checkpoint, ErrNoNavigation, "传送来源与目标相同，任务已暂停")
	}
	if navigation.MaximumCost < 0 || (!navigation.CostKnown && navigation.MaximumCost > 0) {
		return c.pauseUnavailable(ctx, checkpoint, errors.New("navigation cost is unknown"), "移动费用未验证，任务已暂停")
	}
	if !c.authorizeCost(snapshotGold(snapshot), checkpoint, navigation.MaximumCost, unlimited) {
		return c.pauseUnavailable(ctx, checkpoint, errors.New("insufficient leveling budget"), "预算不足，任务已暂停")
	}
	checkpoint.Phase = "prepared"
	checkpoint.StepStartedAt = c.now()
	if navigation.MaximumCost > 0 {
		checkpoint.ReservedSpend += navigation.MaximumCost
	}
	if err := c.save(ctx, checkpoint); err != nil {
		return *checkpoint, err
	}
	sequence := aigame.NextMapEventSequence()
	if err := c.submit(ctx, checkpoint, snapshot, aigame.MapEvent(navigation.WarpEventType, sequence, snapshot.Position.X, snapshot.Position.Y, -1)); err != nil {
		return *checkpoint, err
	}
	state.pending = pendingAction{kind: "warp", warpDestination: navigation.WarpDestination, warpDestinationKnown: true}
	c.updatePending(checkpoint.Plan.ID, state.pending)
	checkpoint.Phase = "submitted"
	if err := c.save(ctx, checkpoint); err != nil {
		return *checkpoint, errors.Join(err, ErrUnknownDelivery)
	}
	return *checkpoint, nil
}

func validWarpEventType(event int32) bool {
	switch event {
	case aigame.MapEventWarp, aigame.MapEventWarpMorning, aigame.MapEventWarpNoon, aigame.MapEventWarpNight:
		return true
	default:
		return false
	}
}

func sameGridPoint(left, right aigame.Point) bool {
	return left.Floor == right.Floor && left.X == right.X && left.Y == right.Y
}

func snapshotGold(snapshot aigame.Snapshot) int64 { return int64(snapshot.Player.Gold) }

func (c *Coordinator) authorizeCost(gold int64, checkpoint *automation.Checkpoint, cost int64, unlimited bool) bool {
	if cost < 0 {
		return false
	}
	if unlimited {
		return true
	}
	if checkpoint.Plan.Budget.MaximumSpend < checkpoint.ReservedSpend || cost > checkpoint.Plan.Budget.MaximumSpend-checkpoint.ReservedSpend {
		return false
	}
	reserve := checkpoint.Plan.Budget.Reserve
	return gold-reserve >= cost
}

func (c *Coordinator) pauseUnavailable(ctx context.Context, checkpoint *automation.Checkpoint, cause error, reason string) (automation.Checkpoint, error) {
	persist, stop := detachedPersistenceContext(ctx)
	defer stop()
	return *checkpoint, errors.Join(c.pause(persist, checkpoint, reason), cause)
}

func (c *Coordinator) tickBattle(ctx context.Context, checkpoint *automation.Checkpoint, snapshot aigame.Snapshot, state progressState) (automation.Checkpoint, error) {
	if snapshot.Battle.Result != "" {
		return c.submitEndBattle(ctx, checkpoint, snapshot, state)
	}
	if battleHasNoLiveEnemy(snapshot) {
		// A partial or final roster is not the native terminal handshake.
		// Wait for RS/RD (or another explicit terminal server result) before EO.
		return *checkpoint, nil
	}
	if !snapshot.Battle.CommandReady {
		return *checkpoint, nil
	}
	if !snapshot.Battle.MyNoKnown || snapshot.Battle.MyNo < 0 || snapshot.Battle.MyNo >= 20 {
		return c.pauseUnavailable(ctx, checkpoint, errors.New("battle MyNo is not verified"), "战斗参与者身份未确认，任务已暂停")
	}
	foundLocal := false
	for _, participant := range snapshot.Battle.Participants {
		if participant.BattleID == snapshot.Battle.MyNo {
			foundLocal = true
			if participant.Dead || participant.HP <= 0 {
				return c.pauseUnavailable(ctx, checkpoint, errors.New("local battle participant is dead"), noRecoveryReason())
			}
			break
		}
	}
	if !foundLocal {
		return c.pauseUnavailable(ctx, checkpoint, errors.New("local battle participant is absent"), "战斗参与者不在服务端名册，任务已暂停")
	}
	target, ok := chooseEnemy(snapshot)
	if !ok {
		return c.pauseUnavailable(ctx, checkpoint, errors.New("no live enemy target"), "没有可验证的敌方目标，任务已暂停")
	}
	checkpoint.Phase = "prepared"
	checkpoint.StepStartedAt = c.now()
	if err := c.save(ctx, checkpoint); err != nil {
		return *checkpoint, err
	}
	command := "H|" + battleHex(target.BattleID)
	retreat := levelingBattleNeedsRecovery(snapshot)
	if retreat {
		command = "E"
	}
	if snapshot.Battle.BPFlags&(aigame.BattlePlayerMenuOff|aigame.BattleEnemySurprise) != 0 {
		command = "N"
	}
	if err := c.submit(ctx, checkpoint, snapshot, aigame.Battle(command)); err != nil {
		return *checkpoint, err
	}
	if snapshot.Battle.HasActivePet() {
		// The native server waits for both owners. Keep both submissions
		// inside the same prepared checkpoint; a crash or ambiguous second
		// write must never replay the already submitted player command.
		next, err := c.Game.Observe(ctx)
		if err != nil {
			return c.pauseUnavailable(ctx, checkpoint, errors.Join(ErrUnknownDelivery, err), "人物指令已提交，宠物指令状态未确认，任务已暂停")
		}
		if next.Battle.Active && next.Battle.Turn == snapshot.Battle.Turn && !next.Battle.Movie && next.Battle.Result == "" {
			if !next.Battle.PetCommandReady() {
				return c.pauseUnavailable(ctx, checkpoint, ErrUnknownDelivery, "人物指令已提交，宠物指令状态未确认，任务已暂停")
			}
			petCommand := levelingPetCommand(next)
			if retreat {
				petCommand = "W|FF|FF"
			}
			if err := c.submit(ctx, checkpoint, next, aigame.Battle(petCommand)); err != nil {
				return *checkpoint, err
			}
		}
	}
	c.updatePending(checkpoint.Plan.ID, pendingAction{kind: "battle", turn: snapshot.Battle.Turn})
	checkpoint.Phase = "submitted"
	if err := c.save(ctx, checkpoint); err != nil {
		return *checkpoint, errors.Join(err, ErrUnknownDelivery)
	}
	return *checkpoint, nil
}

// 2.5 petskill.txt defines ID 1 as PETSKILL_NormalAttack, battle field 1,
// single-character target 6. Use its observed slot, never assume slot zero.
// KS and the active roster must agree before selecting an owned pet's skills.
func levelingPetCommand(snapshot aigame.Snapshot) string {
	const wait = "W|FF|FF"
	if !snapshot.Player.BattlePetSlotKnown || snapshot.Player.BattlePetSlot < 0 || snapshot.Player.BattlePetSlot >= 5 ||
		snapshot.Battle.BPFlags&(aigame.BattlePetMenuOff|aigame.BattleEnemySurprise) != 0 || !snapshot.Battle.HasActivePet() {
		return wait
	}
	var active aigame.BattleParticipant
	for _, actor := range snapshot.Battle.Participants {
		if actor.BattleID == snapshot.Battle.MyNo+5 {
			active = actor
			break
		}
	}
	target, ok := chooseEnemy(snapshot)
	if !ok {
		return wait
	}
	for _, pet := range snapshot.Pets {
		if pet.Slot != snapshot.Player.BattlePetSlot || pet.Graphic <= 0 || pet.Graphic != active.Graphic ||
			(active.Name != pet.Name && (pet.FreeName == "" || active.Name != pet.FreeName)) {
			continue
		}
		for _, skill := range pet.Skills {
			if skill.ID == 1 && skill.Index >= 0 && skill.Index < 7 && skill.Field == 1 && skill.Target == 6 && !skill.DeadTarget {
				return "W|" + battleHex(skill.Index) + "|" + battleHex(target.BattleID)
			}
		}
	}
	return wait
}

func (c *Coordinator) submitEndBattle(ctx context.Context, checkpoint *automation.Checkpoint, snapshot aigame.Snapshot, state progressState) (automation.Checkpoint, error) {
	if checkpoint.Phase == "submitted" {
		return *checkpoint, nil
	}
	checkpoint.Phase = "prepared"
	checkpoint.StepStartedAt = c.now()
	if err := c.save(ctx, checkpoint); err != nil {
		return *checkpoint, err
	}
	if err := c.submit(ctx, checkpoint, snapshot, aigame.EndBattle()); err != nil {
		return *checkpoint, err
	}
	c.updatePending(checkpoint.Plan.ID, pendingAction{kind: "battle-end", turn: snapshot.Battle.Turn})
	checkpoint.Phase = "submitted"
	if err := c.save(ctx, checkpoint); err != nil {
		return *checkpoint, errors.Join(err, ErrUnknownDelivery)
	}
	return *checkpoint, nil
}

func (c *Coordinator) submit(ctx context.Context, checkpoint *automation.Checkpoint, snapshot aigame.Snapshot, action aigame.Action) error {
	generation := c.generationFor(checkpoint.Plan.ID)
	if generation == 0 {
		generation = c.Gate.State().Generation
	}
	err := c.dispatchAction(ctx, generation, func(sendCtx context.Context) error {
		return c.Game.ExecuteExpected(sendCtx, snapshot.Revision, action)
	})
	if action.Kind == aigame.ActionMove && errors.Is(err, aigame.ErrStaleRevision) {
		return err
	}
	if err != nil {
		persist, stop := detachedPersistenceContext(ctx)
		defer stop()
		return errors.Join(ErrUnknownDelivery, err, c.pause(persist, checkpoint, "操作结果未确认，任务已暂停；禁止重试"))
	}
	return nil
}

func battleSide(id int32) int {
	if id >= 0 && id < 10 {
		return 0
	}
	if id >= 10 && id < 20 {
		return 1
	}
	return -1
}

func battleHex(id int32) string {
	return strings.ToUpper(strconv.FormatInt(int64(id), 16))
}

func chooseEnemy(snapshot aigame.Snapshot) (aigame.BattleParticipant, bool) {
	mine := battleSide(snapshot.Battle.MyNo)
	if mine < 0 {
		return aigame.BattleParticipant{}, false
	}
	var selected aigame.BattleParticipant
	found := false
	for _, participant := range snapshot.Battle.Participants {
		if participant.BattleID < 0 || participant.BattleID >= 20 || battleSide(participant.BattleID) == mine || participant.Dead || participant.HP <= 0 {
			continue
		}
		if !found || participant.BattleID < selected.BattleID {
			selected, found = participant, true
		}
	}
	return selected, found
}

func battleHasNoLiveEnemy(snapshot aigame.Snapshot) bool {
	if battleSide(snapshot.Battle.MyNo) < 0 {
		return false
	}
	for _, participant := range snapshot.Battle.Participants {
		if battleSide(participant.BattleID) != battleSide(snapshot.Battle.MyNo) && participant.BattleID >= 0 && participant.BattleID < 20 && !participant.Dead && participant.HP > 0 {
			return false
		}
	}
	return len(snapshot.Battle.Participants) > 0
}

// Run polls a durable handle until it completes or pauses. The context must
// be the ownership lease; when Coordinator.Lease is set that lease wins over
// the caller context. Cancellation is persisted as a pause using a detached
// short-lived context.
func (c *Coordinator) Run(ctx context.Context, handle string) error {
	if err := c.validateDependencies(); err != nil {
		return err
	}
	if c.Lease != nil {
		ctx = c.Lease
	}
	base, err := c.baseContext(ctx)
	if err != nil {
		return err
	}
	base, cancel := context.WithCancel(base)
	run := &activeRun{cancel: cancel, done: make(chan struct{}), generation: c.generationFor(handle)}
	c.mu.Lock()
	if c.active == nil {
		c.active = make(map[string]*activeRun)
	}
	if c.active[handle] != nil {
		c.mu.Unlock()
		cancel()
		return ErrAlreadyRunning
	}
	c.active[handle] = run
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		delete(c.active, handle)
		close(run.done)
		c.mu.Unlock()
	}()
	interval := c.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		checkpoint, tickErr := c.Tick(base, handle)
		if checkpoint.Status != automation.Running {
			return tickErr
		}
		if tickErr != nil {
			return tickErr
		}
		select {
		case <-base.Done():
			persist, stop := detachedPersistenceContext(base)
			defer stop()
			loaded, loadErr := c.Store.Load(persist, handle)
			if loadErr != nil {
				return errors.Join(base.Err(), loadErr)
			}
			if loaded.Status == automation.Running {
				pauseErr := c.pause(persist, &loaded, "控制租约已结束，任务已暂停")
				return errors.Join(base.Err(), pauseErr)
			}
			return base.Err()
		case <-ticker.C:
		}
	}
}

// Compile-time assertions keep accidental API drift visible to callers.
var _ Navigator = NavigatorFunc(nil)
var _ = hex.EncodeToString
