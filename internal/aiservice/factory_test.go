package aiservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aimodels"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/aisupervisor"
	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
)

type factoryTestModels struct {
	model     airuntime.ModelConfig
	defaultID string
	err       error
}

func (source factoryTestModels) GetModelConfig(ctx context.Context, id string) (airuntime.ModelConfig, error) {
	if err := ctx.Err(); err != nil {
		return airuntime.ModelConfig{}, err
	}
	if source.err != nil {
		return airuntime.ModelConfig{}, source.err
	}
	if id != source.model.ID {
		return airuntime.ModelConfig{}, airuntime.ErrNotFound
	}
	return source.model, nil
}

func (source factoryTestModels) GetDefaultModelConfigID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if source.err != nil {
		return "", source.err
	}
	return source.defaultID, nil
}

type factoryTestBackend struct {
	aimcp.UnavailableBackend

	mu           sync.Mutex
	bindings     []aimcp.Binding
	observation  aimcp.Observation
	active       []aimcp.TaskReceipt
	uncertain    []aimcp.TaskReceipt
	activeErr    error
	uncertainErr error
}

func (backend *factoryTestBackend) Observe(ctx context.Context, binding aimcp.Binding) (aimcp.Observation, error) {
	if err := ctx.Err(); err != nil {
		return aimcp.Observation{}, err
	}
	backend.mu.Lock()
	backend.bindings = append(backend.bindings, binding)
	observation := backend.observation
	backend.mu.Unlock()
	return observation, nil
}

func (backend *factoryTestBackend) Active(ctx context.Context) ([]aimcp.TaskReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.activeErr != nil {
		return nil, backend.activeErr
	}
	return append([]aimcp.TaskReceipt(nil), backend.active...), nil
}

func (backend *factoryTestBackend) PendingActions(ctx context.Context) ([]aimcp.TaskReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.uncertainErr != nil {
		return nil, backend.uncertainErr
	}
	return append([]aimcp.TaskReceipt(nil), backend.uncertain...), nil
}

func (backend *factoryTestBackend) binding() aimcp.Binding {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.bindings) == 0 {
		return aimcp.Binding{}
	}
	return backend.bindings[len(backend.bindings)-1]
}

type factoryTestSession struct{}

func (factoryTestSession) Observe(context.Context) (aigame.Snapshot, error) {
	return aigame.Snapshot{}, nil
}

func (factoryTestSession) ExecuteExpected(context.Context, uint64, aigame.Action) error {
	return nil
}

type factoryTestTasks struct {
	receipts []aimcp.TaskReceipt
	closeFn  func()
}

func (tasks *factoryTestTasks) StartTask(context.Context, aimcp.TaskRequest) (aimcp.TaskReceipt, error) {
	return aimcp.TaskReceipt{}, nil
}

func (tasks *factoryTestTasks) StartLeveling(context.Context, aimcp.LevelingRequest) (aimcp.TaskReceipt, error) {
	return aimcp.TaskReceipt{}, nil
}

func (tasks *factoryTestTasks) Status(context.Context, string) (aimcp.TaskReceipt, error) {
	return aimcp.TaskReceipt{}, nil
}

func (tasks *factoryTestTasks) Cancel(context.Context, aimcp.CancelRequest) (aimcp.TaskReceipt, error) {
	return aimcp.TaskReceipt{}, nil
}

func (tasks *factoryTestTasks) Active(context.Context) ([]aimcp.TaskReceipt, error) {
	return append([]aimcp.TaskReceipt(nil), tasks.receipts...), nil
}

func (tasks *factoryTestTasks) Close() {
	if tasks.closeFn != nil {
		tasks.closeFn()
	}
}

type factoryClosableBackend struct {
	*factoryTestBackend
	closeFn func()
}

func (backend *factoryClosableBackend) Close() {
	if backend.closeFn != nil {
		backend.closeFn()
	}
}

func factoryTestRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	buildRoot := filepath.Join(filepath.Dir(filename), "..", "..", "build", "ai")
	if err := os.MkdirAll(buildRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(buildRoot, "factory-test-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func writeFactoryExecutable(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	script := `#!/bin/sh
set -eu
case "${1:-}" in
--version)
  printf '%s\n' 'codex-cli 0.154.0'
  ;;
exec)
  cat >/dev/null
  printf '%s\n' '{"type":"thread.started","thread_id":"factory-thread"}'
  printf '%s\n' '{"type":"turn.started","turn_id":"factory-turn"}'
  printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"factory-agent-ok"}}'
  printf '%s\n' '{"type":"turn.completed","turn_id":"factory-turn","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}'
  ;;
*)
  exit 2
  ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func factoryTestProfile(id string, withSkill bool) airuntime.Profile {
	profile := airuntime.Profile{
		ID:            id,
		Account:       airuntime.AccountIdentity{ID: "internal-account-42", Username: "real-login"},
		Character:     airuntime.CharacterIdentity{ID: "real-login:0", Name: "FactoryHero"},
		ModelConfigID: "model-1",
		Goal: airuntime.Goal{
			Kind: "level", TargetLevel: 10, TargetCharacterID: "real-login:0", StopWhenCompleted: true,
		},
	}
	if withSkill {
		installer, err := aimcp.NewSkillInstaller(filepath.Join(factoryRepoRoot(), "ai", "skills"))
		if err != nil {
			panic(err)
		}
		spec, err := installer.Specification("stoneage-play")
		if err != nil {
			panic(err)
		}
		profile.Skills = []airuntime.SkillVersion{{Name: spec.Name, Version: spec.Version, Digest: "sha256:" + spec.SHA256, Kind: airuntime.SkillKindNative}}
	}
	return profile
}

func factoryRepoRoot() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func newFactoryTestConfig(t *testing.T, root string, models ModelConfigSource, secrets *airuntime.SecretStore, provider SessionProvider) FactoryConfig {
	t.Helper()
	return FactoryConfig{
		Models:           models,
		Secrets:          secrets,
		Sessions:         provider,
		Gateway:          NewGateway(),
		GatewayEndpoint:  "http://127.0.0.1:18081/v1/game",
		SkillRoot:        filepath.Join(factoryRepoRoot(), "ai", "skills"),
		RuntimeRoot:      root,
		CodexBinary:      writeFactoryExecutable(t, root, "fake-codex"),
		MCPBinary:        writeFactoryExecutable(t, root, "fake-mcp"),
		TerminationGrace: 50 * time.Millisecond,
	}
}

func factoryTestModel() airuntime.ModelConfig {
	return airuntime.ModelConfig{
		ID: "model-1", Name: "DeepSeek Flash", Backend: airuntime.ModelBackendCodex,
		Provider: aimodels.DeepSeekProvider, BaseURL: aimodels.DeepSeekBaseURL, Model: aimodels.DeepSeekFlash,
		ReasoningEffort: airuntime.ReasoningEffortHigh, Timeout: time.Minute, MaxOutputTokens: 4096,
	}
}

