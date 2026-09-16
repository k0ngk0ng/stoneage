package aiservice

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

// containerLiveRunner runs one isolated Linux Codex process against a private
// MCP endpoint. The helper is shared by the observation and leveling live
// checks so both tests retain the same secret and container isolation guards.
type containerLiveRunner struct {
	Context    context.Context
	RepoRoot   string
	Artifacts  string
	State      string
	Image      string
	UID        string
	Key        string
	Token      string
	NameSuffix string
}

func (r containerLiveRunner) Run(t *testing.T, request airunner.ExecuteRequest) airunner.Result {
	t.Helper()
	ctx := r.Context
	if ctx == nil {
		ctx = context.Background()
	}
	name := "sa-game-codex-" + request.RequestID + "-" + r.NameSuffix
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "--force", name).Run() })
	args := []string{"run", "--pull", "never", "--name", name, "--interactive", "--user", r.UID, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--tmpfs", "/tmp:rw,nosuid,size=64m", "--add-host", "host.docker.internal:host-gateway", "--env", "HOME=/state/launcher-home",
		"--mount", "type=bind,src=" + r.Artifacts + ",dst=/qa,readonly",
		"--mount", "type=bind,src=" + filepath.Join(r.RepoRoot, "ai", "skills") + ",dst=/qa/skills,readonly",
		"--mount", "type=bind,src=" + r.State + ",dst=/state",
		"--entrypoint", "/qa/bin/stoneage-ai-runner", r.Image,
		"-profile", request.ProfileID, "-state", "/state", "-codex", "/qa/bin/codex", "-mcp", "/qa/bin/stoneage-game-mcp", "-skills", "/qa/skills", "-git", "/usr/bin/git"}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal("encode private request")
	}
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stdin = bytes.NewReader(raw)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	runErr := command.Run()
	for _, secret := range []string{r.Key, r.Token} {
		if bytes.Contains(stdout.Bytes(), []byte(secret)) || bytes.Contains(stderr.Bytes(), []byte(secret)) {
			t.Fatal("credential in process output")
		}
	}
	var response airunner.Response
	if json.Unmarshal(stdout.Bytes(), &response) != nil {
		t.Fatalf("invalid container response; process=%v", runErr)
	}
	if runErr != nil || !response.OK || response.Result == nil {
		t.Fatalf("container turn failed: category=%s process=%v", response.Error, runErr)
	}
	if response.ProfileID != request.ProfileID || response.RequestID != request.RequestID || response.Result.Turn.Status != "completed" || response.Result.ThreadID == "" {
		t.Fatal("unverified container response identity/completion")
	}
	// Keep redacted native tool events for diagnosing discovery/transport
	// failures even when a subsequent gameplay assertion fails. The raw private
	// request and process input are never persisted here.
	nativeEvents := []json.RawMessage{}
	for _, event := range response.Result.Events {
		var item struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(event.Item, &item) == nil && item.Type == "mcp_tool_call" {
			nativeEvents = append(nativeEvents, event.Item)
		}
	}
	eventData, err := json.MarshalIndent(nativeEvents, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Artifacts, "native-mcp-events-"+request.RequestID+".json"), eventData, 0600); err != nil {
		t.Fatal(err)
	}
	// Retain through process exit so daemon state can be independently checked.
	inspect, err := exec.CommandContext(ctx, "docker", "inspect", "--format", `{"user":{{json .Config.User}},"readonly":{{json .HostConfig.ReadonlyRootfs}},"caps":{{json .HostConfig.CapDrop}},"security":{{json .HostConfig.SecurityOpt}},"mounts":{{json .Mounts}},"exit_code":{{json .State.ExitCode}}}`, name).Output()
	var actual struct {
		User           string
		Readonly       bool
		Caps, Security []string
		Mounts         []struct {
			Source, Destination string
			RW                  bool
		}
		ExitCode int `json:"exit_code"`
	}
	if err != nil || json.Unmarshal(inspect, &actual) != nil || actual.User != r.UID || !actual.Readonly || actual.ExitCode != 0 || strings.Join(actual.Caps, ",") != "ALL" || !strings.Contains(strings.Join(actual.Security, ","), "no-new-privileges") {
		t.Fatal("actual container isolation/exit mismatch")
	}
	expectedMounts := map[string]string{"/qa": r.Artifacts, "/qa/skills": filepath.Join(r.RepoRoot, "ai", "skills"), "/state": r.State}
	if len(actual.Mounts) != len(expectedMounts) {
		t.Fatal("unexpected container mount count")
	}
	for _, mount := range actual.Mounts {
		if expectedMounts[mount.Destination] != mount.Source || mount.RW != (mount.Destination == "/state") {
			t.Fatal("unexpected actual mount source/write access")
		}
	}
	if err := exec.CommandContext(ctx, "docker", "rm", name).Run(); err != nil {
		t.Fatal("cleanup completed model container")
	}
	return *response.Result
}

func containerLiveSkillRead(result airunner.Result) bool {
	return containerLiveNamedSkillRead(result, "stoneage-play")
}

func containerLiveNamedSkillRead(result airunner.Result, name string) bool {
	for _, event := range result.Events {
		var item struct {
			Type     string `json:"type"`
			Command  string `json:"command"`
			Output   string `json:"aggregated_output"`
			ExitCode int    `json:"exit_code"`
		}
		if json.Unmarshal(event.Item, &item) == nil && item.Type == "command_execution" && item.ExitCode == 0 && strings.Contains(item.Command, name+"/SKILL.md") && strings.Contains(item.Output, name) {
			return true
		}
	}
	return false
}
