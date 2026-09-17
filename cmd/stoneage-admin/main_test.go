package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/admin"
	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/auth"
)

func TestAIRuntimeRemainsUnavailableUntilAllServerOwnedSettingsArePresent(t *testing.T) {
	got := missingAIRuntimeOptions(aiRuntimeOptions{})
	for _, name := range []string{"ai-web-base-url", "ai-web-server-id", "ai-web-agent-socket", "ai-runtime-root"} {
		if !containsString(got, name) {
			t.Fatalf("missing settings = %v, want %s", got, name)
		}
	}
	if runtime, err := configureAIRuntime(context.Background(), nil, nil, nil, aiRuntimeOptions{}); runtime != nil || err != nil {
		t.Fatalf("incomplete runtime = %v, err=%v; model-only startup should remain available", runtime, err)
	}
}

func TestContainerAIRuntimeRequiresContainerSettingsWithoutHostBinaries(t *testing.T) {
	root := t.TempDir()
	options := aiRuntimeOptions{
		RuntimeRoot: filepath.Join(root, "runtime"),
		WebBaseURL:  "http://web.example", WebServerID: "main", WebAgentSocket: filepath.Join(root, "agent.sock"),
		SkillRoot: filepath.Join(root, "skills"), FundingDir: filepath.Join(root, "funding"),
		KnowledgeDataDir: filepath.Join(root, "knowledge"), MapDataDir: filepath.Join(root, "maps"),
		AutomationDB: filepath.Join(root, "automation.db"), ReceiptDB: filepath.Join(root, "receipts.db"),
		RuntimeImage: "stoneage-ai-runtime:test",
	}
	if !aiContainerRequested(options) {
		t.Fatal("runtime image did not select container mode")
	}
	missing := missingAIRuntimeOptions(options)
	for _, name := range []string{
		"ai-container-network", "ai-docker-binary", "ai-container-gateway-url",
	} {
		if !containsString(missing, name) {
			t.Fatalf("missing settings = %v, want %s", missing, name)
		}
	}
	if containsString(missing, "ai-codex-binary") || containsString(missing, "ai-mcp-binary") {
		t.Fatalf("container mode incorrectly requires host binaries: %v", missing)
	}
	if aiConnectionTesterRequested(options) {
		t.Fatal("container mode exposed a host Codex connection tester")
	}
	if !aiConnectionTesterRequested(aiRuntimeOptions{CodexBinary: "/private/codex"}) {
		t.Fatal("local Codex connection tester was not recognized")
	}
	if got := normalizeAIRuntimeOptions(options).BrokerDB; got != filepath.Join(options.RuntimeRoot, "broker.db") {
		t.Fatalf("default broker database = %q, want inside runtime root", got)
	}

	// A complete-looking local setup must become incomplete when one
	// container-only option is introduced. This prevents a partial container
	// deployment from silently falling back to local Codex.
	options.CodexBinary = filepath.Join(root, "codex")
	options.MCPBinary = filepath.Join(root, "mcp")
	if got := missingAIRuntimeOptions(options); !containsString(got, "ai-container-network") {
		t.Fatalf("partial container setup was accepted: %v", got)
	}
}

func TestAIRuntimeRemainsUnavailableWhenGameplayDataIsMissing(t *testing.T) {
	root := t.TempDir()
	options := aiRuntimeOptions{
		RuntimeRoot: filepath.Join(root, "runtime"),
		WebBaseURL:  "http://web.example", WebServerID: "main", WebAgentSocket: filepath.Join(root, "agent.sock"),
		MCPBinary: filepath.Join(root, "mcp"), SkillRoot: filepath.Join(root, "skills"),
		FundingDir: filepath.Join(root, "funding"), CodexBinary: filepath.Join(root, "codex"),
		KnowledgeDataDir: filepath.Join(root, "knowledge"), MapDataDir: filepath.Join(root, "maps"),
		AutomationDB: filepath.Join(root, "plans.db"), ReceiptDB: filepath.Join(root, "receipts.db"),
	}
	options.KnowledgeDataDir = ""
	if runtime, err := configureAIRuntime(context.Background(), nil, nil, nil, options); runtime != nil || err != nil {
		t.Fatalf("runtime with missing knowledge data = %v, err=%v; automatic tasks must stay unavailable", runtime, err)
	}
}

