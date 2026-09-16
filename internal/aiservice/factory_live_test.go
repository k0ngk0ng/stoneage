package aiservice

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aimodels"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

const factoryNativeSkillFixturePrefix = "STONEAGE_PLAY_FACTORY_NATIVE_SKILL_"

type factoryLiveGame struct {
	mu        sync.Mutex
	observes  int
	writes    int
	closed    int
	account   string
	character string
}

func (game *factoryLiveGame) Observe(ctx context.Context) (aigame.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return aigame.Snapshot{}, err
	}
	game.mu.Lock()
	defer game.mu.Unlock()
	game.observes++
	return aigame.Snapshot{
		Account: game.account, Character: game.character, Revision: 1,
		Connected: true, Phase: aigame.PhaseWorld,
		Position: aigame.Point{Floor: 0, X: 10, Y: 10},
		Player:   aigame.PlayerSnapshot{Name: game.character, Level: 1, HP: 100, MaxHP: 100, HasStatus: true},
	}, nil
}

func (game *factoryLiveGame) ExecuteExpected(context.Context, uint64, aigame.Action) error {
	game.mu.Lock()
	game.writes++
	game.mu.Unlock()
	return errors.New("factory live fixture does not permit writes")
}

type factoryLiveEvidence struct {
	Test                string         `json:"test"`
	Status              string         `json:"status"`
	CodexVersion        string         `json:"codex_version"`
	Model               string         `json:"model"`
	Provider            string         `json:"provider"`
	NativeSkill         string         `json:"native_skill"`
	NativeSkillMarkerOK bool           `json:"native_skill_marker_verified"`
	MCPObserveCalls     int            `json:"mcp_observe_calls"`
	Events              map[string]int `json:"events"`
	Usage               aicodex.Usage  `json:"usage"`
	RecordedAt          string         `json:"recorded_at"`
}

