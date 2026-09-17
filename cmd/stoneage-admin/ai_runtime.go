package main

// This file is the process composition boundary for the optional AI runtime.
// The admin HTTP server can start with only the model registry; game login,
// funding, MCP and Codex are enabled only when every server-owned runtime
// setting is present and valid.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/admin"
	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/aifunding"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiprovision"
	"github.com/k0ngk0ng/stoneage/internal/airemote"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/aiservice"
	"github.com/k0ngk0ng/stoneage/internal/aisupervisor"
	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

// aiRuntimeOptions contains only command-line/environment configuration. It
// is intentionally separate from airuntime.Profile so a model or profile
// cannot choose a socket, executable, or filesystem boundary.
type aiRuntimeOptions struct {
	GameAddress         string
	RuntimeRoot         string
	MCPBinary           string
	SkillRoot           string
	RuntimeImage        string
	ContainerNetwork    string
	DockerBinary        string
	BrokerDB            string
	ContainerGatewayURL string
	FundingDir          string
	CodexBinary         string
	KnowledgeDataDir    string
	MapDataDir          string
	StockItems          string
	HealingItems        string
	NPCRegistry         string
	AutomationDB        string
	ReceiptDB           string
	GatewayListen       string
	GatewayPort         int
}

// aiRuntimeWiring owns every process-level AI dependency started by the
// admin command. Close is safe to call more than once and keeps the Gateway
// alive until all supervisor sessions have revoked their capabilities.
type aiRuntimeWiring struct {
	provisioner  *aiprovision.Provisioner
	funding      *aifunding.Manager
	provider     *aiprovision.ProfileSessionProvider
	broker       *aibroker.Broker
	remote       *airemote.Hub
	runtimeImage string
	gateway      *aiservice.Gateway
	listener     net.Listener
	gatewayHTTP  *http.Server
	plans        *automation.SQLiteStore
	receipts     *aiservice.ReceiptStore
	factory      *aiservice.Factory
	supervisor   *aisupervisor.Supervisor
	profiles     aiProfileLister
	restorer     aiProfileRestorer
	admin        admin.AIProfileRuntime

	closeOnce sync.Once
	closeErr  error
}

// aiProfileLister and aiProfileRestorer deliberately contain only the two
// operations needed for startup recovery. Keeping these as small interfaces
// lets the recovery policy be tested without constructing a Codex factory or
// opening a game connection.
type aiProfileLister interface {
	ListProfiles(context.Context) ([]airuntime.Profile, error)
}

type aiProfileRestorer interface {
	Restore(context.Context, string) error
}

// aiProfileRestoreFailure keeps startup errors useful to the operator while
// preventing a provider, process, or test-double error from putting a secret
// in the admin process error log. The original error remains available to
// errors.Is/errors.As for callers that need to classify it.
type aiProfileRestoreFailure struct {
	profileID string
	cause     error
}

func (failure *aiProfileRestoreFailure) Error() string {
	if failure == nil {
		return "AI profile restore failed"
	}
	if failure.profileID == "" {
		if errors.Is(failure.cause, context.Canceled) {
			return "AI profile restore canceled"
		}
		if errors.Is(failure.cause, context.DeadlineExceeded) {
			return "AI profile restore deadline exceeded"
		}
		return "AI profile restore failed"
	}
	if errors.Is(failure.cause, context.Canceled) {
		return fmt.Sprintf("AI profile %q restore canceled", failure.profileID)
	}
	if errors.Is(failure.cause, context.DeadlineExceeded) {
		return fmt.Sprintf("AI profile %q restore deadline exceeded", failure.profileID)
	}
	return fmt.Sprintf("AI profile %q restore failed", failure.profileID)
}