func factoryTestSecrets(t *testing.T, root string) *airuntime.SecretStore {
	t.Helper()
	secrets, err := airuntime.NewSecretStore(filepath.Join(root, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.WriteKey("model-1", "test-deepseek-key"); err != nil {
		t.Fatal(err)
	}
	return secrets
}

func TestFactoryOpenComposesCodexGatewayAndPrivateSkills(t *testing.T) {
	root := factoryTestRoot(t)
	profile := factoryTestProfile("factory-profile", true)
	secrets := factoryTestSecrets(t, root)
	models := factoryTestModels{model: factoryTestModel(), defaultID: "model-1"}
	backend := &factoryTestBackend{observation: aimcp.Observation{
		CharacterID: "real-login:0", CharacterName: "FactoryHero", Connected: true, Ready: true,
		Character: aimcp.Entity{ID: "real-login:0", Name: "FactoryHero", Level: 10, HP: 100, MaxHP: 100},
	}}
	backend.active = []aimcp.TaskReceipt{{Handle: "task-1", Status: aimcp.ReceiptRunning, State: "running", Reason: "training"}}
	backend.uncertain = []aimcp.TaskReceipt{{Handle: "action-old", Status: aimcp.ReceiptUnknown, Reason: "outcome must be reconciled"}}

	gatewayGate := aicontrol.New()
	claimed, _, err := gatewayGate.Switch(gatewayGate.State().Generation, aicontrol.Agent, "provider claimed agent")
	if err != nil {
		t.Fatal(err)
	}
	var closeCount atomic.Int32
	provider := SessionProviderFunc(func(context.Context, airuntime.Profile) (SessionLease, error) {
		return SessionLease{
			Backend: backend,
			Gate:    gatewayGate,
			Close:   func() { closeCount.Add(1) },
		}, nil
	})
	config := newFactoryTestConfig(t, root, models, secrets, provider)
	// Exercise replacement of a stale token left by an interrupted process.
	stateDir := filepath.Join(root, "state", profile.ID)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(stateDir, "game-capability.token")
	if err := os.WriteFile(tokenPath, []byte("stale-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	factory, err := NewFactory(config)
	if err != nil {
		t.Fatal(err)
	}
	session, err := factory.Open(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	if session.Runner == nil || session.Observe == nil || session.Close == nil {
		t.Fatal("factory returned incomplete agent session")
	}

	snapshot, err := session.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.GameReady || !snapshot.GoalComplete || len(snapshot.ActiveTasks) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	var contextFields map[string]json.RawMessage
	if err := json.Unmarshal(snapshot.Context, &contextFields); err != nil {
		t.Fatal(err)
	}
	var uncertain []aimcp.TaskReceipt
	if err := json.Unmarshal(contextFields["uncertain_actions"], &uncertain); err != nil {
		t.Fatal(err)
	}
	if len(uncertain) != 1 || uncertain[0].Handle != "action-old" {
		t.Fatalf("uncertain actions = %+v", uncertain)
	}
	bound := backend.binding()
	if bound.AccountID != profile.Account.Username || bound.AccountID == profile.Account.ID || bound.Generation != claimed.Generation {
		t.Fatalf("binding = %+v, profile account = %+v, claimed = %+v", bound, profile.Account, claimed)
	}
	if snapshot.ProgressKey == "" || strings.Contains(snapshot.ProgressKey, "|") == false {
		t.Fatalf("progress key = %q", snapshot.ProgressKey)
	}

	token, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	tokenValue := strings.TrimSpace(string(token))
	if tokenValue == "" || tokenValue == "stale-token" {
		t.Fatalf("token was not replaced: %q", tokenValue)
	}
	if info, err := os.Stat(tokenPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token mode = %v, err=%v", info.Mode().Perm(), err)
	}
	if response := call(config.Gateway, tokenValue, `{"operation":"observe","arguments":{}}`); response.Code != 200 {
		t.Fatalf("gateway response = %d: %s", response.Code, response.Body.String())
	}

	codexHome := filepath.Join(root, "codex", profile.ID)
	configPath := filepath.Join(codexHome, "config.toml")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configBytes), "test-deepseek-key") || !strings.Contains(string(configBytes), tokenPath) {
		t.Fatalf("generated Codex config missing private model/MCP settings: %s", configBytes)
	}
	for _, path := range []string{configPath, filepath.Join(codexHome, "models.json")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("runtime file %s mode = %v, err=%v", path, info.Mode().Perm(), err)
		}
	}
	installed := filepath.Join(root, "workspaces", profile.ID, ".agents", "skills", "stoneage-play", "SKILL.md")
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("installed skill missing: %v", err)
	}

	result, err := session.Runner.Run(context.Background(), aicodex.RunRequest{ProfileID: profile.ID, Prompt: "observe the game"})
	if err != nil || result.Turn.Status != aicodex.TurnCompleted || result.ThreadID != "factory-thread" {
		t.Fatalf("Codex result = %+v, err=%v", result, err)
	}

	session.Close()
	session.Close()
	if got := closeCount.Load(); got != 1 {
		t.Fatalf("provider close count = %d", got)
	}
	if _, err := os.Lstat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capability token after close: %v", err)
	}
	if response := call(config.Gateway, tokenValue, `{"operation":"observe","arguments":{}}`); response.Code != 401 {
		t.Fatalf("revoked gateway token response = %d", response.Code)
	}
	if state := gatewayGate.State(); state.Mode != aicontrol.Manual || state.Generation <= claimed.Generation {
		t.Fatalf("gate was not fenced on close: %+v", state)
	}
}

