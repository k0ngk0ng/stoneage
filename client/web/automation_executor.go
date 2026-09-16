package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aileveling"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
	"github.com/k0ngk0ng/stoneage/internal/aiservice"
	"github.com/k0ngk0ng/stoneage/internal/automation"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
)

const (
	webAutomationPollInterval = 200 * time.Millisecond
	webAutomationMaxSeconds   = 30 * 24 * 60 * 60
	webAutomationMaxDeaths    = 1000000
	webAutomationMaxTargets   = 16
	webAutomationMaxBudget    = int64(2000000000)
)

// AutomationExecutorConfig contains only server-owned, immutable gameplay
// dependencies. The browser supplies selectors and limits through
// AutomationStartRequest; it cannot replace the knowledge, map or durable
// stores used by this executor.
type AutomationExecutorConfig struct {
	Knowledge    *aiknowledge.Knowledge
	Tiles        aiservice.TileNavigator
	Plans        automation.Store
	Receipts     *aiservice.ReceiptStore
	NPCs         aiservice.NPCRegistry
	StockItems   map[string]aiservice.StockContract
	HealingItems map[string]aiservice.HealingItemContract

	PollInterval      time.Duration
	NoProgressTimeout time.Duration
}

// AutomationExecutor is the real Web task/leveling adapter. It composes the
// reviewed aiservice gameplay builder around the already authenticated Web
// TCP stream. It never calls aigame.Connect or creates a second socket.
type AutomationExecutor struct {
	config AutomationExecutorConfig

	// recoverMu guards the process-local reconnect registry.  The registry
	// intentionally stores only immutable recovery metadata and the original
	// selectors; live backends remain owned by their old Web session and are
	// closed when that session detaches.
	recoverMu  sync.Mutex
	recoveries map[string]*automationRecoveryRecord
}

// NewAutomationExecutor validates the fixed gameplay dependencies. A nil
// dependency is an unavailable executor rather than a partially functional
// implementation which might fabricate a route, cost or completion result.
func NewAutomationExecutor(config AutomationExecutorConfig) (*AutomationExecutor, error) {
	if config.Knowledge == nil {
		return nil, errors.New("automation knowledge is unavailable")
	}
	if strings.TrimSpace(config.Knowledge.Fingerprint()) == "" {
		return nil, errors.New("automation knowledge fingerprint is unavailable")
	}
	if config.Tiles == nil {
		return nil, errors.New("automation map navigation is unavailable")
	}
	if config.Plans == nil {
		return nil, errors.New("automation plan store is unavailable")
	}
	if config.Receipts == nil {
		return nil, errors.New("automation receipt store is unavailable")
	}
	if config.PollInterval <= 0 {
		config.PollInterval = webAutomationPollInterval
	}
	if config.PollInterval > time.Minute {
		return nil, errors.New("automation poll interval is too long")
	}
	if config.NoProgressTimeout < 0 || config.NoProgressTimeout > 30*24*time.Hour {
		return nil, errors.New("automation no-progress timeout is invalid")
	}
	return &AutomationExecutor{config: config, recoveries: make(map[string]*automationRecoveryRecord)}, nil
}