func (failure *aiProfileRestoreFailure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

// RestoreActive starts profiles whose durable status requests an active
// session. A missing runtime wiring is a normal model-only configuration and
// is therefore a no-op. Profiles are read before any restore is attempted so
// a profile paused or stopped in the database is never treated as running.
func (w *aiRuntimeWiring) RestoreActive(ctx context.Context) error {
	if w == nil || w.profiles == nil || w.restorer == nil {
		return nil
	}
	return restoreActiveProfiles(ctx, w.profiles, w.restorer)
}

// restoreActiveProfiles applies the startup recovery policy independently of
// process composition. One profile failure does not prevent later active
// profiles from being restored. Context cancellation intentionally stops the
// remaining queue, while preserving any failures already observed.
func restoreActiveProfiles(ctx context.Context, profiles aiProfileLister, restorer aiProfileRestorer) error {
	if profiles == nil || restorer == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	configured, err := profiles.ListProfiles(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return &aiProfileRestoreFailure{cause: err}
	}

	failures := make([]error, 0)
	for _, profile := range configured {
		if profile.Status != airuntime.ProfileStatusActive {
			continue
		}
		if err := ctx.Err(); err != nil {
			failures = append(failures, &aiProfileRestoreFailure{cause: err})
			break
		}
		if err := restorer.Restore(ctx, profile.ID); err != nil {
			failures = append(failures, &aiProfileRestoreFailure{profileID: profile.ID, cause: err})
		}
	}
	return errors.Join(failures...)
}

func (w *aiRuntimeWiring) AdminRuntime() admin.AIProfileRuntime {
	if w == nil {
		return nil
	}
	return w.admin
}

// Close stops the supervisor before closing the private HTTP listener. The
// supervisor's Close method also closes its FactoryCloser, so the factory is
// called directly only when supervisor construction did not complete.
func (w *aiRuntimeWiring) Close() error {
	if w == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		var errs []error
		if w.supervisor != nil {
			if err := w.supervisor.Close(); err != nil {
				errs = append(errs, err)
			}
		} else if w.factory != nil {
			if err := w.factory.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if w.provider != nil {
			if err := w.provider.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if w.gateway != nil {
			w.gateway.Close()
		}
		if w.gatewayHTTP != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := w.gatewayHTTP.Shutdown(ctx)
			cancel()
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs = append(errs, err)
			}
		}
		// Shutdown closes listeners registered by Serve, but it is allowed to
		// race the Serve goroutine during startup. Close the concrete listener
		// as well so Close always fences the private endpoint before returning.
		if w.listener != nil {
			if err := w.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, err)
			}
		}
		// The supervisor/factory has fenced all profile sessions before the
		// broker is closed. Closing the Gateway listener before the broker also
		// prevents a late capability request while Docker transitions stop.
		if w.broker != nil {
			if err := w.broker.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if w.remote != nil {
			if err := w.remote.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if w.receipts != nil {
			if err := w.receipts.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if w.plans != nil {
			if err := w.plans.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		w.closeErr = errors.Join(errs...)
	})
	return w.closeErr
}

// missingAIRuntimeOptions returns the required settings in stable order. An
// empty list means that the caller may safely construct a real runtime.
func missingAIRuntimeOptions(options aiRuntimeOptions) []string {
	options = normalizeAIRuntimeOptions(options)
	container := aiContainerRequested(options)
	missing := make([]string, 0, 13)
	required := []aiRuntimeRequirement{
		{"ai-game-address", options.GameAddress},
		{"ai-runtime-root", options.RuntimeRoot},
	}
	if container {
		required = append(required,
			aiRuntimeRequirement{"ai-skill-root", options.SkillRoot},
			aiRuntimeRequirement{"ai-funding-dir", options.FundingDir},
			aiRuntimeRequirement{"ai-knowledge-data-dir", options.KnowledgeDataDir},
			aiRuntimeRequirement{"ai-map-data-dir", options.MapDataDir},
			aiRuntimeRequirement{"ai-automation-db", options.AutomationDB},
			aiRuntimeRequirement{"ai-receipt-db", options.ReceiptDB},
			aiRuntimeRequirement{"ai-runtime-image", options.RuntimeImage},
			aiRuntimeRequirement{"ai-container-network", options.ContainerNetwork},
			aiRuntimeRequirement{"ai-docker-binary", options.DockerBinary},
			aiRuntimeRequirement{"ai-broker-db", options.BrokerDB},
			aiRuntimeRequirement{"ai-container-gateway-url", options.ContainerGatewayURL},
		)
	} else {
		required = append(required,
			aiRuntimeRequirement{"ai-mcp-binary", options.MCPBinary},
			aiRuntimeRequirement{"ai-skill-root", options.SkillRoot},
			aiRuntimeRequirement{"ai-funding-dir", options.FundingDir},
			aiRuntimeRequirement{"ai-codex-binary", options.CodexBinary},
			aiRuntimeRequirement{"ai-knowledge-data-dir", options.KnowledgeDataDir},
			aiRuntimeRequirement{"ai-map-data-dir", options.MapDataDir},
			aiRuntimeRequirement{"ai-automation-db", options.AutomationDB},
			aiRuntimeRequirement{"ai-receipt-db", options.ReceiptDB},
		)
	}
	for _, requirement := range required {
		if strings.TrimSpace(requirement.value) == "" {
			missing = append(missing, requirement.name)
		}
	}
	return missing
}

type aiRuntimeRequirement struct {
	name  string
	value string
}

func aiRuntimeComplete(options aiRuntimeOptions) bool {
	return len(missingAIRuntimeOptions(options)) == 0
}

// aiContainerRequested is deliberately based on any container-only setting.
// A partial container configuration must stay unavailable instead of falling
// back to a host Codex process with a different isolation boundary.
func aiContainerRequested(options aiRuntimeOptions) bool {
	return strings.TrimSpace(options.RuntimeImage) != "" ||
		strings.TrimSpace(options.ContainerNetwork) != "" ||
		strings.TrimSpace(options.DockerBinary) != "" ||
		strings.TrimSpace(options.BrokerDB) != "" ||
		strings.TrimSpace(options.ContainerGatewayURL) != ""
}

func aiConnectionTesterRequested(options aiRuntimeOptions) bool {
	return strings.TrimSpace(options.CodexBinary) != "" && !aiContainerRequested(options)
}

// aiContainerProbeRequested identifies the minimal server-owned settings for
// a model-only container probe. It intentionally does not require game
// address, map/NPC data, funding, or a Gateway: the probe must remain usable
// while the optional gameplay runtime is unavailable.
func aiContainerProbeRequested(options aiRuntimeOptions) bool {
	return strings.TrimSpace(options.RuntimeImage) != "" &&
		strings.TrimSpace(options.ContainerNetwork) != "" &&
		strings.TrimSpace(options.DockerBinary) != ""
}

// configureAIContainerModelProbeBroker constructs the standalone broker used
// when gameplay runtime composition is incomplete. A complete runtime shares
// its already-open broker instead, so the journal lock is never duplicated.
// The fallback journal lives under the private AI database directory and is
// used only for short-lived probe request records.
func configureAIContainerModelProbeBroker(options aiRuntimeOptions, dataRoot string) (*aibroker.Broker, error) {
	options = normalizeAIRuntimeOptions(options)
	if !aiContainerProbeRequested(options) {
		return nil, nil
	}
	journalPath := strings.TrimSpace(options.BrokerDB)
	if journalPath == "" {
		dataRoot = strings.TrimSpace(dataRoot)
		if dataRoot == "" || !filepath.IsAbs(dataRoot) || dataRoot == string(filepath.Separator) {
			return nil, errors.New("AI container probe requires a private data root")
		}
		journalPath = filepath.Join(dataRoot, "connection-probe.db")
	}
	return aibroker.New(aibroker.Config{
		DockerBinary: options.DockerBinary,
		Image:        options.RuntimeImage,
		Network:      options.ContainerNetwork,
		JournalPath:  journalPath,
	})
}

// normalizeAIRuntimeOptions applies only safe, deterministic defaults. The
// broker journal may be derived from an explicitly supplied absolute runtime
// root; image, network, Docker and the advertised URL remain explicit so a
// partial container setup cannot silently change execution mode.
func normalizeAIRuntimeOptions(options aiRuntimeOptions) aiRuntimeOptions {
	options.GameAddress = strings.TrimSpace(options.GameAddress)
	options.RuntimeRoot = strings.TrimSpace(options.RuntimeRoot)
	options.MCPBinary = strings.TrimSpace(options.MCPBinary)
	options.SkillRoot = strings.TrimSpace(options.SkillRoot)
	options.RuntimeImage = strings.TrimSpace(options.RuntimeImage)
	options.ContainerNetwork = strings.TrimSpace(options.ContainerNetwork)
	options.DockerBinary = strings.TrimSpace(options.DockerBinary)
	options.BrokerDB = strings.TrimSpace(options.BrokerDB)
	options.ContainerGatewayURL = strings.TrimSpace(options.ContainerGatewayURL)
	options.FundingDir = strings.TrimSpace(options.FundingDir)
	options.CodexBinary = strings.TrimSpace(options.CodexBinary)
	options.KnowledgeDataDir = strings.TrimSpace(options.KnowledgeDataDir)
	options.MapDataDir = strings.TrimSpace(options.MapDataDir)
	options.AutomationDB = strings.TrimSpace(options.AutomationDB)
	options.ReceiptDB = strings.TrimSpace(options.ReceiptDB)
	options.GatewayListen = strings.TrimSpace(options.GatewayListen)
	if aiContainerRequested(options) && options.BrokerDB == "" {
		root := filepath.Clean(options.RuntimeRoot)
		if filepath.IsAbs(root) && root != string(filepath.Separator) {
			options.BrokerDB = filepath.Join(root, "broker.db")
		}
	}
	return options
}

// configureAIRuntime composes the optional runtime. It does not start a
// Codex process or make a model request. Incomplete configuration returns
// (nil, nil), allowing the model-only admin console to remain available and
// report an explicit stopped/unavailable runtime.
func configureAIRuntime(ctx context.Context, authStore *auth.Store, modelStore *airuntime.Store, modelSecrets *airuntime.SecretStore, options aiRuntimeOptions) (*aiRuntimeWiring, error) {
	options = normalizeAIRuntimeOptions(options)
	if !aiRuntimeComplete(options) {
		return nil, nil
	}
	if authStore == nil || modelStore == nil || modelSecrets == nil {
		return nil, errors.New("AI runtime requires auth and AI stores")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	containerMode := aiContainerRequested(options)
	var listenAddress string
	var containerGatewayEndpoint string
	var err error
	if containerMode {
		listenAddress, err = normalizeAIContainerGatewayListen(options.GatewayListen, options.GatewayPort)
		if err != nil {
			return nil, err
		}
		containerGatewayEndpoint, err = normalizeAIContainerGatewayURL(options.ContainerGatewayURL)
		if err != nil {
			return nil, err
		}
	} else {
		listenAddress, err = normalizeAIGatewayListen(options.GatewayListen, options.GatewayPort)
		if err != nil {
			return nil, err
		}
	}
	if containerMode {
		if err := validateAIBrokerDBPath(options.RuntimeRoot, options.BrokerDB); err != nil {
			return nil, err
		}
	}
	if err := validateAIDedicatedPath(options.RuntimeRoot, "AI runtime root"); err != nil {
		return nil, err
	}
	if err := validateAIDedicatedPath(options.FundingDir, "AI funding directory"); err != nil {
		return nil, err
	}
	// These snapshots are loaded once from server-owned data volumes and are
	// never writable through the AI runtime. A missing or invalid data volume
	// keeps the whole gameplay runtime unavailable rather than exposing a
	// model-only session with a falsely usable automation surface.
	knowledge, err := aiknowledge.LoadDataDir(ctx, options.KnowledgeDataDir)
	if err != nil {
		return nil, fmt.Errorf("load AI knowledge data: %w", err)
	}
	npcs, err := aiservice.LoadNPCRegistry(options.NPCRegistry, knowledge.Fingerprint())
	if err != nil {
		return nil, fmt.Errorf("load AI NPC contracts: %w", err)
	}
	healingItems, err := aiservice.LoadHealingItemsForData(options.HealingItems, knowledge.Fingerprint(), filepath.Join(options.KnowledgeDataDir, "itemset.txt"))
	if err != nil {
		return nil, fmt.Errorf("load AI healing item contracts: %w", err)
	}
	stockItems, err := aiservice.LoadStockItems(options.StockItems, knowledge.Fingerprint(), npcs, healingItems)
	if err != nil {
		return nil, fmt.Errorf("load AI stock contracts: %w", err)
	}
	navigator, err := ainavigation.LoadDataDir(ctx, options.MapDataDir)
	if err != nil {
		return nil, fmt.Errorf("load AI map data: %w", err)
	}
	plans, err := automation.OpenStore(options.AutomationDB)
	if err != nil {
		return nil, fmt.Errorf("open AI automation store: %w", err)
	}
	wiring := &aiRuntimeWiring{plans: plans, profiles: modelStore}
	cleanupOnError := true
	defer func() {
		if cleanupOnError {
			_ = wiring.Close()
		}
	}()
	if containerMode {
		docker, err := aibroker.NewDocker(options.DockerBinary)
		if err != nil {
			return nil, fmt.Errorf("configure AI Docker transport: %w", err)
		}
		remote, err := airemote.New(airemote.Config{
			Docker:     docker,
			StatePath:  filepath.Join(options.RuntimeRoot, "remote-workers.json"),
			GatewayURL: containerGatewayEndpoint,
			Guard: func(ctx context.Context, id string) error {
				if wiring.supervisor == nil {
					return admin.ErrAIRuntimeUnavailable
				}
				return wiring.supervisor.PrepareExecutorChange(ctx, id)
			},
			Start: func(ctx context.Context, id string) error {
				if wiring.supervisor == nil {
					return admin.ErrAIRuntimeUnavailable
				}
				return wiring.supervisor.Start(ctx, id)
			},
		})
		if err != nil {
			return nil, fmt.Errorf("configure AI remote transport: %w", err)
		}
		wiring.remote, wiring.runtimeImage = remote, options.RuntimeImage
		broker, err := aibroker.New(aibroker.Config{
			Docker:       remote,
			DockerBinary: options.DockerBinary,
			Image:        options.RuntimeImage,
			Network:      options.ContainerNetwork,
			JournalPath:  options.BrokerDB,
		})
		if err != nil {
			return nil, fmt.Errorf("configure AI container broker: %w", err)
		}
		wiring.broker = broker
	}
	receipts, err := aiservice.OpenReceiptStore(options.ReceiptDB)
	if err != nil {
		return nil, fmt.Errorf("open AI receipt store: %w", err)
	}
	wiring.receipts = receipts
	gameplayBuilder, err := aiservice.NewGameplayBuilder(aiservice.GameplayConfig{
		Plans:        plans,
		Tiles:        navigator,
		NPCs:         npcs,
		HealingItems: healingItems,
		StockItems:   stockItems,
	})
	if err != nil {
		return nil, fmt.Errorf("configure AI gameplay: %w", err)
	}

	gameSecrets, err := aiprovision.NewSecretStore(filepath.Join(options.RuntimeRoot, "game-secrets"))
	if err != nil {
		return nil, fmt.Errorf("configure AI game secrets: %w", err)
	}
	funding, err := aifunding.NewManager(options.FundingDir)
	if err != nil {
		return nil, fmt.Errorf("configure AI funding: %w", err)
	}
	provisioner, err := aiprovision.New(aiprovision.Config{
		ProviderConfig: aiprovision.ProviderConfig{
			Auth:       authStore,
			Secrets:    gameSecrets,
			Game:       aiprovision.AigameConnector{},
			GameConfig: aigame.Config{Address: options.GameAddress},
		},
		Profiles: modelStore,
	})
	if err != nil {
		return nil, fmt.Errorf("configure AI provisioning: %w", err)
	}
	if err := provisioner.ReconcileInitialAccounts(ctx); err != nil {
		return nil, fmt.Errorf("reconcile AI initialization: %w", err)
	}
	provider := provisioner.Provider()
	if provider == nil {
		return nil, errors.New("configure AI provisioning: provider unavailable")
	}
	wiring.provisioner = provisioner
	wiring.funding = funding
	wiring.provider = provider

	gateway := aiservice.NewGateway()
	wiring.gateway = gateway
	listener, gatewayHTTP, endpoint, err := listenAIGateway(gateway, listenAddress)
	if err != nil {
		return nil, err
	}
	wiring.listener = listener
	wiring.gatewayHTTP = gatewayHTTP
	if containerMode {
		endpoint = containerGatewayEndpoint
	}

	sessions := &fundedAIProfileProvider{provisioner: provisioner, provider: provider, funding: funding}
	factoryConfig := aiservice.FactoryConfig{
		Models:          modelStore,
		Secrets:         modelSecrets,
		Sessions:        sessions,
		Gateway:         gateway,
		GatewayEndpoint: endpoint,
		SkillRoot:       options.SkillRoot,
		RuntimeRoot:     options.RuntimeRoot,
		Backend:         gameplayBuilder,
		Knowledge:       knowledge,
		Receipts:        receipts,
		MemoryStore:     modelStore,
	}
	if containerMode {
		// A typed nil *Broker inside this interface would select the
		// container runner even for an explicitly local Codex runtime.
		factoryConfig.ContainerBroker = wiring.broker
	} else {
		factoryConfig.CodexBinary = options.CodexBinary
		factoryConfig.MCPBinary = options.MCPBinary
	}
	factory, err := aiservice.NewFactory(factoryConfig)
	if err != nil {
		return nil, fmt.Errorf("configure AI Codex factory: %w", err)
	}
	wiring.factory = factory
	supervisor, err := aisupervisor.New(ctx, modelStore, factory, aisupervisor.Config{})
	if err != nil {
		return nil, fmt.Errorf("configure AI supervisor: %w", err)
	}
	wiring.supervisor = supervisor
	wiring.restorer = supervisor
	adapter, err := admin.NewAISupervisorRuntimeAdapter(supervisor)
	if err != nil {
		return nil, fmt.Errorf("configure AI admin runtime: %w", err)
	}
	wiring.admin = adapter
	cleanupOnError = false
	return wiring, nil
}

// fundedAIProfileProvider translates the provisioner's lease type into the
// service factory's lease type and publishes funding only for a live AI
// session. The policy is server-owned and is revoked synchronously with the
// session, so profile JSON or model output cannot grant it.
type fundedAIProfileProvider struct {
	provisioner *aiprovision.Provisioner
	provider    *aiprovision.ProfileSessionProvider
	funding     *aifunding.Manager
}

func (p *fundedAIProfileProvider) Open(ctx context.Context, profile airuntime.Profile) (aiservice.SessionLease, error) {
	if p == nil || p.provisioner == nil || p.provider == nil || p.funding == nil {
		return aiservice.SessionLease{}, errors.New("AI session provider is unavailable")
	}
	binding, err := p.provisioner.Binding(ctx, profile.ID)
	if err != nil {
		return aiservice.SessionLease{}, err
	}
	if profile.UnlimitedFunds {
		if err := p.funding.Provision(binding.AccountUsername, binding.CharacterSlot, profile.ExternalSpendLimit); err != nil {
			return aiservice.SessionLease{}, err
		}
	} else if err := p.funding.Revoke(binding.AccountUsername, binding.CharacterSlot); err != nil {
		return aiservice.SessionLease{}, err
	}

	lease, err := p.provider.Open(ctx, profile)
	if err != nil {
		_ = p.funding.Revoke(binding.AccountUsername, binding.CharacterSlot)
		return aiservice.SessionLease{}, err
	}
	fundingLookup := aiservice.FundingLookup(func(lookupCtx context.Context) (bool, error) {
		if lookupCtx == nil {
			lookupCtx = context.Background()
		}
		if err := lookupCtx.Err(); err != nil {
			return false, err
		}
		return p.funding.Allowed(binding.AccountUsername, binding.CharacterSlot), nil
	})
	var closeOnce sync.Once
	closeLease := lease.Close
	lease.Close = func() {
		closeOnce.Do(func() {
			_ = p.funding.Revoke(binding.AccountUsername, binding.CharacterSlot)
			if closeLease != nil {
				closeLease()
			}
		})
	}
	return aiservice.SessionLease{
		Session: lease.Session,
		Funding: fundingLookup,
		Wake:    lease.Wake,
		Close:   lease.Close,
	}, nil
}

// aiPlayerProvisionerAdapter is the only command-side bridge from the
// admin's non-secret create request to aiprovision. The game account name
// and password are intentionally left empty here so aiprovision generates
// both server-side and keeps the password in its private secret store.
type aiPlayerProvisionerAdapter struct {
	provisioner *aiprovision.Provisioner
	initializer aiprovision.Initializer
}

func newAIPlayerProvisionerAdapter(provisioner *aiprovision.Provisioner, initializers ...aiprovision.Initializer) admin.AIPlayerProvisioner {
	if provisioner == nil {
		return nil
	}
	var initializer aiprovision.Initializer
	if len(initializers) > 0 {
		initializer = initializers[0]
	}
	return &aiPlayerProvisionerAdapter{provisioner: provisioner, initializer: initializer}
}

func (adapter *aiPlayerProvisionerAdapter) Provision(ctx context.Context, request admin.AIPlayerProvisionRequest) (airuntime.Profile, error) {
	if adapter == nil || adapter.provisioner == nil {
		return airuntime.Profile{}, errors.New("AI game player provisioner is unavailable")
	}
	initial, err := adapter.initialRequest(ctx, request.InitialState)
	if err != nil {
		return airuntime.Profile{}, err
	}
	created, err := adapter.provisioner.Provision(ctx, aiprovision.CreateRequest{
		InitialState: initial, Initializer: adapter.initializer,
		ProfileID:       request.ProfileID,
		CharacterSlot:   request.CharacterSlot,
		CharacterName:   request.CharacterName,
		CharacterCreate: defaultAICharacterCreate(),
		UnlimitedFunds:  request.UnlimitedFunds,
		Profile: airuntime.Profile{
			ID:                 request.ProfileID,
			ModelConfigID:      request.ModelConfigID,
			Personality:        request.Personality,
			Goal:               request.Goal,
			Skills:             request.Skills,
			DailyTokenBudget:   request.DailyTokenBudget,
			ExternalSpendLimit: request.ExternalSpendLimit,
			Status:             request.Status,
		},
		Actor:   request.Actor,
		ActorID: request.ActorID,
	})
	if err != nil {
		return airuntime.Profile{}, err
	}
	return created.Profile, nil
}

func (adapter *aiPlayerProvisionerAdapter) InitialState(ctx context.Context, id string) (*aiprovision.InitialState, error) {
	return adapter.provisioner.InitialState(ctx, id)
}

// defaultAICharacterCreate is a reviewed server-side stock character setup.
// The browser chooses only the character name and slot; image, attributes
// and hometown remain fixed so character creation cannot become a privilege
// or resource injection surface.
func defaultAICharacterCreate() aigame.CharacterCreate {
	return aigame.CharacterCreate{
		Image: 100000, FaceImage: 30000,
		Vital: 5, Strength: 5, Toughness: 5, Dexterity: 5,
		Earth: 10, Hometown: 0,
	}
}

func validateAIDedicatedPath(path, label string) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." || !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return fmt.Errorf("%s must be a non-root absolute path", label)
	}
	return nil
}

