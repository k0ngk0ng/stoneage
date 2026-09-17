package main

import (
	"context"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/airemote"
)

func TestLocalCommandUsesPublishedIsolatedWorker(t *testing.T) {
	hub, err := airemote.New(airemote.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	w := &aiRuntimeWiring{remote: hub, runtimeImage: "ghcr.io/example/ai-runtime@sha256:" + strings.Repeat("a", 64), webPublicURL: "https://web.example"}
	result, err := w.CreateCommand(context.Background(), "player-1", "https://admin.internal.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"docker run --rm -d", "--read-only", "--cap-drop ALL", "--entrypoint /usr/local/bin/stoneage-ai-worker", "--profile player-1", "--endpoint https://web.example/api/ai/worker", "--start", w.runtimeImage} {
		if !strings.Contains(result.Command, part) {
			t.Fatalf("command missing %q", part)
		}
	}
	for _, forbidden := range []string{"ssh", "/var/run/docker.sock", ".codex/config.toml", "type=bind", "--privileged"} {
		if strings.Contains(result.Command, forbidden) {
			t.Fatalf("unsafe local execution requirement %q", forbidden)
		}
	}
	status, err := w.ExecutorStatus(context.Background(), "player-1")
	if err != nil || status.Location != "server" {
		t.Fatalf("copy changed executor: %+v %v", status, err)
	}
}

func TestLocalCommandQuotesShellMetacharacters(t *testing.T) {
	tests := map[string]string{
		"docker":                    "docker",
		"--profile":                 "--profile",
		"https://game.example/path": "https://game.example/path",
		"":                          "''",
		"a'$(echo no)":              "'a'\"'\"'$(echo no)'",
		"--name; echo PWNED":        "'--name; echo PWNED'",
		"line1\nline2":              "'line1\nline2'",
		`$(touch /tmp/pwned)`:       "'$(touch /tmp/pwned)'",
		`a\\b\"c`:                   `'a\\b\"c'`,
	}
	for input, want := range tests {
		if got := quoteLocalCommandArg(input); got != want {
			t.Errorf("quoteLocalCommandArg(%q) = %q, want %q", input, got, want)
		}
	}
}
