package aibroker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

type fakeDocker struct {
	mu      sync.Mutex
	calls   []fakeDockerCall
	run     func(context.Context, RunSpec, []byte) (DockerResult, error)
	stop    []string
	started chan struct{}
}

type fakeDockerCall struct {
	spec    RunSpec
	payload []byte
}

type inspectDocker struct {
	fakeDocker
	mu     sync.Mutex
	states []DockerContainerState
	errors []error
	calls  []string
}

func (docker *inspectDocker) Inspect(_ context.Context, containerName string) (DockerContainerState, error) {
	docker.mu.Lock()
	defer docker.mu.Unlock()
	index := len(docker.calls)
	docker.calls = append(docker.calls, containerName)
	if index < len(docker.errors) && docker.errors[index] != nil {
		return "", docker.errors[index]
	}
	if index < len(docker.states) {
		return docker.states[index], nil
	}
	return DockerContainerRunning, nil
}

func (docker *inspectDocker) InspectCalls() []string {
	docker.mu.Lock()
	defer docker.mu.Unlock()
	return append([]string(nil), docker.calls...)
}

func (docker *fakeDocker) Run(ctx context.Context, spec RunSpec, payload []byte) (DockerResult, error) {
	docker.mu.Lock()
	docker.calls = append(docker.calls, fakeDockerCall{spec: cloneRunSpec(spec), payload: append([]byte(nil), payload...)})
	if docker.started != nil {
		select {
		case <-docker.started:
		default:
			close(docker.started)
		}
	}
	run := docker.run
	docker.mu.Unlock()
	if run != nil {
		return run(ctx, spec, payload)
	}
	return DockerResult{Stdout: []byte(`{"ok":true,"result":{"thread_id":"thread-fake","turn":{"status":"completed"},"process":{"status":"exited","exit_code":0}}}`)}, nil
}

func (docker *fakeDocker) Stop(_ context.Context, containerName string) error {
	docker.mu.Lock()
	docker.stop = append(docker.stop, containerName)
	docker.mu.Unlock()
	return nil
}

func (docker *fakeDocker) Calls() []fakeDockerCall {
	docker.mu.Lock()
	defer docker.mu.Unlock()
	return append([]fakeDockerCall(nil), docker.calls...)
}

func (docker *fakeDocker) Stops() []string {
	docker.mu.Lock()
	defer docker.mu.Unlock()
	return append([]string(nil), docker.stop...)
}

func cloneRunSpec(spec RunSpec) RunSpec {
	spec.Mounts = append([]VolumeMount(nil), spec.Mounts...)
	spec.Volumes = append([]VolumeMount(nil), spec.Volumes...)
	spec.CapDrop = append([]string(nil), spec.CapDrop...)
	spec.SecurityOpt = append([]string(nil), spec.SecurityOpt...)
	spec.Command = append([]string(nil), spec.Command...)
	spec.Environment = cloneEnvironment(spec.Environment)
	spec.Tmpfs = cloneEnvironment(spec.Tmpfs)
	return spec
}

func testRequest(profileID, requestID string) airunner.ExecuteRequest {
	return airunner.ExecuteRequest{
		ProfileID: profileID,
		RequestID: requestID,
		Run:       airunner.RunRequest{Prompt: "observe"},
		Model:     airunner.Model{Provider: "deepseek", BaseURL: "https://api.deepseek.com/", Model: "deepseek-flash", APIKey: "private-api-key"},
		MCP:       airunner.MCP{Endpoint: "http://gateway:9080/v1/game", Token: strings.Repeat("g", 43), CharacterID: "character-1", Generation: 1},
	}
}

func runningJournalEntry(profileID, requestID string) JournalEntry {
	return JournalEntry{ProfileID: profileID, RequestID: requestID, PayloadHash: strings.Repeat("a", 64), State: RunRunning,
		ContainerName: ContainerName(profileID, requestID), VolumeName: ProfileVolumeName(profileID), UpdatedAt: time.Now()}
}