func TestFactoryAcceptsGenericResponsesProviderAndUsesMaterializedProviderID(t *testing.T) {
	for _, test := range []struct {
		name, provider, baseURL, providerID string
	}{
		{name: "openai", provider: "openai", baseURL: aimodels.OpenAIBaseURL, providerID: "stoneage_openai"},
		{name: "custom", provider: "local-compatible", baseURL: "https://models.example.test/v1", providerID: "local-compatible"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := factoryTestRoot(t)
			profile := factoryTestProfile("generic-provider-"+test.name, false)
			secrets := factoryTestSecrets(t, root)
			model := factoryTestModel()
			model.Provider = test.provider
			model.BaseURL = test.baseURL
			model.Model = "gpt-4o"
			model.WireAPI = airuntime.ModelProviderResponses
			model.ReasoningEffort = ""
			models := factoryTestModels{model: model, defaultID: model.ID}
			backend := &factoryTestBackend{}
			provider := SessionProviderFunc(func(context.Context, airuntime.Profile) (SessionLease, error) {
				return SessionLease{Backend: backend, Gate: aicontrol.New(), Close: func() {}}, nil
			})
			factory, err := NewFactory(newFactoryTestConfig(t, root, models, secrets, provider))
			if err != nil {
				t.Fatal(err)
			}
			session, err := factory.Open(context.Background(), profile)
			if err != nil {
				t.Fatal(err)
			}
			runner, ok := session.Runner.(*aicodex.Runner)
			if !ok {
				t.Fatalf("runner type = %T", session.Runner)
			}
			runnerConfig := runner.Config()
			if runnerConfig.TurnTimeout != model.Timeout {
				t.Fatal("model timeout was not passed to the local runner")
			}
			if runnerConfig.Provider.Name != test.providerID || runnerConfig.Provider.WireAPI != airuntime.ModelProviderResponses || runnerConfig.Provider.BaseURL != test.baseURL || runnerConfig.ModelCatalog != "" || runnerConfig.ReasoningEffort != "" {
				t.Fatalf("generic runner config = %+v", runnerConfig)
			}
			configBytes, err := os.ReadFile(filepath.Join(root, "codex", profile.ID, "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(configBytes), "model_provider = '"+test.providerID+"'") || !strings.Contains(string(configBytes), "base_url = '"+test.baseURL+"'") || strings.Contains(string(configBytes), "model_catalog_json") || strings.Contains(string(configBytes), "model_reasoning_effort") {
				t.Fatalf("generic Codex config = %s", configBytes)
			}
			session.Close()
		})
	}
}

func TestFactoryCloseHandoffDoesNotLeakSlowProviderLease(t *testing.T) {
	root := factoryTestRoot(t)
	profile := factoryTestProfile("slow-profile", false)
	secrets := factoryTestSecrets(t, root)
	models := factoryTestModels{model: factoryTestModel(), defaultID: "model-1"}
	entered := make(chan struct{})
	release := make(chan struct{})
	var closeCount atomic.Int32
	provider := SessionProviderFunc(func(context.Context, airuntime.Profile) (SessionLease, error) {
		close(entered)
		<-release
		return SessionLease{Backend: &factoryTestBackend{}, Close: func() { closeCount.Add(1) }}, nil
	})
	factory, err := NewFactory(newFactoryTestConfig(t, root, models, secrets, provider))
	if err != nil {
		t.Fatal(err)
	}
	openDone := make(chan error, 1)
	go func() {
		_, err := factory.Open(context.Background(), profile)
		openDone <- err
	}()
	<-entered
	if err := factory.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-openDone:
		if !errors.Is(err, aisupervisor.ErrClosed) {
			t.Fatalf("slow Open error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow Open did not return")
	}
	if got := closeCount.Load(); got != 1 {
		t.Fatalf("late provider lease close count = %d", got)
	}
	if _, err := os.Stat(filepath.Join(root, "state", profile.ID, "game-capability.token")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("late capability token = %v", err)
	}
}