// TestLiveCodexFactoryNativeSkillAndMCP exercises the production aiservice
// Factory composition boundary once, with a real Codex/DeepSeek turn and a
// fake authenticated game session behind the private Gateway. It is opt-in
// because the turn uses the configured provider and may incur provider cost.
func TestLiveCodexFactoryNativeSkillAndMCP(t *testing.T) {
	if os.Getenv("STONEAGE_DEEPSEEK_FACTORY_MCP_LIVE_TEST") != "1" {
		t.Skip("explicit live Factory/MCP check not requested")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal("cannot resolve repository root")
	}
	codexBinary := strings.TrimSpace(os.Getenv("STONEAGE_CODEX_BINARY"))
	if !filepath.IsAbs(codexBinary) {
		t.Fatal("STONEAGE_CODEX_BINARY must be an absolute path")
	}
	keyPath := strings.TrimSpace(os.Getenv("STONEAGE_DEEPSEEK_KEY_FILE"))
	if keyPath == "" {
		keyPath = filepath.Join(repoRoot, "vendor", "deepseek", "key")
	}
	if !filepath.IsAbs(keyPath) {
		t.Fatal("DeepSeek key path must be absolute")
	}
	key, err := readFactoryLiveKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	base := filepath.Join(repoRoot, "build", "ai")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal("create build/ai")
	}
	root, err := os.MkdirTemp(base, "factory-mcp-live-")
	if err != nil {
		t.Fatal("create isolated Factory root")
	}
	defer os.RemoveAll(root)
	for _, directory := range []string{"runtime", "models", "secrets", "gocache", "home", "tmp", "codex", "cache", "config", "data"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0700); err != nil {
			t.Fatal("create Factory fixture directory")
		}
	}
	isolatedEnv := factoryLiveIsolatedEnvironment(root)
	versionCommand := exec.Command(codexBinary, "--version")
	versionCommand.Dir = root
	versionCommand.Env = isolatedEnv
	versionOutput, err := versionCommand.Output()
	if err != nil {
		t.Fatal("cannot query isolated Codex version")
	}
	if err := aimodels.CheckCodexVersion(string(versionOutput)); err != nil {
		t.Fatal(err)
	}
	codexVersion := strings.TrimSpace(string(versionOutput))

	installer, err := aimcp.NewSkillInstaller(filepath.Join(repoRoot, "ai", "skills"))
	if err != nil {
		t.Fatal("create skill installer")
	}
	spec, err := installer.Verify("stoneage-play")
	if err != nil {
		t.Fatal("verify native skill catalog entry")
	}

	store, err := airuntime.OpenStore(filepath.Join(root, "models", "models.db"))
	if err != nil {
		t.Fatal("open model store")
	}
	defer store.Close()
	secrets, err := airuntime.NewSecretStore(filepath.Join(root, "secrets"))
	if err != nil {
		t.Fatal("open model secret store")
	}
	model, err := store.CreateModelConfig(context.Background(), airuntime.ModelConfig{
		ID: "factory-live-model", Name: "DeepSeek Flash live fixture", Backend: airuntime.ModelBackendCodex,
		Provider: aimodels.DeepSeekProvider, BaseURL: aimodels.DeepSeekBaseURL, Model: aimodels.DeepSeekFlash,
		ReasoningEffort: airuntime.ReasoningEffortHigh, Timeout: 2 * time.Minute, MaxOutputTokens: 256,
	})
	if err != nil {
		t.Fatal("create model configuration")
	}
	if err := secrets.WriteKey(model.ID, key); err != nil {
		t.Fatal("write model key")
	}

	game := &factoryLiveGame{account: "factory-user", character: "FactoryHero"}
	profile := airuntime.Profile{
		ID:            "factory-live-profile",
		Account:       airuntime.AccountIdentity{ID: "factory-account", Username: game.account},
		Character:     airuntime.CharacterIdentity{ID: "factory-character", Name: game.character},
		ModelConfigID: model.ID,
		Skills:        []airuntime.SkillVersion{{Name: spec.Name, Version: spec.Version, Kind: airuntime.SkillKindNative, Digest: "sha256:" + spec.SHA256}},
		Status:        airuntime.ProfileStatusActive,
	}
	provider := SessionProviderFunc(func(context.Context, airuntime.Profile) (SessionLease, error) {
		return SessionLease{Session: game, Close: func() {
			game.mu.Lock()
			game.closed++
			game.mu.Unlock()
		}}, nil
	})

	mcpBinary := filepath.Join(root, "stoneage-game-mcp")
	build := exec.Command("go", "build", "-o", mcpBinary, "./cmd/stoneage-game-mcp")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, "gocache"))
	if output, err := build.CombinedOutput(); err != nil {
		_ = output
		t.Fatal("build game MCP sidecar")
	}
	gitBinary, err := exec.LookPath("git")
	if err != nil || !filepath.IsAbs(gitBinary) {
		t.Fatal("find isolated workspace git")
	}

	gateway := NewGateway()
	gatewayHTTP := httptest.NewServer(gateway)
	defer gatewayHTTP.Close()
	factory, err := NewFactory(FactoryConfig{
		Models: store, Secrets: secrets, Sessions: provider, Gateway: gateway,
		GatewayEndpoint: gatewayHTTP.URL + "/v1/game", SkillInstaller: installer,
		RuntimeRoot: filepath.Join(root, "runtime"), StateRoot: filepath.Join(root, "runtime", "state"),
		WorkspaceRoot: filepath.Join(root, "runtime", "workspaces"), CodexHomeRoot: filepath.Join(root, "runtime", "codex"),
		CodexBinary: codexBinary, MCPBinary: mcpBinary, GitBinary: gitBinary,
		TerminationGrace: 2 * time.Second,
	})
	if err != nil {
		t.Fatal("create aiservice Factory")
	}
	defer factory.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	session, err := factory.Open(ctx, profile)
	if err != nil {
		t.Fatal("open Factory agent session")
	}
	closed := false
	defer func() {
		if !closed {
			session.Close()
		}
	}()

	workspace := filepath.Join(root, "runtime", "workspaces", profile.ID)
	marker, err := addFactoryNativeSkillFixtureMarker(workspace)
	if err != nil {
		t.Fatal("prepare Factory native skill fixture")
	}
	prompt := "Use and follow the installed native stoneage-play skill. It contains a private native skill fixture marker; read that marker from the skill rather than guessing it. You must call game_observe exactly once before answering. After the tool result, reply with exactly STONEAGE_FACTORY_MCP_LIVE_OK:<marker-from-the-skill> and nothing else."
	result, err := session.Runner.Run(ctx, aicodex.RunRequest{ProfileID: profile.ID, Prompt: prompt})
	if err != nil {
		t.Fatalf("Factory Codex/MCP live turn failed (details suppressed): %T; timeout=%v", err, ctx.Err() != nil)
	}
	if strings.Contains(result.Stderr, key) || strings.Contains(result.LastMessage, key) {
		t.Fatal("Factory result contained model credential material")
	}
	eventCounts := make(map[string]int)
	answer := false
	for _, event := range result.Events {
		eventCounts[event.Type]++
		if event.Type == "item.completed" && event.ItemType == "agent_message" {
			var item struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(event.Item, &item) == nil && strings.TrimSpace(item.Text) == "STONEAGE_FACTORY_MCP_LIVE_OK:"+marker {
				answer = true
			}
		}
		if bytes.Contains(event.Raw, []byte(key)) {
			t.Fatal("Factory event contained model credential material")
		}
	}
	game.mu.Lock()
	observes, writes, closeCount := game.observes, game.writes, game.closed
	game.mu.Unlock()
	if !answer || result.Process.Status != aicodex.ProcessExited || result.Process.ExitCode != 0 || result.Turn.Status != aicodex.TurnCompleted || observes != 1 || writes != 0 {
		t.Fatalf("Factory live evidence incomplete: process=%+v turn=%+v marker=%v observes=%d writes=%d", result.Process, result.Turn, answer, observes, writes)
	}

	// Closing the AgentSession must revoke the Gateway capability and remove
	// the private token file that was provisioned for the sidecar.
	session.Close()
	closed = true
	if _, err := os.Stat(filepath.Join(root, "runtime", "state", profile.ID, "game-capability.token")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Factory capability token was not removed: %v", err)
	}
	game.mu.Lock()
	closeCount = game.closed
	game.mu.Unlock()
	if closeCount != 1 {
		t.Fatalf("Factory session close count = %d, want 1", closeCount)
	}

	evidence := factoryLiveEvidence{
		Test: "TestLiveCodexFactoryNativeSkillAndMCP", Status: "passed",
		CodexVersion: codexVersion, Model: aimodels.DeepSeekFlash,
		Provider: aimodels.DeepSeekProvider, NativeSkill: spec.Name,
		NativeSkillMarkerOK: answer, MCPObserveCalls: observes, Events: eventCounts,
		Usage: result.Usage, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	evidencePath, err := persistFactoryLiveEvidence(repoRoot, evidence, key)
	if err != nil {
		t.Fatal("persist Factory live evidence")
	}
	t.Logf("Factory native skill/MCP live evidence: file=%s codex=%s usage=%+v", evidencePath, evidence.CodexVersion, evidence.Usage)
}