func normalizeAIGatewayListen(address string, port int) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		address = "127.0.0.1:0"
	}
	host, configuredPort, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("AI Gateway listen address is invalid")
	}
	if port < -1 || port > 65535 {
		return "", errors.New("AI Gateway port must be between 0 and 65535")
	}
	if port >= 0 {
		configuredPort = strconv.Itoa(port)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return "", errors.New("AI Gateway must listen on a loopback address")
	}
	parsedPort, err := strconv.Atoi(configuredPort)
	if err != nil || parsedPort < 0 || parsedPort > 65535 {
		return "", errors.New("AI Gateway listen port is invalid")
	}
	return net.JoinHostPort(ip.String(), configuredPort), nil
}

// normalizeAIContainerGatewayListen validates the address that the host-side
// Gateway binds for child containers. A fixed, reachable port is required so
// it can agree with the separately advertised container URL. The wildcard
// IPv4 address is allowed only in this mode; otherwise an explicit private
// interface address is required.
func normalizeAIContainerGatewayListen(address string, port int) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", errors.New("container AI Gateway listen address is required")
	}
	host, configuredPort, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" {
		return "", errors.New("container AI Gateway listen address is invalid")
	}
	if port < -1 || port > 65535 {
		return "", errors.New("AI Gateway port must be between 0 and 65535")
	}
	if port >= 0 {
		configuredPort = strconv.Itoa(port)
	}
	parsedPort, err := strconv.Atoi(configuredPort)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return "", errors.New("container AI Gateway listen port must be between 1 and 65535")
	}
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	if host == "0.0.0.0" {
		return net.JoinHostPort(host, strconv.Itoa(parsedPort)), nil
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || !ip.IsPrivate() {
		return "", errors.New("container AI Gateway must listen on 0.0.0.0 or an explicit private address")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(parsedPort)), nil
}