func TestConfigureAIRuntimeWiresFakeDependenciesAndOwnsGateway(t *testing.T) {
	root := t.TempDir()
	authStore, err := auth.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer authStore.Close()
	if err := authStore.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	modelStore, err := airuntime.OpenWithSecrets(":memory:", filepath.Join(root, "model-secrets"))
	if err != nil {
		t.Fatal(err)
	}
	defer modelStore.Close()
	codex := filepath.Join(root, "codex")
	mcp := filepath.Join(root, "mcp")
	for _, path := range []string{codex, mcp} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	skillRoot, err := filepath.Abs(filepath.Join("..", "..", "ai", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	options := aiRuntimeOptions{
		RuntimeRoot: filepath.Join(root, "runtime"),
		WebBaseURL:  "http://web.example", WebServerID: "main", WebAgentSocket: filepath.Join(root, "agent.sock"),
		MCPBinary: mcp, SkillRoot: skillRoot, FundingDir: filepath.Join(root, "funding"),
		CodexBinary: codex, GatewayListen: "127.0.0.1:0", GatewayPort: -1,
		KnowledgeDataDir: filepath.Join("..", "..", "server", "legacy", "source", "2.5", "gmsv", "data"),
		MapDataDir:       filepath.Join("..", "..", "server", "legacy", "source", "2.5", "gmsv", "data"),
		AutomationDB:     filepath.Join(root, "automation.db"), ReceiptDB: filepath.Join(root, "receipts.db"),
	}
	invalid := options
	invalid.NPCRegistry = filepath.Join(root, "wrong-data-contracts.json")
	if err := os.WriteFile(invalid.NPCRegistry, []byte(`{"version":1,"knowledge_fingerprint":"wrong-game-data","npcs":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if runtime, err := configureAIRuntime(context.Background(), authStore, modelStore, mustModelSecrets(t, root), invalid); err == nil || runtime != nil || !strings.Contains(err.Error(), "NPC contracts") {
		t.Fatalf("mismatched NPC contracts accepted: runtime=%v err=%v", runtime, err)
	}
	if _, err := os.Stat(options.AutomationDB); !os.IsNotExist(err) {
		t.Fatalf("invalid NPC contracts created an automation store: %v", err)
	}
	invalid = options
	invalid.HealingItems = filepath.Join(root, "wrong-healing-items.json")
	if err := os.WriteFile(invalid.HealingItems, []byte(`{"version":1,"knowledge_fingerprint":"wrong-game-data","items":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if runtime, err := configureAIRuntime(context.Background(), authStore, modelStore, mustModelSecrets(t, root), invalid); err == nil || runtime != nil || !strings.Contains(err.Error(), "healing item contracts") {
		t.Fatalf("mismatched healing contracts accepted: runtime=%v err=%v", runtime, err)
	}
	if _, err := os.Stat(options.AutomationDB); !os.IsNotExist(err) {
		t.Fatalf("invalid healing contracts created store: %v", err)
	}
	invalid = options
	invalid.StockItems = filepath.Join(root, "wrong-stock.json")
	if err := os.WriteFile(invalid.StockItems, []byte(`{"version":1,"knowledge_fingerprint":"wrong-data","offers":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if runtime, err := configureAIRuntime(context.Background(), authStore, modelStore, mustModelSecrets(t, root), invalid); err == nil || runtime != nil || !strings.Contains(err.Error(), "stock contracts") {
		t.Fatalf("invalid stock accepted: %v", err)
	}
	if _, err := os.Stat(options.AutomationDB); !os.IsNotExist(err) {
		t.Fatalf("invalid stock created store: %v", err)
	}
	runtime, err := configureAIRuntime(context.Background(), authStore, modelStore, mustModelSecrets(t, root), options)
	if err != nil {
		t.Fatal(err)
	}
	if runtime == nil || runtime.AdminRuntime() == nil || runtime.listener == nil || runtime.gatewayHTTP == nil || runtime.factory == nil || runtime.plans == nil || runtime.receipts == nil {
		t.Fatalf("runtime wiring incomplete: %#v", runtime)
	}
	if got, want := runtime.gatewayEndpoint, webAgentGameEndpoint(options.WebBaseURL); got != want {
		t.Fatalf("local runner gateway endpoint = %q, want Web frontdoor %q", got, want)
	}
	address := runtime.listener.Addr().String()
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := net.Dial("tcp", address); err == nil {
		t.Fatalf("AI Gateway listener %s remained reachable after close", address)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}

	// Local mode must leave ContainerBroker as a nil interface. Otherwise a
	// typed-nil *aibroker.Broker bypasses Factory's host-binary validation and
	// makes a broken local runtime look like a container runtime.
	for name, mutate := range map[string]func(*aiRuntimeOptions){
		"CodexBinary": func(candidate *aiRuntimeOptions) {
			candidate.CodexBinary = filepath.Join(root, "missing-codex")
		},
		"MCPBinary": func(candidate *aiRuntimeOptions) {
			candidate.MCPBinary = filepath.Join(root, "missing-mcp")
		},
	} {
		t.Run("local mode rejects missing "+name, func(t *testing.T) {
			candidate := options
			mutate(&candidate)
			configured, configureErr := configureAIRuntime(context.Background(), authStore, modelStore, mustModelSecrets(t, root), candidate)
			if configured != nil {
				_ = configured.Close()
			}
			if configureErr == nil || configured != nil {
				t.Fatalf("local runtime with missing %s was accepted: runtime=%v err=%v", name, configured, configureErr)
			}
		})
	}
}

func mustModelSecrets(t *testing.T, root string) *airuntime.SecretStore {
	t.Helper()
	secrets, err := airuntime.NewSecretStore(filepath.Join(root, "runtime-model-secrets"))
	if err != nil {
		t.Fatal(err)
	}
	return secrets
}

func TestNormalizeAIGatewayListenRequiresLoopback(t *testing.T) {
	if _, err := normalizeAIGatewayListen("0.0.0.0:8080", -1); err == nil {
		t.Fatal("wildcard AI Gateway listener was accepted")
	}
	if got, err := normalizeAIGatewayListen("127.0.0.1:8080", 9090); err != nil || got != "127.0.0.1:9090" {
		t.Fatalf("gateway port override = %q, err=%v", got, err)
	}
	if _, err := normalizeAIGatewayListen("127.0.0.1:-1", -1); err == nil {
		t.Fatal("negative listener port was accepted")
	}
	if _, err := normalizeAIGatewayListen("127.0.0.1:8080", 65536); err == nil {
		t.Fatal("out-of-range gateway port was accepted")
	}
}

func TestWebAgentGameEndpointUsesWebFrontdoor(t *testing.T) {
	for _, test := range []struct {
		base string
		want string
	}{
		{base: "http://web.example", want: "http://web.example/v1/game"},
		{base: "http://web.example/", want: "http://web.example/v1/game"},
		{base: "  https://web.example  ", want: "https://web.example/v1/game"},
	} {
		if got := webAgentGameEndpoint(test.base); got != test.want {
			t.Errorf("webAgentGameEndpoint(%q) = %q, want %q", test.base, got, test.want)
		}
	}
}

func TestNormalizeAIContainerGatewayListenRequiresFixedReachableAddress(t *testing.T) {
	valid := []struct {
		address string
		want    string
	}{
		{address: "0.0.0.0:8081", want: "0.0.0.0:8081"},
		{address: "192.168.10.20:8081", want: "192.168.10.20:8081"},
		{address: "[fd00::20]:8081", want: "[fd00::20]:8081"},
	}
	for _, test := range valid {
		got, err := normalizeAIContainerGatewayListen(test.address, -1)
		if err != nil || got != test.want {
			t.Errorf("normalizeAIContainerGatewayListen(%q) = %q, %v; want %q", test.address, got, err, test.want)
		}
	}
	for _, address := range []string{"", ":8081", "127.0.0.1:8081", "[::1]:8081", "8.8.8.8:8081", "0.0.0.0:0"} {
		if got, err := normalizeAIContainerGatewayListen(address, -1); err == nil {
			t.Errorf("normalizeAIContainerGatewayListen(%q) = %q, want error", address, got)
		}
	}
	if got, err := normalizeAIContainerGatewayListen("0.0.0.0:8081", 9090); err != nil || got != "0.0.0.0:9090" {
		t.Fatalf("container gateway port override = %q, err=%v", got, err)
	}
}

func TestValidateAIContainerGatewayURL(t *testing.T) {
	valid := []string{
		"http://stoneage-gateway:8081/v1/game",
		"https://stoneage-gateway.example/v1/game",
		"https://192.168.1.20:8443/v1/game",
	}
	for _, value := range valid {
		if err := validateAIContainerGatewayURL(value); err != nil {
			t.Errorf("validateAIContainerGatewayURL(%q) = %v", value, err)
		}
	}
	invalid := []string{
		"http://127.0.0.1:8081/v1/game",
		"http://localhost:8081/v1/game",
		"http://0.0.0.0:8081/v1/game",
		"http://[::]:8081/v1/game",
		"http://user:password@stoneage-gateway:8081/v1/game",
		"http://stoneage-gateway:8081/v1/game?query=1",
		"http://stoneage-gateway:8081/v1/game#fragment",
		"http://stoneage-gateway:8081/other",
		"http:///v1/game",
		"ftp://stoneage-gateway:8081/v1/game",
		"http://stoneage-gateway:0/v1/game",
	}
	for _, value := range invalid {
		if err := validateAIContainerGatewayURL(value); err == nil {
			t.Errorf("validateAIContainerGatewayURL(%q) succeeded, want error", value)
		}
	}
}

func TestConfigureAIRuntimeWiresContainerBrokerWithoutHostBinaries(t *testing.T) {
	root := t.TempDir()
	authStore := mustAuthStore(t)
	defer authStore.Close()
	modelStore, err := airuntime.OpenWithSecrets(":memory:", filepath.Join(root, "model-secrets"))
	if err != nil {
		t.Fatal(err)
	}
	defer modelStore.Close()
	dockerPath := filepath.Join(root, "fake-docker")
	invokedPath := filepath.Join(root, "docker-invoked")
	script := "#!/bin/sh\nprintf invoked > " + shellQuote(invokedPath) + "\nexit 1\n"
	if err := os.WriteFile(dockerPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	skillRoot := mustSkillRoot(t)
	port := freeTCPPort(t)
	runtimeRoot := filepath.Join(root, "runtime")
	options := aiRuntimeOptions{
		RuntimeRoot: runtimeRoot,
		WebBaseURL:  "http://web.example", WebServerID: "main", WebAgentSocket: filepath.Join(root, "agent.sock"),
		SkillRoot: skillRoot, FundingDir: filepath.Join(root, "funding"),
		// These host paths are intentionally unusable: container mode must
		// never pass them into Factory or require them to construct the agent.
		CodexBinary: filepath.Join(root, "missing-host-codex"), MCPBinary: filepath.Join(root, "missing-host-mcp"),
		KnowledgeDataDir: mustGameDataDir(t), MapDataDir: mustGameDataDir(t),
		AutomationDB: filepath.Join(root, "automation.db"), ReceiptDB: filepath.Join(root, "receipts.db"),
		RuntimeImage:     "registry.example/stoneage-ai@sha256:runtime-test",
		ContainerNetwork: "stoneage-backend", DockerBinary: dockerPath,
		ContainerGatewayURL: "http://stoneage-gateway:8081/v1/game",
		GatewayListen:       "0.0.0.0:" + strconv.Itoa(port), GatewayPort: -1,
	}
	runtime, err := configureAIRuntime(context.Background(), authStore, modelStore, mustModelSecrets(t, root), options)
	if err != nil {
		t.Fatal(err)
	}
	if runtime == nil || runtime.broker == nil || runtime.factory == nil || runtime.listener == nil {
		t.Fatalf("container runtime wiring incomplete: %#v", runtime)
	}
	if runtime.gatewayEndpoint != options.ContainerGatewayURL {
		t.Fatalf("container runner gateway endpoint = %q, want %q", runtime.gatewayEndpoint, options.ContainerGatewayURL)
	}
	brokerConfig := runtime.broker.Config()
	if brokerConfig.Image != options.RuntimeImage || brokerConfig.Network != options.ContainerNetwork || brokerConfig.DockerBinary != dockerPath {
		t.Fatalf("container broker config = %+v", brokerConfig)
	}
	if want := filepath.Join(runtimeRoot, "broker.db"); brokerConfig.JournalPath != want {
		t.Fatalf("broker journal = %q, want %q", brokerConfig.JournalPath, want)
	}
	if _, err := os.Stat(brokerConfig.JournalPath); err != nil {
		t.Fatalf("broker journal was not created: %v", err)
	}
	if _, err := os.Stat(invokedPath); err == nil {
		t.Fatal("fake Docker was invoked during runtime construction")
	}
	address := runtime.listener.Addr().String()
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := net.Dial("tcp", address); err == nil {
		t.Fatalf("container AI Gateway listener %s remained reachable after close", address)
	}
}

func TestConfigureAIRuntimeFailureReleasesContainerResources(t *testing.T) {
	root := t.TempDir()
	authStore := mustAuthStore(t)
	defer authStore.Close()
	modelStore, err := airuntime.OpenWithSecrets(":memory:", filepath.Join(root, "model-secrets"))
	if err != nil {
		t.Fatal(err)
	}
	defer modelStore.Close()
	dockerPath := filepath.Join(root, "fake-docker")
	if err := os.WriteFile(dockerPath, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	port := freeTCPPort(t)
	runtimeRoot := filepath.Join(root, "runtime")
	options := aiRuntimeOptions{
		RuntimeRoot: runtimeRoot,
		WebBaseURL:  "http://web.example", WebServerID: "main", WebAgentSocket: filepath.Join(root, "agent.sock"),
		// The missing skill root passes the incomplete-settings gate, then
		// forces NewFactory to fail after the broker and Gateway are owned.
		SkillRoot: filepath.Join(root, "missing-skills"), FundingDir: filepath.Join(root, "funding"),
		KnowledgeDataDir: mustGameDataDir(t), MapDataDir: mustGameDataDir(t),
		AutomationDB: filepath.Join(root, "automation.db"), ReceiptDB: filepath.Join(root, "receipts.db"),
		RuntimeImage:     "registry.example/stoneage-ai@sha256:runtime-test",
		ContainerNetwork: "stoneage-backend", DockerBinary: dockerPath,
		ContainerGatewayURL: "http://stoneage-gateway:8081/v1/game",
		GatewayListen:       "0.0.0.0:" + strconv.Itoa(port), GatewayPort: -1,
	}
	if runtime, err := configureAIRuntime(context.Background(), authStore, modelStore, mustModelSecrets(t, root), options); runtime != nil || err == nil {
		t.Fatalf("failed container configuration = %v, err=%v", runtime, err)
	}
	listener, err := net.Listen("tcp", "0.0.0.0:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("Gateway listener was not released after construction failure: %v", err)
	}
	listener.Close()
	journal, err := aibroker.OpenSQLiteJournal(filepath.Join(runtimeRoot, "broker.db"))
	if err != nil {
		t.Fatalf("broker journal was not released after construction failure: %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustAuthStore(t *testing.T) *auth.Store {
	t.Helper()
	store, err := auth.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store
}

func mustSkillRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "ai", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func mustGameDataDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "server", "legacy", "source", "2.5", "gmsv", "data"))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func TestBootstrapAIModelCreatesReviewedDefaultAndPrivateKey(t *testing.T) {
	root := t.TempDir()
	store, err := airuntime.OpenWithSecrets(filepath.Join(root, "models.db"), filepath.Join(root, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secrets, err := airuntime.NewSecretStore(filepath.Join(root, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "deepseek-key")
	if err := os.WriteFile(keyPath, []byte("sk-bootstrap-original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapAIModel(context.Background(), store, secrets, keyPath); err != nil {
		t.Fatal(err)
	}
	defaultID, err := store.GetDefaultModelConfigID(context.Background())
	if err != nil || defaultID == "" {
		t.Fatalf("default model = %q, err=%v", defaultID, err)
	}
	models, err := store.ListModelConfigs(context.Background())
	if err != nil || len(models) != 1 {
		t.Fatalf("models = %#v, err=%v", models, err)
	}
	if models[0].ID != defaultID || !models[0].HasKey || models[0].Provider != "deepseek" || models[0].Model != "deepseek-flash" {
		t.Fatalf("bootstrapped model = %#v", models[0])
	}
	stored, err := secrets.ReadKey(defaultID)
	if err != nil || stored != "sk-bootstrap-original" {
		t.Fatalf("stored key = %q, err=%v", stored, err)
	}

	if err := os.WriteFile(keyPath, []byte("sk-bootstrap-replacement\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapAIModel(context.Background(), store, secrets, keyPath); err != nil {
		t.Fatal(err)
	}
	stored, err = secrets.ReadKey(defaultID)
	if err != nil || stored != "sk-bootstrap-original" {
		t.Fatalf("existing key was overwritten: %q, err=%v", stored, err)
	}
}

func TestBootstrapAIModelReusesReviewedModelWithoutOverwritingExistingKey(t *testing.T) {
	root := t.TempDir()
	store, err := airuntime.OpenWithSecrets(filepath.Join(root, "models.db"), filepath.Join(root, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secrets, err := airuntime.NewSecretStore(filepath.Join(root, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := admin.NewReviewedAIModelConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.ID = "model-existing"
	config.HasKey = true
	created, err := store.CreateModelConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.WriteKey(created.ID, "sk-existing"); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "deepseek-key")
	if err := os.WriteFile(keyPath, []byte("sk-new-file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapAIModel(context.Background(), store, secrets, keyPath); err != nil {
		t.Fatal(err)
	}
	defaultID, err := store.GetDefaultModelConfigID(context.Background())
	if err != nil || defaultID != created.ID {
		t.Fatalf("default model = %q, err=%v", defaultID, err)
	}
	stored, err := secrets.ReadKey(created.ID)
	if err != nil || stored != "sk-existing" {
		t.Fatalf("existing key was overwritten: %q, err=%v", stored, err)
	}
}

func TestReadAIModelKeyFileRequiresPrivateRegularFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "key")
	if err := os.WriteFile(path, []byte("sk-key"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readAIModelKeyFile(path); err == nil {
		t.Fatal("world-readable key file was accepted")
	}
}