func readFactoryLiveKey(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("DeepSeek key file is not a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("cannot read configured DeepSeek key file")
	}
	key := strings.TrimSpace(string(data))
	if key == "" || strings.ContainsAny(key, "\x00\r\n") {
		return "", errors.New("configured DeepSeek key is invalid")
	}
	return key, nil
}

func addFactoryNativeSkillFixtureMarker(workspace string) (string, error) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	marker := factoryNativeSkillFixturePrefix + strings.ToUpper(hex.EncodeToString(nonce[:]))
	path := filepath.Join(workspace, ".agents", "skills", "stoneage-play", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	data = append(data, []byte("\n\n## Native runtime fixture\n\nThe private native skill fixture marker is `"+marker+"`. When an authorized integration check asks for this marker, return it verbatim.\n")...)
	return marker, os.WriteFile(path, data, 0600)
}

func factoryLiveIsolatedEnvironment(root string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(root, "home"),
		"CODEX_HOME=" + filepath.Join(root, "codex"),
		"TMPDIR=" + filepath.Join(root, "tmp"),
		"TMP=" + filepath.Join(root, "tmp"),
		"TEMP=" + filepath.Join(root, "tmp"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(root, "gitconfig"),
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
	}
}

func persistFactoryLiveEvidence(repoRoot string, evidence factoryLiveEvidence, secret string) (string, error) {
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return "", err
	}
	if secret != "" && bytes.Contains(data, []byte(secret)) {
		return "", errors.New("Factory live evidence contains credential material")
	}
	base := filepath.Join(repoRoot, "build", "ai")
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(base, ".factory-live-evidence-*")
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
	if err := temporary.Chmod(0600); err != nil {
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
	destination := filepath.Join(base, "factory-mcp-live-evidence.json")
	if err := os.Rename(path, destination); err != nil {
		return "", err
	}
	keep = true
	return destination, nil
}
