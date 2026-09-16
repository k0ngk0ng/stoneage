package aicodex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func isolatedRuntimeTestRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	base := filepath.Join(filepath.Dir(filename), "..", "..", "build", "ai")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "aicodex-path-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func writeIsolatedRuntimeTestBinary(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "fake-codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func changeRuntimeTestDir(t *testing.T, path string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func writeFakeCodex(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-codex")
	script := `#!/bin/sh
set -eu
if [ -n "${AICD_ARGS_LOG:-}" ]; then
  : > "$AICD_ARGS_LOG"
  for arg in "$@"; do printf '%s\n' "$arg" >> "$AICD_ARGS_LOG"; done
fi
if [ -n "${AICD_PROMPT_LOG:-}" ]; then cat > "$AICD_PROMPT_LOG"; fi
if [ -n "${AICD_ENV_LOG:-}" ]; then
  : > "$AICD_ENV_LOG"
  printf 'HOME=%s\n' "$HOME" >> "$AICD_ENV_LOG"
  printf 'TMPDIR=%s\n' "$TMPDIR" >> "$AICD_ENV_LOG"
  env | grep -E 'OPENAI_API_KEY|DEEPSEEK_API_KEY|CODEX_API_KEY' >> "$AICD_ENV_LOG" || true
fi
case "${AICD_HELPER_MODE:-normal}" in
normal)
  printf '%s\n' '{"type":"thread.started","thread_id":"thread-test-1"}'
  printf '%s\n' '{"type":"turn.started","turn_id":"turn-test-1"}'
  printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"STONEAGE_AGENT_OK"}}'
  printf '%s\n' '{"type":"turn.completed","turn_id":"turn-test-1","usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}'
  ;;
secret)
  printf '%s\n' "api_key=$CODEX_API_KEY" >&2
  printf '%s\n' "{\"type\":\"thread.started\",\"thread_id\":\"thread-test-secret\"}"
  printf '%s\n' "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"$CODEX_API_KEY\"}}"
  printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
  ;;
no-turn)
  printf '%s\n' '{"type":"thread.started","thread_id":"thread-test-no-turn"}'
  ;;
failed)
  printf '%s\n' '{"type":"thread.started","thread_id":"thread-test-failed"}'
  printf '%s\n' '{"type":"turn.completed","usage":{"total_tokens":1}}'
  exit 17
  ;;
malformed)
  printf '%s\n' '{"type":"thread.started","thread_id":"thread-test-malformed"}'
  printf '%s\n' 'this is not json'
  ;;
sleep)
  trap '' TERM
  sleep 30
  ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestRunner(t *testing.T, binary string, root string, timeouts ...time.Duration) *Runner {
	t.Helper()
	work := filepath.Join(root, "work")
	state := filepath.Join(root, "state")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	var timeout time.Duration
	if len(timeouts) > 0 {
		timeout = timeouts[0]
	}
	runner, err := New(Config{
		Binary: binary, CWD: work, StateRoot: state,
		TurnTimeout: timeout,
		Environment: testHelperEnvironment(),
		SecretProvider: SecretProviderFunc(func(context.Context, string) (string, error) {
			return "test-secret-value", nil
		}),
		Limits:           Limits{MaxEventBytes: 4096, MaxEvents: 32, MaxStdoutBytes: 1 << 20},
		TerminationGrace: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func testHelperEnvironment() map[string]string {
	result := make(map[string]string)
	for _, name := range []string{"AICD_ARGS_LOG", "AICD_PROMPT_LOG", "AICD_ENV_LOG", "AICD_HELPER_MODE"} {
		if value, ok := os.LookupEnv(name); ok {
			result[name] = value
		}
	}
	return result
}

func TestRunnerParsesEventsAndPersistsOnlyVerifiedCompletion(t *testing.T) {
	binary := writeFakeCodex(t)
	root := t.TempDir()
	argsLog := filepath.Join(root, "args")
	promptLog := filepath.Join(root, "prompt")
	if err := os.Setenv("AICD_ARGS_LOG", argsLog); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("AICD_PROMPT_LOG", promptLog); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("AICD_ARGS_LOG")
	defer os.Unsetenv("AICD_PROMPT_LOG")
	runner := newTestRunner(t, binary, root)
	result, err := runner.Run(context.Background(), RunRequest{ProfileID: "agent-1", Prompt: "observe game state"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Process.Status != ProcessExited || result.Process.ExitCode != 0 {
		t.Fatalf("process status = %+v", result.Process)
	}
	if result.Turn.Status != TurnCompleted || result.ThreadID != "thread-test-1" {
		t.Fatalf("turn/thread = %+v %q", result.Turn, result.ThreadID)
	}
	if result.LastMessage != "STONEAGE_AGENT_OK" || result.Usage.TotalTokens != 18 {
		t.Fatalf("result = %+v", result)
	}
	prompt, err := os.ReadFile(promptLog)
	if err != nil || string(prompt) != "observe game state" {
		t.Fatalf("prompt = %q, err=%v", prompt, err)
	}
	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	argText := string(args)
	if strings.Contains(argText, "--last") || !strings.HasSuffix(argText, "-\n") {
		t.Fatalf("unsafe or non-stdin args: %q", argText)
	}
	cp, err := runner.LoadCheckpoint("agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if cp.State != CheckpointCompleted || cp.ThreadID != result.ThreadID || !cp.TurnCompleted {
		t.Fatalf("checkpoint = %+v", cp)
	}
	info, err := os.Stat(filepath.Join(root, "state", "agent-1", "thread.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("checkpoint mode = %v, err=%v", info.Mode().Perm(), err)
	}
}

func TestRunnerResumesExactCheckpointThread(t *testing.T) {
	binary := writeFakeCodex(t)
	root := t.TempDir()
	argsLog := filepath.Join(root, "args")
	if err := os.Setenv("AICD_ARGS_LOG", argsLog); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("AICD_ARGS_LOG")
	runner := newTestRunner(t, binary, root)
	if _, err := runner.NewTurn(context.Background(), "agent-resume", "first"); err != nil {
		t.Fatal(err)
	}
	result, err := runner.ResumeTurn(context.Background(), "agent-resume", "second")
	if err != nil {
		t.Fatal(err)
	}
	if result.ThreadID != "thread-test-1" {
		t.Fatalf("thread changed: %q", result.ThreadID)
	}
	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	text := string(args)
	if !strings.Contains(text, "resume\n") || !strings.Contains(text, "thread-test-1\n") || strings.Contains(text, "--last") {
		t.Fatalf("resume args = %q", text)
	}
	if _, err := runner.Run(context.Background(), RunRequest{ProfileID: "agent-resume", Resume: true, ThreadID: "wrong", Prompt: "third"}); !errors.Is(err, ErrThreadMismatch) {
		t.Fatalf("wrong thread err = %v", err)
	}
}

func TestRunnerDoesNotTreatExitOrTurnAloneAsSuccess(t *testing.T) {
	for _, mode := range []struct {
		name string
		err  error
	}{
		{name: "no-turn", err: ErrTurnIncomplete},
		{name: "failed", err: ErrProcessExit},
		{name: "malformed", err: ErrEventMalformed},
	} {
		t.Run(mode.name, func(t *testing.T) {
			binary := writeFakeCodex(t)
			root := t.TempDir()
			if err := os.Setenv("AICD_HELPER_MODE", mode.name); err != nil {
				t.Fatal(err)
			}
			defer os.Unsetenv("AICD_HELPER_MODE")
			runner := newTestRunner(t, binary, root)
			result, err := runner.NewTurn(context.Background(), "agent-failure", "prompt")
			if !errors.Is(err, mode.err) {
				t.Fatalf("err = %v, want %v", err, mode.err)
			}
			if result.Checkpoint.State == CheckpointCompleted {
				t.Fatalf("unverified result marked complete: %+v", result.Checkpoint)
			}
		})
	}
}

func TestRunnerRedactsCredentialAndUsesPrivateCodexHome(t *testing.T) {
	binary := writeFakeCodex(t)
	root := t.TempDir()
	if err := os.Setenv("AICD_HELPER_MODE", "secret"); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("AICD_HELPER_MODE")
	runner, err := New(Config{
		Binary: binary, CWD: filepath.Join(root, "work"), StateRoot: filepath.Join(root, "state"),
		CodexHomeRoot: filepath.Join(root, "homes"),
		SecretProvider: SecretProviderFunc(func(context.Context, string) (string, error) {
			return "test-secret-value", nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.NewTurn(context.Background(), "agent-secret", "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Stderr, "test-secret-value") || strings.Contains(result.LastMessage, "test-secret-value") {
		t.Fatalf("credential leaked: stderr=%q message=%q", result.Stderr, result.LastMessage)
	}
	for _, event := range result.Events {
		if strings.Contains(string(event.Raw), "test-secret-value") {
			t.Fatal("credential leaked in event")
		}
	}
	home := filepath.Join(root, "homes", "agent-secret")
	if _, err := os.Stat(home); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerIsolatesHomeTempAndRepositoryRoot(t *testing.T) {
	binary := writeFakeCodex(t)
	root := t.TempDir()
	envLog := filepath.Join(root, "env")
	if err := os.Setenv("AICD_ENV_LOG", envLog); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("HOME", filepath.Join(root, "operator-home")); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("TMPDIR", filepath.Join(root, "operator-tmp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("OPENAI_API_KEY", "inherited-secret"); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("AICD_ENV_LOG")
	defer os.Unsetenv("HOME")
	defer os.Unsetenv("TMPDIR")
	defer os.Unsetenv("OPENAI_API_KEY")
	runner := newTestRunner(t, binary, root)
	if _, err := runner.NewTurn(context.Background(), "agent-isolated", "prompt"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "operator-home") || strings.Contains(text, "operator-tmp") || strings.Contains(text, "inherited-secret") || strings.Contains(text, "OPENAI_API_KEY") {
		t.Fatalf("operator environment leaked: %q", text)
	}
	if !strings.Contains(text, filepath.Join(root, "state", "agent-isolated", "process-home")) || !strings.Contains(text, filepath.Join(root, "state", "agent-isolated", "tmp")) {
		t.Fatalf("isolated directories missing: %q", text)
	}
	if info, err := os.Stat(filepath.Join(root, "work", ".git")); err != nil || !info.IsDir() {
		t.Fatalf("workspace has no independent git root: %v", err)
	}
}

func TestRunnerCancellationTerminatesProcessGroup(t *testing.T) {
	binary := writeFakeCodex(t)
	root := t.TempDir()
	if err := os.Setenv("AICD_HELPER_MODE", "sleep"); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("AICD_HELPER_MODE")
	runner := newTestRunner(t, binary, root)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	result, err := runner.NewTurn(ctx, "agent-cancel", "prompt")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel err = %v, result=%+v", err, result)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("cancellation did not terminate the process promptly")
	}
	if result.Process.Status == ProcessExited && result.Process.ExitCode == 0 && result.Turn.Status == TurnCompleted {
		t.Fatal("canceled process reported a completed turn")
	}
}

func TestRunnerUsesGeneratedConfigWithoutIgnoringIt(t *testing.T) {
	binary := writeFakeCodex(t)
	root := t.TempDir()
	configPath := filepath.Join(root, "source-config.toml")
	if err := os.WriteFile(configPath, []byte("model = \"deepseek-flash\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	argsLog := filepath.Join(root, "args")
	if err := os.Setenv("AICD_ARGS_LOG", argsLog); err != nil {
		t.Fatal(err)
	}
	defer os.Unsetenv("AICD_ARGS_LOG")
	runner, err := New(Config{
		Binary: binary, CWD: filepath.Join(root, "work"), StateRoot: filepath.Join(root, "state"),
		CodexHomeRoot: filepath.Join(root, "homes"), ConfigFile: configPath,
		Environment: testHelperEnvironment(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.NewTurn(context.Background(), "agent-config", "prompt"); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), "--ignore-user-config") {
		t.Fatal("generated config was explicitly ignored")
	}
	installed := filepath.Join(root, "homes", "agent-config", "config.toml")
	data, err := os.ReadFile(installed)
	if err != nil || string(data) != "model = \"deepseek-flash\"\n" {
		t.Fatalf("installed config = %q, err=%v", data, err)
	}
	info, err := os.Stat(installed)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("installed config mode = %v, err=%v", info.Mode().Perm(), err)
	}
}

func TestConfigRejectsOperatorCodexAndProjectConfigPaths(t *testing.T) {
	root := isolatedRuntimeTestRoot(t)
	binary := writeIsolatedRuntimeTestBinary(t, root)
	operatorHome := filepath.Join(root, "operator-home")
	operatorCodex := filepath.Join(operatorHome, ".codex")
	if err := os.MkdirAll(operatorCodex, 0700); err != nil {
		t.Fatal(err)
	}
	operatorAlias := filepath.Join(root, "operator-codex-alias")
	if err := os.Symlink(operatorCodex, operatorAlias); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	projectCodexTarget := filepath.Join(root, "project-codex")
	if err := os.MkdirAll(projectCodexTarget, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(projectCodexTarget, filepath.Join(project, ".codex")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", operatorHome)
	t.Setenv("CODEX_HOME", operatorCodex)
	changeRuntimeTestDir(t, project)

	safe := filepath.Join(root, "runtime")
	base := Config{Binary: binary, CWD: filepath.Join(safe, "work"), StateRoot: filepath.Join(safe, "state")}
	cases := map[string]func(*Config){
		"state-root":      func(config *Config) { config.StateRoot = operatorCodex },
		"workspace-root":  func(config *Config) { config.WorkspaceRoot = filepath.Join(operatorAlias, "workspace") },
		"codex-home-root": func(config *Config) { config.CodexHomeRoot = filepath.Join(operatorAlias, "homes") },
		"codex-home": func(config *Config) {
			config.CodexHomeRoot = ""
			config.CodexHome = filepath.Join(operatorAlias, "agent")
		},
		"cwd":            func(config *Config) { config.CWD = filepath.Join(project, ".codex") },
		"config-source":  func(config *Config) { config.ConfigFile = filepath.Join(operatorAlias, "config.toml") },
		"catalog-source": func(config *Config) { config.ModelCatalog = filepath.Join(project, ".codex", "models.json") },
		"project-target": func(config *Config) { config.CWD = projectCodexTarget },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			config := base
			mutate(&config)
			if _, err := New(config); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New error = %v, want isolated path rejection", err)
			}
		})
	}
}

func TestConfigAllowsIndependentBuildAIRuntimePaths(t *testing.T) {
	root := isolatedRuntimeTestRoot(t)
	binary := writeIsolatedRuntimeTestBinary(t, root)
	operatorHome := filepath.Join(root, "operator-home")
	if err := os.MkdirAll(filepath.Join(operatorHome, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", operatorHome)
	t.Setenv("CODEX_HOME", filepath.Join(operatorHome, ".codex"))
	changeRuntimeTestDir(t, project)
	runner, err := New(Config{
		Binary: binary, CWD: filepath.Join(root, "runtime", "work"), StateRoot: filepath.Join(root, "runtime", "state"),
		CodexHomeRoot: filepath.Join(root, "runtime", "codex"), ConfigFile: filepath.Join(root, "runtime", "source-config.toml"),
	})
	if err != nil {
		t.Fatalf("independent runtime rejected: %v", err)
	}
	if got := runner.Config().CodexHomeRoot; got != filepath.Join(root, "runtime", "codex") {
		t.Fatalf("normalized CodexHomeRoot = %q", got)
	}
}

func TestBuildEnvironmentPinsAllProcessIsolationVariables(t *testing.T) {
	root := isolatedRuntimeTestRoot(t)
	operator := filepath.Join(root, "operator")
	for _, name := range []string{"HOME", "USERPROFILE", "CODEX_HOME", "TMPDIR", "TMP", "TEMP", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(name, filepath.Join(operator, name))
	}
	t.Setenv("OPENAI_API_KEY", "inherited-secret")
	isolatedHome := filepath.Join(root, "isolated-home")
	isolatedTemp := filepath.Join(root, "isolated-temp")
	environment := buildEnvironment(filepath.Join(root, "codex-home"), isolatedHome, isolatedTemp, "CODEX_API_KEY", "fixture-secret", map[string]string{
		"HOME":            filepath.Join(operator, "extra-home"),
		"CODEX_HOME":      filepath.Join(operator, "extra-codex"),
		"XDG_DATA_HOME":   filepath.Join(operator, "extra-data"),
		"OPENAI_API_KEY":  "extra-secret",
		"STONEAGE_MARKER": "kept",
	})
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			values[name] = value
		}
	}
	want := map[string]string{
		"CODEX_HOME":      filepath.Join(root, "codex-home"),
		"HOME":            isolatedHome,
		"USERPROFILE":     isolatedHome,
		"TMPDIR":          isolatedTemp,
		"TMP":             isolatedTemp,
		"TEMP":            isolatedTemp,
		"XDG_CACHE_HOME":  filepath.Join(isolatedHome, "cache"),
		"XDG_CONFIG_HOME": filepath.Join(isolatedHome, "xdg-config"),
		"XDG_DATA_HOME":   filepath.Join(isolatedHome, "xdg-data"),
		"XDG_STATE_HOME":  filepath.Join(isolatedHome, "xdg-state"),
		"XDG_RUNTIME_DIR": filepath.Join(isolatedHome, "xdg-runtime"),
		"CODEX_API_KEY":   "fixture-secret",
		"STONEAGE_MARKER": "kept",
	}
	for name, expected := range want {
		if values[name] != expected {
			t.Fatalf("%s = %q, want %q; env=%v", name, values[name], expected, values)
		}
	}
	for _, entry := range environment {
		if strings.Contains(entry, operator) || strings.Contains(entry, "inherited-secret") || strings.Contains(entry, "OPENAI_API_KEY") {
			t.Fatalf("operator environment leaked or overrode isolation: %q", entry)
		}
	}
}

func TestRunRejectsStateSymlinkAliasBeforeCreatingLock(t *testing.T) {
	root := isolatedRuntimeTestRoot(t)
	binary := writeIsolatedRuntimeTestBinary(t, root)
	operatorHome := filepath.Join(root, "operator-home")
	operatorCodex := filepath.Join(operatorHome, ".codex")
	if err := os.MkdirAll(operatorCodex, 0700); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(root, "runtime", "state")
	if err := os.MkdirAll(stateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(operatorCodex, filepath.Join(stateRoot, "agent")); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", operatorHome)
	t.Setenv("CODEX_HOME", operatorCodex)
	changeRuntimeTestDir(t, project)
	runner, err := New(Config{Binary: binary, CWD: filepath.Join(root, "runtime", "work"), StateRoot: stateRoot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.NewTurn(context.Background(), "agent", "fixture prompt"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Run error = %v, want state isolation rejection", err)
	}
	if _, err := os.Stat(filepath.Join(operatorCodex, ".stoneage-profile.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state lock touched operator Codex home: %v", err)
	}
}
