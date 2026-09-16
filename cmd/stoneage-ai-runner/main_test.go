package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

func TestParseOptionsUsesFixedEnvironmentAliases(t *testing.T) {
	environment := map[string]string{
		"STONEAGE_AI_PROFILE": "profile-env",
		"STONEAGE_AI_STATE":   "/private/state",
		"STONEAGE_AI_CODEX":   "/private/codex",
		"STONEAGE_AI_MCP":     "/private/mcp",
		"STONEAGE_AI_SKILLS":  "/private/skills",
		"STONEAGE_AI_GIT":     "/private/git",
	}
	opts, err := parseOptions(nil, func(name string) string { return environment[name] })
	if err != nil {
		t.Fatal(err)
	}
	if opts != (options{profile: "profile-env", state: "/private/state", codex: "/private/codex", mcp: "/private/mcp", skills: "/private/skills", git: "/private/git"}) {
		t.Fatalf("options=%+v", opts)
	}

	override, err := parseOptions([]string{"-profile", "profile-flag", "-state-root", "/flag/state", "-codex-binary", "/flag/codex", "-mcp", "/flag/mcp", "-skill-root", "/flag/skills"}, func(name string) string { return environment[name] })
	if err != nil {
		t.Fatal(err)
	}
	if override.profile != "profile-flag" || override.state != "/flag/state" || override.codex != "/flag/codex" || override.mcp != "/flag/mcp" || override.skills != "/flag/skills" {
		t.Fatalf("flag options=%+v", override)
	}
}

func TestRunEmitsOneStableJSONLineForInvalidRequest(t *testing.T) {
	root := t.TempDir()
	codex := writeCommandTestExecutable(t, root, "#!/bin/sh\nexit 0\n")
	mcp := writeCommandTestExecutable(t, root, "#!/bin/sh\nexit 0\n")
	args := []string{
		"-profile", "profile-1",
		"-state", filepath.Join(root, "state"),
		"-codex", codex,
		"-mcp", mcp,
		"-skills", repositorySkillRootForCommandTest(t),
	}
	var stdout, stderr bytes.Buffer
	status := run(context.Background(), args, strings.NewReader("{}{}"), &stdout, &stderr)
	if status != 1 {
		t.Fatalf("status=%d, want 1", status)
	}
	if got := stderr.String(); got != "invalid_request\n" {
		t.Fatalf("stderr=%q", got)
	}
	if strings.Count(stdout.String(), "\n") != 1 || strings.HasSuffix(stdout.String(), "\n\n") {
		t.Fatalf("stdout is not one line: %q", stdout.String())
	}
	var response airunner.Response
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error != "invalid_request" {
		t.Fatalf("response=%+v", response)
	}
}

func TestRunRejectsUnknownArgumentWithoutUsageDiagnostic(t *testing.T) {
	var stdout, stderr bytes.Buffer
	status := run(context.Background(), []string{"-not-a-fixed-option"}, strings.NewReader("{}"), &stdout, &stderr)
	if status != 2 || stdout.Len() != 0 || stderr.String() != "invalid_config\n" {
		t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
	}
}

func writeCommandTestExecutable(t *testing.T, root, content string) string {
	t.Helper()
	path := filepath.Join(root, "command-test-"+strings.ReplaceAll(filepath.Base(t.Name()), "/", "-"))
	// Tests in this package create one executable per test; append a suffix if
	// a caller invokes the helper more than once in the same test.
	for index := 0; ; index++ {
		candidate := path
		if index > 0 {
			candidate += "-" + string(rune('0'+index))
		}
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			path = candidate
			break
		}
	}
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func repositorySkillRootForCommandTest(t *testing.T) string {
	t.Helper()
	return filepath.Join(repositoryRootForCommandTest(t), "ai", "skills")
}

func repositoryRootForCommandTest(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
