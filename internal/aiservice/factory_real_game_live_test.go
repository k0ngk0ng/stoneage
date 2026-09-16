package aiservice

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aimodels"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

const (
	realGameFactoryLiveOptIn = "STONEAGE_REAL_GAME_FACTORY_LIVE_TEST"
	realGameFactoryAddress   = "127.0.0.1:39065"
	realGameFactoryAccount   = "adminqa"
	realGameFactoryPassword  = "local"
	realGameFactoryCharacter = "QAPlayer"
	realGameFactoryCodex     = "/Users/jason/.local/bin/codex"
	realGameFactorySkillMark = "STONEAGE_PLAY_REAL_GAME_FACTORY_"
)

// realGameFactorySession records the protocol observations and submissions
// made by the production gameplay builder. The wrapper is deliberately
// restrictive: this QA check must never move, battle, spend, or otherwise
// mutate the named game session.
type realGameFactorySession struct {
	session *aigame.Session

	mu           sync.Mutex
	observations []aigame.Snapshot
	actions      []aigame.Action
}

func (session *realGameFactorySession) Observe(ctx context.Context) (aigame.Snapshot, error) {
	if session == nil || session.session == nil {
		return aigame.Snapshot{}, aigame.ErrClosed
	}
	snapshot, err := session.session.Observe(ctx)
	if err != nil {
		return aigame.Snapshot{}, err
	}
	session.mu.Lock()
	session.observations = append(session.observations, snapshot)
	session.mu.Unlock()
	return snapshot, nil
}

func (session *realGameFactorySession) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if session == nil || session.session == nil {
		return aigame.ErrClosed
	}
	session.mu.Lock()
	session.actions = append(session.actions, action)
	session.mu.Unlock()
	// OwnStateRefresher uses exactly this read-only request. Any other action
	// is rejected before reaching the QA server, so a bad model turn cannot
	// change the fixture even if it ignores the prompt.
	if action.Kind != aigame.ActionStatus || action.Command != "AI" {
		return errors.New("real game Factory check permits only status AI")
	}
	return session.session.ExecuteExpected(ctx, revision, action)
}

func (session *realGameFactorySession) snapshots() []aigame.Snapshot {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return append([]aigame.Snapshot(nil), session.observations...)
}

