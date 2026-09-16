package aibroker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

type probeCleanupDocker struct {
	fakeDocker
	mu          sync.Mutex
	present     bool
	state       DockerContainerState
	inspectErr  error
	removeCalls []string
	volumeCalls []string
}

func (docker *probeCleanupDocker) Inspect(_ context.Context, containerName string) (DockerContainerState, error) {
	docker.mu.Lock()
	defer docker.mu.Unlock()
	if docker.inspectErr != nil {
		return "", docker.inspectErr
	}
	if !docker.present {
		return "", ErrContainerNotFound
	}
	return docker.state, nil
}

func (docker *probeCleanupDocker) Remove(_ context.Context, containerName string) error {
	docker.mu.Lock()
	defer docker.mu.Unlock()
	docker.removeCalls = append(docker.removeCalls, containerName)
	if !docker.present {
		return ErrContainerNotFound
	}
	if !terminalContainerState(docker.state) {
		return fmt.Errorf("%w: refusing live test container", ErrDocker)
	}
	docker.present = false
	return nil
}

func (docker *probeCleanupDocker) RemoveVolume(_ context.Context, volumeName string) error {
	docker.mu.Lock()
	defer docker.mu.Unlock()
	docker.volumeCalls = append(docker.volumeCalls, volumeName)
	if docker.present {
		return fmt.Errorf("%w: volume is still attached", ErrDocker)
	}
	return nil
}

func probeCleanupRequest(profileID, requestID string) airunner.ExecuteRequest {
	return airunner.ExecuteRequest{
		ProfileID: profileID,
		RequestID: requestID,
		Probe:     true,
		Run:       airunner.RunRequest{Prompt: "Reply with exactly STONEAGE_CONNECTION_TEST_OK. Do not use tools."},
		Model:     airunner.Model{Provider: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-flash", APIKey: "probe-cleanup-key"},
	}
}

func TestBrokerCleanupProbeRemovesUnknownContainerThenVolume(t *testing.T) {
	docker := &probeCleanupDocker{present: true, state: DockerContainerExited}
	docker.fakeDocker.run = func(context.Context, RunSpec, []byte) (DockerResult, error) {
		return DockerResult{ExitCode: 1}, errors.New("provider process failed")
	}
	broker := newFakeBroker(t, docker, NewMemoryJournal())
	request := probeCleanupRequest("probe-cleanup", "request-1")
	if _, err := broker.Run(context.Background(), request); !errors.Is(err, ErrRunUnknown) {
		t.Fatalf("Run error=%v, want unknown", err)
	}
	if err := broker.CleanupProbe(context.Background(), request); err != nil {
		t.Fatalf("CleanupProbe error=%v", err)
	}
	docker.mu.Lock()
	defer docker.mu.Unlock()
	if len(docker.removeCalls) != 1 || len(docker.volumeCalls) != 1 {
		t.Fatalf("cleanup calls container=%v volume=%v", docker.removeCalls, docker.volumeCalls)
	}
}

func TestBrokerCleanupProbeLeavesUncertainContainerAndVolume(t *testing.T) {
	docker := &probeCleanupDocker{present: true, state: DockerContainerRunning}
	docker.fakeDocker.run = func(context.Context, RunSpec, []byte) (DockerResult, error) {
		return DockerResult{ExitCode: 1}, errors.New("provider process failed")
	}
	broker := newFakeBroker(t, docker, NewMemoryJournal())
	request := probeCleanupRequest("probe-running", "request-1")
	if _, err := broker.Run(context.Background(), request); !errors.Is(err, ErrRunUnknown) {
		t.Fatalf("Run error=%v, want unknown", err)
	}
	if err := broker.CleanupProbe(context.Background(), request); !errors.Is(err, ErrRunRunning) {
		t.Fatalf("CleanupProbe error=%v, want running fence", err)
	}
	docker.mu.Lock()
	defer docker.mu.Unlock()
	if len(docker.removeCalls) != 0 || len(docker.volumeCalls) != 0 {
		t.Fatalf("uncertain cleanup removed resources: container=%v volume=%v", docker.removeCalls, docker.volumeCalls)
	}
}

func TestBrokerCleanupProbeRequiresProbeAndExactPayload(t *testing.T) {
	docker := &probeCleanupDocker{present: true, state: DockerContainerExited}
	broker := newFakeBroker(t, docker, NewMemoryJournal())
	request := probeCleanupRequest("probe-hash", "request-1")
	if _, err := broker.Run(context.Background(), request); err != nil {
		t.Fatalf("Run error=%v", err)
	}
	changed := request
	changed.Model.APIKey = "different-key"
	if err := broker.CleanupProbe(context.Background(), changed); !errors.Is(err, ErrJournalConflict) {
		t.Fatalf("payload mismatch error=%v, want journal conflict", err)
	}
	game := request
	game.Probe = false
	game.MCP = airunner.MCP{Endpoint: "http://gateway:9080/v1/game", Token: "invalid", CharacterID: "character-1"}
	if err := broker.CleanupProbe(context.Background(), game); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("game cleanup error=%v, want invalid request", err)
	}
	docker.mu.Lock()
	defer docker.mu.Unlock()
	if len(docker.volumeCalls) != 0 {
		t.Fatalf("mismatched/game cleanup removed volume: %v", docker.volumeCalls)
	}
}
