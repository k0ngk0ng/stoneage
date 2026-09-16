package admin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/aisupervisor"
)

type fakeAISupervisorControl struct {
	status aisupervisor.Status
	err    error
	start  []string
	pause  []string
	stop   []string
}

func (fake *fakeAISupervisorControl) ProfileStatus(context.Context, string) (aisupervisor.Status, error) {
	if fake.err != nil {
		return aisupervisor.Status{}, fake.err
	}
	return fake.status, nil
}

func (fake *fakeAISupervisorControl) Start(_ context.Context, profileID string) error {
	fake.start = append(fake.start, profileID)
	return fake.err
}

func (fake *fakeAISupervisorControl) Pause(_ context.Context, profileID string) error {
	fake.pause = append(fake.pause, profileID)
	return fake.err
}

func (fake *fakeAISupervisorControl) Stop(_ context.Context, profileID string) error {
	fake.stop = append(fake.stop, profileID)
	return fake.err
}

func TestAISupervisorRuntimeAdapterMapsStatusAndDelegatesLifecycle(t *testing.T) {
	updated := time.Unix(123, 0).UTC()
	fake := &fakeAISupervisorControl{status: aisupervisor.Status{
		State: "waiting", Message: "waiting for game event", ThreadID: "thread-1", UpdatedAt: updated,
		NextDecisionAt: updated.Add(5 * time.Minute),
	}}
	adapter, err := NewAISupervisorRuntimeAdapter(fake)
	if err != nil {
		t.Fatal(err)
	}
	status, err := adapter.ProfileStatus(context.Background(), "profile-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.ProfileID != "profile-1" || status.State != "waiting" || status.Message != "waiting for game event" || status.Backend != "codex" || status.SessionID != "thread-1" || !status.UpdatedAt.Equal(updated) {
		t.Fatalf("mapped status = %#v", status)
	}
	if !status.NextDecisionAt.Equal(fake.status.NextDecisionAt) {
		t.Fatal("adapter discarded the next life decision time")
	}
	if err := adapter.StartProfile(context.Background(), "profile-1"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.PauseProfile(context.Background(), "profile-1"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.StopProfile(context.Background(), "profile-1"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(fake.start, ",") != "profile-1" || strings.Join(fake.pause, ",") != "profile-1" || strings.Join(fake.stop, ",") != "profile-1" {
		t.Fatalf("lifecycle calls start=%v pause=%v stop=%v", fake.start, fake.pause, fake.stop)
	}
}

func TestAISupervisorRuntimeAdapterUsesLastErrorWhenMessageMissing(t *testing.T) {
	fake := &fakeAISupervisorControl{status: aisupervisor.Status{LastError: "paused after a failed turn"}}
	adapter, err := NewAISupervisorRuntimeAdapter(fake)
	if err != nil {
		t.Fatal(err)
	}
	status, err := adapter.ProfileStatus(context.Background(), "profile-2")
	if err != nil {
		t.Fatal(err)
	}
	if status.ProfileID != "profile-2" || status.Message != "paused after a failed turn" {
		t.Fatalf("status = %#v", status)
	}
}

func TestAICodexModelConnectionTesterUsesPrivateKeyAndRedactsFailure(t *testing.T) {
	root := t.TempDir()
	store, err := airuntime.OpenWithSecrets(filepath.Join(root, "models.db"), filepath.Join(root, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secretStore, err := airuntime.NewSecretStore(filepath.Join(root, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.CreateModelConfig(context.Background(), airuntime.ModelConfig{
		ID: "model-1", Name: "DeepSeek Flash", Backend: airuntime.ModelBackendCodex,
		Provider: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-flash",
		ReasoningEffort: airuntime.ReasoningEffortHigh, Timeout: time.Minute, MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	secret := "sk-admin-runtime-test"
	if err := secretStore.WriteKey(config.ID, secret); err != nil {
		t.Fatal(err)
	}

	successBinary := writeAdminConnectionTestCodex(t, root, true)
	workRoot := filepath.Join(root, "connection-tests")
	stateRoot := filepath.Join(root, "connection-state")
	tester, err := NewAICodexModelConnectionTester(AIModelConnectionTesterOptions{
		Models: store, Secrets: secretStore, CodexBinary: successBinary, WorkRoot: workRoot,
		StateRoot: stateRoot, ConnectionTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tester.TestModelConfig(context.Background(), config.ID); err != nil {
		t.Fatalf("connection test failed: %v", err)
	}
	entries, err := os.ReadDir(workRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("connection test left disposable data: %v", entries)
	}
	stateEntries, err := os.ReadDir(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(stateEntries) != 0 {
		t.Fatalf("connection test left disposable state: %v", stateEntries)
	}

	wrongMessageBinary := writeAdminConnectionTestCodexWithMessage(t, root, "not-the-required-probe-result")
	wrongMessageTester, err := NewAICodexModelConnectionTester(AIModelConnectionTesterOptions{
		Models: store, Secrets: secretStore, CodexBinary: wrongMessageBinary, WorkRoot: workRoot,
		ConnectionTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = wrongMessageTester.TestModelConfig(context.Background(), config.ID)
	if !errors.Is(err, ErrAIModelConnectionTest) || strings.Contains(err.Error(), secret) {
		t.Fatalf("wrong probe result = %v", err)
	}

	failureBinary := writeAdminConnectionTestCodex(t, root, false)
	failingTester, err := NewAICodexModelConnectionTester(AIModelConnectionTesterOptions{
		Models: store, Secrets: secretStore, CodexBinary: failureBinary, WorkRoot: workRoot,
		ConnectionTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = failingTester.TestModelConfig(context.Background(), config.ID)
	if !errors.Is(err, ErrAIModelConnectionTest) || strings.Contains(err.Error(), secret) {
		t.Fatalf("failure = %v", err)
	}
}

func TestAICodexModelConnectionTesterRejectsMissingKeyWithoutRunningCodex(t *testing.T) {
	root := t.TempDir()
	store, err := airuntime.OpenWithSecrets(filepath.Join(root, "models.db"), filepath.Join(root, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	secretStore, err := airuntime.NewSecretStore(filepath.Join(root, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.CreateModelConfig(context.Background(), airuntime.ModelConfig{
		ID: "model-2", Name: "DeepSeek Flash", Backend: airuntime.ModelBackendCodex,
		Provider: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-flash",
		ReasoningEffort: airuntime.ReasoningEffortHigh, Timeout: time.Minute, MaxOutputTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "ran")
	binary := writeAdminConnectionTestCodexWithMarker(t, root, marker)
	tester, err := NewAICodexModelConnectionTester(AIModelConnectionTesterOptions{
		Models: store, Secrets: secretStore, CodexBinary: binary, WorkRoot: filepath.Join(root, "connection-tests"),
		ConnectionTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = tester.TestModelConfig(context.Background(), config.ID)
	if !errors.Is(err, ErrAIModelConnectionTest) || strings.Contains(err.Error(), "model-2") {
		t.Fatalf("missing key failure = %v", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Codex ran despite missing key, stat error=%v", statErr)
	}
}

func writeAdminConnectionTestCodex(t *testing.T, root string, success bool) string {
	t.Helper()
	path := filepath.Join(root, "fake-codex-connection")
	mode := `
printf '%s\n' 'api_key=sk-admin-runtime-test' >&2
printf '%s\n' '{"type":"thread.started","thread_id":"connection-test-thread"}'
printf '%s\n' '{"type":"turn.started","turn_id":"connection-test-turn"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"STONEAGE_CONNECTION_TEST_OK"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"total_tokens":1}}'
`
	if !success {
		mode = `
printf '%s\n' 'api_key=sk-admin-runtime-test' >&2
printf '%s\n' '{"type":"thread.started","thread_id":"connection-test-thread"}'
exit 9
`
	}
	script := "#!/bin/sh\nset -eu\n" + mode
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeAdminConnectionTestCodexWithMarker(t *testing.T, root, marker string) string {
	t.Helper()
	path := filepath.Join(root, "fake-codex-missing-key")
	script := "#!/bin/sh\nset -eu\ntouch " + shellQuote(marker) + "\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeAdminConnectionTestCodexWithMessage(t *testing.T, root, message string) string {
	t.Helper()
	path := filepath.Join(root, "fake-codex-wrong-message")
	script := "#!/bin/sh\nset -eu\nprintf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"connection-test-thread\"}'\nprintf '%s\\n' '{\"type\":\"turn.started\",\"turn_id\":\"connection-test-turn\"}'\nprintf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"" + message + "\"}}'\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{\"total_tokens\":1}}'\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