// Start validates the request against the current gate lease and starts one
// deterministic server-side run. ctx is the context returned by Gate.Switch;
// it is intentionally retained as the run lifetime instead of using an HTTP
// request context.
func (executor *AutomationExecutor) Start(ctx context.Context, session *AutomationSession, request AutomationStartRequest) (AutomationHandle, error) {
	if executor == nil {
		return nil, errAutomationUnavailable
	}
	if ctx == nil {
		return nil, errors.New("automation lease is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateAutomationStart(session, request); err != nil {
		return nil, err
	}
	if err := executor.validateSupplyOffer(request.Config.Supply); err != nil {
		return nil, err
	}
	snapshot, err := session.Observe(ctx)
	if err != nil {
		return nil, err
	}
	binding, err := webAutomationBuildBinding(snapshot, request.Generation, request.Config.CharacterBuild)
	if err != nil {
		return nil, err
	}
	state := session.State()
	if state.Generation != request.Generation || state.Mode != request.Mode {
		return nil, aicontrol.ErrStale
	}
	identity, identityReady := automationIdentityFromBinding(binding, snapshot, session.session.serverLineID())
	if identityReady && executor.recoveryBusy(identity) {
		return nil, errAutomationRecoveryBusy
	}

	builder, err := aiservice.NewGameplayBuilder(aiservice.GameplayConfig{
		Plans:        executor.recoveryPlanStore(identity, request.Config),
		Tiles:        executor.config.Tiles,
		NPCs:         executor.config.NPCs,
		HealingItems: executor.config.HealingItems,
		StockItems:   executor.config.StockItems,
	})
	if err != nil {
		return nil, fmt.Errorf("configure automation gameplay: %w", err)
	}
	backendValue, err := builder(ctx, aiservice.BackendInput{
		Binding:        binding,
		CharacterBuild: request.Config.CharacterBuild.Clone(),
		Gate:           session.session.gate,
		Session:        session,
		Knowledge:      executor.config.Knowledge,
		Receipts:       executor.config.Receipts,
		// Ordinary Web players deliberately have no AI funding callback. The
		// resulting observation keeps UnlimitedFunds=false even when the
		// account's visible gold is zero or absent.
		Lease: ctx,
		Wake:  nil,
	})
	if err != nil {
		return nil, fmt.Errorf("build automation gameplay: %w", err)
	}
	backend, ok := backendValue.(*aiservice.GameBackend)
	if !ok || backend == nil {
		if closer, ok := backendValue.(interface{ Close() }); ok {
			closer.Close()
		}
		return nil, errors.New("automation gameplay backend is unavailable")
	}
	backendOwned := false
	defer func() {
		if !backendOwned {
			backend.Close()
		}
	}()

	handle := &webAutomationHandle{
		executor:      executor,
		session:       session,
		backend:       backend,
		binding:       binding,
		identity:      identity,
		config:        cloneAutomationConfig(request.Config),
		mode:          request.Mode,
		generation:    request.Generation,
		ctx:           ctx,
		poll:          executor.config.PollInterval,
		noProgress:    executor.config.NoProgressTimeout,
		plans:         executor.config.Plans,
		requestBudget: request.Config.Budget,
	}
	gameTasks, ok := backend.Tasks.(*aiservice.GameTasks)
	if !ok || gameTasks == nil {
		return nil, errors.New("automation task controller is unavailable")
	}
	handle.gameTasks = gameTasks

	switch request.Mode {
	case aicontrol.Quest:
		handle.questTasks = gameTasks.Quests
		if handle.questTasks == nil {
			return nil, errors.New("automation quest controller is unavailable")
		}
		questRequest, err := handle.questRequest(request.Config)
		if err != nil {
			return nil, err
		}
		if err := handle.validateQuestBudget(ctx, questRequest); err != nil {
			return nil, err
		}
		receipt, err := handle.questTasks.StartTask(ctx, questRequest)
		if err != nil {
			return nil, err
		}
		handle.taskHandle = receipt.Handle
		if receipt.Status == aimcp.ReceiptRunning || receipt.Status == aimcp.ReceiptPending {
			handle.startWatcher()
		} else {
			handle.terminalReceipt = &receipt
		}
	case aicontrol.Leveling:
		handle.leveling = gameTasks.Leveling
		if handle.leveling == nil {
			return nil, errors.New("automation leveling controller is unavailable")
		}
		// Keep the server-owned timeout on the controller as well as in the
		// request. Coordinator.Resume uses the controller default after a
		// lease handoff, while Start persists the explicit setting with the
		// checkpoint.
		handle.leveling.NoProgressTimeout = executor.config.NoProgressTimeout
		levelingRequest, err := handle.levelingRequest(request.Config)
		if err != nil {
			return nil, err
		}
		receipt, err := handle.leveling.Start(ctx, levelingRequest)
		if err != nil {
			return nil, err
		}
		handle.taskHandle = receipt.Handle
		if receipt.Status == aimcp.ReceiptRunning || receipt.Status == aimcp.ReceiptPending {
			// Coordinator.Start only creates the durable checkpoint. The Web
			// executor owns the corresponding Run loop so this path is also
			// resumable after GameTasks' original lease has been cancelled.
			handle.startLevelingRunner(ctx)
			handle.startWatcher()
		} else {
			handle.terminalReceipt = &receipt
		}
	default:
		return nil, errors.New("unsupported automation mode")
	}
	backendOwned = true
	return handle, nil
}

// buildWebAutomationHandle composes a fresh gameplay backend around the
// supplied authenticated Web session without creating a task checkpoint.
// Recovery uses this path so none of the old lease-bound Engine, Coordinator
// or GameTasks objects can survive a disconnected session.
func (executor *AutomationExecutor) buildWebAutomationHandle(ctx context.Context, session *AutomationSession, request AutomationStartRequest, binding aimcp.Binding) (*webAutomationHandle, error) {
	if executor == nil || session == nil || session.session == nil || session.session.gate == nil {
		return nil, errors.New("automation session is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("automation lease is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot, err := session.Observe(ctx)
	if err != nil {
		return nil, err
	}
	identity, _ := automationIdentityFromBinding(binding, snapshot, session.session.serverLineID())
	builder, err := aiservice.NewGameplayBuilder(aiservice.GameplayConfig{
		Plans:        executor.recoveryPlanStore(identity, request.Config),
		Tiles:        executor.config.Tiles,
		NPCs:         executor.config.NPCs,
		HealingItems: executor.config.HealingItems,
		StockItems:   executor.config.StockItems,
	})
	if err != nil {
		return nil, fmt.Errorf("configure automation gameplay: %w", err)
	}
	backendValue, err := builder(ctx, aiservice.BackendInput{
		Binding:        binding,
		CharacterBuild: request.Config.CharacterBuild.Clone(),
		Gate:           session.session.gate,
		Session:        session,
		Knowledge:      executor.config.Knowledge,
		Receipts:       executor.config.Receipts,
		Lease:          ctx,
		Wake:           nil,
	})
	if err != nil {
		return nil, fmt.Errorf("build automation gameplay: %w", err)
	}
	backend, ok := backendValue.(*aiservice.GameBackend)
	if !ok || backend == nil {
		if closer, ok := backendValue.(interface{ Close() }); ok {
			closer.Close()
		}
		return nil, errors.New("automation gameplay backend is unavailable")
	}
	handle := &webAutomationHandle{
		executor:      executor,
		session:       session,
		backend:       backend,
		binding:       binding,
		identity:      identity,
		config:        cloneAutomationConfig(request.Config),
		mode:          request.Mode,
		generation:    request.Generation,
		ctx:           ctx,
		poll:          executor.config.PollInterval,
		noProgress:    executor.config.NoProgressTimeout,
		plans:         executor.config.Plans,
		requestBudget: request.Config.Budget,
	}
	gameTasks, ok := backend.Tasks.(*aiservice.GameTasks)
	if !ok || gameTasks == nil {
		handle.closeBackend()
		return nil, errors.New("automation task controller is unavailable")
	}
	handle.gameTasks = gameTasks
	switch request.Mode {
	case aicontrol.Quest:
		handle.questTasks = gameTasks.Quests
		if handle.questTasks == nil {
			handle.closeBackend()
			return nil, errors.New("automation quest controller is unavailable")
		}
	case aicontrol.Leveling:
		handle.leveling = gameTasks.Leveling
		if handle.leveling == nil {
			handle.closeBackend()
			return nil, errors.New("automation leveling controller is unavailable")
		}
		handle.leveling.NoProgressTimeout = executor.config.NoProgressTimeout
	default:
		handle.closeBackend()
		return nil, errors.New("unsupported automation mode")
	}
	return handle, nil
}

// Preview resolves the same knowledge snapshot as Start and reports a
// server-backed preflight. It never creates a checkpoint or writes the game
// socket, so changing form values cannot execute an action.
func (executor *AutomationExecutor) Preview(ctx context.Context, session *AutomationSession, request AutomationStartRequest) (AutomationPreview, error) {
	if executor == nil {
		return AutomationPreview{}, errAutomationUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateAutomationPreview(session, request); err != nil {
		return AutomationPreview{}, err
	}
	snapshot, err := session.Observe(ctx)
	if err != nil {
		return AutomationPreview{}, err
	}
	binding, err := webAutomationBuildBinding(snapshot, request.Generation, request.Config.CharacterBuild)
	if err != nil {
		return AutomationPreview{}, err
	}
	result := AutomationPreview{Problems: []string{}}
	if err := executor.validateSupplyOffer(request.Config.Supply); err != nil {
		return result, err
	}
	var plan automation.Plan
	switch request.Mode {
	case aicontrol.Quest:
		if err := aiservice.ValidateQuestPetBinding(webAutomationObservation(snapshot, binding.CharacterID), binding.CharacterID, request.Config.SelectedPetID); err != nil {
			return AutomationPreview{}, err
		}
		builder := aiplanner.New(executor.config.Knowledge)
		build := builder.BuildTask
		if request.Config.IncludeDependencies {
			build = builder.BuildChain
		}
		plan, err = build(ctx, request.Config.TaskID, aiplanner.TaskOptions{
			CharacterID:    binding.CharacterID,
			SelectedPetID:  request.Config.SelectedPetID,
			MaximumSeconds: request.Config.MaximumSeconds,
			MaximumDeaths:  request.Config.MaximumDeaths,
			ReserveGold:    request.Config.Budget.Reserve,
		})
	case aicontrol.Leveling:
		result.Budget = AutomationBudget{Reserve: request.Config.Budget.Reserve, MaximumSpend: request.Config.Budget.MaximumSpend}
		observation := webAutomationObservation(snapshot, binding.CharacterID)
		result.AlreadyComplete = levelingTargetsComplete(snapshot, binding.CharacterID, request.Config.Targets, request.Config.TargetPolicy)
		if build := request.Config.CharacterBuild; build != nil {
			settled := snapshot.Connected && snapshot.Phase == aigame.PhaseWorld && snapshot.Player.HasStatus && snapshot.Player.HP > 0 && !snapshot.Battle.Active && !snapshot.Trade.Active && !snapshot.Trade.Pending && snapshot.Player.StatPointsKnown && snapshot.Player.UnspentStatPoints >= 0 && snapshot.Player.UnspentStatPoints <= int32(build.ReservePoints)
			pending, err := executor.config.Receipts.HasUnknown(ctx, binding, "")
			if err != nil {
				return AutomationPreview{}, err
			}
			result.AlreadyComplete = result.AlreadyComplete && settled && !pending
			if pending {
				result.Problems = append(result.Problems, "上次游戏操作结果尚未确认，不能自动加点")
			}
		}
		if !result.AlreadyComplete {
			result.Problems = append(result.Problems, executor.supplyPreviewProblems(ctx, session, request, binding, snapshot)...)
			if !observation.Connected || !observation.Ready {
				result.Problems = append(result.Problems, "游戏状态尚未同步")
			}
			if observation.CharacterID != binding.CharacterID {
				result.Problems = append(result.Problems, "角色身份不一致")
			}
			if observation.Gold < request.Config.Budget.Reserve {
				result.Problems = append(result.Problems, "可用石币不足保留金额")
			}
			for _, target := range request.Config.Targets {
				if levelingTargetPresent(snapshot, binding.CharacterID, target) {
					continue
				}
				result.Problems = append(result.Problems, "找不到练级目标："+strings.TrimSpace(target.ID))
			}
		}
	default:
		return AutomationPreview{}, errors.New("unsupported automation mode")
	}
	if err != nil {
		return AutomationPreview{}, err
	}
	if request.Mode == aicontrol.Quest {
		result.Budget = webAutomationBudget(plan.Budget)
		for _, stage := range plan.Stages {
			result.TaskOrder = append(result.TaskOrder, stage.Title)
		}
		if err := validateWebQuestBudget(plan.Budget, request.Config.Budget); err != nil {
			result.Problems = append(result.Problems, err.Error())
		}
		observation := aiservice.ProjectAutomationObservation(aiservice.ProjectObservation(binding, snapshot), executor.config.NPCs)
		preflight, err := executor.previewQuest(ctx, session, request, binding, plan, observation)
		if err != nil {
			return AutomationPreview{}, err
		}
		result.AlreadyComplete = preflight.AlreadyComplete
		result.Problems = append(result.Problems, preflight.Problems...)
	}
	result.Ready = len(result.Problems) == 0
	return result, nil
}

func validateAutomationStart(session *AutomationSession, request AutomationStartRequest) error {
	if session == nil || session.session == nil || session.session.gate == nil {
		return errors.New("automation session is unavailable")
	}
	if request.SessionID != "" && request.SessionID != session.ID {
		return errors.New("automation session identity does not match")
	}
	if request.Generation == 0 {
		return aicontrol.ErrStale
	}
	if request.Mode != aicontrol.Quest && request.Mode != aicontrol.Leveling {
		return errors.New("unsupported automation mode")
	}
	if request.Config.IncludeDependencies && request.Mode != aicontrol.Quest {
		return errors.New("自动前置任务仅适用于自动任务模式")
	}
	if err := validateWebSupply(request.Mode, request.Config.Supply); err != nil {
		return err
	}
	if err := validateWebCharacterBuild(request.Mode, request.Config.CharacterBuild); err != nil {
		return err
	}
	if request.Config.MaximumSeconds <= 0 || request.Config.MaximumSeconds > webAutomationMaxSeconds || request.Config.MaximumDeaths < 0 || request.Config.MaximumDeaths > webAutomationMaxDeaths {
		return errors.New("invalid automation execution limits")
	}
	if err := validateAutomationBudget(request.Config.Budget); err != nil {
		return err
	}
	if request.Config.OfflineContinue {
		return errors.New("offline continuation is unavailable for the Web session")
	}
	if request.Mode == aicontrol.Quest && strings.TrimSpace(request.Config.TaskID) == "" {
		return errors.New("task_id is required for quest automation")
	}
	if request.Mode == aicontrol.Leveling {
		if len(request.Config.Targets) == 0 || len(request.Config.Targets) > webAutomationMaxTargets {
			return errors.New("leveling targets are required")
		}
		if request.Config.TargetPolicy != "all" && request.Config.TargetPolicy != "any" {
			return errors.New("leveling target policy must be all or any")
		}
		seen := make(map[string]struct{}, len(request.Config.Targets))
		for _, target := range request.Config.Targets {
			kind := strings.ToLower(strings.TrimSpace(target.Kind))
			id := strings.TrimSpace(target.ID)
			if (kind != "character" && kind != "pet") || target.Level <= 0 || target.Level > 1000 || (kind == "pet" && id == "") {
				return errors.New("leveling target identity or level is invalid")
			}
			key := kind + ":" + id
			if _, ok := seen[key]; ok {
				return errors.New("duplicate leveling target")
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}

func validateAutomationPreview(session *AutomationSession, request AutomationStartRequest) error {
	if session == nil || session.session == nil || session.session.gate == nil {
		return errors.New("automation session is unavailable")
	}
	if request.SessionID != "" && request.SessionID != session.ID {
		return errors.New("automation session identity does not match")
	}
	if request.Generation == 0 {
		return aicontrol.ErrStale
	}
	if request.Mode != aicontrol.Quest && request.Mode != aicontrol.Leveling {
		return errors.New("unsupported automation mode")
	}
	if request.Config.IncludeDependencies && request.Mode != aicontrol.Quest {
		return errors.New("自动前置任务仅适用于自动任务模式")
	}
	if err := validateWebSupply(request.Mode, request.Config.Supply); err != nil {
		return err
	}
	if err := validateWebCharacterBuild(request.Mode, request.Config.CharacterBuild); err != nil {
		return err
	}
	if request.Config.MaximumSeconds < 0 || request.Config.MaximumSeconds > webAutomationMaxSeconds || request.Config.MaximumDeaths < 0 || request.Config.MaximumDeaths > webAutomationMaxDeaths {
		return errors.New("invalid automation execution limits")
	}
	if err := validateAutomationBudget(request.Config.Budget); err != nil {
		return err
	}
	if request.Mode == aicontrol.Quest && strings.TrimSpace(request.Config.TaskID) == "" {
		return errors.New("task_id is required for quest automation")
	}
	if request.Mode == aicontrol.Leveling {
		if len(request.Config.Targets) == 0 || len(request.Config.Targets) > webAutomationMaxTargets {
			return errors.New("leveling targets are required")
		}
		if request.Config.TargetPolicy != "all" && request.Config.TargetPolicy != "any" {
			return errors.New("leveling target policy must be all or any")
		}
	}
	return nil
}

func validateAutomationBudget(budget AutomationBudget) error {
	for _, value := range []int64{budget.Minimum, budget.ExpectedLow, budget.ExpectedHigh, budget.Reserve, budget.MaximumSpend} {
		if value < 0 || value > webAutomationMaxBudget {
			return errors.New("invalid automation budget")
		}
	}
	if budget.ExpectedHigh > 0 && budget.ExpectedLow > budget.ExpectedHigh {
		return errors.New("automation budget range is invalid")
	}
	return nil
}

func (handle *webAutomationHandle) questRequest(config AutomationConfig) (aimcp.TaskRequest, error) {
	parameters := make(map[string]json.RawMessage, 5)
	put := func(name string, value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		parameters[name] = raw
		return nil
	}
	if err := put("maximum_seconds", config.MaximumSeconds); err != nil {
		return aimcp.TaskRequest{}, err
	}
	if err := put("maximum_deaths", config.MaximumDeaths); err != nil {
		return aimcp.TaskRequest{}, err
	}
	if err := put("reserve_gold", config.Budget.Reserve); err != nil {
		return aimcp.TaskRequest{}, err
	}
	if config.IncludeDependencies {
		if err := put("include_dependencies", true); err != nil {
			return aimcp.TaskRequest{}, err
		}
	}
	if config.SelectedPetID != "" {
		if err := put("selected_pet_id", config.SelectedPetID); err != nil {
			return aimcp.TaskRequest{}, err
		}
	}
	return aimcp.TaskRequest{TaskID: config.TaskID, Parameters: parameters}, nil
}

func (handle *webAutomationHandle) levelingRequest(config AutomationConfig) (aileveling.StartRequest, error) {
	targets := make([]aileveling.Target, 0, len(config.Targets))
	for _, target := range config.Targets {
		targets = append(targets, aileveling.Target{Kind: strings.ToLower(strings.TrimSpace(target.Kind)), ID: strings.TrimSpace(target.ID), Level: target.Level})
	}
	return aileveling.StartRequest{
		Targets: targets, TargetPolicy: config.TargetPolicy,
		MaximumSeconds: config.MaximumSeconds, MaximumDeaths: config.MaximumDeaths,
		ReserveGold: config.Budget.Reserve, MaximumSpend: config.Budget.MaximumSpend,
		NoProgressTimeout: handle.noProgress,
		Parameters:        webSupplyParameters(config.Supply),
	}, nil
}

func (handle *webAutomationHandle) validateQuestBudget(ctx context.Context, request aimcp.TaskRequest) error {
	if handle == nil || handle.questTasks == nil || handle.questTasks.Builder == nil {
		return errors.New("automation quest planner is unavailable")
	}
	plan, err := handle.questTasks.Builder.Task(ctx, request)
	if err != nil {
		return err
	}
	return validateWebQuestBudget(plan.Budget, handle.requestBudget)
}

func validateWebQuestBudget(quoted automation.Budget, budget AutomationBudget) error {
	if budget.MaximumSpend > 0 && quoted.MaximumSpend > budget.MaximumSpend {
		return errors.New("花费上限低于知识库核验的最高费用")
	}
	if budget.Minimum > 0 && budget.Minimum != quoted.Minimum {
		return errors.New("任务最低费用与知识库不一致")
	}
	if budget.ExpectedLow > 0 && budget.ExpectedLow != quoted.ExpectedLow {
		return errors.New("任务预计费用与知识库不一致")
	}
	if budget.ExpectedHigh > 0 && budget.ExpectedHigh != quoted.ExpectedHigh {
		return errors.New("任务最高费用与知识库不一致")
	}
	return nil
}

func webAutomationBinding(snapshot aigame.Snapshot, generation uint64) (aimcp.Binding, error) {
	account := strings.TrimSpace(snapshot.Account)
	character := strings.TrimSpace(snapshot.Character)
	if account == "" || character == "" {
		return aimcp.Binding{}, errors.New("server character identity is unavailable")
	}
	slot := -1
	for _, candidate := range snapshot.Characters {
		if candidate.Name == character {
			slot = candidate.Slot
			break
		}
	}
	characterID := account + ":" + character
	if slot >= 0 {
		characterID = account + ":" + strconv.Itoa(slot)
	}
	binding := aimcp.Binding{AccountID: account, CharacterID: characterID, CharacterName: character, Generation: generation}
	if err := binding.Validate(); err != nil {
		return aimcp.Binding{}, err
	}
	return binding, nil
}

func webAutomationBudget(budget automation.Budget) AutomationBudget {
	return AutomationBudget{Minimum: budget.Minimum, ExpectedLow: budget.ExpectedLow, ExpectedHigh: budget.ExpectedHigh, Reserve: budget.Reserve, MaximumSpend: budget.MaximumSpend}
}

func webAutomationObservation(snapshot aigame.Snapshot, characterID string) automation.Observation {
	return aiservice.ProjectAutomationObservation(aiservice.ProjectObservation(aimcp.Binding{CharacterID: characterID}, snapshot), nil)
}

func levelingTargetPresent(snapshot aigame.Snapshot, characterID string, target AutomationTarget) bool {
	kind := strings.ToLower(strings.TrimSpace(target.Kind))
	id := strings.TrimSpace(target.ID)
	if kind == "character" {
		return id == "" || id == characterID || id == snapshot.Character || id == strconv.FormatInt(int64(snapshot.Player.ID), 10)
	}
	if kind != "pet" || id == "" {
		return false
	}
	for _, pet := range snapshot.Pets {
		if pet.IdentityKnown && pet.StableID == id {
			return true
		}
	}
	return false
}

func levelingTargetSatisfied(snapshot aigame.Snapshot, characterID string, target AutomationTarget) bool {
	if !levelingTargetPresent(snapshot, characterID, target) {
		return false
	}
	kind := strings.ToLower(strings.TrimSpace(target.Kind))
	if kind == "character" {
		return int(snapshot.Player.Level) >= target.Level
	}
	for _, pet := range snapshot.Pets {
		if pet.IdentityKnown && pet.StableID == strings.TrimSpace(target.ID) {
			return int(pet.Level) >= target.Level
		}
	}
	return false
}

func levelingTargetsComplete(snapshot aigame.Snapshot, characterID string, targets []AutomationTarget, policy string) bool {
	matched := 0
	for _, target := range targets {
		if levelingTargetSatisfied(snapshot, characterID, target) {
			matched++
		}
	}
	if strings.EqualFold(strings.TrimSpace(policy), "any") {
		return matched > 0
	}
	return len(targets) > 0 && matched == len(targets)
}

// webAutomationHandle owns one durable task handle. Lifecycle operations are
// serialized so a simultaneous pause/takeover/resume cannot launch a second
// run or replay an uncertain game action.
type webAutomationHandle struct {
	mu sync.Mutex

	executor   *AutomationExecutor
	session    *AutomationSession
	backend    *aiservice.GameBackend
	binding    aimcp.Binding
	identity   automationIdentity
	config     AutomationConfig
	mode       aicontrol.Mode
	generation uint64
	ctx        context.Context
	poll       time.Duration
	noProgress time.Duration
	plans      automation.Store

	gameTasks        *aiservice.GameTasks
	questTasks       *aiservice.Tasks
	leveling         *aileveling.Coordinator
	taskHandle       string
	requestBudget    AutomationBudget
	terminalReceipt  *aimcp.TaskReceipt
	watchDone        chan struct{}
	levelDone        chan struct{}
	stopped          bool
	detached         bool
	backendCloseOnce sync.Once
}

func (handle *webAutomationHandle) startLevelingRunner(ctx context.Context) {
	if handle == nil || handle.leveling == nil || ctx == nil {
		return
	}
	handle.mu.Lock()
	leveling, id := handle.leveling, handle.taskHandle
	done := make(chan struct{})
	handle.levelDone = done
	handle.mu.Unlock()
	if leveling == nil || strings.TrimSpace(id) == "" {
		close(done)
		return
	}
	go func() {
		defer close(done)
		err := leveling.Run(ctx, id)
		if err == nil {
			return
		}
		// Run persists cancellation and other failures itself where possible.
		// Only issue the detached fallback while the durable receipt still says
		// running, avoiding a second revision write after a normal pause.
		status, statusErr := leveling.Status(context.Background(), id)
		if statusErr != nil || (status.Status != aimcp.ReceiptRunning && status.Status != aimcp.ReceiptPending) {
			return
		}
		persist, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = pauseCheckpointPreservingPhase(persist, handle.plans, id, "练级执行中断，需要核验游戏状态")
		cancel()
	}()
}

func (handle *webAutomationHandle) closeBackend() {
	if handle == nil {
		return
	}
	handle.backendCloseOnce.Do(func() {
		if handle.backend != nil {
			handle.backend.Close()
		}
	})
}

// Activate completes a terminal start after the HTTP adapter has installed
// the handle in the session. This ordering lets an already-complete plan
// release the Gate without racing setAutomation during start.
func (handle *webAutomationHandle) Activate() {
	if handle == nil {
		return
	}
	handle.mu.Lock()
	receipt := handle.terminalReceipt
	handle.terminalReceipt = nil
	handle.mu.Unlock()
	if receipt != nil {
		handle.finishReceipt(*receipt)
	}
}

func (handle *webAutomationHandle) startWatcher() {
	if handle == nil {
		return
	}
	handle.mu.Lock()
	if handle.watchDone != nil {
		select {
		case <-handle.watchDone:
			handle.watchDone = nil
		default:
			handle.mu.Unlock()
			return
		}
	}
	done := make(chan struct{})
	handle.watchDone = done
	ctx := handle.ctx
	handle.mu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(handle.poll)
		defer ticker.Stop()
		for {
			receipt, err := handle.status(context.Background())
			if err == nil && receipt.Status != aimcp.ReceiptRunning && receipt.Status != aimcp.ReceiptPending {
				handle.finishReceipt(receipt)
				return
			}
			select {
			case <-ctx.Done():
				// The deterministic controller persists a cancelled lease as a
				// pause. Keep polling briefly so the lifecycle response never
				// races that durable write.
			case <-ticker.C:
			}
			if ctx.Err() != nil {
				deadline := time.NewTimer(3 * time.Second)
				select {
				case <-deadline.C:
					return
				case <-ticker.C:
				}
				deadline.Stop()
			}
		}
	}()
}

func (handle *webAutomationHandle) status(ctx context.Context) (aimcp.TaskReceipt, error) {
	handle.mu.Lock()
	mode, id := handle.mode, handle.taskHandle
	quest, level := handle.questTasks, handle.leveling
	handle.mu.Unlock()
	if mode == aicontrol.Quest && quest != nil {
		return quest.Status(ctx, id)
	}
	if mode == aicontrol.Leveling && level != nil {
		return level.Status(ctx, id)
	}
	return aimcp.TaskReceipt{}, errors.New("automation task status is unavailable")
}

func (handle *webAutomationHandle) finishReceipt(receipt aimcp.TaskReceipt) {
	if handle == nil {
		return
	}
	terminal := receipt.Status == aimcp.ReceiptConfirmed || receipt.Status == aimcp.ReceiptFailed || receipt.Status == aimcp.ReceiptCancelled || receipt.Status == aimcp.ReceiptUnknown
	handle.mu.Lock()
	executor, id := handle.executor, handle.taskHandle
	if handle.stopped || handle.detached {
		handle.mu.Unlock()
		return
	}
	generation, mode := handle.generation, handle.mode
	if terminal {
		// The watcher has already read a durable terminal receipt. Clear its
		// marker before releasing the lock so an immediate resume can install a
		// fresh watcher without a stale closed channel blocking it.
		handle.watchDone = nil
		// Keep the registry operation inside the handle lock. Detach takes the
		// same lock before registering its paused checkpoint; moving this call
		// after unlock would let a late terminal watcher erase that new offer.
		if executor != nil {
			executor.clearRecoveryHandle(id)
		}
	}
	handle.mu.Unlock()
	if handle.session == nil || handle.session.session == nil {
		return
	}
	state := handle.session.State()
	if receipt.Status == aimcp.ReceiptConfirmed {
		if state.Generation == generation && state.Mode == mode {
			_, _, _ = handle.session.session.gate.Switch(generation, aicontrol.Manual, "自动化已完成")
		}
		_ = handle.session.session.clearAutomation(generation)
		handle.closeBackend()
		return
	}
	if receipt.Status == aimcp.ReceiptFailed || receipt.Status == aimcp.ReceiptCancelled || receipt.Status == aimcp.ReceiptUnknown {
		if state.Generation == generation && state.Mode == mode {
			_, _, _ = handle.session.session.gate.Switch(generation, aicontrol.Paused, receipt.Reason)
		}
	}
}

func (handle *webAutomationHandle) Stop(ctx context.Context) error {
	if handle == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	handle.mu.Lock()
	if handle.stopped {
		executor, id := handle.executor, handle.taskHandle
		handle.mu.Unlock()
		if executor != nil {
			executor.clearRecoveryHandle(id)
		}
		return nil
	}
	handle.stopped = true
	mode, id := handle.mode, handle.taskHandle
	executor := handle.executor
	quest, level := handle.questTasks, handle.leveling
	handle.mu.Unlock()
	var err error
	switch mode {
	case aicontrol.Quest:
		if quest != nil {
			_, err = quest.Cancel(ctx, aimcp.CancelRequest{Handle: id, Reason: "自动化已停止"})
		}
	case aicontrol.Leveling:
		if level != nil {
			_, err = level.Cancel(ctx, id, "自动化已停止")
		}
	}
	handle.closeBackend()
	if executor != nil {
		executor.clearRecoveryHandle(id)
	}
	return err
}

func (handle *webAutomationHandle) Pause(ctx context.Context) error {
	if handle == nil {
		return errors.New("automation handle is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		receipt, err := handle.status(ctx)
		if err != nil {
			return err
		}
		if receipt.Status != aimcp.ReceiptRunning && receipt.Status != aimcp.ReceiptPending {
			handle.finishReceipt(receipt)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(handle.poll):
		}
	}
}

func (handle *webAutomationHandle) Resume(ctx context.Context) error {
	if handle == nil || handle.session == nil || handle.session.session == nil {
		return errors.New("automation handle is unavailable")
	}
	if ctx == nil {
		return errors.New("automation lease is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	handle.mu.Lock()
	if handle.stopped {
		handle.mu.Unlock()
		return errors.New("automation handle is stopped")
	}
	mode, id := handle.mode, handle.taskHandle
	quest, level := handle.questTasks, handle.leveling
	backend := handle.backend
	handle.mu.Unlock()
	state := handle.session.State()
	if state.Mode != mode || state.Generation == 0 {
		return aicontrol.ErrOwner
	}
	if backend == nil {
		return errors.New("automation gameplay backend is unavailable")
	}
	// AutomationSession is a bound capability, not a view over the mutable
	// tcpSession lease. A resume gets a new generation, so every controller
	// reference must be rebound to a new session object; the old object keeps
	// its cancelled generation and can never borrow this token.
	boundSession := &AutomationSession{ID: handle.session.ID, session: handle.session.session, mode: mode, generation: state.Generation}
	backend.Binding.Generation = state.Generation
	backend.Owner = mode
	backend.Binding.CharacterID = handle.binding.CharacterID
	backend.Session = boundSession

	switch mode {
	case aicontrol.Quest:
		if quest == nil {
			return errors.New("automation quest controller is unavailable")
		}
		// aiservice.Tasks binds its worker lifetime to the lease supplied at
		// construction. Recreate only this controller around the same engine
		// and verified builder; the durable checkpoint remains the identity.
		replacement := &aiservice.Tasks{Engine: quest.Engine, Builder: quest.Builder, CharacterID: quest.CharacterID, Lease: ctx}
		receipt, err := replacement.Resume(ctx, id)
		if err != nil {
			return err
		}
		handle.mu.Lock()
		handle.questTasks = replacement
		if handle.gameTasks != nil {
			handle.gameTasks.Quests = replacement
		}
		handle.session = boundSession
		handle.generation = state.Generation
		handle.binding.Generation = state.Generation
		handle.ctx = ctx
		handle.mu.Unlock()
		if receipt.Status == aimcp.ReceiptRunning || receipt.Status == aimcp.ReceiptPending {
			handle.startWatcher()
		} else {
			handle.finishReceipt(receipt)
		}
		return nil
	case aicontrol.Leveling:
		if level == nil {
			return errors.New("automation leveling controller is unavailable")
		}
		// Coordinator.Resume performs the authoritative pending-step
		// reconciliation and rebinding under the new Gate lease. Keeping the
		// same coordinator preserves the verified area/parameter settings.
		level.Game = boundSession
		receipt, err := level.Resume(ctx, id, state.Generation)
		if err != nil {
			return err
		}
		handle.mu.Lock()
		handle.session = boundSession
		handle.generation = state.Generation
		handle.binding.Generation = state.Generation
		handle.ctx = ctx
		handle.mu.Unlock()
		if receipt.Status == aimcp.ReceiptRunning || receipt.Status == aimcp.ReceiptPending {
			handle.startLevelingRunner(ctx)
			handle.startWatcher()
		} else {
			handle.finishReceipt(receipt)
		}
		return nil
	default:
		return errors.New("unsupported automation mode")
	}
}

// resumeFresh resumes a handle whose backend and controllers were composed
// for the current lease. It intentionally does not call Start and therefore
// never creates a new durable checkpoint.
func (handle *webAutomationHandle) resumeFresh(ctx context.Context) error {
	if handle == nil || handle.session == nil || handle.session.session == nil {
		return errors.New("automation handle is unavailable")
	}
	if ctx == nil {
		return errors.New("automation lease is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	handle.mu.Lock()
	mode, id := handle.mode, handle.taskHandle
	quest, level := handle.questTasks, handle.leveling
	handle.mu.Unlock()
	state := handle.session.State()
	if state.Generation == 0 || state.Mode != mode {
		return aicontrol.ErrOwner
	}
	handle.mu.Lock()
	handle.ctx = ctx
	handle.generation = state.Generation
	handle.binding.Generation = state.Generation
	handle.mu.Unlock()
	switch mode {
	case aicontrol.Quest:
		if quest == nil {
			return errors.New("automation quest controller is unavailable")
		}
		receipt, err := quest.Resume(ctx, id)
		if err != nil {
			return err
		}
		if receipt.Status == aimcp.ReceiptRunning || receipt.Status == aimcp.ReceiptPending {
			handle.startWatcher()
		} else {
			// Defer terminal cleanup until the HTTP layer has installed this
			// handle in the session, matching Start's Activate ordering.
			handle.mu.Lock()
			handle.terminalReceipt = &receipt
			handle.mu.Unlock()
		}
		return nil
	case aicontrol.Leveling:
		if level == nil {
			return errors.New("automation leveling controller is unavailable")
		}
		receipt, err := level.Resume(ctx, id, state.Generation)
		if err != nil {
			return err
		}
		if receipt.Status == aimcp.ReceiptRunning || receipt.Status == aimcp.ReceiptPending {
			handle.startLevelingRunner(ctx)
			handle.startWatcher()
		} else {
			// See the quest branch above: Activate owns terminal release after
			// the HTTP layer has successfully installed the handle.
			handle.mu.Lock()
			handle.terminalReceipt = &receipt
			handle.mu.Unlock()
		}
		return nil
	default:
		return errors.New("unsupported automation mode")
	}
}

var _ Automation = (*AutomationExecutor)(nil)
var _ AutomationHandle = (*webAutomationHandle)(nil)
var _ automationPauser = (*webAutomationHandle)(nil)
var _ automationResumer = (*webAutomationHandle)(nil)
var _ automationStopper = (*webAutomationHandle)(nil)

// Keep the concrete navigation type referenced here so a build-time change
// cannot accidentally replace the map adapter with an unverified route.
var _ aiservice.TileNavigator = (*ainavigation.Navigator)(nil)

func validateWebCharacterBuild(mode aicontrol.Mode, build *characterbuild.Policy) error {
	if build == nil {
		return nil
	}
	if mode != aicontrol.Leveling {
		return errors.New("人物自动加点仅用于自动练级")
	}
	return build.Validate()
}

// Automatic stat changes require a canonical character slot, so a reconnect
// cannot change receipt scope from an account:name fallback to account:slot.
func webAutomationBuildBinding(snapshot aigame.Snapshot, generation uint64, build *characterbuild.Policy) (aimcp.Binding, error) {
	binding, err := webAutomationBinding(snapshot, generation)
	if err != nil || build == nil {
		return binding, err
	}
	matches := 0
	slot := -1
	for _, candidate := range snapshot.Characters {
		if candidate.Name == snapshot.Character {
			matches++
			slot = candidate.Slot
		}
	}
	if matches != 1 || slot < 0 || binding.CharacterID != binding.AccountID+":"+strconv.Itoa(slot) {
		return aimcp.Binding{}, errors.New("角色列表尚未同步，暂不能自动加点；请重新登录游戏后再试")
	}
	return binding, nil
}