func TestFactoryDuplicateOpenIsRejectedWhileProviderIsBusy(t *testing.T) {
	root := factoryTestRoot(t)
	profile := factoryTestProfile("duplicate-profile", false)
	secrets := factoryTestSecrets(t, root)
	models := factoryTestModels{model: factoryTestModel(), defaultID: "model-1"}
	entered := make(chan struct{})
	release := make(chan struct{})
	provider := SessionProviderFunc(func(ctx context.Context, _ airuntime.Profile) (SessionLease, error) {
		close(entered)
		select {
		case <-release:
			return SessionLease{Backend: &factoryTestBackend{}, Close: func() {}}, nil
		case <-ctx.Done():
			return SessionLease{}, ctx.Err()
		}
	})
	factory, err := NewFactory(newFactoryTestConfig(t, root, models, secrets, provider))
	if err != nil {
		t.Fatal(err)
	}
	openDone := make(chan error, 1)
	go func() {
		_, err := factory.Open(context.Background(), profile)
		openDone <- err
	}()
	<-entered
	if _, err := factory.Open(context.Background(), profile); !errors.Is(err, ErrFactoryBusy) {
		t.Fatalf("duplicate Open error = %v", err)
	}
	close(release)
	if err := <-openDone; err != nil {
		t.Fatalf("first Open error = %v", err)
	}
	if err := factory.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFactoryCleanupFencesGateBeforeBlockingProviderClose(t *testing.T) {
	root := factoryTestRoot(t)
	profile := factoryTestProfile("blocking-close", false)
	secrets := factoryTestSecrets(t, root)
	models := factoryTestModels{model: factoryTestModel(), defaultID: "model-1"}
	gate := aicontrol.New()
	backend := &factoryTestBackend{observation: aimcp.Observation{Connected: true, Ready: true, CharacterID: profile.Character.ID}}
	closeStarted := make(chan struct{})
	releaseClose := make(chan struct{})
	provider := SessionProviderFunc(func(context.Context, airuntime.Profile) (SessionLease, error) {
		return SessionLease{Backend: backend, Gate: gate, Close: func() {
			close(closeStarted)
			<-releaseClose
		}}, nil
	})
	factory, err := NewFactory(newFactoryTestConfig(t, root, models, secrets, provider))
	if err != nil {
		t.Fatal(err)
	}
	session, err := factory.Open(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		session.Close()
		close(done)
	}()
	<-closeStarted
	if state := gate.State(); state.Mode != aicontrol.Manual {
		t.Fatalf("gate remained owned while provider close blocked: %+v", state)
	}
	close(releaseClose)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("blocking close did not finish")
	}
}

func TestFactoryCleanupClosesTasksOrBackendBeforeProviderLease(t *testing.T) {
	for _, test := range []struct {
		name       string
		withTasks  bool
		wantEvents []string
	}{
		{name: "tasks", withTasks: true, wantEvents: []string{"tasks", "provider"}},
		{name: "backend", withTasks: false, wantEvents: []string{"backend", "provider"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := factoryTestRoot(t)
			profile := factoryTestProfile("close-order-"+test.name, false)
			secrets := factoryTestSecrets(t, root)
			models := factoryTestModels{model: factoryTestModel(), defaultID: "model-1"}
			var mu sync.Mutex
			events := []string{}
			record := func(event string) {
				mu.Lock()
				events = append(events, event)
				mu.Unlock()
			}
			gate := aicontrol.New()
			backend := &factoryClosableBackend{factoryTestBackend: &factoryTestBackend{}}
			backend.closeFn = func() {
				if state := gate.State().Mode; state != aicontrol.Manual {
					t.Errorf("backend closed before gate fence: %s", state)
				}
				record("backend")
			}
			var tasks *factoryTestTasks
			if test.withTasks {
				tasks = &factoryTestTasks{closeFn: func() { record("tasks") }}
			}
			provider := SessionProviderFunc(func(context.Context, airuntime.Profile) (SessionLease, error) {
				return SessionLease{Backend: backend, Gate: gate, Tasks: tasks, Close: func() { record("provider") }}, nil
			})
			factory, err := NewFactory(newFactoryTestConfig(t, root, models, secrets, provider))
			if err != nil {
				t.Fatal(err)
			}
			session, err := factory.Open(context.Background(), profile)
			if err != nil {
				t.Fatal(err)
			}
			session.Close()
			mu.Lock()
			got := append([]string(nil), events...)
			mu.Unlock()
			if strings.Join(got, ",") != strings.Join(test.wantEvents, ",") {
				t.Fatalf("cleanup order = %v, want %v", got, test.wantEvents)
			}
		})
	}
}

