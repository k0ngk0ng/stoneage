package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/airemote"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

func TestWorkerActivityLogsAreStructuredAndRedacted(t *testing.T) {
	var output bytes.Buffer
	worker := &worker{logger: log.New(&output, "", 0)}
	command := airemote.Command{
		Kind:      airemote.KindRun,
		ProfileID: "profile-1",
		RequestID: "request-1",
		Payload:   json.RawMessage(`{"prompt":"private prompt","session_token":"session-secret","api_key":"api-secret"}`),
	}
	worker.logCommand(command)
	started := worker.logTaskStarted(command.Kind, command)
	worker.logTaskCompleted(command.Kind, command, started.Add(-time.Second), "dead", "runner_failed")

	logs := output.String()
	for _, value := range []string{
		"event=command_received",
		"event=task_started",
		"event=task_completed",
		"kind=run",
		"profile=profile-1",
		"request_id=request-1",
		"duration_ms=",
		"status=dead",
		"error=runner_failed",
	} {
		if !strings.Contains(logs, value) {
			t.Fatalf("logs missing %q: %s", value, logs)
		}
	}
	for _, secret := range []string{"private prompt", "session-secret", "api-secret"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("logs leaked %q: %s", secret, logs)
		}
	}
	if got := workerLogID("token with spaces"); got != "redacted" {
		t.Fatalf("unsafe log id=%q, want redacted", got)
	}
}