// normalizeAIContainerGatewayURL validates the fixed endpoint placed in the
// container's MCP configuration. It must be reachable from the container
// network and cannot redirect requests through credentials or URL extras.
func normalizeAIContainerGatewayURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("container AI Gateway URL is required")
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.Contains(value, "#") || parsed.Path != "/v1/game" || parsed.RawPath != "" || parsed.Opaque != "" {
		return "", errors.New("container AI Gateway URL must be an http(s) URL with path /v1/game")
	}
	hostname := parsed.Hostname()
	if hostname == "" || strings.Contains(hostname, "%") {
		return "", errors.New("container AI Gateway URL must have a reachable hostname")
	}
	canonicalHost := strings.ToLower(strings.TrimSuffix(hostname, "."))
	if canonicalHost == "localhost" || canonicalHost == "*" || canonicalHost == "0" || canonicalHost == "::" {
		return "", errors.New("container AI Gateway URL must not use a loopback or wildcard host")
	}
	ip := net.ParseIP(hostname)
	if ip == nil {
		ip = net.ParseIP(strings.TrimSuffix(hostname, "."))
	}
	if ip != nil && (ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() || !ip.IsGlobalUnicast()) {
		return "", errors.New("container AI Gateway URL must not use a loopback or wildcard host")
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return "", errors.New("container AI Gateway URL has an invalid port")
	}
	if rawPort := parsed.Port(); rawPort != "" {
		parsedPort, portErr := strconv.Atoi(rawPort)
		if portErr != nil || parsedPort < 1 || parsedPort > 65535 {
			return "", errors.New("container AI Gateway URL has an invalid port")
		}
	}
	return parsed.String(), nil
}