func TestFactoryDoesNotExposeProviderOrBackendDiagnostics(t *testing.T) {
	root := factoryTestRoot(t)
	profile := factoryTestProfile("error-profile", false)
	secrets := factoryTestSecrets(t, root)
	models := factoryTestModels{model: factoryTestModel(), defaultID: "model-1"}
	secret := "password=super-secret-token"
	providerError := SessionProviderFunc(func(context.Context, airuntime.Profile) (SessionLease, error) {
		return SessionLease{}, fmt.Errorf("upstream %s", secret)
	})
	factory, err := NewFactory(newFactoryTestConfig(t, root, models, secrets, providerError))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := factory.Open(context.Background(), profile); !errors.Is(err, ErrFactorySession) || strings.Contains(err.Error(), secret) {
		t.Fatalf("provider diagnostic leaked: %v", err)
	}

	backendError := SessionProviderFunc(func(context.Context, airuntime.Profile) (SessionLease, error) {
		return SessionLease{Session: factoryTestSession{}, Close: func() {}}, nil
	})
	config := newFactoryTestConfig(t, root, models, secrets, backendError)
	config.Backend = func(context.Context, BackendInput) (aimcp.Backend, error) {
		return nil, fmt.Errorf("builder %s", secret)
	}
	factory, err = NewFactory(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := factory.Open(context.Background(), profile); !errors.Is(err, ErrFactoryProvision) || strings.Contains(err.Error(), secret) {
		t.Fatalf("backend diagnostic leaked: %v", err)
	}
}

func TestFactoryRejectsProtectedRuntimeRootsBeforeWriting(t *testing.T) {
	root := factoryTestRoot(t)
	operatorHome := filepath.Join(root, "operator-home")
	operatorCodex := filepath.Join(operatorHome, ".codex")
	if err := os.MkdirAll(operatorCodex, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", operatorHome)
	t.Setenv("CODEX_HOME", operatorCodex)

	models := factoryTestModels{model: factoryTestModel(), defaultID: "model-1"}
	secrets := factoryTestSecrets(t, root)
	provider := SessionProviderFunc(func(context.Context, airuntime.Profile) (SessionLease, error) {
		return SessionLease{Session: factoryTestSession{}, Close: func() {}}, nil
	})
	config := newFactoryTestConfig(t, root, models, secrets, provider)
	config.RuntimeRoot = operatorCodex
	config.StateRoot = filepath.Join(operatorCodex, "state")
	config.WorkspaceRoot = filepath.Join(operatorCodex, "workspaces")
	config.CodexHomeRoot = filepath.Join(operatorCodex, "codex")
	if _, err := NewFactory(config); !errors.Is(err, ErrFactoryConfig) {
		t.Fatalf("protected runtime roots error = %v", err)
	}
	for _, path := range []string{
		filepath.Join(operatorCodex, "state"),
		filepath.Join(operatorCodex, "workspaces"),
		filepath.Join(operatorCodex, "codex"),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("protected runtime path %q was written: %v", path, err)
		}
	}
}

func TestFactoryRejectsLateRuntimeSymlinkBeforeProtectedWrite(t *testing.T) {
	root := factoryTestRoot(t)
	operatorHome := filepath.Join(root, "operator-home")
	operatorCodex := filepath.Join(operatorHome, ".codex")
	if err := os.MkdirAll(operatorCodex, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", operatorHome)
	t.Setenv("CODEX_HOME", operatorCodex)

	models := factoryTestModels{model: factoryTestModel(), defaultID: "model-1"}
	secrets := factoryTestSecrets(t, root)
	provider := SessionProviderFunc(func(context.Context, airuntime.Profile) (SessionLease, error) {
		return SessionLease{Session: factoryTestSession{}, Close: func() {}}, nil
	})
	runtimeAlias := filepath.Join(root, "runtime-alias")
	config := newFactoryTestConfig(t, root, models, secrets, provider)
	config.RuntimeRoot = runtimeAlias
	config.StateRoot = filepath.Join(runtimeAlias, "state")
	config.WorkspaceRoot = filepath.Join(runtimeAlias, "workspaces")
	config.CodexHomeRoot = filepath.Join(runtimeAlias, "codex")
	factory, err := NewFactory(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(operatorCodex, runtimeAlias); err != nil {
		t.Fatal(err)
	}
	profile := factoryTestProfile("late-symlink-profile", false)
	if _, err := factory.Open(context.Background(), profile); !errors.Is(err, ErrFactoryProvision) {
		t.Fatalf("late runtime symlink error = %v", err)
	}
	for _, path := range []string{
		filepath.Join(operatorCodex, "state"),
		filepath.Join(operatorCodex, "workspaces"),
		filepath.Join(operatorCodex, "codex"),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("protected runtime path %q was written: %v", path, err)
		}
	}
}

func TestCapabilityTokenPreparationRejectsSymlinkAndReplacementIsPrivate(t *testing.T) {
	root := factoryTestRoot(t)
	guard, err := runtimepath.NewGuard()
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside-token")
	if err := os.WriteFile(outside, []byte("must-survive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(stateDir, "game-capability.token")
	if err := os.Symlink(outside, tokenPath); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareCapabilityToken(guard, stateDir); err == nil {
		t.Fatal("symlink token target was accepted")
	}
	if err := os.Remove(tokenPath); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareCapabilityToken(guard, stateDir)
	if err != nil || prepared != tokenPath {
		t.Fatalf("prepared token path = %q, err=%v", prepared, err)
	}
	if err := replaceCapabilityToken(prepared, "fresh-token"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "must-survive\n" {
		t.Fatalf("outside token changed: %q, err=%v", got, err)
	}
	if info, err := os.Stat(prepared); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("replacement mode = %v, err=%v", info.Mode().Perm(), err)
	}
}

func TestGoalReachedRequiresConnectedReadyAndMatchingTargetType(t *testing.T) {
	base := aimcp.Observation{Connected: true, Ready: true, CharacterID: "character-1", Character: aimcp.Entity{ID: "character-1", Level: 20}}
	goal := airuntime.Goal{TargetLevel: 20, StopWhenCompleted: true, TargetCharacterID: "character-1"}
	if !goalReached(base, goal) {
		t.Fatal("matching character goal was not completed")
	}
	for index, observation := range []aimcp.Observation{
		base,
		{Connected: false, Ready: true, CharacterID: base.CharacterID, Character: base.Character},
		{Connected: true, Ready: false, CharacterID: base.CharacterID, Character: base.Character},
		{Connected: true, Ready: true, CharacterID: "other", Character: base.Character},
	} {
		if index == 0 {
			continue
		}
		if goalReached(observation, goal) {
			t.Fatalf("invalid character observation completed goal: %+v", observation)
		}
	}
	petGoal := airuntime.Goal{TargetLevel: 20, StopWhenCompleted: true, TargetCharacterID: "pet-1", Metadata: map[string]string{"target_kind": "pet"}}
	if goalReached(base, petGoal) || !goalReached(aimcp.Observation{Connected: true, Ready: true, CharacterID: base.CharacterID, Character: base.Character, Pets: []aimcp.Entity{{ID: "pet-1", Level: 20}}}, petGoal) {
		t.Fatal("pet identity/type matching is incorrect")
	}
	if goalReached(base, airuntime.Goal{TargetLevel: 20, StopWhenCompleted: true, TargetCharacterID: base.CharacterID, Metadata: map[string]string{"target_kind": "unknown"}}) {
		t.Fatal("unknown target kind completed goal")
	}
}