func TestWorkerEndToEndRunReplayStopAndServerRestartReconnect(t *testing.T) {
	stateRoot := t.TempDir()
	runner := filepath.Join(stateRoot, "fake-runner.sh")
	script := `#!/bin/sh
set -eu
input=$(cat)
if echo "$input" | grep -q 'stop-me'; then
  trap 'exit 143' TERM INT
  while :; do sleep 1; done
fi
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}'
printf '%s\n' '{"type":"turn.started","turn_id":"turn-1"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"WORKER_OK"}}'
printf '%s\n' '{"type":"turn.completed","turn_id":"turn-1","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}'
`
	if err := os.WriteFile(runner, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	mcp := filepath.Join(stateRoot, "fake-mcp.sh")
	if err := os.WriteFile(mcp, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	skillRoot := filepath.Join(cwd, "..", "..", "ai", "skills")
	store := airemote.NewMemoryStore()
	hub, err := airemote.New(airemote.Config{Store: store, PublicBaseURL: "http://control.invalid", GatewayURL: "http://gateway.invalid/v1/game", LeaseTimeout: 3 * time.Second, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	serverHandler := &atomic.Value{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serverHandler.Load().(http.Handler).ServeHTTP(w, r) }))
	defer server.Close()
	// The actual public endpoint is known only after httptest allocates it.
	_ = hub.Close()
	hub, err = airemote.New(airemote.Config{Store: store, PublicBaseURL: server.URL, GatewayURL: "http://gateway.invalid/v1/game", LeaseTimeout: 3 * time.Second, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := hub.Invite(context.Background(), "profile-1", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	serverHandler.Store(hub.Handler())
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	workerErr := make(chan error, 1)
	go func() {
		workerErr <- run(workerCtx, []string{"--endpoint", invite.Endpoint, "--profile", "profile-1", "--enrollment-token", invite.Token, "--state-root", stateRoot, "--runner", runner, "--codex", runner, "--mcp", mcp, "--skills", skillRoot, "--poll-seconds", "1"})
	}()
	waitUntil(t, 3*time.Second, func() bool { status, _ := hub.Status(context.Background(), "profile-1"); return status.Online })
	journal := aibroker.NewMemoryJournal()
	broker, err := aibroker.New(aibroker.Config{Docker: hub, Journal: journal, Image: "runtime@sha256:test", Network: "network", StopTimeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	request := workerRequest("request-1", "complete")
	response, err := broker.Execute(context.Background(), request)
	if err != nil || !response.OK {
		t.Fatalf("worker execution response=%+v err=%v", response, err)
	}
	replay, err := broker.Execute(context.Background(), request)
	if err != nil || !replay.OK {
		t.Fatalf("worker replay response=%+v err=%v", replay, err)
	}
	// Rebuild Hub from the same durable route/profile store. The next poll gets
	// 401, then worker reconnects with the stable session credential.
	hub2, err := airemote.New(airemote.Config{Store: store, PublicBaseURL: server.URL, GatewayURL: "http://gateway.invalid/v1/game", LeaseTimeout: 3 * time.Second, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	serverHandler.Store(hub2.Handler())
	waitUntil(t, 5*time.Second, func() bool { status, _ := hub2.Status(context.Background(), "profile-1"); return status.Online })
	broker2, err := aibroker.New(aibroker.Config{Docker: hub2, Journal: journal, Image: "runtime@sha256:test", Network: "network", StopTimeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	stopRequest := workerRequest("request-2", "stop-me")
	stopCtx, cancelStop := context.WithCancel(context.Background())
	stopResult := make(chan error, 1)
	go func() { _, runErr := broker2.Execute(stopCtx, stopRequest); stopResult <- runErr }()
	waitUntil(t, 3*time.Second, func() bool {
		status, _ := hub2.Status(context.Background(), "profile-1")
		if status.RequestID == "request-2" {
			return true
		}

		return false
	})
	cancelStop()
	select {
	case <-stopResult:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not settle")
	}
	cancelWorker()
	select {
	case <-workerErr:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestWorkerEnrollmentRetriesLostResponseWithPersistedSession(t *testing.T) {
	stateRoot := t.TempDir()
	var hub *airemote.Hub
	var dropped atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/ai/worker/connect" && dropped.CompareAndSwap(false, true) {
			// Let Hub commit the enrollment, then close the transport before the
			// worker can decode its response. The retry must use the pending
			// session credential persisted before this request.
			recorded := httptest.NewRecorder()
			hub.Handler().ServeHTTP(recorded, r)
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				return
			}
			connection, _, err := hijacker.Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		}
		hub.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	var err error
	hub, err = airemote.New(airemote.Config{Store: airemote.NewMemoryStore(), PublicBaseURL: server.URL, LeaseTimeout: 3 * time.Second, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := hub.Invite(context.Background(), "profile-1", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	workerErr := make(chan error, 1)
	go func() {
		workerErr <- run(workerCtx, []string{"--endpoint", invite.Endpoint, "--profile", "profile-1", "--enrollment-token", invite.Token, "--state-root", stateRoot, "--poll-seconds", "1"})
	}()
	statePath := filepath.Join(stateRoot, "worker", "worker.json")
	waitUntil(t, 3*time.Second, func() bool {
		raw, readErr := os.ReadFile(statePath)
		if !dropped.Load() || readErr != nil {
			return false
		}
		var state workerState
		return json.Unmarshal(raw, &state) == nil && state.WorkerID != "" && state.SessionToken != "" && state.Epoch > 0 && state.ProfileID == "profile-1"
	})
	raw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state workerState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	status, err := hub.Status(context.Background(), "profile-1")
	if err != nil || !status.Online || status.WorkerID != state.WorkerID || status.Epoch != state.Epoch {
		t.Fatalf("worker status=%+v state=%+v err=%v", status, state, err)
	}
	cancelWorker()
	select {
	case err := <-workerErr:
		if err != nil {
			t.Fatalf("worker stop error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

func TestWorkerReusesPersistedSessionBeforeEnrollment(t *testing.T) {
	stateRoot := t.TempDir()
	var hub *airemote.Hub
	var connectCount atomic.Int32
	var pollCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hub == nil {
			http.Error(w, "hub is not ready", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/api/ai/worker/poll" {
			pollCount.Add(1)
		}
		hub.Handler().ServeHTTP(w, r)
		if r.URL.Path == "/api/ai/worker/connect" {
			connectCount.Add(1)
		}
	}))
	defer server.Close()
	hub, err := airemote.New(airemote.Config{Store: airemote.NewMemoryStore(), PublicBaseURL: server.URL, LeaseTimeout: 3 * time.Second, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := hub.Invite(context.Background(), "profile-1", server.URL)
	if err != nil {
		t.Fatal(err)
	}

	startWorker := func() (<-chan error, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		workerErr := make(chan error, 1)
		go func() {
			workerErr <- run(ctx, []string{"--endpoint", invite.Endpoint, "--profile", "profile-1", "--enrollment-token", invite.Token, "--state-root", stateRoot, "--poll-seconds", "1"})
		}()
		return workerErr, cancel
	}
	workerErr, cancelWorker := startWorker()
	waitUntil(t, 3*time.Second, func() bool {
		status, _ := hub.Status(context.Background(), "profile-1")
		return status.Online && pollCount.Load() >= 1
	})
	cancelWorker()
	select {
	case err := <-workerErr:
		if err != nil {
			t.Fatalf("first worker stop error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first worker did not stop")
	}

	// Reusing the same command is expected to send only the persisted session.
	// The consumed enrollment token must not be attempted again, because Hub
	// treats it as unauthorized after the first successful enrollment.
	workerErr, cancelWorker = startWorker()
	waitUntil(t, 3*time.Second, func() bool {
		status, _ := hub.Status(context.Background(), "profile-1")
		return status.Online && connectCount.Load() >= 2 && pollCount.Load() >= 2
	})
	cancelWorker()
	select {
	case err := <-workerErr:
		if err != nil {
			t.Fatalf("second worker stop error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second worker did not stop")
	}
}

func TestWorkerFallsBackToRotatedEnrollmentAfterOldSessionUnauthorized(t *testing.T) {
	stateRoot := t.TempDir()
	var hub *airemote.Hub
	var pollCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hub == nil {
			http.Error(w, "hub is not ready", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/api/ai/worker/poll" {
			pollCount.Add(1)
		}
		hub.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	hub, err := airemote.New(airemote.Config{Store: airemote.NewMemoryStore(), PublicBaseURL: server.URL, LeaseTimeout: 50 * time.Millisecond, CommandTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	firstInvite, err := hub.Invite(context.Background(), "profile-1", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	startWorker := func(invite airemote.Invitation) (<-chan error, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		workerErr := make(chan error, 1)
		go func() {
			workerErr <- run(ctx, []string{"--endpoint", invite.Endpoint, "--profile", "profile-1", "--enrollment-token", invite.Token, "--state-root", stateRoot, "--poll-seconds", "1"})
		}()
		return workerErr, cancel
	}
	workerErr, cancelWorker := startWorker(firstInvite)
	waitUntil(t, 3*time.Second, func() bool {
		status, _ := hub.Status(context.Background(), "profile-1")
		return status.Online && pollCount.Load() >= 1
	})
	statePath := filepath.Join(stateRoot, "worker", "worker.json")
	readState := func() workerState {
		raw, readErr := os.ReadFile(statePath)
		if readErr != nil {
			t.Fatalf("read worker state: %v", readErr)
		}
		var state workerState
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatalf("decode worker state: %v", err)
		}
		return state
	}
	oldState := readState()
	cancelWorker()
	select {
	case err := <-workerErr:
		if err != nil {
			t.Fatalf("first worker stop error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first worker did not stop")
	}
	waitUntil(t, 3*time.Second, func() bool {
		status, _ := hub.Status(context.Background(), "profile-1")
		return !status.Online
	})

	rotatedInvite, err := hub.Invite(context.Background(), "profile-1", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if rotatedInvite.Token == "" || rotatedInvite.Token == firstInvite.Token {
		t.Fatal("Hub did not rotate the offline enrollment token")
	}
	workerErr, cancelWorker = startWorker(rotatedInvite)
	waitUntil(t, 3*time.Second, func() bool {
		status, _ := hub.Status(context.Background(), "profile-1")
		return status.Online && pollCount.Load() >= 2
	})
	newState := readState()
	if newState.SessionToken == oldState.SessionToken {
		t.Fatal("worker reused unauthorized session token after enrollment rotation")
	}
	if newState.Epoch == 0 {
		t.Fatal("worker did not persist the re-enrolled session epoch")
	}
	cancelWorker()
	select {
	case err := <-workerErr:
		if err != nil {
			t.Fatalf("second worker stop error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second worker did not stop")
	}
}

func TestExecuteRunReleasesProcessBeforeResultPost(t *testing.T) {
	stateRoot := t.TempDir()
	jobs, err := openJobStore(stateRoot, filepath.Join(stateRoot, "runner"), "test-namespace")
	if err != nil {
		t.Fatal(err)
	}
	resultEntered := make(chan struct{})
	releaseResult := make(chan struct{})
	var resultBlocked atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var result airemote.ResultRequest
		if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if result.State != "stopped" && resultBlocked.CompareAndSwap(false, true) {
			close(resultEntered)
			<-releaseResult
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	request := workerRequest("request-release", "complete")
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	command := airemote.Command{ProtocolVersion: airemote.ProtocolVersion, CommandID: "command-release", Kind: airemote.KindRun, WorkerID: "worker-1", WorkerEpoch: 1, ProfileID: "profile-1", RequestID: request.RequestID, ContainerName: "stoneage-run-release", PayloadHash: hashBytes(payload), Payload: payload}
	worker := &worker{
		opts:      options{server: server.URL, profile: "profile-1", stateRoot: stateRoot},
		client:    server.Client(),
		state:     workerState{WorkerID: "worker-1", ProfileID: "profile-1", SessionToken: "session-token", Epoch: 1},
		jobs:      jobs,
		processes: make(map[string]*processState),
		namespace: "test-namespace",
		executeTurn: func(context.Context, options, airunner.ExecuteRequest) (airunner.Response, error) {
			return airunner.Response{OK: true, ProfileID: "profile-1", RequestID: request.RequestID, Result: &airunner.Result{Process: airunner.Process{Status: "exited", ExitCode: 0}}}, nil
		},
	}
	done := make(chan struct{})
	go func() {
		worker.executeRun(context.Background(), command)
		close(done)
	}()
	select {
	case <-resultEntered:
	case <-time.After(time.Second):
		t.Fatal("result post was not reached")
	}
	worker.mu.Lock()
	_, processPresent := worker.processes[command.ContainerName]
	worker.mu.Unlock()
	if processPresent {
		t.Fatal("worker process remained busy while result post was blocked")
	}
	record, ok := jobs.get(command.ContainerName)
	if !ok || record.State != "exited" {
		t.Fatalf("journal record=%+v exists=%v", record, ok)
	}
	stopDone := make(chan struct{})
	go func() {
		worker.executeStop(context.Background(), airemote.Command{ProtocolVersion: airemote.ProtocolVersion, CommandID: "command-stop-release", Kind: airemote.KindStop, WorkerID: "worker-1", WorkerEpoch: 1, ProfileID: "profile-1", RequestID: request.RequestID, ContainerName: command.ContainerName, PayloadHash: command.PayloadHash})
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("stop waited for blocked run result post")
	}
	close(releaseResult)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run did not finish after result post release")
	}
}

func workerRequest(id, prompt string) airunner.ExecuteRequest {
	return airunner.ExecuteRequest{ProfileID: "profile-1", RequestID: id, Run: airunner.RunRequest{Prompt: prompt}, Model: airunner.Model{Provider: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-flash", APIKey: "secret-key"}, MCP: airunner.MCP{Endpoint: "http://gateway.invalid/v1/game", Token: strings.Repeat("g", 43), CharacterID: "character-1", Generation: 1}}
}
func waitUntil(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