func testJournalRoot(t *testing.T) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join("build", "ai"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join("build", "ai"), "aibroker-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func newFakeBroker(t *testing.T, docker Docker, journal Journal) *Broker {
	t.Helper()
	broker, err := New(Config{Docker: docker, Journal: journal, Image: "registry.example/stoneage-ai@sha256:abc", Network: "stoneage-backend", StopTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return broker
}

func TestBrokerBuildsIsolatedSpecAndReplaysCompletedRun(t *testing.T) {
	docker := &fakeDocker{}
	broker := newFakeBroker(t, docker, NewMemoryJournal())
	request := testRequest("profile-1", "request-1")
	response, err := broker.Execute(context.Background(), request)
	if err != nil || !response.OK {
		t.Fatalf("first response=%+v err=%v", response, err)
	}
	responseAgain, err := broker.Execute(context.Background(), request)
	if err != nil || !responseAgain.OK {
		t.Fatalf("replay response=%+v err=%v", responseAgain, err)
	}
	calls := docker.Calls()
	if len(calls) != 1 {
		t.Fatalf("Docker calls=%d, want one", len(calls))
	}
	call := calls[0]
	if call.spec.Image != "registry.example/stoneage-ai@sha256:abc" || call.spec.Network != "stoneage-backend" {
		t.Fatalf("fixed image/network were changed: %+v", call.spec)
	}
	if call.spec.User != "10001" || !call.spec.ReadOnlyRootfs || !call.spec.NoNewPrivileges || len(call.spec.CapDrop) != 1 || call.spec.CapDrop[0] != "ALL" {
		t.Fatalf("insecure runtime spec: %+v", call.spec)
	}
	if len(call.spec.Mounts) != 1 || len(call.spec.Volumes) != 1 || call.spec.Mounts[0] != call.spec.Volumes[0] || call.spec.Mounts[0].Target != "/var/lib/stoneage-ai" || call.spec.Mounts[0].ReadOnly {
		t.Fatalf("unexpected mounts: %+v", call.spec)
	}
	if len(call.spec.Command) != 2 || call.spec.Command[0] != "-profile" || call.spec.Command[1] != "profile-1" {
		t.Fatalf("unexpected runner command: %#v", call.spec.Command)
	}
	if call.spec.Environment["CODEX_HOME"] != "" || call.spec.Environment["STONEAGE_AI_STATE_ROOT"] != "/var/lib/stoneage-ai" || call.spec.Environment["STONEAGE_AI_CODEX_BINARY"] != "/usr/local/bin/codex" {
		t.Fatalf("runtime environment boundary violated: %#v", call.spec.Environment)
	}
	if !strings.Contains(string(call.payload), request.Model.APIKey) || !strings.Contains(string(call.payload), request.MCP.Token) {
		t.Fatal("private request credentials were not delivered on stdin")
	}
	entry, err := broker.journal.Get(context.Background(), request.ProfileID, request.RequestID)
	if err != nil || entry.State != RunCompleted {
		t.Fatalf("journal entry=%+v err=%v", entry, err)
	}
	raw, _ := json.Marshal(entry)
	if strings.Contains(string(raw), request.Model.APIKey) || strings.Contains(string(raw), request.MCP.Token) {
		t.Fatalf("journal persisted a credential: %s", raw)
	}
}

func TestBrokerAcceptsCapabilityFreeModelProbe(t *testing.T) {
	docker := &fakeDocker{run: func(_ context.Context, _ RunSpec, payload []byte) (DockerResult, error) {
		var request airunner.ExecuteRequest
		if err := json.Unmarshal(payload, &request); err != nil {
			return DockerResult{}, err
		}
		if !request.Probe || request.MCP != (airunner.MCP{}) || len(request.Skills) != 0 {
			return DockerResult{}, errors.New("probe carried game capability")
		}
		return DockerResult{Stdout: []byte(`{"ok":true,"result":{"thread_id":"probe-thread","last_message":"STONEAGE_CONNECTION_TEST_OK","turn":{"status":"completed"},"process":{"status":"exited","exit_code":0}}}`)}, nil
	}}
	broker := newFakeBroker(t, docker, NewMemoryJournal())
	request := airunner.ExecuteRequest{
		ProfileID: "model-probe", RequestID: "probe-request", Probe: true,
		Run:   airunner.RunRequest{Prompt: "Reply with exactly STONEAGE_CONNECTION_TEST_OK. Do not use tools."},
		Model: airunner.Model{Provider: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-flash", APIKey: "private-probe-key"},
	}
	response, err := broker.Execute(context.Background(), request)
	if err != nil || !response.OK || response.Result == nil || response.Result.LastMessage != "STONEAGE_CONNECTION_TEST_OK" {
		t.Fatalf("probe response=%+v err=%v", response, err)
	}
	calls := docker.Calls()
	if len(calls) != 1 || len(calls[0].spec.Mounts) != 1 || calls[0].spec.User != "10001" || !calls[0].spec.ReadOnlyRootfs || !calls[0].spec.NoNewPrivileges {
		t.Fatalf("probe did not use isolated container spec: %+v", calls)
	}
}

type cancelAtUpdateJournal struct {
	Journal
	cancel context.CancelFunc
}

func (journal cancelAtUpdateJournal) Update(ctx context.Context, entry JournalEntry) error {
	journal.cancel()
	return journal.Journal.Update(ctx, entry)
}

func TestBrokerPersistsObservedCompletionWhenCallerCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := NewMemoryJournal()
	broker := newFakeBroker(t, &fakeDocker{}, cancelAtUpdateJournal{Journal: store, cancel: cancel})
	request := testRequest("cancel-after-exit", "request-1")
	if _, err := broker.Run(ctx, request); err != nil {
		t.Fatalf("caller cancellation discarded observed completion: %v", err)
	}
	entry, err := store.Get(context.Background(), request.ProfileID, request.RequestID)
	if err != nil || entry.State != RunCompleted {
		t.Fatalf("completed turn was left unresolved: state=%s err=%v", entry.State, err)
	}
}

func TestBrokerRejectsRequestHashConflictAndProfileBusy(t *testing.T) {
	docker := &fakeDocker{}
	broker := newFakeBroker(t, docker, NewMemoryJournal())
	request := testRequest("profile-1", "request-1")
	if _, err := broker.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	conflict := request
	conflict.Run.Prompt = "different"
	if _, err := broker.Execute(context.Background(), conflict); !errors.Is(err, ErrJournalConflict) {
		t.Fatalf("conflict error=%v", err)
	}

	blocking := &fakeDocker{started: make(chan struct{})}
	blocking.run = func(ctx context.Context, _ RunSpec, _ []byte) (DockerResult, error) {
		<-ctx.Done()
		return DockerResult{}, ctx.Err()
	}
	busyBroker := newFakeBroker(t, blocking, NewMemoryJournal())
	first := testRequest("profile-busy", "request-1")
	finished := make(chan error, 1)
	firstCtx, cancel := context.WithCancel(context.Background())
	go func() {
		_, err := busyBroker.Execute(firstCtx, first)
		finished <- err
	}()
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("first Docker run did not start")
	}
	second := testRequest("profile-busy", "request-2")
	if _, err := busyBroker.Execute(context.Background(), second); !errors.Is(err, ErrProfileBusy) {
		t.Fatalf("profile busy error=%v", err)
	}
	cancel()
	var canceledErr error
	select {
	case canceledErr = <-finished:
	case <-time.After(time.Second):
		t.Fatal("canceled run did not finish")
	}
	if !errors.Is(canceledErr, ErrRunUnknown) {
		t.Fatalf("canceled run error=%v", canceledErr)
	}
	if len(blocking.Stops()) != 1 {
		t.Fatalf("Stop calls=%d, want one", len(blocking.Stops()))
	}
}

func TestBrokerMarksUncertainOutputUnknownAndStopsContainer(t *testing.T) {
	docker := &fakeDocker{run: func(context.Context, RunSpec, []byte) (DockerResult, error) {
		return DockerResult{Stdout: []byte(`{"ok":true}`), Stderr: []byte("private-api-key")}, nil
	}}
	broker := newFakeBroker(t, docker, NewMemoryJournal())
	request := testRequest("profile-uncertain", "request-1")
	response, err := broker.Execute(context.Background(), request)
	if !errors.Is(err, ErrRunUnknown) || response.OK || response.Error != string(RunUnknown) {
		t.Fatalf("uncertain response=%+v err=%v", response, err)
	}
	if len(docker.Stops()) != 1 {
		t.Fatalf("Stop calls=%d, want one", len(docker.Stops()))
	}
	if _, err := broker.Execute(context.Background(), request); !errors.Is(err, ErrRunUnknown) {
		t.Fatalf("unknown replay error=%v", err)
	}
	if len(docker.Calls()) != 1 {
		t.Fatalf("unknown replay started Docker %d times", len(docker.Calls()))
	}
}

func TestSQLiteJournalPersistsOutcomeAndCrossBrokerProfileClaim(t *testing.T) {
	if err := os.MkdirAll(filepath.Join("build", "ai"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join("build", "ai"), "aibroker-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := filepath.Join(root, "runs.db")
	journalOne, err := OpenSQLiteJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	journalTwo, err := OpenSQLiteJournal(path)
	if err != nil {
		_ = journalOne.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journalOne.Close(); _ = journalTwo.Close() })
	docker := &fakeDocker{}
	brokerOne := newFakeBroker(t, docker, journalOne)
	request := testRequest("profile-persist", "request-1")
	if _, err := brokerOne.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	brokerTwo := newFakeBroker(t, docker, journalTwo)
	if _, err := brokerTwo.Execute(context.Background(), request); err != nil {
		t.Fatalf("reopen replay error=%v", err)
	}
	if got := len(docker.Calls()); got != 1 {
		t.Fatalf("reopened journal started Docker %d times", got)
	}

	// Keep the first profile unresolved while two independent journal/ broker
	// instances race requests. SQLite's partial unique index must admit one.
	raceDocker := &fakeDocker{started: make(chan struct{})}
	raceDocker.run = func(ctx context.Context, _ RunSpec, _ []byte) (DockerResult, error) {
		<-ctx.Done()
		return DockerResult{}, ctx.Err()
	}
	raceOne, err := OpenSQLiteJournal(filepath.Join(root, "race.db"))
	if err != nil {
		t.Fatal(err)
	}
	raceTwo, err := OpenSQLiteJournal(filepath.Join(root, "race.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raceOne.Close(); _ = raceTwo.Close() })
	raceBrokerOne := newFakeBroker(t, raceDocker, raceOne)
	raceBrokerTwo := newFakeBroker(t, raceDocker, raceTwo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, 2)
	go func() {
		_, callErr := raceBrokerOne.Execute(ctx, testRequest("profile-race", "request-a"))
		results <- callErr
	}()
	select {
	case <-raceDocker.started:
	case <-time.After(time.Second):
		t.Fatal("racing Docker run did not start")
	}
	go func() {
		_, callErr := raceBrokerTwo.Execute(context.Background(), testRequest("profile-race", "request-b"))
		results <- callErr
	}()
	cancel()
	var busy bool
	for range 2 {
		select {
		case callErr := <-results:
			if errors.Is(callErr, ErrProfileBusy) {
				busy = true
			}
		case <-time.After(time.Second):
			t.Fatal("racing runs did not finish")
		}
	}
	if !busy {
		t.Fatal("cross-broker profile race did not reject the second run")
	}
}

func TestJournalPathBrokerLockIsExclusiveAndPrivate(t *testing.T) {
	root := testJournalRoot(t)
	path := filepath.Join(root, "runs.db")
	config := Config{Docker: &fakeDocker{}, Image: "registry.example/stoneage-ai@sha256:abc", Network: "stoneage-backend", JournalPath: path}
	first, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(config)
	if second != nil || !errors.Is(err, ErrJournalBusy) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second broker=%v err=%v, want journal busy", second, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := New(config)
	if err != nil {
		t.Fatalf("journal lock was not released: %v", err)
	}
	defer third.Close()
	info, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("journal lock mode=%o, want 600", info.Mode().Perm())
	}
}

func TestBrokerRechecksRecoveredContainerAndMarksTerminalUnknown(t *testing.T) {
	journal := NewMemoryJournal()
	entry := runningJournalEntry("recovered-profile", "request-1")
	if created, err := journal.Create(context.Background(), entry); err != nil || !created {
		t.Fatalf("seed running entry created=%v err=%v", created, err)
	}
	docker := &inspectDocker{states: []DockerContainerState{DockerContainerRunning, DockerContainerRemoving, DockerContainerExited}}
	broker := newFakeBroker(t, docker, journal)
	defer broker.Close()
	if got := len(docker.InspectCalls()); got != 1 {
		t.Fatalf("startup inspect calls=%d, want one", got)
	}
	result, err := broker.Lookup(context.Background(), entry.ProfileID, entry.RequestID)
	if !errors.Is(err, ErrRunRunning) || result.State != RunRunning {
		t.Fatalf("removing recovery result=%+v err=%v", result, err)
	}
	if got := len(docker.InspectCalls()); got != 2 {
		t.Fatalf("removing inspect calls=%d, want two", got)
	}
	result, err = broker.Lookup(context.Background(), entry.ProfileID, entry.RequestID)
	if !errors.Is(err, ErrRunUnknown) || result.State != RunUnknown {
		t.Fatalf("terminal recovery result=%+v err=%v", result, err)
	}
	if got := len(docker.InspectCalls()); got != 3 {
		t.Fatalf("terminal inspect calls=%d, want three", got)
	}
	updated, err := journal.Get(context.Background(), entry.ProfileID, entry.RequestID)
	if err != nil || updated.State != RunUnknown {
		t.Fatalf("recovered journal=%+v err=%v", updated, err)
	}
}

func TestBrokerKeepsRecoveredRunOnDockerQueryFailure(t *testing.T) {
	journal := NewMemoryJournal()
	entry := runningJournalEntry("query-failure-profile", "request-1")
	if created, err := journal.Create(context.Background(), entry); err != nil || !created {
		t.Fatalf("seed running entry created=%v err=%v", created, err)
	}
	docker := &inspectDocker{errors: []error{ErrDocker, ErrDocker}}
	broker := newFakeBroker(t, docker, journal)
	defer broker.Close()
	result, err := broker.Lookup(context.Background(), entry.ProfileID, entry.RequestID)
	if !errors.Is(err, ErrRunRunning) || result.State != RunRunning {
		t.Fatalf("query failure result=%+v err=%v", result, err)
	}
	updated, err := journal.Get(context.Background(), entry.ProfileID, entry.RequestID)
	if err != nil || updated.State != RunRunning {
		t.Fatalf("query failure changed journal=%+v err=%v", updated, err)
	}
	if got := len(docker.InspectCalls()); got != 2 {
		t.Fatalf("query failure inspect calls=%d, want two", got)
	}
}

func TestBrokerDoesNotReconcileRunClaimedAfterStartup(t *testing.T) {
	journal := NewMemoryJournal()
	old := runningJournalEntry("old-profile", "request-1")
	if created, err := journal.Create(context.Background(), old); err != nil || !created {
		t.Fatalf("seed running entry created=%v err=%v", created, err)
	}
	docker := &inspectDocker{states: []DockerContainerState{DockerContainerRunning}, fakeDocker: fakeDocker{}}
	broker := newFakeBroker(t, docker, journal)
	defer broker.Close()
	if _, err := broker.Execute(context.Background(), testRequest("new-profile", "request-1")); err != nil {
		t.Fatalf("new request failed: %v", err)
	}
	if got := len(docker.InspectCalls()); got != 1 {
		t.Fatalf("new claim triggered recovery inspect calls=%d, want startup-only one", got)
	}
}

func TestJournalCompareAndUpdateDoesNotOverwriteCompleted(t *testing.T) {
	tests := []struct {
		name    string
		journal Journal
		close   func()
	}{
		{name: "memory", journal: NewMemoryJournal()},
	}
	root := testJournalRoot(t)
	sqlite, err := OpenSQLiteJournal(filepath.Join(root, "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	tests = append(tests, struct {
		name    string
		journal Journal
		close   func()
	}{name: "sqlite", journal: sqlite, close: func() { _ = sqlite.Close() }})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.close != nil {
				t.Cleanup(test.close)
			}
			entry := runningJournalEntry("cas-profile-"+test.name, "request-1")
			entry.State = RunCompleted
			entry.Response = []byte(`{"ok":true}`)
			if created, err := test.journal.Create(context.Background(), entry); err != nil || !created {
				t.Fatalf("seed completed entry created=%v err=%v", created, err)
			}
			candidate := entry
			candidate.State = RunUnknown
			candidate.ErrorCode = string(RunUnknown)
			candidate.Response = marshalUnknownResponse(entry.ProfileID, entry.RequestID)
			err := test.journal.(JournalCAS).UpdateIfState(context.Background(), RunRunning, candidate)
			if !errors.Is(err, ErrJournalState) {
				t.Fatalf("CAS error=%v, want state changed", err)
			}
			// Even a caller holding the current state cannot revise a terminal
			// result, including replacing its response while retaining completed.
			for _, target := range []RunState{RunRunning, RunUnknown, RunCompleted} {
				candidate.State = target
				if err := test.journal.(JournalCAS).UpdateIfState(context.Background(), RunCompleted, candidate); !errors.Is(err, ErrJournalState) {
					t.Fatalf("completed CAS to %s error=%v, want immutable outcome", target, err)
				}
				if err := test.journal.Update(context.Background(), candidate); !errors.Is(err, ErrJournalState) {
					t.Fatalf("completed update to %s error=%v", target, err)
				}
			}
			updated, err := test.journal.Get(context.Background(), entry.ProfileID, entry.RequestID)
			if err != nil || updated.State != RunCompleted || string(updated.Response) != string(entry.Response) || updated.ErrorCode != entry.ErrorCode {
				t.Fatalf("completed entry was overwritten: %+v err=%v", updated, err)
			}
		})
	}
}
