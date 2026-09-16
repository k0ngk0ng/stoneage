package aiservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

type factoryContainerDocker struct {
	mu      sync.Mutex
	request airunner.ExecuteRequest
	calls   int
}

func (docker *factoryContainerDocker) Run(_ context.Context, _ aibroker.RunSpec, payload []byte) (aibroker.DockerResult, error) {
	request, err := airunner.DecodeRequest(bytes.NewReader(payload))
	if err != nil {
		return aibroker.DockerResult{}, err
	}
	docker.mu.Lock()
	docker.request, docker.calls = request, docker.calls+1
	docker.mu.Unlock()
	result := airunner.Result{
		ProfileID: request.ProfileID, ThreadID: "container-thread", LastMessage: "observed",
		Process: airunner.Process{Status: "exited", ExitCode: 0},
		Turn:    airunner.Turn{Status: "completed"}, Usage: airunner.Usage{InputTokens: 7, OutputTokens: 3},
		Checkpoint: airunner.Checkpoint{Version: 1, ProfileID: request.ProfileID, ThreadID: "container-thread",
			State: "completed", TurnStatus: "completed", TurnCompleted: true},
	}
	raw, err := json.Marshal(airunner.Response{OK: true, ProfileID: request.ProfileID, RequestID: request.RequestID, Result: &result})
	return aibroker.DockerResult{Stdout: raw}, err
}

func (*factoryContainerDocker) Stop(context.Context, string) error { return nil }

func TestFactoryContainerModeNeedsNoHostCodexAndKeepsCredentialsOutOfTransportState(t *testing.T) {
	root := factoryTestRoot(t)
	profile := factoryTestProfile("container-profile", true)
	docker := &factoryContainerDocker{}
	broker, err := aibroker.New(aibroker.Config{Docker: docker, Journal: aibroker.NewMemoryJournal(),
		Image: "stoneage-ai-runtime:test", Network: "stoneage-ai-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	backend := &factoryTestBackend{observation: aimcp.Observation{Connected: true, Ready: true,
		CharacterID: profile.Character.ID, CharacterName: profile.Character.Name}}
	config := FactoryConfig{
		Models:  factoryTestModels{model: factoryTestModel(), defaultID: "model-1"},
		Secrets: factoryTestSecrets(t, root), Gateway: NewGateway(),
		GatewayEndpoint: "http://game-control:8081/v1/game", RuntimeRoot: root,
		SkillRoot: filepath.Join(factoryRepoRoot(), "ai", "skills"), ContainerBroker: broker,
		Sessions: SessionProviderFunc(func(context.Context, airuntime.Profile) (SessionLease, error) {
			return SessionLease{Backend: backend, Close: func() {}}, nil
		}),
	}
	factory, err := NewFactory(config)
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	session, err := factory.Open(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.Runner.Run(context.Background(), aicodex.RunRequest{ProfileID: profile.ID, Prompt: "observe"})
	if err != nil || result.ThreadID != "container-thread" || result.Usage.InputTokens != 7 {
		t.Fatalf("container runner result missing authoritative completion or usage: %v", err)
	}
	docker.mu.Lock()
	request, calls := docker.request, docker.calls
	docker.mu.Unlock()
	if calls != 1 || request.Model.APIKey != "test-deepseek-key" || request.MCP.Token == "" || request.MCP.CharacterID != profile.Character.ID || request.MCP.Generation == 0 {
		t.Fatal("container did not receive its server-owned model and game binding")
	}
	if len(request.Skills) != 1 || request.Skills[0].Name != "stoneage-play" || request.Skills[0].Digest != profile.Skills[0].Digest {
		t.Fatal("container did not receive the canonical native skill selection")
	}
	if response := call(config.Gateway, request.MCP.Token, `{"operation":"observe","arguments":{}}`); response.Code != 200 {
		t.Fatalf("container game capability rejected: %d", response.Code)
	}
	for _, name := range []string{"codex", "workspaces"} {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("container mode created a host %s tree", name)
		}
	}
	if err := filepath.WalkDir(filepath.Join(root, "state"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(request.Model.APIKey)) || bytes.Contains(data, []byte(request.MCP.Token)) {
			t.Error("transport state contains a provider or game credential")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	session.Close()
	if response := call(config.Gateway, request.MCP.Token, `{"operation":"observe","arguments":{}}`); response.Code != 401 {
		t.Fatalf("closed container session retained its capability: %d", response.Code)
	}
}