// validateAIContainerGatewayURL is the error-only form used by callers that
// already retain the original URL string.
func validateAIContainerGatewayURL(value string) error {
	_, err := normalizeAIContainerGatewayURL(value)
	return err
}

func validateAIBrokerDBPath(runtimeRoot, brokerDB string) error {
	root := filepath.Clean(strings.TrimSpace(runtimeRoot))
	path := filepath.Clean(strings.TrimSpace(brokerDB))
	if err := validateAIDedicatedPath(root, "AI runtime root"); err != nil {
		return err
	}
	if err := validateAIDedicatedPath(path, "AI broker database"); err != nil {
		return err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve AI runtime root: %w", err)
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve AI broker database: %w", err)
	}
	relative, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return errors.New("AI broker database must be inside the AI runtime root")
	}
	return nil
}

func listenAIGateway(gateway *aiservice.Gateway, address string) (net.Listener, *http.Server, string, error) {
	if gateway == nil {
		return nil, nil, "", errors.New("AI Gateway is unavailable")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, nil, "", fmt.Errorf("listen AI Gateway: %w", err)
	}
	server := &http.Server{
		Handler:           gateway,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	host, _, splitErr := net.SplitHostPort(listener.Addr().String())
	if splitErr != nil {
		_ = listener.Close()
		return nil, nil, "", errors.New("AI Gateway listener address is invalid")
	}
	_, port, splitErr := net.SplitHostPort(listener.Addr().String())
	if splitErr != nil {
		_ = listener.Close()
		return nil, nil, "", errors.New("AI Gateway listener port is invalid")
	}
	endpointURL := &url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/v1/game"}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// The server is private and has no caller-visible error channel. A
			// failed listener makes subsequent MCP calls fail closed; startup
			// validation still returns before this goroutine is launched.
		}
	}()
	return listener, server, endpointURL.String(), nil
}
