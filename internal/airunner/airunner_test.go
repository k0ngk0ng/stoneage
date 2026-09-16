package airunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicodex"
)

func TestDecodeRequestBoundsAndStrictJSON(t *testing.T) {
	valid := []byte(`{"profile_id":"profile-1"}`)
	if len(valid) >= MaxRequestBytes {
		t.Fatal("test JSON unexpectedly exceeds request limit")
	}
	boundary := append(append([]byte(nil), valid...), bytes.Repeat([]byte(" "), MaxRequestBytes-len(valid))...)
	if len(boundary) != MaxRequestBytes {
		t.Fatalf("boundary length=%d, want %d", len(boundary), MaxRequestBytes)
	}
	if _, err := DecodeRequest(bytes.NewReader(boundary)); err != nil {
		t.Fatalf("exact request limit rejected: %v", err)
	}
	if _, err := DecodeRequest(bytes.NewReader(append(boundary, ' '))); !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("oversized request error=%v, want ErrRequestTooLarge", err)
	}

	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "unknown top-level field", raw: `{"profile_id":"p","unexpected":true}`},
		{name: "unknown nested field", raw: `{"run_request":{"prompt":"x","unexpected":true}}`},
		{name: "duplicate top-level key", raw: `{"profile_id":"p","profile_id":"q"}`},
		{name: "duplicate nested key", raw: `{"run_request":{"prompt":"x","prompt":"y"}}`},
		{name: "trailing value", raw: `{"profile_id":"p"}{}`},
		{name: "empty", raw: "  \n"},
		{name: "resume without thread", raw: `{"run_request":{"prompt":"x","resume":true}}`},
		{name: "thread without resume", raw: `{"run_request":{"prompt":"x","thread_id":"thread-1"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeRequest(strings.NewReader(test.raw)); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("DecodeRequest error=%v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestDecodeRequestPreservesExactResumeThread(t *testing.T) {
	request, err := DecodeRequest(strings.NewReader(`{"profile_id":"p","run_request":{"prompt":"resume","resume":true,"thread_id":"thread-exact-1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !request.Run.Resume || request.Run.ThreadID != "thread-exact-1" {
		t.Fatalf("decoded request=%+v", request.Run)
	}
}

func TestRedactResultRemovesCredentialsFromEveryOutputField(t *testing.T) {
	apiKey := "api-key-for-test"
	token := strings.Repeat("t", 43)
	result := redactResult(aicodex.Result{
		ProfileID:   "profile-1",
		ThreadID:    "thread-1",
		LastMessage: apiKey + " " + token,
		Stderr:      "token=" + token,
		Turn:        aicodex.TurnResult{Error: apiKey},
		Process:     aicodex.ProcessResult{Signal: token},
		Events: []aicodex.Event{{
			Type:  "item.completed",
			Error: apiKey,
			Item:  json.RawMessage(fmt.Sprintf(`{"text":%q}`, apiKey)),
			Raw:   json.RawMessage(fmt.Sprintf(`{"text":%q}`, token)),
		}},
	}, apiKey, token)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(apiKey)) || bytes.Contains(encoded, []byte(token)) {
		t.Fatalf("result contains a credential: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte("redacted")) {
		t.Fatalf("result did not contain redaction marker: %s", encoded)
	}
}

func TestExecutorCreatesPrivateProfileAndResumesExactThread(t *testing.T) {
	root := t.TempDir()
	codex := writeRunnerTestExecutable(t, root, `#!/bin/sh
set -eu
if [ -n "${AIRUNNER_ARGS_LOG:-}" ]; then
  : > "$AIRUNNER_ARGS_LOG"
  for arg in "$@"; do printf '%s\n' "$arg" >> "$AIRUNNER_ARGS_LOG"; done
fi
printf '%s\n' '{"type":"thread.started","thread_id":"thread-airunner-1"}'
printf '%s\n' '{"type":"turn.started","turn_id":"turn-airunner-1"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"AIRUNNER_OK"}}'
printf '%s\n' '{"type":"turn.completed","turn_id":"turn-airunner-1","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}'
`)
	mcp := writeRunnerTestExecutable(t, root, "#!/bin/sh\nexit 0\n")
	argsLog := filepath.Join(root, "codex.args")
	stateRoot := filepath.Join(root, "state")
	skillRoot := repositorySkillRoot(t)
	executor, err := New(Config{
		ProfileID:        "profile-1",
		StateRoot:        stateRoot,
		CodexBinary:      codex,
		MCPBinary:        mcp,
		SkillRoot:        skillRoot,
		Environment:      map[string]string{"AIRUNNER_ARGS_LOG": argsLog},
		TerminationGrace: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := ExecuteRequest{
		ProfileID: "profile-1", RequestID: "req-1",
		Run:    RunRequest{Prompt: "observe"},
		Model:  Model{Provider: "deepseek", BaseURL: "https://api.deepseek.com/", Model: "deepseek-flash", ReasoningEffort: "high", ContextWindow: 1048576, APIKey: "private-api-key"},
		Skills: []Skill{{Name: "stoneage-play", Version: "1.2.0"}},
		MCP:    MCP{Endpoint: "http://127.0.0.1:1/v1/game", Token: strings.Repeat("c", 43), CharacterID: "character-1", Generation: 1},
	}
	response, err := executor.Execute(context.Background(), request)
	if err != nil || !response.OK || response.Result == nil {
		t.Fatalf("first execution response=%+v err=%v", response, err)
	}
	if response.RequestID != "req-1" || response.Result.LastMessage != "AIRUNNER_OK" || response.Result.ThreadID != "thread-airunner-1" {
		t.Fatalf("first response=%+v", response)
	}
	if response.Result.Stderr != "" || bytes.Contains(mustJSON(t, response), []byte("private-api-key")) || bytes.Contains(mustJSON(t, response), []byte(request.MCP.Token)) {
		t.Fatalf("credential leaked in response: %+v", response)
	}
	assertPrivateFile(t, filepath.Join(stateRoot, ownerFileName), 0600)
	assertPrivateFile(t, filepath.Join(stateRoot, "state", "profile-1", tokenFileName), 0600)
	token, err := os.ReadFile(filepath.Join(stateRoot, "state", "profile-1", tokenFileName))
	if err != nil || strings.TrimSpace(string(token)) != request.MCP.Token {
		t.Fatalf("stored MCP token=%q err=%v", token, err)
	}
	if _, err := os.Stat(filepath.Join(stateRoot, "workspaces", "profile-1", ".agents", "skills", "stoneage-play", "SKILL.md")); err != nil {
		t.Fatalf("installed skill missing: %v", err)
	}

	resume := request
	resume.RequestID = "req-2"
	resume.Run = RunRequest{Prompt: "continue", Resume: true, ThreadID: "thread-airunner-1"}
	response, err = executor.Execute(context.Background(), resume)
	if err != nil || !response.OK || response.Result == nil || response.Result.ThreadID != "thread-airunner-1" {
		t.Fatalf("resume response=%+v err=%v", response, err)
	}
	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	argText := string(args)
	if !strings.Contains(argText, "exec\nresume\n") || !strings.Contains(argText, "thread-airunner-1\n") || strings.Contains(argText, "--last") {
		t.Fatalf("resume args=%q", argText)
	}
}

func TestExecutorRejectsOwnerAndProfileMismatch(t *testing.T) {
	root := t.TempDir()
	codex := writeRunnerTestExecutable(t, root, `#!/bin/sh
printf '%s\n' '{"type":"thread.started","thread_id":"thread-owner-1"}'
printf '%s\n' '{"type":"turn.completed"}'
`)
	mcp := writeRunnerTestExecutable(t, root, "#!/bin/sh\nexit 0\n")
	executor, err := New(Config{ProfileID: "profile-1", StateRoot: filepath.Join(root, "state"), CodexBinary: codex, MCPBinary: mcp, SkillRoot: repositorySkillRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	base := ExecuteRequest{
		ProfileID: "profile-1", RequestID: "req-1", Run: RunRequest{Prompt: "x"},
		Model: Model{Provider: "deepseek", BaseURL: "https://api.deepseek.com/", Model: "deepseek-flash", APIKey: "key"},
		MCP:   MCP{Endpoint: "http://127.0.0.1:1/v1/game", Token: strings.Repeat("d", 43), CharacterID: "character-1", Generation: 2},
	}
	if _, err := executor.Execute(context.Background(), base); err != nil {
		t.Fatalf("initial execution: %v", err)
	}
	otherCharacter := base
	otherCharacter.RequestID = "req-2"
	otherCharacter.MCP.CharacterID = "character-2"
	if _, err := executor.Execute(context.Background(), otherCharacter); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("owner mismatch error=%v", err)
	}
	otherProfile := base
	otherProfile.ProfileID = "profile-2"
	if _, err := executor.Execute(context.Background(), otherProfile); !errors.Is(err, ErrProfileMismatch) {
		t.Fatalf("profile mismatch error=%v", err)
	}
}

func TestExecutorPropagatesCancellation(t *testing.T) {
	root := t.TempDir()
	codex := writeRunnerTestExecutable(t, root, `#!/bin/sh
trap '' TERM
while :; do sleep 1; done
`)
	mcp := writeRunnerTestExecutable(t, root, "#!/bin/sh\nexit 0\n")
	executor, err := New(Config{
		ProfileID: "profile-cancel", StateRoot: filepath.Join(root, "state"), CodexBinary: codex, MCPBinary: mcp, SkillRoot: repositorySkillRoot(t),
		TerminationGrace: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := ExecuteRequest{
		ProfileID: "profile-cancel", RequestID: "req-cancel", Run: RunRequest{Prompt: "wait"},
		Model: Model{Provider: "deepseek", BaseURL: "https://api.deepseek.com/", Model: "deepseek-flash", APIKey: "key"},
		MCP:   MCP{Endpoint: "http://127.0.0.1:1/v1/game", Token: strings.Repeat("e", 43), CharacterID: "character-cancel", Generation: 1},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	response, err := executor.Execute(ctx, request)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error=%v response=%+v", err, response)
	}
	if response.OK || response.Error != "deadline_exceeded" {
		t.Fatalf("cancellation response=%+v", response)
	}
}

func TestValidateMCPRejectsControlCharacterIdentity(t *testing.T) {
	mcp := MCP{Endpoint: "http://127.0.0.1:1/v1/game", Token: strings.Repeat("x", 43), CharacterID: "character\n-injected", Generation: 1}
	if err := validateMCP(mcp); !errors.Is(err, ErrMCPConfig) {
		t.Fatalf("control character identity accepted: %v", err)
	}
}

func TestValidateHTTPURLAcceptsOriginOnlyBaseURL(t *testing.T) {
	for _, value := range []string{"https://api.deepseek.com", "http://127.0.0.1:8080"} {
		if err := validateHTTPURL(value); err != nil {
			t.Errorf("validateHTTPURL(%q)=%v, want nil", value, err)
		}
	}
}

func TestExecutorProbeSkipsGameCapabilityAndSkills(t *testing.T) {
	root := t.TempDir()
	argsLog := filepath.Join(root, "probe.args")
	codex := writeRunnerTestExecutable(t, root, `#!/bin/sh
set -eu
: > "$PROBE_ARGS_LOG"
for arg in "$@"; do printf '%s\n' "$arg" >> "$PROBE_ARGS_LOG"; done
if grep -q 'mcp_servers' "$CODEX_HOME/config.toml"; then exit 41; fi
printf '%s\n' '{"type":"thread.started","thread_id":"probe-thread-1"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"STONEAGE_CONNECTION_TEST_OK"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"total_tokens":1}}'
`)
	mcp := writeRunnerTestExecutable(t, root, "#!/bin/sh\nexit 99\n")
	executor, err := New(Config{
		ProfileID: "probe-profile", StateRoot: filepath.Join(root, "state"), CodexBinary: codex,
		MCPBinary: mcp, SkillRoot: repositorySkillRoot(t), Environment: map[string]string{"PROBE_ARGS_LOG": argsLog},
	})
	if err != nil {
		t.Fatal(err)
	}
	key := "probe-private-key"
	response, err := executor.Execute(context.Background(), ExecuteRequest{
		ProfileID: "probe-profile", RequestID: "probe-request", Probe: true,
		Run:   RunRequest{Prompt: "Reply with exactly STONEAGE_CONNECTION_TEST_OK. Do not use tools."},
		Model: Model{Provider: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-flash", ReasoningEffort: "high", APIKey: key},
	})
	if err != nil || !response.OK || response.Result == nil || response.Result.LastMessage != "STONEAGE_CONNECTION_TEST_OK" {
		t.Fatalf("probe response=%+v err=%v", response, err)
	}
	if _, err := os.Stat(filepath.Join(executor.profiles.stateDir, tokenFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe created game token: %v", err)
	}
	if _, err := os.Stat(executor.profiles.owner); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe created game owner marker: %v", err)
	}
	encoded, err := json.Marshal(response)
	if err != nil || bytes.Contains(encoded, []byte(key)) {
		t.Fatalf("probe response leaked model key: %s", encoded)
	}
}

func TestExecutorProbeRejectsGameCapability(t *testing.T) {
	root := t.TempDir()
	codex := writeRunnerTestExecutable(t, root, "#!/bin/sh\nexit 1\n")
	mcp := writeRunnerTestExecutable(t, root, "#!/bin/sh\nexit 1\n")
	executor, err := New(Config{ProfileID: "probe-profile", StateRoot: filepath.Join(root, "state"), CodexBinary: codex, MCPBinary: mcp, SkillRoot: repositorySkillRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	request := ExecuteRequest{ProfileID: "probe-profile", RequestID: "probe-request", Probe: true,
		Run: RunRequest{Prompt: "probe"}, Model: Model{Provider: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-flash", APIKey: "key"},
		MCP: MCP{Endpoint: "http://127.0.0.1:1/v1/game", Token: strings.Repeat("x", 43), CharacterID: "game-character", Generation: 1}}
	if _, err := executor.Execute(context.Background(), request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("probe accepted game capability: %v", err)
	}
}

func writeRunnerTestExecutable(t *testing.T, root, content string) string {
	t.Helper()
	path := filepath.Join(root, fmt.Sprintf("runner-test-%d", time.Now().UnixNano()))
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func repositorySkillRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(filename), "..", "..", "ai", "skills")
}

func assertPrivateFile(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		t.Fatalf("file %s mode/type=%v, want regular %o", path, info.Mode(), mode)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
