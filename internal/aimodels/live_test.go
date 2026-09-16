package aimodels

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in integration check is never part of an ordinary test run. It
// makes one small, real Codex request against the selected DeepSeek provider.
// All Codex/git state is isolated below the test's workspace-local TMPDIR.
func TestLiveDeepSeekCodex(t *testing.T) {
	if os.Getenv("STONEAGE_DEEPSEEK_LIVE_TEST") != "1" {
		t.Skip("explicit live provider check not requested")
	}
	binary := os.Getenv("STONEAGE_CODEX_BINARY")
	keyFile := os.Getenv("STONEAGE_DEEPSEEK_KEY_FILE")
	if !filepath.IsAbs(binary) || !filepath.IsAbs(keyFile) {
		t.Fatal("absolute Codex executable and key file paths are required")
	}
	keyBytes, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal("cannot read configured DeepSeek key file")
	}
	key := strings.TrimSpace(string(keyBytes))
	keyBytes = nil
	root := t.TempDir()
	home := filepath.Join(root, "home")
	codexHome := filepath.Join(root, "codex")
	workspace := filepath.Join(root, "workspace")
	tmp := filepath.Join(root, "tmp")
	for _, dir := range []string{home, workspace, tmp} {
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	files, err := Materialize(codexHome, RuntimeSettings{APIKey: key})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + codexHome, "TMPDIR=" + tmp, "LANG=en_US.UTF-8"}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	init := exec.CommandContext(ctx, "git", "-c", "init.templateDir=", "init", "--quiet", workspace)
	init.Env = env
	if err = init.Run(); err != nil {
		t.Fatal("cannot initialize isolated agent workspace")
	}
	version := exec.CommandContext(ctx, binary, "--version")
	version.Env = env
	version.Dir = workspace
	out, err := version.Output()
	if err != nil {
		t.Fatal("cannot query Codex version")
	}
	if err = CheckCodexVersion(string(out)); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "exec", "--json", "--skip-git-repo-check", "--color", "never", "-")
	cmd.Env = env
	cmd.Dir = workspace
	cmd.Stdin = strings.NewReader("This is a model connectivity check, not a coding task. Do not use tools, inspect files, or run commands. Reply with exactly STONEAGE_DEEPSEEK_CONNECTED and nothing else.")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	// Never put raw CLI output in test failures: upstream errors can contain
	// headers or configuration. Persist only a sanitized summary of the run.
	if bytes.Contains(stdout.Bytes(), []byte(key)) || bytes.Contains(stderr.Bytes(), []byte(key)) {
		t.Fatal("CLI output contained credential material")
	}
	if err != nil {
		t.Fatalf("Codex provider check failed (output suppressed): %T; timeout=%v", err, ctx.Err() != nil)
	}
	completed, answer, started := false, false, false
	var usage map[string]any
	for _, line := range bytes.Split(stdout.Bytes(), []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event map[string]json.RawMessage
		if json.Unmarshal(line, &event) != nil {
			t.Fatal("CLI emitted invalid JSON event")
		}
		var kind string
		_ = json.Unmarshal(event["type"], &kind)
		switch kind {
		case "thread.started":
			started = true
		case "turn.failed", "error":
			t.Fatal("CLI reported a failed turn (details suppressed)")
		case "turn.completed":
			completed = true
			_ = json.Unmarshal(event["usage"], &usage)
		case "item.completed":
			var item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			_ = json.Unmarshal(event["item"], &item)
			if item.Type == "agent_message" && strings.TrimSpace(item.Text) == "STONEAGE_DEEPSEEK_CONNECTED" {
				answer = true
			}
		}
	}
	if !started || !completed || !answer {
		t.Fatalf("provider evidence incomplete: thread=%v completed=%v expected_answer=%v", started, completed, answer)
	}
	config, _ := os.ReadFile(files.ConfigPath)
	if !bytes.Contains(config, []byte(DeepSeekFlash)) || !bytes.Contains(config, []byte(DeepSeekBaseURL)) {
		t.Fatal("live request used an unexpected model configuration")
	}
	t.Logf("Codex %s; model=%s; provider=%s; completed=true; usage=%v", strings.TrimSpace(string(out)), DeepSeekFlash, DeepSeekProvider, usage)
}