func (session *realGameFactorySession) actionsSnapshot() []aigame.Action {
	if session == nil {
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return append([]aigame.Action(nil), session.actions...)
}

func (session *realGameFactorySession) latest() aigame.Snapshot {
	if session == nil || session.session == nil {
		return aigame.Snapshot{}
	}
	return session.session.Snapshot()
}

// realGameFactoryBackend records calls crossing the private Gateway. It
// forwards reads and task operations to the production GameBackend, while
// refusing the mutation entry points to protect QA data during this test.
type realGameFactoryBackend struct {
	aimcp.Backend

	mu              sync.Mutex
	calls           []string
	observations    []aimcp.Observation
	levelingRequest []aimcp.LevelingRequest
	levelingReceipt []aimcp.TaskReceipt
	statusHandles   []string
	statusReceipts  []aimcp.TaskReceipt
	closeOnce       sync.Once
}

func (backend *realGameFactoryBackend) recordCall(name string) {
	backend.mu.Lock()
	backend.calls = append(backend.calls, name)
	backend.mu.Unlock()
}

func (backend *realGameFactoryBackend) Observe(ctx context.Context, binding aimcp.Binding) (aimcp.Observation, error) {
	backend.recordCall("observe")
	observation, err := backend.Backend.Observe(ctx, binding)
	if err == nil {
		backend.mu.Lock()
		backend.observations = append(backend.observations, cloneRealGameFactoryObservation(observation))
		backend.mu.Unlock()
	}
	return observation, err
}

func (backend *realGameFactoryBackend) QueryKnowledge(ctx context.Context, binding aimcp.Binding, query aimcp.KnowledgeQuery) (aimcp.KnowledgeResult, error) {
	backend.recordCall("knowledge")
	return backend.Backend.QueryKnowledge(ctx, binding, query)
}

func (backend *realGameFactoryBackend) StartTask(context.Context, aimcp.Binding, aimcp.TaskRequest) (aimcp.TaskReceipt, error) {
	backend.recordCall("start_task")
	return aimcp.TaskReceipt{}, aimcp.ErrBackend
}

func (backend *realGameFactoryBackend) StartLeveling(ctx context.Context, binding aimcp.Binding, request aimcp.LevelingRequest) (aimcp.TaskReceipt, error) {
	backend.recordCall("start_leveling")
	backend.mu.Lock()
	backend.levelingRequest = append(backend.levelingRequest, cloneRealGameFactoryLevelingRequest(request))
	backend.mu.Unlock()
	receipt, err := backend.Backend.StartLeveling(ctx, binding, request)
	if err == nil {
		backend.mu.Lock()
		backend.levelingReceipt = append(backend.levelingReceipt, cloneRealGameFactoryReceipt(receipt))
		backend.mu.Unlock()
	}
	return receipt, err
}

func (backend *realGameFactoryBackend) TaskStatus(ctx context.Context, binding aimcp.Binding, handle string) (aimcp.TaskReceipt, error) {
	backend.recordCall("task_status")
	backend.mu.Lock()
	backend.statusHandles = append(backend.statusHandles, handle)
	backend.mu.Unlock()
	receipt, err := backend.Backend.TaskStatus(ctx, binding, handle)
	if err == nil {
		backend.mu.Lock()
		backend.statusReceipts = append(backend.statusReceipts, cloneRealGameFactoryReceipt(receipt))
		backend.mu.Unlock()
	}
	return receipt, err
}

func (backend *realGameFactoryBackend) Cancel(context.Context, aimcp.Binding, aimcp.CancelRequest) (aimcp.TaskReceipt, error) {
	backend.recordCall("cancel")
	return aimcp.TaskReceipt{}, aimcp.ErrBackend
}

func (backend *realGameFactoryBackend) GameAction(context.Context, aimcp.Binding, aimcp.TypedAction) (aimcp.ActionReceipt, error) {
	backend.recordCall("action")
	return aimcp.ActionReceipt{}, aimcp.ErrBackend
}

func (backend *realGameFactoryBackend) snapshot() (calls []string, observations []aimcp.Observation, requests []aimcp.LevelingRequest, starts, statuses []aimcp.TaskReceipt, handles []string) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return append([]string(nil), backend.calls...),
		append([]aimcp.Observation(nil), backend.observations...),
		append([]aimcp.LevelingRequest(nil), backend.levelingRequest...),
		append([]aimcp.TaskReceipt(nil), backend.levelingReceipt...),
		append([]aimcp.TaskReceipt(nil), backend.statusReceipts...),
		append([]string(nil), backend.statusHandles...)
}

func (backend *realGameFactoryBackend) Close() {
	if backend == nil {
		return
	}
	backend.closeOnce.Do(func() {
		if closer, ok := backend.Backend.(interface{ Close() }); ok {
			closer.Close()
		}
	})
}

type realGameFactoryEvidence struct {
	Test                    string                `json:"test"`
	Status                  string                `json:"status"`
	Gateway                 string                `json:"gateway"`
	Character               string                `json:"character"`
	CodexVersion            string                `json:"codex_version"`
	Model                   string                `json:"model"`
	Provider                string                `json:"provider"`
	WireAPI                 string                `json:"wire_api"`
	ApprovalPolicy          string                `json:"approval_policy"`
	SandboxMode             string                `json:"sandbox_mode"`
	NativeSkill             string                `json:"native_skill"`
	NativeSkillMarkerOK     bool                  `json:"native_skill_marker_verified"`
	MCPCalls                map[string]int        `json:"mcp_calls"`
	MCPCallOrder            []string              `json:"mcp_call_order"`
	GameObservation         realGameFactoryGame   `json:"game_observation"`
	StablePetIDs            []string              `json:"stable_pet_ids"`
	UnlimitedFunds          bool                  `json:"unlimited_funds"`
	LevelingRequest         aimcp.LevelingRequest `json:"leveling_request"`
	LevelingReceipt         aimcp.TaskReceipt     `json:"leveling_receipt"`
	TaskStatusReceipt       aimcp.TaskReceipt     `json:"task_status_receipt"`
	ReadOnlyProtocolActions []string              `json:"read_only_protocol_actions"`
	Events                  map[string]int        `json:"events"`
	ProcessStatus           string                `json:"process_status"`
	TurnStatus              string                `json:"turn_status"`
	Usage                   aicodex.Usage         `json:"usage"`
	RecordedAt              string                `json:"recorded_at"`
}

