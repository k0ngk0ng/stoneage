package aiservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/aicodex"
	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/aimodels"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

// This entry point requires a preloaded packaged runtime. It never builds or
// pulls an image, substitutes a Docker adapter, or mounts workspace binaries.
// Opting in authorizes the fresh QA character, model turn and temporary Docker
// profile volume; completed test-owned volumes are removed during cleanup.
func TestLiveFactoryBrokerCodexLeveling(t *testing.T) {
	if os.Getenv("STONEAGE_FACTORY_BROKER_LEVELING_LIVE_TEST") != "1" {
		t.Skip("explicit packaged-image/QA/provider opt-in required")
	}
	image := strings.TrimSpace(os.Getenv("STONEAGE_FACTORY_BROKER_IMAGE"))
	if image == "" {
		t.Fatal("STONEAGE_FACTORY_BROKER_IMAGE must name an already loaded ai-runtime image; no build or pull attempted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", `{"id":{{json .Id}},"entrypoint":{{json .Config.Entrypoint}}}`, image).Output()
	var actual struct {
		ID         string   `json:"id"`
		Entrypoint []string `json:"entrypoint"`
	}
	if err != nil || json.Unmarshal(raw, &actual) != nil || !strings.HasPrefix(actual.ID, "sha256:") || len(actual.Entrypoint) != 1 || actual.Entrypoint[0] != "/usr/local/bin/stoneage-ai-runner" {
		t.Fatal("preloaded packaged ai-runtime image required; no build or pull attempted")
	}
	runLiveContainerCodexLeveling(t, actual.ID)
}

// Observes the real CLI without altering its spec, input or result. Inspect
// happens before the broker removes the completed container.
type factoryBrokerLiveDocker struct {
	*aibroker.DockerCLI
	mu            sync.Mutex
	specs         []aibroker.RunSpec
	requests      []airunner.ExecuteRequest // private in-memory capability, never serialized
	inspected     int
	inspectionErr error
}

func (d *factoryBrokerLiveDocker) Run(ctx context.Context, spec aibroker.RunSpec, payload []byte) (aibroker.DockerResult, error) {
	request, err := airunner.DecodeRequest(bytes.NewReader(payload))
	if err != nil {
		return aibroker.DockerResult{}, err
	}
	d.mu.Lock()
	d.specs = append(d.specs, spec)
	d.requests = append(d.requests, request)
	d.mu.Unlock()
	result, runErr := d.DockerCLI.Run(ctx, spec, payload)
	if runErr != nil {
		return result, runErr
	}
	raw, err := exec.CommandContext(ctx, d.Binary, "inspect", "--format", `{"image":{{json .Image}},"user":{{json .Config.User}},"readonly":{{json .HostConfig.ReadonlyRootfs}},"caps":{{json .HostConfig.CapDrop}},"security":{{json .HostConfig.SecurityOpt}},"mounts":{{json .Mounts}},"exit_code":{{json .State.ExitCode}}}`, spec.ContainerName).Output()
	var actual struct {
		Image, User    string
		Readonly       bool
		Caps, Security []string
		Mounts         []struct {
			Type, Name, Destination string
			RW                      bool
		}
		ExitCode int `json:"exit_code"`
	}
	valid := err == nil && json.Unmarshal(raw, &actual) == nil && actual.Image == spec.Image && actual.User == "10001" && actual.Readonly && actual.ExitCode == 0 && strings.Join(actual.Caps, ",") == "ALL" && strings.Contains(strings.Join(actual.Security, ","), "no-new-privileges")
	valid = valid && len(actual.Mounts) == 1
	if valid {
		m := actual.Mounts[0]
		valid = m.Type == "volume" && m.Name == spec.VolumeName && m.Destination == "/var/lib/stoneage-ai" && m.RW
	}
	d.mu.Lock()
	if valid {
		d.inspected++
	} else {
		d.inspectionErr = errors.New("actual packaged container configuration or exit mismatch")
	}
	d.mu.Unlock()
	// Inspection is an independent assertion, never fabricated runtime output.
	return result, nil
}

func runFactoryBrokerLiveLeveling(t *testing.T, ctx context.Context, f *movementCrossMapLiveFreshAI, binding aimcp.Binding, gate *aicontrol.Gate, backend *containerLevelingBackend, gateway *Gateway, endpoint string, request airunner.ExecuteRequest, image, artifacts string) airunner.Result {
	t.Helper()
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	dockerPath, err = filepath.EvalSymlinks(dockerPath)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := aibroker.NewDocker(dockerPath)
	if err != nil {
		t.Fatal(err)
	}
	daemon := &factoryBrokerLiveDocker{DockerCLI: cli}
	root := filepath.Join(f.Root, "factory-broker")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	broker, err := aibroker.New(aibroker.Config{Docker: daemon, Image: image, Network: "bridge", JournalPath: filepath.Join(root, "broker.json"), NamePrefix: "sa-qa-" + movementCrossMapLiveHex(t, 8)})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	t.Cleanup(func() {
		// Never remove a live/unknown container merely to make the test clean.
		daemon.mu.Lock()
		specs := append([]aibroker.RunSpec(nil), daemon.specs...)
		daemon.mu.Unlock()
		for _, spec := range specs {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			state, err := daemon.Inspect(cleanupCtx, spec.ContainerName)
			if !errors.Is(err, aibroker.ErrContainerNotFound) {
				cancel()
				t.Errorf("retaining QA volume: container outcome is %s; inspect error=%v", state, err)
				continue
			}
			err = exec.CommandContext(cleanupCtx, dockerPath, "volume", "rm", spec.VolumeName).Run()
			cancel()
			if err != nil {
				t.Error("remove completed test-owned profile volume")
			}
		}
	})
	models, err := airuntime.OpenStore(filepath.Join(root, "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer models.Close()
	secrets, err := airuntime.NewSecretStore(filepath.Join(root, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	model, err := models.CreateModelConfig(ctx, factoryBrokerLiveModelConfig(request.Model))
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.WriteKey(model.ID, request.Model.APIKey); err != nil {
		t.Fatal("store private QA model key")
	}
	profile := airuntime.Profile{ID: request.ProfileID, Account: airuntime.AccountIdentity{ID: "qa-account", Username: binding.AccountID}, Character: airuntime.CharacterIdentity{ID: binding.CharacterID, Name: binding.CharacterName}, ModelConfigID: model.ID, Status: airuntime.ProfileStatusActive, UnlimitedFunds: true}
	for _, skill := range request.Skills {
		profile.Skills = append(profile.Skills, airuntime.SkillVersion{Name: skill.Name, Kind: airuntime.SkillKindNative})
	}
	factory, err := NewFactory(FactoryConfig{Models: models, Secrets: secrets, Gateway: gateway, GatewayEndpoint: endpoint, RuntimeRoot: filepath.Join(root, "transport"), SkillRoot: filepath.Join(f.RepoRoot, "ai", "skills"), ContainerBroker: broker,
		Sessions: SessionProviderFunc(func(_ context.Context, selected airuntime.Profile) (SessionLease, error) {
			if selected.ID != profile.ID || selected.Character.ID != binding.CharacterID {
				return SessionLease{}, errors.New("QA identity mismatch")
			}
			return SessionLease{Backend: backend, Gate: gate, Close: func() {}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	session, err := factory.Open(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	run := aicodex.RunRequest{ProfileID: profile.ID, RequestID: request.RequestID, Prompt: request.Run.Prompt}
	result, err := session.Runner.Run(ctx, run)
	if err != nil {
		t.Fatal("Factory/broker packaged model turn failed:", err)
	}
	replay, err := session.Runner.Run(ctx, run)
	if err != nil || replay.ThreadID != result.ThreadID || replay.LastMessage != result.LastMessage {
		t.Fatal("same request did not replay its durable model result")
	}
	daemon.mu.Lock()
	count, inspected, inspectionErr := len(daemon.specs), daemon.inspected, daemon.inspectionErr
	requests := append([]airunner.ExecuteRequest(nil), daemon.requests...)
	daemon.mu.Unlock()
	if count != 1 || inspected != 1 || inspectionErr != nil {
		t.Fatalf("actual Docker run/replay/isolation unverified: runs=%d inspected=%d error=%v", count, inspected, inspectionErr)
	}
	submitted := requests[0]
	entry, err := broker.Lookup(ctx, profile.ID, submitted.RequestID)
	if err != nil || entry.State != aibroker.RunCompleted || !entry.Response.OK || entry.Response.Result == nil {
		t.Fatal("broker completion missing")
	}
	authoritative := *entry.Response.Result
	if result.ThreadID != authoritative.ThreadID || result.LastMessage != authoritative.LastMessage || result.Usage.InputTokens != authoritative.Usage.InputTokens || result.Usage.OutputTokens != authoritative.Usage.OutputTokens {
		t.Fatal("Factory result differs from broker journal")
	}
	// Close the Factory while its session is still active, then verify cleanup.
	if err := factory.Close(); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	closes := backend.closeCalls
	backend.mu.Unlock()
	if closes != 1 {
		t.Fatalf("Factory failed to close the gameplay backend: closes=%d", closes)
	}
	if call(gateway, submitted.MCP.Token, `{"operation":"observe","arguments":{}}`).Code != 401 {
		t.Fatal("Factory close retained game capability")
	}
	for _, path := range []string{filepath.Join(root, "transport"), filepath.Join(root, "broker.json")} {
		err := filepath.WalkDir(path, func(path string, e fs.DirEntry, walkErr error) error {
			if walkErr != nil || e.IsDir() {
				return walkErr
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, secret := range []string{request.Model.APIKey, submitted.MCP.Token} {
				if secret != "" && bytes.Contains(raw, []byte(secret)) {
					return errors.New("credential in durable transport state")
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"codex", "workspaces"} {
		if _, err := os.Stat(filepath.Join(root, "transport", name)); !os.IsNotExist(err) {
			t.Fatal("Factory created host Codex state")
		}
	}
	raw, err := json.MarshalIndent(map[string]any{"passed": false, "status": "component_checks_only", "image_id": image, "actual_runs": count, "uid": "10001", "profile_volume_verified": true, "durable_replay_verified": true, "capability_revoked": true, "credential_free_transport_verified": true, "events": authoritative.Events}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{request.Model.APIKey, submitted.MCP.Token} {
		if secret != "" && bytes.Contains(raw, []byte(secret)) {
			t.Fatal("credential in evidence")
		}
	}
	if err := os.WriteFile(filepath.Join(artifacts, "factory-broker-evidence.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return authoritative
}

func factoryBrokerLiveModelConfig(model airunner.Model) airuntime.ModelConfig {
	return airuntime.ModelConfig{ID: "qa-model", Name: "QA DeepSeek", Backend: airuntime.ModelBackendCodex, Provider: model.Provider, BaseURL: model.BaseURL, Model: model.Model, WireAPI: airuntime.ModelProviderResponses, ReasoningEffort: model.ReasoningEffort, Timeout: 7 * time.Minute, MaxOutputTokens: 4096, HasKey: true}
}

// Exercise the actual store validation before an expensive live Docker run.
func TestFactoryBrokerLiveModelConfig(t *testing.T) {
	root := factoryTestRoot(t)
	store, err := airuntime.OpenStore(filepath.Join(root, "models.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	config := factoryBrokerLiveModelConfig(airunner.Model{Provider: aimodels.DeepSeekProvider, BaseURL: aimodels.DeepSeekBaseURL, Model: aimodels.DeepSeekFlash, ReasoningEffort: "high"})
	saved, err := store.CreateModelConfig(context.Background(), config)
	if err != nil {
		t.Fatal("live model configuration is invalid:", err)
	}
	if saved.MaxOutputTokens <= 0 || saved.WireAPI != airuntime.ModelProviderResponses {
		t.Fatal("invalid live model limits or protocol")
	}
}
