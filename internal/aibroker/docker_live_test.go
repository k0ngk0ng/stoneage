package aibroker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const dockerLiveOptIn = "STONEAGE_BROKER_DOCKER_LIVE_TEST"

// This test uses real Docker and SQLite, but a fixed Alpine process instead
// of Codex. It verifies process/container recovery, not a model turn.
func TestLiveDockerBrokerRecovery(t *testing.T) {
	if os.Getenv(dockerLiveOptIn) != "1" {
		t.Skip("set STONEAGE_BROKER_DOCKER_LIVE_TEST=1; requires an existing alpine:3.22 image")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	docker, err = filepath.EvalSymlinks(docker)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(repo, "build", "ai")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "docker-broker-live-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if filepath.Base(docker) != "docker" {
		// OrbStack installs a multicall executable behind the docker symlink.
		// Keep its required argv[0] while giving NewDocker a regular, private
		// launcher. This forwards every argument to the real CLI unchanged.
		launcher := filepath.Join(root, "docker")
		quoted := "'" + strings.ReplaceAll(docker, "'", "'\\''") + "'"
		if err := os.WriteFile(launcher, []byte("#!/bin/bash\nexec -a docker "+quoted+" \"$@\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		docker = launcher
	}
	image, err := dockerLiveCommand(docker, "image", "inspect", "--format", "{{.Id}}", "alpine:3.22")
	if err != nil {
		t.Fatalf("existing alpine:3.22 image is required; no pull is attempted: %v (%s)", err, strings.TrimSpace(image))
	}
	image = strings.TrimSpace(image)
	prefix := "sa-ai-qa-" + strings.TrimPrefix(filepath.Base(root), "docker-broker-live-")
	config := Config{DockerBinary: docker, Image: image, Network: "none", NamePrefix: prefix, StopTimeout: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	for _, profile := range []string{"normal", "crash", "completed-crash"} {
		container := deterministicName(prefix, "run", profile, "request-1")
		volume := deterministicName(prefix, "profile", profile)
		t.Cleanup(func() {
			_, _ = dockerLiveCommand(docker, "rm", "--force", container)
			if _, err := dockerLiveCommand(docker, "volume", "inspect", volume); err == nil {
				if _, err := dockerLiveCommand(docker, "volume", "rm", volume); err != nil {
					t.Errorf("remove QA volume: %v", err)
				}
			}
		})
	}

	config.JournalPath = filepath.Join(root, "normal.db")
	config.RunnerCommand = []string{"/bin/sh", "-c", `cat >/dev/null; printf '%s\n' '{"ok":true,"result":{"thread_id":"qa-only","turn":{"status":"completed"},"process":{"status":"exited","exit_code":0}}}'`}
	broker, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	request := testRequest("normal", "request-1")
	result, runErr := broker.Run(ctx, request)
	closeErr := broker.Close()
	if runErr != nil || closeErr != nil || result.State != RunCompleted {
		t.Fatalf("normal Docker exit: state=%s run=%v close=%v", result.State, runErr, closeErr)
	}
	if _, err := dockerLiveCommand(docker, "inspect", deterministicName(prefix, "run", "normal", "request-1")); err == nil {
		t.Fatal("normal completed container was not cleaned after journal commit")
	}
	// A command that would fail proves the cached response is replayed after
	// database reopen without launching another process.
	config.RunnerCommand = []string{"/bin/sh", "-c", "exit 97"}
	broker, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	result, runErr = broker.Run(ctx, request)
	closeErr = broker.Close()
	if runErr != nil || closeErr != nil || result.State != RunCompleted {
		t.Fatalf("completed replay: state=%s run=%v close=%v", result.State, runErr, closeErr)
	}
	t.Log("real container exit and completed-result replay passed")

	config.JournalPath = filepath.Join(root, "crash.db")
	config.RunnerCommand = []string{"/bin/sh", "-c", "cat >/dev/null; touch /tmp/qa-ready; exec sleep 120"}
	encoded, err := json.Marshal(configForDockerLiveHelper{Docker: docker, Image: image, Prefix: prefix, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.OpenFile(filepath.Join(root, "broker-child.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	child := exec.Command(os.Args[0], "-test.run=^TestLiveDockerBrokerCrashHelper$", "-test.timeout=150s")
	child.Env = append(os.Environ(), "STONEAGE_BROKER_CRASH_HELPER="+string(encoded))
	child.Stdout, child.Stderr = log, log
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	childWaited := false
	t.Cleanup(func() {
		if !childWaited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	container := deterministicName(prefix, "run", "crash", "request-1")
	dockerLiveWait(t, ctx, func() bool {
		_, err := dockerLiveCommand(docker, "exec", container, "test", "-f", "/tmp/qa-ready")
		return err == nil
	})
	// Inspect the actual daemon state without printing container environment
	// or stdin. This confirms the launched isolation settings, not just args.
	inspect, err := dockerLiveCommand(docker, "inspect", "--format", `{"user":{{json .Config.User}},"readonly":{{json .HostConfig.ReadonlyRootfs}},"network":{{json .HostConfig.NetworkMode}},"caps":{{json .HostConfig.CapDrop}},"security":{{json .HostConfig.SecurityOpt}},"mounts":{{json .Mounts}},"env":{{json .Config.Env}},"command":{{json .Config.Cmd}},"log":{{json .HostConfig.LogConfig}},"auto_remove":{{json .HostConfig.AutoRemove}}}`, container)
	if err != nil {
		t.Fatal(err)
	}
	var actual struct {
		User           string
		Readonly       bool
		Network        string
		Caps, Security []string
		Mounts         []struct{ Type, Name, Destination string }
		AutoRemove     bool `json:"auto_remove"`
		Log            struct {
			Type   string
			Config map[string]string
		}
	}
	if err := json.Unmarshal([]byte(inspect), &actual); err != nil {
		t.Fatal(err)
	}
	fixtureRequest := testRequest("crash", "request-1")
	if strings.Contains(inspect, fixtureRequest.Model.APIKey) || strings.Contains(inspect, fixtureRequest.MCP.Token) {
		t.Fatal("stdin-only fixture credentials leaked into Docker configuration")
	}
	if actual.User != "10001" || !actual.Readonly || actual.Network != "none" || len(actual.Mounts) != 1 || actual.Mounts[0].Type != "volume" || actual.Mounts[0].Destination != runtimeStateRoot || actual.Mounts[0].Name != deterministicName(prefix, "profile", "crash") || !containsDockerLive(actual.Caps, "ALL") || !containsDockerLive(actual.Security, "no-new-privileges") {
		t.Fatal("actual Docker container does not meet the broker isolation contract")
	}
	if actual.AutoRemove || actual.Log.Type != "local" || actual.Log.Config["max-size"] != "20m" || actual.Log.Config["max-file"] != "1" || actual.Log.Config["compress"] != "false" {
		t.Fatal("actual Docker container does not retain bounded recovery logs")
	}
	competing, err := New(config)
	if err == nil {
		_ = competing.Close()
		t.Fatal("a second broker acquired the journal while its owner was alive")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	childWaited = true
	broker, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	result, err = broker.Lookup(ctx, "crash", "request-1")
	if !errors.Is(err, ErrRunRunning) || result.State != RunRunning {
		t.Fatalf("live orphan was not retained: state=%s err=%v", result.State, err)
	}
	if _, err := dockerLiveCommand(docker, "exec", container, "test", "-f", "/tmp/qa-ready"); err != nil {
		t.Fatal("recovery stopped the live orphan container")
	}
	t.Log("broker process killed; reopened broker preserved the live container")
	if _, err := dockerLiveCommand(docker, "kill", container); err != nil {
		t.Fatal(err)
	}
	dockerLiveWait(t, ctx, func() bool {
		result, err = broker.Lookup(ctx, "crash", "request-1")
		return errors.Is(err, ErrRunUnknown) && result.State == RunUnknown
	})
	if _, err := broker.Run(ctx, testRequest("crash", "request-1")); !errors.Is(err, ErrRunUnknown) {
		t.Fatalf("unknown request was replayed: %v", err)
	}
	if _, err := broker.Run(ctx, testRequest("crash", "request-2")); !errors.Is(err, ErrProfileBusy) {
		t.Fatalf("unresolved profile was reused: %v", err)
	}
	t.Log("terminated container reconciled to unknown; both replay and new profile work remain fenced")
	ready, err := broker.UnknownReviewReady(ctx, "crash", "request-1", result.Entry.UpdatedAt)
	if err != nil || !ready {
		t.Fatalf("terminated container review readiness: ready=%v err=%v", ready, err)
	}
	if _, err := dockerLiveCommand(docker, "inspect", container); err != nil {
		t.Fatal("readiness removed the retained unknown container")
	}
	if _, err := broker.Run(ctx, testRequest("crash", "request-2")); !errors.Is(err, ErrProfileBusy) {
		t.Fatalf("readiness released the unresolved profile: %v", err)
	}

	review, err := broker.ReviewUnknown(ctx, "crash", "request-1", result.Entry.UpdatedAt, "qa-operator", ReviewReasonAcceptUncertainOutcome)
	if err != nil || review.Review == nil || review.State != RunUnknown {
		t.Fatalf("review terminated container: state=%s err=%v", review.State, err)
	}
	if err = broker.Close(); err != nil {
		t.Fatal(err)
	}
	reviewConfig := config
	reviewConfig.RunnerCommand = []string{"/bin/sh", "-c", `cat >/dev/null; printf '%s\n' '{"ok":true,"result":{"thread_id":"qa-reviewed-new","turn":{"status":"completed"},"process":{"status":"exited","exit_code":0}}}'`}
	reviewedBroker, err := New(reviewConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer reviewedBroker.Close()
	old, err := reviewedBroker.Lookup(ctx, "crash", "request-1")
	if !errors.Is(err, ErrRunUnknown) || old.Entry.Review == nil {
		t.Fatalf("review did not survive broker restart: %v", err)
	}
	if _, err = reviewedBroker.Run(ctx, testRequest("crash", "request-1")); !errors.Is(err, ErrRunUnknown) {
		t.Fatalf("review replayed old request: %v", err)
	}
	reviewContainer := deterministicName(prefix, "run", "crash", "request-2")
	t.Cleanup(func() { _, _ = dockerLiveCommand(docker, "rm", "--force", reviewContainer) })
	fresh, err := reviewedBroker.Run(ctx, testRequest("crash", "request-2"))
	if err != nil || fresh.State != RunCompleted {
		t.Fatalf("reviewed profile new request: state=%s err=%v", fresh.State, err)
	}
	if _, err = reviewedBroker.Lookup(ctx, "crash", "request-1"); !errors.Is(err, ErrRunUnknown) {
		t.Fatalf("fresh request rewrote old outcome: %v", err)
	}
	t.Log("real Docker unknown review survives broker restart; fresh request completes while old request remains unknown")

	// A second orphan completes normally after the broker dies. Its daemon-held
	// output must survive until a replacement broker records the exact result.
	completedConfig := config
	completedConfig.JournalPath = filepath.Join(root, "completed-crash.db")
	encoded, err = json.Marshal(configForDockerLiveHelper{Docker: docker, Image: image, Prefix: prefix, Root: root, Profile: "completed-crash"})
	if err != nil {
		t.Fatal(err)
	}
	completedChild := exec.Command(os.Args[0], "-test.run=^TestLiveDockerBrokerCrashHelper$", "-test.timeout=150s")
	completedChild.Env = append(os.Environ(), "STONEAGE_BROKER_CRASH_HELPER="+string(encoded))
	completedChild.Stdout, completedChild.Stderr = log, log
	if err := completedChild.Start(); err != nil {
		t.Fatal(err)
	}
	completedWaited := false
	t.Cleanup(func() {
		if !completedWaited {
			_ = completedChild.Process.Kill()
			_ = completedChild.Wait()
		}
	})
	completedContainer := deterministicName(prefix, "run", "completed-crash", "request-1")
	dockerLiveWait(t, ctx, func() bool {
		_, err := dockerLiveCommand(docker, "exec", completedContainer, "test", "-f", "/tmp/qa-ready")
		return err == nil
	})
	if err := completedChild.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = completedChild.Wait()
	completedWaited = true
	if _, err := dockerLiveCommand(docker, "exec", completedContainer, "touch", "/tmp/qa-finish"); err != nil {
		t.Fatal(err)
	}
	dockerLiveWait(t, ctx, func() bool {
		state, err := dockerLiveCommand(docker, "inspect", "--format", "{{.State.Status}}", completedContainer)
		return err == nil && strings.TrimSpace(state) == "exited"
	})
	recovered, err := New(completedConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	completed, err := recovered.Lookup(ctx, "completed-crash", "request-1")
	if err != nil || completed.State != RunCompleted || completed.Response.Result == nil || completed.Response.Result.ThreadID != "qa-recovered" {
		t.Fatalf("orphan completion was not recovered: state=%s err=%v", completed.State, err)
	}
	if _, err := dockerLiveCommand(docker, "inspect", completedContainer); err == nil {
		t.Fatal("completed container was not removed after durable recovery")
	}
	completed, err = recovered.Lookup(ctx, "completed-crash", "request-1")
	if err != nil || completed.State != RunCompleted {
		t.Fatalf("recovered result was not durable after container cleanup: state=%s err=%v", completed.State, err)
	}
	t.Log("broker process killed; orphan completion recovered from real Docker logs and retained after cleanup")
	evidence := map[string]any{"test": t.Name(), "status": "passed", "image": image, "normal_exit_replay": true, "concurrent_broker_rejected": true, "broker_process_killed": true, "live_orphan_preserved": true, "killed_container_reconciled_unknown": true, "orphan_completion_recovered": true, "completed_container_cleaned": true, "unknown_review_persisted": true, "reviewed_profile_fresh_request_completed": true, "old_reviewed_request_remains_unknown": true, "model_invoked": false}
	raw, _ := json.MarshalIndent(evidence, "", "  ")
	if err := os.WriteFile(filepath.Join(base, "docker-broker-live-evidence.json"), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

type configForDockerLiveHelper struct{ Docker, Image, Prefix, Root, Profile string }

func TestLiveDockerBrokerCrashHelper(t *testing.T) {
	raw := os.Getenv("STONEAGE_BROKER_CRASH_HELPER")
	if raw == "" || os.Getenv(dockerLiveOptIn) != "1" {
		t.Skip("subprocess fixture only")
	}
	var q configForDockerLiveHelper
	if err := json.Unmarshal([]byte(raw), &q); err != nil {
		t.Fatal(err)
	}
	repo, _ := filepath.Abs("../..")
	rel, err := filepath.Rel(filepath.Join(repo, "build", "ai"), q.Root)
	if err != nil || !strings.HasPrefix(rel, "docker-broker-live-") || strings.ContainsAny(rel, "/\\") {
		t.Fatal("helper root is outside project QA directory")
	}
	profile := "crash"
	command := "cat >/dev/null; touch /tmp/qa-ready; exec sleep 120"
	if q.Profile == "completed-crash" {
		profile = q.Profile
		command = `cat >/dev/null; touch /tmp/qa-ready; while [ ! -f /tmp/qa-finish ]; do sleep 0.1; done; printf '%s\n' '{"ok":true,"profile_id":"completed-crash","request_id":"request-1","result":{"thread_id":"qa-recovered","turn":{"status":"completed"},"process":{"status":"exited","exit_code":0}}}'`
	} else if q.Profile != "" {
		t.Fatal("unsupported helper profile")
	}
	broker, err := New(Config{DockerBinary: q.Docker, Image: q.Image, Network: "none", NamePrefix: q.Prefix, JournalPath: filepath.Join(q.Root, profile+".db"), StopTimeout: time.Second, RunnerCommand: []string{"/bin/sh", "-c", command}})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	_, err = broker.Run(context.Background(), testRequest(profile, "request-1"))
	t.Fatalf("fixture broker returned before parent killed it: %v", err)
}

func dockerLiveCommand(binary string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	return string(out), err
}

func dockerLiveWait(t *testing.T, ctx context.Context, ready func() bool) {
	t.Helper()
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("Docker QA condition timed out")
		case <-ticker.C:
		}
	}
}

func containsDockerLive(values []string, expected string) bool {
	for _, value := range values {
		if value == expected || value == expected+":true" {
			return true
		}
	}
	return false
}