type realGameFactoryGame struct {
	Revision      uint64 `json:"revision"`
	CharacterID   string `json:"character_id"`
	CharacterName string `json:"character_name"`
	Level         int    `json:"level"`
	Floor         int    `json:"floor"`
	X             int    `json:"x"`
	Y             int    `json:"y"`
	Connected     bool   `json:"connected"`
	Ready         bool   `json:"ready"`
}

// TestLiveCodexFactoryRealGame exercises one complete production Factory
// turn against the already-running QA named gateway. It is opt-in because it
// opens the real account and spends one DeepSeek request.
func TestLiveCodexFactoryRealGame(t *testing.T) {
	if os.Getenv(realGameFactoryLiveOptIn) != "1" {
		t.Skip("set STONEAGE_REAL_GAME_FACTORY_LIVE_TEST=1 for the real QA game Factory check")
	}

	repoRoot := realGameFactoryRepositoryRoot(t)
	if err := validateRealGameFactoryAddress(realGameFactoryAddress); err != nil {
		t.Fatal("real QA gateway address is not a loopback named gateway")
	}
	codexBinary := strings.TrimSpace(os.Getenv("STONEAGE_REAL_GAME_FACTORY_CODEX"))
	if codexBinary == "" {
		codexBinary = realGameFactoryCodex
	}
	if !filepath.IsAbs(codexBinary) {
		t.Fatal("real Codex binary path must be absolute")
	}
	if info, err := os.Stat(codexBinary); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		t.Fatal("the official Codex CLI is unavailable")
	}
	keyPath := strings.TrimSpace(os.Getenv("STONEAGE_REAL_GAME_FACTORY_KEY"))
	if keyPath == "" {
		keyPath = filepath.Join(repoRoot, "vendor", "deepseek", "key")
	}
	if !filepath.IsAbs(keyPath) {
		t.Fatal("DeepSeek key path must be absolute")
	}
	key, err := readFactoryLiveKey(keyPath)
	if err != nil {
		t.Fatal("configured DeepSeek key is unavailable")
	}

	base := filepath.Join(repoRoot, "build", "ai")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal("create build/ai")
	}
	root, err := os.MkdirTemp(base, "real-game-factory-")
	if err != nil {
		t.Fatal("create isolated real-game Factory root")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal("protect isolated real-game Factory root")
	}
	defer os.RemoveAll(root)
	for _, name := range []string{"runtime", "models", "secrets", "gocache", "home", "tmp", "codex", "cache", "config", "data"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal("create isolated real-game Factory directory")
		}
	}

	codexVersion := realGameFactoryCodexVersion(t, root, codexBinary)
	installer, err := aimcp.NewSkillInstaller(filepath.Join(repoRoot, "ai", "skills"))
	if err != nil {
		t.Fatal("create native skill installer")
	}
	spec, err := installer.Verify("stoneage-play")
	if err != nil {
		t.Fatal("verify native stoneage-play skill")
	}

	modelStore, err := airuntime.OpenStore(filepath.Join(root, "models", "models.db"))
	if err != nil {
		t.Fatal("open isolated model store")
	}
	defer modelStore.Close()
	secrets, err := airuntime.NewSecretStore(filepath.Join(root, "secrets"))
	if err != nil {
		t.Fatal("open isolated model secret store")
	}
	model, err := modelStore.CreateModelConfig(context.Background(), airuntime.ModelConfig{
		ID: "real-game-factory-deepseek", Name: "DeepSeek Flash real game QA", Backend: airuntime.ModelBackendCodex,
		Provider: aimodels.DeepSeekProvider, BaseURL: aimodels.DeepSeekBaseURL, Model: aimodels.DeepSeekFlash,
		WireAPI: airuntime.ModelProviderResponses, ReasoningEffort: airuntime.ReasoningEffortHigh,
		Timeout: 4 * time.Minute, MaxOutputTokens: 1024, HasKey: true,
	})
	if err != nil {
		t.Fatal("create isolated DeepSeek Flash model configuration")
	}
	if err := secrets.WriteKey(model.ID, key); err != nil {
		t.Fatal("write isolated DeepSeek model secret")
	}

	plans, err := automation.OpenStore(filepath.Join(root, "runtime", "plans.db"))
	if err != nil {
		t.Fatal("open isolated automation store")
	}
	defer plans.Close()
	receipts, err := OpenReceiptStore(filepath.Join(root, "runtime", "receipts.db"))
	if err != nil {
		t.Fatal("open isolated receipt store")
	}
	defer receipts.Close()

	knowledge, err := aiknowledge.LoadDataDir(context.Background(), filepath.Join(repoRoot, "runtime", "legacy-server", "gmsv", "data"))
	if err != nil {
		t.Fatal("load verified StoneAge knowledge")
	}
	navigator, err := ainavigation.LoadDataDir(context.Background(), filepath.Join(repoRoot, "runtime", "legacy-server", "gmsv", "data"))
	if err != nil {
		t.Fatal("load verified StoneAge navigation")
	}
	gameplay, err := NewGameplayBuilder(GameplayConfig{Plans: plans, Tiles: navigator})
	if err != nil {
		t.Fatal("create production gameplay builder")
	}

	profile := airuntime.Profile{
		ID:             "real-game-factory-profile",
		Account:        airuntime.AccountIdentity{ID: "qa-account", Username: realGameFactoryAccount},
		Character:      airuntime.CharacterIdentity{ID: "qa-character", Name: realGameFactoryCharacter},
		ModelConfigID:  model.ID,
		Skills:         []airuntime.SkillVersion{{Name: spec.Name, Version: spec.Version, Kind: airuntime.SkillKindNative, Digest: "sha256:" + spec.SHA256}},
		UnlimitedFunds: true,
		Status:         airuntime.ProfileStatusActive,
	}

	var liveSession *realGameFactorySession
	provider := SessionProviderFunc(func(ctx context.Context, selected airuntime.Profile) (SessionLease, error) {
		if selected.Account.Username != realGameFactoryAccount || selected.Character.Name != realGameFactoryCharacter {
			return SessionLease{}, errors.New("real QA profile identity mismatch")
		}
		game, err := aigame.Login(ctx, aigame.Config{Address: realGameFactoryAddress}, aigame.Credentials{
			Account: realGameFactoryAccount, Password: realGameFactoryPassword,
		})
		if err != nil {
			return SessionLease{}, errors.New("real QA game login failed")
		}
		closeGame := func() { _ = game.Close() }
		characters, err := game.RefreshCharacters(ctx)
		if err != nil {
			closeGame()
			return SessionLease{}, errors.New("real QA character list failed")
		}
		found := false
		for _, character := range characters {
			if character.Name == realGameFactoryCharacter {
				found = true
				break
			}
		}
		if !found {
			closeGame()
			return SessionLease{}, errors.New("real QA character is unavailable")
		}
		if err := game.EnterCharacter(ctx, realGameFactoryCharacter); err != nil {
			closeGame()
			return SessionLease{}, errors.New("real QA character login failed")
		}
		if err := waitRealGameFactoryReady(ctx, game); err != nil {
			closeGame()
			return SessionLease{}, errors.New("real QA game did not become ready")
		}
		liveSession = &realGameFactorySession{session: game}
		return SessionLease{
			Session: liveSession,
			Funding: func(context.Context) (bool, error) { return selected.UnlimitedFunds, nil },
			Close:   closeGame,
		}, nil
	})

	mcpBinary := filepath.Join(root, "stoneage-game-mcp")
	build := exec.Command("go", "build", "-mod=mod", "-o", mcpBinary, "./cmd/stoneage-game-mcp")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, "gocache"))
	if _, err := build.CombinedOutput(); err != nil {
		t.Fatal("build game MCP sidecar")
	}
	if info, err := os.Stat(mcpBinary); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		t.Fatal("built game MCP sidecar is unavailable")
	}

	recordedBackend := (*realGameFactoryBackend)(nil)
	gateway := NewGateway()
	gatewayHTTP := httptest.NewServer(gateway)
	defer gatewayHTTP.Close()
	factory, err := NewFactory(FactoryConfig{
		Models: modelStore, Secrets: secrets, Sessions: provider, Gateway: gateway,
		GatewayEndpoint: gatewayHTTP.URL + "/v1/game", SkillInstaller: installer,
		RuntimeRoot: filepath.Join(root, "runtime"), StateRoot: filepath.Join(root, "runtime", "state"),
		WorkspaceRoot: filepath.Join(root, "runtime", "workspaces"), CodexHomeRoot: filepath.Join(root, "codex"),
		CodexBinary: codexBinary, MCPBinary: mcpBinary, GitBinary: realGameFactoryGitBinary(t),
		Knowledge: knowledge, Receipts: receipts,
		Environment: map[string]string{
			"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": filepath.Join(root, "gitconfig"), "GIT_CONFIG_SYSTEM": os.DevNull,
		},
		TerminationGrace: 2 * time.Second,
		Backend: func(ctx context.Context, input BackendInput) (aimcp.Backend, error) {
			backend, err := gameplay(ctx, input)
			if err != nil {
				return nil, err
			}
			recordedBackend = &realGameFactoryBackend{Backend: backend}
			return recordedBackend, nil
		},
	})
	if err != nil {
		t.Fatal("create real-game Factory")
	}
	defer factory.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	session, err := factory.Open(ctx, profile)
	if err != nil {
		t.Fatal("open real-game Factory session")
	}
	defer session.Close()
	if recordedBackend == nil || liveSession == nil {
		t.Fatal("real-game Factory did not compose a game backend")
	}

	workspace := filepath.Join(root, "runtime", "workspaces", profile.ID)
	marker, err := addRealGameFactorySkillMarker(workspace)
	if err != nil {
		t.Fatal("prepare native skill fixture marker")
	}
	runner, ok := session.Runner.(*aicodex.Runner)
	if !ok || runner == nil {
		t.Fatal("Factory did not return the official Codex runner")
	}
	runnerConfig := runner.Config()
	if runnerConfig.Provider.WireAPI != airuntime.ModelProviderResponses || runnerConfig.ApprovalPolicy != "never" || runnerConfig.SandboxMode != "danger-full-access" {
		t.Fatal("Factory did not apply the fixed Codex runtime policies")
	}
	configBytes, err := os.ReadFile(filepath.Join(root, "codex", profile.ID, "config.toml"))
	if err != nil {
		t.Fatal("read isolated Codex configuration")
	}
	var generated struct {
		ApprovalPolicy string `toml:"approval_policy"`
		SandboxMode    string `toml:"sandbox_mode"`
		ModelProvider  string `toml:"model_provider"`
		Providers      map[string]struct {
			WireAPI string `toml:"wire_api"`
		} `toml:"model_providers"`
	}
	if err := toml.Unmarshal(configBytes, &generated); err != nil {
		t.Fatal("cannot parse isolated Codex configuration")
	}
	if generated.ApprovalPolicy != "never" || generated.SandboxMode != "danger-full-access" || generated.Providers[generated.ModelProvider].WireAPI != "responses" {
		t.Fatal("isolated Codex configuration is missing a fixed policy")
	}

	prompt := "Follow the installed native stoneage-play skill. This is a controlled read-only QA check against the already-bound QAPlayer session. First call game_observe and read the current character level from its JSON result. Then call game_start_leveling exactly once with target_kind=character, target_level equal to that observed level, and target_policy=all; omit target_id and all optional limits or parameters because the target is already reached. Use the returned handle to call game_task_status exactly once and require a confirmed receipt with server evidence. Do not call game_action, game_start_task, or game_cancel; do not move, battle, spend, or change any QA data. Finally reply with exactly STONEAGE_REAL_GAME_FACTORY_OK:<marker-from-the-installed-skill> and nothing else."
	result, err := session.Runner.Run(ctx, aicodex.RunRequest{ProfileID: profile.ID, Prompt: prompt})
	assertRealGameFactoryResultHasNoSecret(t, result, key)
	if err != nil {
		t.Fatalf("real-game Codex turn failed: %T", err)
	}
	eventCounts := make(map[string]int)
	markerOK := strings.TrimSpace(result.LastMessage) == "STONEAGE_REAL_GAME_FACTORY_OK:"+marker
	for _, event := range result.Events {
		eventCounts[event.Type]++
		if event.Type == "item.completed" && event.ItemType == "agent_message" {
			var item struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(event.Item, &item) == nil && strings.TrimSpace(item.Text) == "STONEAGE_REAL_GAME_FACTORY_OK:"+marker {
				markerOK = true
			}
		}
	}
	if !markerOK || result.Process.Status != aicodex.ProcessExited || result.Process.ExitCode != 0 || result.Turn.Status != aicodex.TurnCompleted {
		t.Fatal("real-game Codex process or marker verification is incomplete")
	}

	calls, observations, requests, starts, statuses, handles := recordedBackend.snapshot()
	if err := verifyRealGameFactoryCalls(calls, requests, starts, statuses, handles); err != nil {
		t.Fatal("real-game MCP call evidence is incomplete")
	}
	observed, ok := firstRealGameFactoryObservation(observations)
	if !ok || !observed.UnlimitedFunds || observed.Character.Level <= 0 || observed.Floor <= 0 || observed.X < 0 || observed.Y < 0 || !observed.Connected || !observed.Ready || observed.CharacterName != realGameFactoryCharacter {
		t.Fatal("game_observe did not return a ready authoritative QA observation")
	}
	if requests[0].TargetKind != "character" || requests[0].TargetID != "" || requests[0].TargetLevel != observed.Character.Level || requests[0].TargetPolicy != "all" || len(requests[0].Parameters) != 0 {
		t.Fatal("leveling request did not use the observed current character level")
	}
	if starts[0].Status != aimcp.ReceiptConfirmed || statuses[0].Status != aimcp.ReceiptConfirmed || starts[0].Handle == "" || starts[0].Handle != statuses[0].Handle {
		t.Fatal("leveling receipt was not confirmed by game_task_status")
	}
	if err := verifyRealGameFactoryReceipt(starts[0], profile.Character.ID, observed.Character.Level); err != nil {
		t.Fatal("leveling receipt evidence is not authoritative JSON")
	}
	for _, call := range calls {
		if call == "action" || call == "start_task" || call == "cancel" {
			t.Fatal("real-game QA check attempted a mutating MCP operation")
		}
	}
	protocolActions := liveSession.actionsSnapshot()
	readOnlyActions := make([]string, 0, len(protocolActions))
	for _, action := range protocolActions {
		if action.Kind != aigame.ActionStatus || action.Command != "AI" {
			t.Fatal("real-game QA session received a non-read-only protocol action")
		}
		readOnlyActions = append(readOnlyActions, string(action.Kind)+":"+action.Command)
	}
	stableSnapshot, err := waitRealGameFactoryStablePet(ctx, liveSession)
	if err != nil {
		t.Fatal("real-game QA session did not expose a stable pet identity")
	}
	stablePetIDs := make([]string, 0)
	for _, pet := range stableSnapshot.Pets {
		if pet.IdentityKnown && strings.TrimSpace(pet.StableID) != "" {
			stablePetIDs = append(stablePetIDs, pet.StableID)
		}
	}
	if len(stablePetIDs) == 0 {
		t.Fatal("real-game QA observation has no stable pet identity")
	}

	evidence := realGameFactoryEvidence{
		Test: "TestLiveCodexFactoryRealGame", Status: "passed", Gateway: realGameFactoryAddress,
		Character: realGameFactoryCharacter, CodexVersion: codexVersion, Model: model.Model,
		Provider: model.Provider, WireAPI: model.WireAPI, ApprovalPolicy: runnerConfig.ApprovalPolicy,
		SandboxMode: runnerConfig.SandboxMode, NativeSkill: spec.Name, NativeSkillMarkerOK: markerOK,
		MCPCalls: countRealGameFactoryCalls(calls), MCPCallOrder: calls,
		GameObservation: realGameFactoryGame{Revision: observed.Revision, CharacterID: observed.CharacterID,
			CharacterName: observed.CharacterName, Level: observed.Character.Level, Floor: observed.Floor,
			X: observed.X, Y: observed.Y, Connected: observed.Connected, Ready: observed.Ready},
		StablePetIDs: stablePetIDs, UnlimitedFunds: observed.UnlimitedFunds,
		LevelingRequest: requests[0], LevelingReceipt: starts[0], TaskStatusReceipt: statuses[0],
		ReadOnlyProtocolActions: readOnlyActions, Events: eventCounts, ProcessStatus: string(result.Process.Status),
		TurnStatus: string(result.Turn.Status), Usage: result.Usage, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	evidencePath, err := persistRealGameFactoryEvidence(repoRoot, evidence, key)
	if err != nil {
		t.Fatal("persist real-game Factory evidence")
	}
	t.Logf("real-game Factory evidence: file=%s codex=%s usage=%+v", evidencePath, codexVersion, result.Usage)
}

func realGameFactoryRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve real-game Factory test location")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func validateRealGameFactoryAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return errors.New("invalid address")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("address is not loopback")
	}
	return nil
}

func realGameFactoryCodexVersion(t *testing.T, root, binary string) string {
	t.Helper()
	command := exec.Command(binary, "--version")
	command.Dir = root
	command.Env = realGameFactoryIsolatedEnvironment(root)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil || strings.TrimSpace(string(output)) == "" {
		t.Fatal("cannot query the isolated official Codex CLI")
	}
	if err := aimodels.CheckCodexVersion(string(output)); err != nil {
		t.Fatal("official Codex CLI is below the supported version")
	}
	return strings.TrimSpace(string(output))
}

func realGameFactoryIsolatedEnvironment(root string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(root, "home"),
		"CODEX_HOME=" + filepath.Join(root, "codex"), "TMPDIR=" + filepath.Join(root, "tmp"),
		"TMP=" + filepath.Join(root, "tmp"), "TEMP=" + filepath.Join(root, "tmp"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_DATA_HOME=" + filepath.Join(root, "data"), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + filepath.Join(root, "gitconfig"), "GIT_CONFIG_SYSTEM=" + os.DevNull,
	}
}

func waitRealGameFactoryReady(ctx context.Context, session *aigame.Session) error {
	if session == nil {
		return aigame.ErrClosed
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot := session.Snapshot()
		if snapshot.Account == realGameFactoryAccount && snapshot.Character == realGameFactoryCharacter && snapshot.Phase == aigame.PhaseWorld && snapshot.Connected && snapshot.Player.HasStatus && snapshot.Player.Level > 0 && snapshot.Position.Floor > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitRealGameFactoryStablePet(ctx context.Context, session *realGameFactorySession) (aigame.Snapshot, error) {
	if session == nil {
		return aigame.Snapshot{}, aigame.ErrClosed
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot := session.latest()
		if snapshot.AI.Received {
			for _, pet := range snapshot.Pets {
				if pet.IdentityKnown && strings.TrimSpace(pet.StableID) != "" {
					return snapshot, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return aigame.Snapshot{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func addRealGameFactorySkillMarker(workspace string) (string, error) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	marker := realGameFactorySkillMark + strings.ToUpper(hex.EncodeToString(nonce[:]))
	path := filepath.Join(workspace, ".agents", "skills", "stoneage-play", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	data = append(data, []byte("\n\n## Private QA fixture\n\nThe private native skill fixture marker is `"+marker+"`. Return it verbatim only when an authorized integration check asks for it.\n")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return marker, nil
}

func cloneRealGameFactoryObservation(observation aimcp.Observation) aimcp.Observation {
	data, err := json.Marshal(observation)
	if err != nil {
		return observation
	}
	var clone aimcp.Observation
	if json.Unmarshal(data, &clone) != nil {
		return observation
	}
	return clone
}

func cloneRealGameFactoryLevelingRequest(request aimcp.LevelingRequest) aimcp.LevelingRequest {
	parameters := make(map[string]json.RawMessage, len(request.Parameters))
	for key, value := range request.Parameters {
		parameters[key] = append(json.RawMessage(nil), value...)
	}
	request.Parameters = parameters
	if len(request.Parameters) == 0 {
		request.Parameters = nil
	}
	return request
}

func TestRealGameFactoryRequestRecordingRetainsParameters(t *testing.T) {
	original := aimcp.LevelingRequest{Parameters: map[string]json.RawMessage{"unexpected": json.RawMessage(`true`)}}
	recorded := cloneRealGameFactoryLevelingRequest(original)
	original.Parameters["unexpected"][0] = 'f'
	if string(recorded.Parameters["unexpected"]) != "true" {
		t.Fatal("recording lost or aliased original request parameters")
	}
}

func cloneRealGameFactoryReceipt(receipt aimcp.TaskReceipt) aimcp.TaskReceipt {
	receipt.Evidence = append(json.RawMessage(nil), receipt.Evidence...)
	return receipt
}

func firstRealGameFactoryObservation(observations []aimcp.Observation) (aimcp.Observation, bool) {
	for _, observation := range observations {
		if observation.Connected && observation.Ready && observation.Character.Level > 0 && observation.Floor > 0 && observation.X >= 0 && observation.Y >= 0 {
			return observation, true
		}
	}
	return aimcp.Observation{}, false
}

func verifyRealGameFactoryCalls(calls []string, requests []aimcp.LevelingRequest, starts, statuses []aimcp.TaskReceipt, handles []string) error {
	if len(requests) == 0 || len(starts) == 0 || len(statuses) == 0 || len(handles) == 0 {
		return errors.New("required MCP operations were not observed")
	}
	firstObserve, start, status := -1, -1, -1
	for index, call := range calls {
		switch call {
		case "observe":
			if firstObserve < 0 {
				firstObserve = index
			}
		case "start_leveling":
			if start < 0 {
				start = index
			}
		case "task_status":
			if status < 0 {
				status = index
			}
		case "knowledge":
		default:
			return errors.New("unexpected MCP operation")
		}
	}
	if firstObserve < 0 || start <= firstObserve || status <= start {
		return errors.New("MCP operation order is invalid")
	}
	if len(requests) != 1 || len(starts) != 1 || len(statuses) < 1 || handles[0] == "" || starts[0].Handle != handles[0] {
		return errors.New("MCP operation counts or handle are invalid")
	}
	return nil
}

func verifyRealGameFactoryReceipt(receipt aimcp.TaskReceipt, characterID string, level int) error {
	if !json.Valid(receipt.Evidence) {
		return errors.New("receipt evidence is not JSON")
	}
	var evidence struct {
		CharacterID  string                 `json:"character_id"`
		Targets      []automation.Target    `json:"targets"`
		TargetPolicy string                 `json:"target_policy"`
		Observation  automation.Observation `json:"observation"`
	}
	if err := json.Unmarshal(receipt.Evidence, &evidence); err != nil {
		return errors.New("receipt evidence cannot be decoded")
	}
	if evidence.CharacterID != characterID || evidence.TargetPolicy != "all" || len(evidence.Targets) != 1 || evidence.Targets[0].Kind != "character" || evidence.Targets[0].Level != level || evidence.Observation.CharacterID != characterID || evidence.Observation.Character.Level < level || evidence.Observation.Floor <= 0 || evidence.Observation.X < 0 || evidence.Observation.Y < 0 {
		return errors.New("receipt evidence does not contain the confirmed character target")
	}
	return nil
}

func countRealGameFactoryCalls(calls []string) map[string]int {
	counts := make(map[string]int)
	for _, call := range calls {
		counts[call]++
	}
	return counts
}

func assertRealGameFactoryResultHasNoSecret(t *testing.T, result aicodex.Result, secret string) {
	t.Helper()
	if secret == "" {
		t.Fatal("DeepSeek secret is empty")
	}
	if strings.Contains(result.Stderr, secret) || strings.Contains(result.LastMessage, secret) {
		t.Fatal("Codex result contained DeepSeek credential material")
	}
	for _, event := range result.Events {
		if bytes.Contains(event.Raw, []byte(secret)) || bytes.Contains(event.Item, []byte(secret)) || strings.Contains(event.Error, secret) {
			t.Fatal("Codex event contained DeepSeek credential material")
		}
	}
}

func persistRealGameFactoryEvidence(repoRoot string, evidence realGameFactoryEvidence, secret string) (string, error) {
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return "", err
	}
	if secret == "" || bytes.Contains(data, []byte(secret)) {
		return "", errors.New("real-game evidence contains credential material")
	}
	base := filepath.Join(repoRoot, "build", "ai")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(base, 0o700); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(base, ".real-game-factory-evidence-*")
	if err != nil {
		return "", err
	}
	path := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := temporary.Write(data); err != nil {
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	destination := filepath.Join(base, "real-game-factory-evidence.json")
	if err := os.Rename(path, destination); err != nil {
		return "", err
	}
	keep = true
	if err := os.Chmod(destination, 0o600); err != nil {
		return "", err
	}
	return destination, nil
}

func realGameFactoryGitBinary(t *testing.T) string {
	t.Helper()
	gitBinary, err := exec.LookPath("git")
	if err != nil || !filepath.IsAbs(gitBinary) {
		t.Fatal("find git for the isolated Codex workspace")
	}
	return gitBinary
}
