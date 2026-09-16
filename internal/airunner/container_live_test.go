package airunner

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aimodels"
	"github.com/pelletier/go-toml/v2"
)

// Opt-in proof of Linux runner -> official Codex -> native Skill -> MCP ->
// authenticated fixture. Uses existing images and prebuilt Linux binaries;
// never pulls/builds an image and does not claim a released-image/game test.
func TestLiveContainerCodexSkillMCP(t *testing.T) {
	if os.Getenv("STONEAGE_CONTAINER_CODEX_LIVE_TEST") != "1" {
		t.Skip("requires explicit opt-in, existing Linux image and prepared Linux binaries")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	artifacts := filepath.Join(repo, "build", "ai", "container-runtime-qa")
	for _, name := range []string{"codex", "stoneage-ai-runner", "stoneage-game-mcp"} {
		info, err := os.Lstat(filepath.Join(artifacts, "bin", name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			t.Fatal("prepare Linux executable:", name)
		}
	}
	keyPath := filepath.Join(repo, "vendor", "deepseek", "key")
	info, err := os.Lstat(keyPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		t.Fatal("private provider key required")
	}
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal("read provider key")
	}
	key := strings.TrimSpace(string(keyBytes))
	if key == "" || strings.ContainsAny(key, "\r\n\x00") {
		t.Fatal("invalid provider key")
	}
	image := os.Getenv("STONEAGE_CONTAINER_CODEX_IMAGE")
	if image == "" {
		image = "gcc:13-bookworm"
	}
	imageBytes, err := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", image).Output()
	if err != nil {
		t.Fatal("existing image required; no pull attempted")
	}
	image = strings.TrimSpace(string(imageBytes))
	root, err := os.MkdirTemp(filepath.Join(repo, "build", "ai"), "container-codex-live-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	marker := "CONTAINER_OBSERVATION_" + hex.EncodeToString(random)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	var calls atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/game" || r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(401)
			return
		}
		var envelope struct {
			Operation string          `json:"operation"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&envelope) != nil || envelope.Operation != "observe" || string(envelope.Arguments) != "{}" {
			w.WriteHeader(400)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{
			"revision": 1, "character_id": "container-character", "character_name": marker,
			"connected": true, "ready": true, "phase": "world", "floor": 0, "x": 10, "y": 10,
			"character": map[string]any{"id": "container-character", "name": marker, "level": 1, "hp": 100, "max_hp": 100, "alive": true},
			"battle":    map[string]any{"active": false},
		}})
	}))
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	defer server.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	request := ExecuteRequest{
		ProfileID: "container-live", RequestID: "turn-1",
		Run:    RunRequest{Prompt: "Read the complete installed .agents/skills/stoneage-play/SKILL.md using the shell tool and follow it. Then call game_observe exactly once. Reply with exactly the character_name returned by that tool, and nothing else. Do not perform any game write."},
		Model:  Model{Provider: aimodels.DeepSeekProvider, BaseURL: aimodels.DeepSeekBaseURL, Model: aimodels.DeepSeekFlash, ReasoningEffort: "high", APIKey: key},
		Skills: []Skill{{Name: "stoneage-play"}},
		MCP:    MCP{Endpoint: fmt.Sprintf("http://host.docker.internal:%d/v1/game", port), Token: token, CharacterID: "container-character", Generation: 1},
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal("encode request")
	}
	name := "sa-codex-qa-" + filepath.Base(root)
	defer func() { _ = exec.Command("docker", "rm", "--force", name).Run() }()
	uid := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	if os.Getuid() == 0 {
		t.Fatal("live fixture requires a non-root host UID for private bind ownership")
	}
	common := []string{"run", "--pull", "never", "--user", uid, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,nosuid,size=64m", "--mount", "type=bind,src=" + artifacts + ",dst=/qa,readonly"}
	versionArgs := append(append([]string{}, common...), "--rm", "--network", "none", "--env", "HOME=/tmp", "--env", "CODEX_HOME=/tmp/codex", "--entrypoint", "/qa/bin/codex", image, "--version")
	version, err := exec.Command("docker", versionArgs...).Output()
	if err != nil {
		t.Fatal("Linux Codex version probe failed")
	}
	if err := aimodels.CheckCodexVersion(string(version)); err != nil {
		t.Fatal(err)
	}
	args := append(common, "--name", name, "--interactive", "--add-host", "host.docker.internal:host-gateway", "--env", "HOME=/state/launcher-home", "--mount", "type=bind,src="+state+",dst=/state", "--mount", "type=bind,src="+filepath.Join(repo, "ai", "skills")+",dst=/qa/skills,readonly", "--entrypoint", "/qa/bin/stoneage-ai-runner", image, "-profile", request.ProfileID, "-state", "/state", "-codex", "/qa/bin/codex", "-mcp", "/qa/bin/stoneage-game-mcp", "-skills", "/qa/skills", "-git", "/usr/bin/git")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = bytes.NewReader(raw)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if bytes.Contains(stdout.Bytes(), []byte(key)) || bytes.Contains(stderr.Bytes(), []byte(key)) || bytes.Contains(stdout.Bytes(), []byte(token)) || bytes.Contains(stderr.Bytes(), []byte(token)) {
		t.Fatal("credential leaked into output")
	}
	inspect, err := exec.Command("docker", "inspect", "--format", `{"user":{{json .Config.User}},"readonly":{{json .HostConfig.ReadonlyRootfs}},"caps":{{json .HostConfig.CapDrop}},"security":{{json .HostConfig.SecurityOpt}},"mounts":{{json .Mounts}}}`, name).Output()
	var actual struct {
		User           string
		Readonly       bool
		Caps, Security []string
		Mounts         []struct {
			Source, Destination string
			RW                  bool
		}
	}
	if err != nil || json.Unmarshal(inspect, &actual) != nil || actual.User != uid || !actual.Readonly || strings.Join(actual.Caps, ",") != "ALL" || !strings.Contains(strings.Join(actual.Security, ","), "no-new-privileges") {
		t.Fatal("actual container isolation mismatch")
	}
	expectedMounts := map[string]string{"/qa": artifacts, "/qa/skills": filepath.Join(repo, "ai", "skills"), "/state": state}
	if len(actual.Mounts) != len(expectedMounts) {
		t.Fatal("unexpected container mount count")
	}
	for _, mount := range actual.Mounts {
		if expectedMounts[mount.Destination] != mount.Source || mount.RW != (mount.Destination == "/state") {
			t.Fatal("unexpected container mount or write access")
		}
	}
	var response Response
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("invalid runner response; process=%v", runErr)
	}
	if runErr != nil || !response.OK || response.Result == nil {
		t.Fatalf("container runner failed: category=%s process=%v", response.Error, runErr)
	}
	result := response.Result
	if result.Turn.Status != "completed" || strings.TrimSpace(result.LastMessage) != marker || calls.Load() != 1 {
		t.Fatalf("unverified model/tool result: turn=%s observe_calls=%d", result.Turn.Status, calls.Load())
	}
	skillRead := false
	for _, event := range result.Events {
		var item struct {
			Type     string `json:"type"`
			Command  string `json:"command"`
			Output   string `json:"aggregated_output"`
			ExitCode int    `json:"exit_code"`
		}
		if json.Unmarshal(event.Item, &item) == nil && item.Type == "command_execution" && item.ExitCode == 0 && strings.Contains(item.Command, "stoneage-play/SKILL.md") && strings.Contains(item.Output, "stoneage-play") {
			skillRead = true
		}
	}
	if !skillRead {
		t.Fatal("no successful native Skill read in Codex events")
	}
	config, err := os.ReadFile(filepath.Join(state, "codex", "config.toml"))
	if err != nil {
		t.Fatal("read only isolated generated config")
	}
	var settings struct {
		Approval  string `toml:"approval_policy"`
		Sandbox   string `toml:"sandbox_mode"`
		Provider  string `toml:"model_provider"`
		Catalog   string `toml:"model_catalog_json"`
		Providers map[string]struct {
			WireAPI string `toml:"wire_api"`
		} `toml:"model_providers"`
	}
	if toml.Unmarshal(config, &settings) != nil || settings.Approval != "never" || settings.Sandbox != "danger-full-access" || settings.Providers[settings.Provider].WireAPI != "responses" {
		t.Fatal("isolated policies or Responses provider mismatch")
	}
	if settings.Catalog != "/state/codex/models.json" {
		t.Fatal("isolated model catalog path mismatch")
	}
	catalog, err := os.ReadFile(filepath.Join(state, "codex", "models.json"))
	var modelCatalog struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	if err != nil || json.Unmarshal(catalog, &modelCatalog) != nil || len(modelCatalog.Models) != 1 || modelCatalog.Models[0].Slug != aimodels.DeepSeekFlash {
		t.Fatal("DeepSeek model catalog missing")
	}
	binaryHashes := make(map[string]string)
	for _, name := range []string{"codex", "stoneage-ai-runner", "stoneage-game-mcp"} {
		payload, err := os.ReadFile(filepath.Join(artifacts, "bin", name))
		if err != nil {
			t.Fatal("read QA binary for provenance")
		}
		digest := sha256.Sum256(payload)
		binaryHashes[name] = hex.EncodeToString(digest[:])
	}
	evidence := map[string]any{"actual_container_isolation_verified": true, "binary_sha256": binaryHashes, "test": t.Name(), "passed": true, "image": image, "codex_version": strings.TrimSpace(string(version)), "model": aimodels.DeepSeekFlash, "native_skill_read": skillRead, "model_catalog_verified": true, "mcp_observe_calls": calls.Load(), "random_observation_verified": true, "usage": result.Usage, "isolated_policies_verified": true, "non_root_user": uid, "released_image_verified": false, "real_game_verified": false}
	encoded, _ := json.MarshalIndent(evidence, "", "  ")
	if err := os.WriteFile(filepath.Join(artifacts, "live-evidence.json"), append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("real Linux Codex, native Skill, authenticated MCP observation and isolated policies passed")
}
