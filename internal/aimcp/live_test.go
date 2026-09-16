package aimcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aimodels"
)

const nativeSkillFixturePrefix = "STONEAGE_PLAY_NATIVE_SKILL_"

type liveUsage struct {
	InputTokens       int64 `json:"input_tokens,omitempty"`
	CachedInputTokens int64 `json:"cached_input_tokens,omitempty"`
	OutputTokens      int64 `json:"output_tokens,omitempty"`
	TotalTokens       int64 `json:"total_tokens,omitempty"`
}

type liveEvidence struct {
	Test                string         `json:"test"`
	Status              string         `json:"status"`
	CodexVersion        string         `json:"codex_version"`
	Model               string         `json:"model"`
	Provider            string         `json:"provider"`
	NativeSkill         string         `json:"native_skill"`
	NativeSkillMarkerOK bool           `json:"native_skill_marker_verified"`
	MCPObserveCalls     int32          `json:"mcp_observe_calls"`
	Events              map[string]int `json:"events"`
	Usage               liveUsage      `json:"usage"`
	RecordedAt          string         `json:"recorded_at"`
}

// TestLiveCodexNativeSkillAndMCP is deliberately opt-in. It makes one real
// Codex/DeepSeek turn against a local fake game gateway, proving that the
// checked-in skill is discoverable and the model can call the stdio MCP tool.
// It never connects to a game server. The key is read once from the private
// configured file and is never included in diagnostics.
func TestLiveCodexNativeSkillAndMCP(t *testing.T) {
	if os.Getenv("STONEAGE_DEEPSEEK_MCP_LIVE_TEST") != "1" {
		t.Skip("explicit live Codex/MCP check not requested")
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
	keyInfo, err := os.Lstat(keyPath)
	if err != nil || keyInfo.Mode()&os.ModeSymlink != 0 || !keyInfo.Mode().IsRegular() || keyInfo.Mode().Perm()&0077 != 0 {
		t.Fatal("DeepSeek key file is not a private regular file")
	}
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal("cannot read configured DeepSeek key file")
	}
	key := strings.TrimSpace(string(keyBytes))
	keyBytes = nil
	if key == "" || strings.ContainsAny(key, "\x00\r\n") {
		t.Fatal("configured DeepSeek key is invalid")
	}

	const token = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var calls atomic.Int32
	fakeGateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/game" || request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer "+token {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		calls.Add(1)
		var envelope struct {
			Operation string          `json:"operation"`
			Arguments json.RawMessage `json:"arguments"`
		}
		decoder := json.NewDecoder(request.Body)
		if decoder.Decode(&envelope) != nil || envelope.Operation != "observe" || string(envelope.Arguments) != "{}" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"result":{"revision":1,"character_id":"live-character","character_name":"LiveHero","connected":true,"ready":true,"phase":"world","floor":0,"x":10,"y":10,"character":{"id":"live-character","name":"LiveHero","level":1,"hp":100,"max_hp":100,"alive":true},"battle":{"active":false,"command_ready":false},"spending_known":true}}`))
	}))
	defer fakeGateway.Close()

	base := filepath.Join(repoRoot, "build", "ai")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal("create build/ai")
	}
	root, err := os.MkdirTemp(base, "mcp-live-")
	if err != nil {
		t.Fatal("create isolated live root")
	}
	defer os.RemoveAll(root)
	for _, directory := range []string{"home", "tmp", "workspace", "codex", "gocache", "cache", "config", "data"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0700); err != nil {
			t.Fatal("create isolated live directory")
		}
	}
	workspace := filepath.Join(root, "workspace")
	isolatedEnv := liveIsolatedEnvironment(root)
	versionCommand := exec.Command(codexBinary, "--version")
	versionCommand.Dir = workspace
	versionCommand.Env = isolatedEnv
	versionOutput, err := versionCommand.Output()
	if err != nil {
		t.Fatal("cannot query Codex version")
	}
	if err := aimodels.CheckCodexVersion(string(versionOutput)); err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "-c", "init.templateDir=", "init", "--quiet", workspace)
	git.Dir = root
	git.Env = isolatedEnv
	if err := git.Run(); err != nil {
		t.Fatal("initialize isolated workspace")
	}
	mcpBinary := filepath.Join(root, "stoneage-game-mcp")
	build := exec.Command("go", "build", "-o", mcpBinary, "./cmd/stoneage-game-mcp")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, "gocache"))
	if output, err := build.CombinedOutput(); err != nil {
		_ = output
		t.Fatal("build game MCP sidecar")
	}
	installer, err := NewSkillInstaller(filepath.Join(repoRoot, "ai", "skills"))
	if err != nil {
		t.Fatal("create skill installer")
	}
	if _, err := installer.Install("stoneage-play", workspace); err != nil {
		t.Fatal("install native StoneAge skill")
	}
	marker, err := addNativeSkillFixtureMarker(workspace)
	if err != nil {
		t.Fatal("prepare native skill fixture")
	}
	capabilityPath := filepath.Join(root, "game-capability.token")
	if err := os.WriteFile(capabilityPath, []byte(token+"\n"), 0600); err != nil {
		t.Fatal("write isolated game capability token")
	}
	settings := aimodels.RuntimeSettings{
		APIKey: key, MCPCommand: mcpBinary, MCPArgs: []string{"serve"},
		MCPEnv: map[string]string{
			"STONEAGE_AI_ENDPOINT":           fakeGateway.URL + "/v1/game",
			"STONEAGE_AI_TOKEN_FILE":         capabilityPath,
			"STONEAGE_AI_CHARACTER_ID":       "live-character",
			"STONEAGE_AI_CHARACTER_NAME":     "LiveHero",
			"STONEAGE_AI_CONTROL_GENERATION": "1",
		},
	}
	if _, err := aimodels.Materialize(filepath.Join(root, "codex"), settings); err != nil {
		t.Fatal("materialize isolated Codex configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, codexBinary, "exec", "--json", "--skip-git-repo-check", "--color", "never", "-")
	command.Dir = workspace
	command.Env = isolatedEnv
	command.Stdin = strings.NewReader("Use and follow the installed native stoneage-play skill. It contains a private native skill fixture marker; read that marker from the skill rather than guessing it. You must call game_observe exactly once before answering. After the tool result, reply with exactly STONEAGE_MCP_LIVE_OK:<marker-from-the-skill> and nothing else.")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("Codex/MCP live turn failed (details suppressed): %T; timeout=%v", err, ctx.Err() != nil)
	}
	if strings.Contains(stdout.String(), key) || strings.Contains(stderr.String(), key) || strings.Contains(stdout.String(), token) || strings.Contains(stderr.String(), token) {
		t.Fatal("provider or MCP output contained credential material")
	}
	started, completed, answer := false, false, false
	eventCounts := make(map[string]int)
	var usage liveUsage
	for _, line := range bytes.Split(stdout.Bytes(), []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event map[string]json.RawMessage
		if json.Unmarshal(line, &event) != nil {
			t.Fatal("Codex emitted invalid JSON event")
		}
		var kind string
		_ = json.Unmarshal(event["type"], &kind)
		eventCounts[kind]++
		switch kind {
		case "thread.started":
			started = true
		case "turn.failed", "error":
			t.Fatal("Codex reported a failed turn (details suppressed)")
		case "turn.completed":
			completed = true
			if raw := event["usage"]; len(raw) > 0 {
				if err := json.Unmarshal(raw, &usage); err != nil {
					t.Fatal("Codex usage event was invalid")
				}
			}
		case "item.completed":
			var item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			_ = json.Unmarshal(event["item"], &item)
			if item.Type == "agent_message" && strings.TrimSpace(item.Text) == "STONEAGE_MCP_LIVE_OK:"+marker {
				answer = true
			}
		}
	}
	if !started || !completed || !answer || calls.Load() != 1 {
		t.Fatalf("live evidence incomplete: thread=%v completed=%v native_marker=%v game_calls=%d", started, completed, answer, calls.Load())
	}
	evidence := liveEvidence{
		Test:                "TestLiveCodexNativeSkillAndMCP",
		Status:              "passed",
		CodexVersion:        strings.TrimSpace(string(versionOutput)),
		Model:               aimodels.DeepSeekFlash,
		Provider:            aimodels.DeepSeekProvider,
		NativeSkill:         "stoneage-play",
		NativeSkillMarkerOK: answer,
		MCPObserveCalls:     calls.Load(),
		Events:              eventCounts,
		Usage:               usage,
		RecordedAt:          time.Now().UTC().Format(time.RFC3339Nano),
	}
	evidencePath, err := persistLiveEvidence(repoRoot, "mcp-live-evidence.json", evidence, key, token)
	if err != nil {
		t.Fatal("persist sanitized live evidence")
	}
	t.Logf("native skill/MCP live evidence: file=%s codex=%s usage=%+v", evidencePath, evidence.CodexVersion, evidence.Usage)
}

func addNativeSkillFixtureMarker(workspace string) (string, error) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	marker := nativeSkillFixturePrefix + strings.ToUpper(hex.EncodeToString(nonce[:]))
	path := filepath.Join(workspace, ".agents", "skills", "stoneage-play", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	fixture := fmt.Sprintf("\n\n## Native runtime fixture\n\nThe private native skill fixture marker is `%s`. When an authorized integration check asks for this marker, return it verbatim.\n", marker)
	data = append(data, fixture...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", err
	}
	return marker, nil
}

func liveIsolatedEnvironment(root string) []string {
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

func persistLiveEvidence(repoRoot, name string, evidence liveEvidence, secrets ...string) (string, error) {
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return "", err
	}
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(data, []byte(secret)) {
			return "", errors.New("live evidence contains credential material")
		}
	}
	base := filepath.Join(repoRoot, "build", "ai")
	if err := os.MkdirAll(base, 0700); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(base, ".live-evidence-*")
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
	destination := filepath.Join(base, name)
	if err := os.Rename(path, destination); err != nil {
		return "", err
	}
	keep = true
	return destination, nil
}
