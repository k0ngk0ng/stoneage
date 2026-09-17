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
	w := &aiRuntimeWiring{remote: hub, runtimeImage: "ghcr.io/example/ai-runtime@sha256:" + strings.Repeat("a", 64)}
	result, err := w.CreateCommand(context.Background(), "player-1", "https://game.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"'docker' 'run'", "'--read-only'", "'--cap-drop' 'ALL'", "'--entrypoint' '/usr/local/bin/stoneage-ai-worker'", "'--profile' 'player-1'", "'--endpoint' 'https://game.example/api/ai/worker'", "'--start'", w.runtimeImage} {
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
	if got := quoteLocalCommandArg("a'$(echo no)"); got != "'a'\"'\"'$(echo no)'" {
		t.Fatalf("unexpected quoting %q", got)
	}
}
