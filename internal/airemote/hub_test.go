package airemote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

type fakeDocker struct {
	runs  int
	stops int
}

func (d *fakeDocker) Run(context.Context, aibroker.RunSpec, []byte) (aibroker.DockerResult, error) {
	d.runs++
	return aibroker.DockerResult{ExitCode: 0, Stdout: []byte(`{"ok":true}`)}, nil
}
func (d *fakeDocker) Stop(context.Context, string) error { d.stops++; return nil }

type failingRemoteStore struct {
	*MemoryStore
	fail bool
}

func (s *failingRemoteStore) Save(ctx context.Context, snapshot Snapshot) error {
	if s.fail {
		return errors.New("fixture route store failure")
	}
	return s.MemoryStore.Save(ctx, snapshot)
}

type remoteHarness struct {
	hub    *Hub
	worker ConnectResponse
}

func newHarness(t *testing.T) remoteHarness {
	t.Helper()
	hub, err := New(Config{Docker: &fakeDocker{}, PublicBaseURL: "https://public.example", GatewayURL: "http://gateway:8081/v1/game", LeaseTimeout: time.Second, CommandTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := hub.Invite(context.Background(), "profile-1", "https://public.example")
	if err != nil {
		t.Fatal(err)
	}
	response, err := hub.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-1", ProfileID: "profile-1", EnrollmentToken: invite.Token})
	if err != nil {
		t.Fatal(err)
	}
	return remoteHarness{hub: hub, worker: response}
}

func TestInviteRotatesConsumedOfflineEnrollment(t *testing.T) {
	store := NewMemoryStore()
	hub, err := New(Config{Docker: &fakeDocker{}, Store: store, LeaseTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	first, err := hub.Invite(context.Background(), "profile-1", "https://public.example")
	if err != nil {
		t.Fatal(err)
	}
	connected, err := hub.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-1", ProfileID: "profile-1", EnrollmentToken: first.Token})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	second, err := hub.Invite(context.Background(), "profile-1", "https://public.example")
	if err != nil {
		t.Fatal(err)
	}
	if second.Token == "" || second.Token == first.Token {
		t.Fatalf("rotated invitation token=%q first=%q", second.Token, first.Token)
	}
	if second.Reconnect {
		t.Fatal("offline enrollment was returned as reconnect")
	}
	reloaded, err := New(Config{Docker: &fakeDocker{}, Store: store, LeaseTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("reload rotated enrollment: %v", err)
	}
	status, err := reloaded.Status(context.Background(), "profile-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.WorkerID != "" || status.Epoch != connected.Epoch+1 {
		t.Fatalf("rotated status=%+v, want empty worker and epoch %d", status, connected.Epoch+1)
	}
	if _, err := reloaded.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-1", ProfileID: "profile-1", SessionToken: connected.SessionToken, Epoch: connected.Epoch}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("old session reconnect error=%v, want unauthorized", err)
	}
	newWorker, err := reloaded.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-2", ProfileID: "profile-1", EnrollmentToken: second.Token})
	if err != nil {
		t.Fatalf("new enrollment connect: %v", err)
	}
	if newWorker.Epoch != connected.Epoch+2 {
		t.Fatalf("new worker epoch=%d, want %d", newWorker.Epoch, connected.Epoch+2)
	}
	for _, mixed := range []ProfileRecord{
		{ProfileID: "profile-1", TokenHash: strings.Repeat("0", 64), WorkerID: "worker-1", Epoch: connected.Epoch + 1},
		{ProfileID: "profile-1", TokenHash: strings.Repeat("0", 64), SessionHash: strings.Repeat("0", 64), Epoch: connected.Epoch + 1},
	} {
		corrupt := NewMemoryStore()
		if err := corrupt.Save(context.Background(), Snapshot{Profiles: []ProfileRecord{mixed}}); err != nil {
			t.Fatal(err)
		}
		if _, err := New(Config{Store: corrupt}); !errors.Is(err, aibroker.ErrInvalidConfig) {
			t.Fatalf("mixed profile record=%+v error=%v, want invalid config", mixed, err)
		}
	}
}

func TestHubInspectRestoresDurableTerminalRouteAfterWorkerReplacement(t *testing.T) {
	tests := []struct {
		name       string
		routeState string
		wantState  aibroker.DockerContainerState
		wantErr    error
	}{
		{name: "exited", routeState: string(aibroker.DockerContainerExited), wantState: aibroker.DockerContainerExited},
		{name: "dead", routeState: string(aibroker.DockerContainerDead), wantState: aibroker.DockerContainerDead},
		{name: "stopped", routeState: "stopped", wantState: aibroker.DockerContainerDead},
		{name: "removed", routeState: "removed", wantErr: aibroker.ErrContainerNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			hub, err := New(Config{Store: store, LeaseTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			invite, err := hub.Invite(context.Background(), "profile-1", "https://public.example")
			if err != nil {
				t.Fatal(err)
			}
			oldWorker, err := hub.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-old", ProfileID: "profile-1", EnrollmentToken: invite.Token})
			if err != nil {
				t.Fatal(err)
			}
			if err := hub.saveRoute(RouteRecord{
				ProfileID: "profile-1", WorkerID: oldWorker.WorkerID, WorkerEpoch: oldWorker.Epoch,
				RequestID: "request-1", ContainerName: "stoneage-run-terminal", VolumeName: "stoneage-profile-1",
				PayloadHash: strings.Repeat("a", 64), State: test.routeState, UpdatedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}

			// Reloading loses the in-memory old worker while retaining its durable
			// route and profile binding. Invite then explicitly fences that old
			// enrollment before the replacement worker connects.
			reloaded, err := New(Config{Store: store, LeaseTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			rotated, err := reloaded.Invite(context.Background(), "profile-1", "https://public.example")
			if err != nil || rotated.Token == "" {
				t.Fatalf("rotate offline enrollment: invitation=%+v err=%v", rotated, err)
			}
			if _, err := reloaded.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-new", ProfileID: "profile-1", EnrollmentToken: rotated.Token}); err != nil {
				t.Fatal(err)
			}
			got, err := reloaded.Inspect(context.Background(), "stoneage-run-terminal")
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("inspect error=%v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.wantState {
				t.Fatalf("inspect state=%q err=%v, want state=%q", got, err, test.wantState)
			}
		})
	}
}

func TestHubInspectRejectsOfflineNonTerminalRouteAfterWorkerReplacement(t *testing.T) {
	for _, routeState := range []string{string(aibroker.RunRunning), string(aibroker.RunUnknown)} {
		t.Run(routeState, func(t *testing.T) {
			store := NewMemoryStore()
			hub, err := New(Config{Store: store, LeaseTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			invite, err := hub.Invite(context.Background(), "profile-1", "https://public.example")
			if err != nil {
				t.Fatal(err)
			}
			oldWorker, err := hub.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-old", ProfileID: "profile-1", EnrollmentToken: invite.Token})
			if err != nil {
				t.Fatal(err)
			}
			if err := hub.saveRoute(RouteRecord{
				ProfileID: "profile-1", WorkerID: oldWorker.WorkerID, WorkerEpoch: oldWorker.Epoch,
				RequestID: "request-1", ContainerName: "stoneage-run-nonterminal", VolumeName: "stoneage-profile-1",
				PayloadHash: strings.Repeat("b", 64), State: routeState, UpdatedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}

			reloaded, err := New(Config{Store: store, LeaseTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			rotated, err := reloaded.Invite(context.Background(), "profile-1", "https://public.example")
			if err != nil || rotated.Token == "" {
				t.Fatalf("rotate offline enrollment: invitation=%+v err=%v", rotated, err)
			}
			if _, err := reloaded.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-new", ProfileID: "profile-1", EnrollmentToken: rotated.Token}); err != nil {
				t.Fatal(err)
			}
			if _, err := reloaded.Inspect(context.Background(), "stoneage-run-nonterminal"); !errors.Is(err, aibroker.ErrDocker) {
				t.Fatalf("inspect state=%q err=%v, want Docker error", routeState, err)
			}
		})
	}
}

func TestHubRouteStateSaveFailureDoesNotCreateTerminalProof(t *testing.T) {
	store := &failingRemoteStore{MemoryStore: NewMemoryStore()}
	hub, err := New(Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	route := RouteRecord{
		ProfileID: "profile-1", WorkerID: "worker-old", WorkerEpoch: 1,
		RequestID: "request-1", ContainerName: "stoneage-run-save-failure", VolumeName: "stoneage-profile-1",
		PayloadHash: strings.Repeat("c", 64), State: string(aibroker.RunRunning), UpdatedAt: time.Now().UTC(),
	}
	if err := hub.saveRoute(route); err != nil {
		t.Fatal(err)
	}
	store.fail = true
	if err := hub.updateRouteState(route.ContainerName, string(aibroker.DockerContainerExited)); !errors.Is(err, aibroker.ErrDocker) {
		t.Fatalf("failed route update error=%v, want Docker error", err)
	}
	inMemory, _, err := hub.routeForContainer(route.ContainerName)
	if err != nil || inMemory.State != string(aibroker.RunRunning) {
		t.Fatalf("failed route update changed memory route=%+v err=%v", inMemory, err)
	}
	persisted, err := store.Load(context.Background())
	if err != nil || len(persisted.Routes) != 1 || persisted.Routes[0].State != string(aibroker.RunRunning) {
		t.Fatalf("failed route update changed durable route=%+v err=%v", persisted.Routes, err)
	}
	store.fail = false
	if err := hub.updateRouteState(route.ContainerName, string(aibroker.DockerContainerExited)); err != nil {
		t.Fatal(err)
	}
	persisted, err = store.Load(context.Background())
	if err != nil || len(persisted.Routes) != 1 || persisted.Routes[0].State != string(aibroker.DockerContainerExited) {
		t.Fatalf("successful route update was not durable route=%+v err=%v", persisted.Routes, err)
	}
}

func TestHubDurableTerminalRouteSupportsBrokerUnknownReviewAfterWorkerReplacement(t *testing.T) {
	store := NewMemoryStore()
	hub, err := New(Config{Store: store, LeaseTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := hub.Invite(context.Background(), "profile-1", "https://public.example")
	if err != nil {
		t.Fatal(err)
	}
	oldWorker, err := hub.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-old", ProfileID: "profile-1", EnrollmentToken: invite.Token})
	if err != nil {
		t.Fatal(err)
	}
	const containerName = "stoneage-run-review-terminal"
	const requestID = "request-review"
	if err := hub.saveRoute(RouteRecord{
		ProfileID: "profile-1", WorkerID: oldWorker.WorkerID, WorkerEpoch: oldWorker.Epoch,
		RequestID: requestID, ContainerName: containerName, VolumeName: "stoneage-profile-1",
		PayloadHash: strings.Repeat("d", 64), State: string(aibroker.DockerContainerExited), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(Config{Store: store, LeaseTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := reloaded.Invite(context.Background(), "profile-1", "https://public.example")
	if err != nil || rotated.Token == "" {
		t.Fatalf("rotate offline enrollment: invitation=%+v err=%v", rotated, err)
	}
	if _, err := reloaded.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-new", ProfileID: "profile-1", EnrollmentToken: rotated.Token}); err != nil {
		t.Fatal(err)
	}

	journal := aibroker.NewMemoryJournal()
	entry := aibroker.JournalEntry{ProfileID: "profile-1", RequestID: requestID, PayloadHash: strings.Repeat("d", 64), State: aibroker.RunUnknown, ContainerName: containerName, VolumeName: "stoneage-profile-1", UpdatedAt: time.Now().UTC()}
	if created, err := journal.Create(context.Background(), entry); err != nil || !created {
		t.Fatalf("seed unknown journal: created=%v err=%v", created, err)
	}
	broker, err := aibroker.New(aibroker.Config{Docker: reloaded, Journal: journal, Image: "fixture:local", Network: "none"})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	ready, err := broker.UnknownReviewReady(context.Background(), entry.ProfileID, entry.RequestID, entry.UpdatedAt)
	if err != nil || !ready {
		t.Fatalf("unknown review readiness=%v err=%v", ready, err)
	}
	reviewed, err := broker.ReviewUnknown(context.Background(), entry.ProfileID, entry.RequestID, entry.UpdatedAt, "operator", aibroker.ReviewReasonAcceptUncertainOutcome)
	if err != nil || reviewed.Review == nil {
		t.Fatalf("unknown review result=%+v err=%v", reviewed, err)
	}
	if reviewed.State != aibroker.RunUnknown {
		t.Fatalf("unknown review changed outcome state=%s", reviewed.State)
	}
	route, _, err := reloaded.routeForContainer(containerName)
	if err != nil || route.State != string(aibroker.DockerContainerExited) {
		t.Fatalf("unknown review removed/changed route=%+v err=%v", route, err)
	}
}

func testPayload(t *testing.T) []byte {
	t.Helper()
	request := airunner.ExecuteRequest{ProfileID: "profile-1", RequestID: "request-1", Run: airunner.RunRequest{Prompt: "observe"}, Model: airunner.Model{Provider: "deepseek", BaseURL: "https://api.deepseek.com", Model: "deepseek-flash", APIKey: "secret-key"}, MCP: airunner.MCP{Endpoint: "http://gateway/v1/game", Token: strings.Repeat("g", 43), CharacterID: "character-1"}}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func spec() aibroker.RunSpec {
	return aibroker.RunSpec{ContainerName: "stoneage-run-1", VolumeName: "stoneage-profile-1", Environment: map[string]string{"STONEAGE_AI_PROFILE_ID": "profile-1"}}
}

func pollCommand(t *testing.T, hub *Hub, worker ConnectResponse) *Command {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/ai/worker/poll", strings.NewReader(`{"protocol_version":1,"worker_id":"worker-1","profile_id":"profile-1","epoch":1,"wait_seconds":1}`))
	request.Header.Set("Authorization", "Bearer "+worker.SessionToken)
	request.Header.Set("X-StoneAge-Worker-ID", worker.WorkerID)
	request.Header.Set("X-StoneAge-Profile-ID", worker.ProfileID)
	request.Header.Set("X-StoneAge-Worker-Epoch", "1")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { hub.Handler().ServeHTTP(response, request); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poll timeout")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("poll status=%d body=%s", response.Code, response.Body)
	}
	var poll PollResponse
	if err := json.Unmarshal(response.Body.Bytes(), &poll); err != nil {
		t.Fatal(err)
	}
	if poll.Command == nil {
		t.Fatal("poll returned no command")
	}
	return poll.Command
}

func TestHubRoutesRunAndRedactsPayloadFromStore(t *testing.T) {
	h := newHarness(t)
	payload := testPayload(t)
	result := make(chan aibroker.DockerResult, 1)
	errorsOut := make(chan error, 1)
	go func() {
		value, err := h.hub.Run(context.Background(), spec(), payload)
		result <- value
		errorsOut <- err
	}()
	command := pollCommand(t, h.hub, h.worker)
	if command.Kind != KindRun || command.PayloadHash == "" || !strings.Contains(string(command.Payload), "secret-key") {
		t.Fatalf("bad run command: %+v", command)
	}
	if command.GameEndpoint != "https://public.example/api/ai/worker/v1/game" {
		t.Fatalf("game endpoint=%q", command.GameEndpoint)
	}
	var response ResultRequest
	response.ProtocolVersion, response.CommandID, response.WorkerID, response.WorkerEpoch, response.ProfileID, response.RequestID, response.ContainerName, response.PayloadHash, response.State = ProtocolVersion, command.CommandID, h.worker.WorkerID, h.worker.Epoch, command.ProfileID, command.RequestID, command.ContainerName, command.PayloadHash, "exited"
	response.ExitCode, response.Stdout = 0, []byte(`{"ok":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/ai/worker/result", strings.NewReader(mustJSON(response)))
	req.Header.Set("Authorization", "Bearer "+h.worker.SessionToken)
	req.Header.Set("X-StoneAge-Worker-ID", h.worker.WorkerID)
	req.Header.Set("X-StoneAge-Profile-ID", h.worker.ProfileID)
	req.Header.Set("X-StoneAge-Worker-Epoch", "1")
	r := httptest.NewRecorder()
	h.hub.Handler().ServeHTTP(r, req)
	if r.Code != http.StatusOK {
		t.Fatalf("result status=%d body=%s", r.Code, r.Body)
	}
	if err := <-errorsOut; err != nil {
		t.Fatalf("run error=%v", err)
	}
	got := <-result
	if got.ExitCode != 0 || string(got.Stdout) != `{"ok":true}` {
		t.Fatalf("result=%+v", got)
	}
	snapshot, err := h.hub.store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(snapshot)
	if strings.Contains(string(raw), "secret-key") {
		t.Fatal("route store persisted payload credential")
	}
}

func TestHubBoundProfileDoesNotFallbackWhenWorkerOffline(t *testing.T) {
	fallback := &fakeDocker{}
	hub, err := New(Config{Docker: fallback, CommandTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := hub.Invite(context.Background(), "profile-1", "https://public.example")
	if err != nil {
		t.Fatal(err)
	}
	_, err = hub.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-1", ProfileID: "profile-1", EnrollmentToken: invite.Token})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = hub.Run(ctx, spec(), testPayload(t))
	if !errors.Is(err, aibroker.ErrDocker) {
		t.Fatalf("offline run error=%v", err)
	}
	if fallback.runs != 0 {
		t.Fatal("remote profile fell back to local Docker")
	}
}

func TestHubUnenrolledProfileUsesFallback(t *testing.T) {
	fallback := &fakeDocker{}
	hub, err := New(Config{Docker: fallback})
	if err != nil {
		t.Fatal(err)
	}
	result, err := hub.Run(context.Background(), spec(), testPayload(t))
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("fallback result=%+v err=%v", result, err)
	}
	if fallback.runs != 1 {
		t.Fatalf("fallback runs=%d", fallback.runs)
	}
}

func TestHubGameProxyUsesFixedGateway(t *testing.T) {
	var gotProfile, gotAuth string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotProfile, gotAuth = r.Header.Get("X-StoneAge-Profile-ID"), r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer gateway.Close()
	hub, err := New(Config{GatewayURL: gateway.URL})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := hub.Invite(context.Background(), "profile-1", "https://public.example")
	if err != nil {
		t.Fatal(err)
	}
	_, err = hub.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-1", ProfileID: "profile-1", EnrollmentToken: invite.Token})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/ai/worker/v1/game", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("g", 43))
	rec := httptest.NewRecorder()
	hub.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy status=%d", rec.Code)
	}
	if gotProfile != "" || gotAuth == "" {
		t.Fatalf("proxy headers profile=%q auth=%q", gotProfile, gotAuth)
	}
}

func waitUntilHub(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func mustJSON(value any) string { raw, _ := json.Marshal(value); return string(raw) }

func TestHubConnectKeepsSessionWhenStartFailsAndOnlyGuardsEnrollment(t *testing.T) {
	var guards, starts int
	hub, err := New(Config{Docker: &fakeDocker{}, Guard: func(context.Context, string) error { guards++; return nil }, Start: func(context.Context, string) error { starts++; return errors.New("start rejected") }})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := hub.Invite(context.Background(), "profile-1", "https://public.example")
	if err != nil {
		t.Fatal(err)
	}
	connectedResponse, connectErr := hub.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-1", ProfileID: "profile-1", EnrollmentToken: invite.Token, Start: true})
	fallback := hub.cfg.Docker.(*fakeDocker)
	if fallback.runs != 0 {
		t.Fatal("run during enrollment used fallback Docker")
	}
	if connectErr != nil || connectedResponse.SessionToken == "" || !connectedResponse.StartRequested {
		t.Fatalf("connect response=%+v err=%v", connectedResponse, connectErr)
	}
	poll := httptest.NewRequest(http.MethodPost, "/api/ai/worker/poll", strings.NewReader(`{"protocol_version":1,"worker_id":"worker-1","profile_id":"profile-1","epoch":1,"wait_seconds":1}`))
	poll.Header.Set("Authorization", "Bearer "+connectedResponse.SessionToken)
	poll.Header.Set("X-StoneAge-Worker-ID", "worker-1")
	poll.Header.Set("X-StoneAge-Profile-ID", "profile-1")
	poll.Header.Set("X-StoneAge-Worker-Epoch", "1")
	rec := httptest.NewRecorder()
	go hub.Handler().ServeHTTP(rec, poll)
	waitUntilHub(t, time.Second, func() bool {
		status, _ := hub.Status(context.Background(), "profile-1")
		return status.StartError == "start_failed" && status.StartPhase == "start" && status.StartStage == "start" && status.StartCode == "start_failed" && status.StartDurationMS >= 0
	})
	if guards != 1 || starts != 1 {
		t.Fatalf("callbacks guards=%d starts=%d", guards, starts)
	}
	status, err := hub.Status(context.Background(), "profile-1")
	if err != nil || status.StartPhase != "start" || status.StartStage != "start" || status.StartCode != "start_failed" {
		t.Fatalf("start diagnostics=%+v err=%v", status, err)
	}
	reconnected, err := hub.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-1", ProfileID: "profile-1", SessionToken: connectedResponse.SessionToken, Epoch: connectedResponse.Epoch})
	if err != nil {
		t.Fatal(err)
	}
	if reconnected.SessionToken != connectedResponse.SessionToken || guards != 1 || starts != 1 {
		t.Fatalf("reconnect response=%+v guards=%d starts=%d", reconnected, guards, starts)
	}
}

func TestHubEnrollmentRetryUsesPendingSessionAndCannotReuseTokenAlone(t *testing.T) {
	store := NewMemoryStore()
	guards := 0
	hub, err := New(Config{Store: store, Guard: func(context.Context, string) error { guards++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := hub.Invite(context.Background(), "profile-1", "https://public.example")
	if err != nil {
		t.Fatal(err)
	}
	pendingSession := strings.Repeat("s", 43)
	first, err := hub.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-1", ProfileID: "profile-1", EnrollmentToken: invite.Token, SessionToken: pendingSession, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionToken != pendingSession || first.Epoch == 0 || guards != 0 {
		t.Fatalf("first enrollment response=%+v guards=%d", first, guards)
	}
	retry, err := hub.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-1", ProfileID: "profile-1", EnrollmentToken: invite.Token, SessionToken: pendingSession, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	if retry.SessionToken != first.SessionToken || retry.Epoch != first.Epoch || guards != 0 {
		t.Fatalf("idempotent enrollment response=%+v first=%+v guards=%d", retry, first, guards)
	}
	if _, err := hub.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-2", ProfileID: "profile-1", EnrollmentToken: invite.Token}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("enrollment token alone/other worker error=%v", err)
	}
	reloaded, err := New(Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	reloadedResponse, err := reloaded.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-1", ProfileID: "profile-1", EnrollmentToken: invite.Token, SessionToken: pendingSession})
	if err != nil {
		t.Fatal(err)
	}
	if reloadedResponse.SessionToken != first.SessionToken || reloadedResponse.Epoch != first.Epoch {
		t.Fatalf("reload enrollment response=%+v first=%+v", reloadedResponse, first)
	}
}

func TestHubPollHonorsRequestCancellation(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/api/ai/worker/poll", strings.NewReader(`{"protocol_version":1,"worker_id":"worker-1","profile_id":"profile-1","epoch":1,"wait_seconds":30}`)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+h.worker.SessionToken)
	request.Header.Set("X-StoneAge-Worker-ID", h.worker.WorkerID)
	request.Header.Set("X-StoneAge-Profile-ID", h.worker.ProfileID)
	request.Header.Set("X-StoneAge-Worker-Epoch", "1")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.hub.Handler().ServeHTTP(response, request)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("poll did not stop after request cancellation")
	}
}

func TestMemoryStoreClonesProfiles(t *testing.T) {
	store := NewMemoryStore()
	snapshot := Snapshot{Profiles: []ProfileRecord{{ProfileID: "profile-1", TokenHash: strings.Repeat("a", 64)}}}
	if err := store.Save(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Profiles[0].ProfileID = "mutated-input"
	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	loaded.Profiles[0].ProfileID = "mutated-output"
	reloaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Profiles[0].ProfileID != "profile-1" {
		t.Fatalf("profile snapshot was aliased: %+v", reloaded.Profiles)
	}
}

func TestHubReloadsFencedProfileAndRejectsCorruptSnapshot(t *testing.T) {
	store := NewMemoryStore()
	hub, err := New(Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := hub.Invite(context.Background(), "profile-1", "https://public.example")
	if err != nil {
		t.Fatal(err)
	}
	connected, err := hub.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-1", ProfileID: "profile-1", EnrollmentToken: invite.Token})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	status, err := reloaded.Status(context.Background(), "profile-1")
	if err != nil || status.WorkerID != "worker-1" || status.Epoch != connected.Epoch || status.Online {
		t.Fatalf("reloaded status=%+v err=%v", status, err)
	}
	if _, err := reloaded.connect(context.Background(), ConnectRequest{ProtocolVersion: ProtocolVersion, WorkerID: "worker-1", ProfileID: "profile-1", SessionToken: connected.SessionToken, Epoch: connected.Epoch}); err != nil {
		t.Fatalf("reconnect after reload error=%v", err)
	}
	corrupt := NewMemoryStore()
	_ = corrupt.Save(context.Background(), Snapshot{Profiles: []ProfileRecord{{ProfileID: "profile-1", Consumed: true, WorkerID: "worker-1", SessionHash: "bad", Epoch: 1, TokenHash: strings.Repeat("0", 64)}}})
	if _, err := New(Config{Store: corrupt}); !errors.Is(err, aibroker.ErrInvalidConfig) {
		t.Fatalf("corrupt snapshot error=%v", err)
	}
}
