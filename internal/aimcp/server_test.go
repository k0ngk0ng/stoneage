package aimcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	mu         sync.Mutex
	binding    Binding
	observes   int
	actions    int
	lastAction TypedAction
	err        error
	wrongID    bool
}

func (fake *fakeBackend) Observe(_ context.Context, binding Binding) (Observation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.binding = binding
	fake.observes++
	if fake.err != nil {
		return Observation{}, fake.err
	}
	id := binding.CharacterID
	if fake.wrongID {
		id = "another-character"
	}
	return Observation{Revision: 7, CharacterID: id, Connected: true, Ready: true, UnlimitedFunds: true, Character: Entity{ID: id, Level: 12, Alive: true}}, nil
}

func (fake *fakeBackend) QueryKnowledge(context.Context, Binding, KnowledgeQuery) (KnowledgeResult, error) {
	if fake.err != nil {
		return KnowledgeResult{}, fake.err
	}
	return KnowledgeResult{Revision: "test-revision", Kind: "facts", Entries: []KnowledgeEntry{{ID: "fact-1", Verified: true}}}, nil
}

func (fake *fakeBackend) StartTask(context.Context, Binding, TaskRequest) (TaskReceipt, error) {
	if fake.err != nil {
		return TaskReceipt{}, fake.err
	}
	return TaskReceipt{Handle: "task-1", Status: ReceiptPending, Evidence: json.RawMessage(`{"server":"accepted"}`)}, nil
}

func (fake *fakeBackend) StartLeveling(context.Context, Binding, LevelingRequest) (TaskReceipt, error) {
	if fake.err != nil {
		return TaskReceipt{}, fake.err
	}
	return TaskReceipt{Handle: "level-1", Status: ReceiptPending, Evidence: json.RawMessage(`{"server":"accepted"}`)}, nil
}

func (fake *fakeBackend) TaskStatus(context.Context, Binding, string) (TaskReceipt, error) {
	if fake.err != nil {
		return TaskReceipt{}, fake.err
	}
	return TaskReceipt{Handle: "task-1", Status: ReceiptConfirmed, Evidence: json.RawMessage(`{"server":"complete"}`)}, nil
}

func (fake *fakeBackend) Cancel(context.Context, Binding, CancelRequest) (TaskReceipt, error) {
	if fake.err != nil {
		return TaskReceipt{}, fake.err
	}
	return TaskReceipt{Handle: "task-1", Status: ReceiptCancelled, Evidence: json.RawMessage(`{"server":"cancelled"}`)}, nil
}

func (fake *fakeBackend) GameAction(_ context.Context, binding Binding, action TypedAction) (ActionReceipt, error) {
	fake.mu.Lock()
	fake.binding = binding
	fake.actions++
	fake.lastAction = action
	fake.mu.Unlock()
	if fake.err != nil {
		return ActionReceipt{}, fake.err
	}
	return ActionReceipt{Status: ReceiptConfirmed, Evidence: json.RawMessage(`{"server":"accepted"}`)}, nil
}

func testServer(t *testing.T, backend Backend) (*Server, Binding) {
	t.Helper()
	binding := Binding{AccountID: "account-hidden", CharacterID: "char-fixed", CharacterName: "Hero", Generation: 41}
	server, err := NewServer(backend, binding, Config{})
	if err != nil {
		t.Fatal(err)
	}
	return server, binding
}

func rpcLine(t *testing.T, output []byte, index int) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if index >= len(lines) {
		t.Fatalf("response %d missing in %q", index, string(output))
	}
	var value map[string]any
	if err := json.Unmarshal([]byte(lines[index]), &value); err != nil {
		t.Fatalf("response %d: %v", index, err)
	}
	return value
}

func TestServerHandshakeToolsAndBinding(t *testing.T) {
	fake := &fakeBackend{}
	server, binding := testServer(t, fake)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{"_meta":{"progressToken":0}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"game_observe","arguments":{},"_meta":{"progressToken":1}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if got := rpcLine(t, output.Bytes(), 0)["result"].(map[string]any)["protocolVersion"]; got != ProtocolVersion {
		t.Fatalf("protocol version = %v", got)
	}
	if _, ok := rpcLine(t, output.Bytes(), 1)["result"]; !ok {
		t.Fatalf("ping response has no result: %s", output.String())
	}
	tools := rpcLine(t, output.Bytes(), 2)["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 7 {
		t.Fatalf("tool count = %d, want 7", len(tools))
	}
	call := rpcLine(t, output.Bytes(), 3)["result"].(map[string]any)["structuredContent"].(map[string]any)
	if call["character_id"] != binding.CharacterID {
		t.Fatalf("observation character = %v", call["character_id"])
	}
	if unlimited, ok := call["unlimited_funds"].(bool); !ok || !unlimited {
		t.Fatalf("observation funding flag = %#v", call["unlimited_funds"])
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.binding != binding || fake.observes != 1 {
		t.Fatalf("backend binding = %+v observes=%d, want %+v/1", fake.binding, fake.observes, binding)
	}
}

func TestServerRejectsRawRoleAndNotificationActions(t *testing.T) {
	fake := &fakeBackend{}
	server, _ := testServer(t, fake)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"game_action","arguments":{"kind":"raw","function":"ClientLogin"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"game_action","arguments":{"kind":"chat","text":"hi","character_id":"other"}}}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"game_action","arguments":{"kind":"chat","text":"late"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	first := rpcLine(t, output.Bytes(), 1)
	if first["error"].(map[string]any)["code"] != float64(-32602) {
		t.Fatalf("raw action response = %s", output.String())
	}
	second := rpcLine(t, output.Bytes(), 2)
	if second["error"].(map[string]any)["code"] != float64(-32602) {
		t.Fatalf("role parameter response = %s", output.String())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.actions != 0 {
		t.Fatalf("rejected/notification actions reached backend: %d", fake.actions)
	}
}

func TestTypedActionCarriesObservationRevision(t *testing.T) {
	fake := &fakeBackend{}
	server, _ := testServer(t, fake)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"game_action","arguments":{"kind":"chat","expected_revision":9,"text":"hello"}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), `"isError":true`) || strings.Contains(output.String(), `"error":{"code"`) {
		t.Fatalf("typed action rejected: %s", output.String())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.actions != 1 || fake.lastAction.ExpectedRevision != 9 || fake.lastAction.Kind != "chat" {
		t.Fatalf("backend action = %+v count=%d", fake.lastAction, fake.actions)
	}
}

func TestTypedLookActionCarriesDirection(t *testing.T) {
	fake := &fakeBackend{}
	server, _ := testServer(t, fake)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"game_action","arguments":{"kind":"look","expected_revision":9,"direction":4}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), `"isError":true`) || strings.Contains(output.String(), `"error":{"code"`) {
		t.Fatalf("typed look action rejected: %s", output.String())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.actions != 1 || fake.lastAction.Kind != "look" || fake.lastAction.Direction != 4 {
		t.Fatalf("backend look action = %+v count=%d", fake.lastAction, fake.actions)
	}
}

func TestServerRejectsDuplicateJSONKeys(t *testing.T) {
	fake := &fakeBackend{}
	server, _ := testServer(t, fake)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"game_action","name":"game_observe","arguments":{}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), `"code":-32600`) != 1 {
		t.Fatalf("duplicate request was not rejected: %s", output.String())
	}
}

func TestServerSanitizesBackendErrorsAndWrongRole(t *testing.T) {
	fake := &fakeBackend{err: errors.New("token=super-secret password=do-not-return")}
	server, _ := testServer(t, fake)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"game_observe","arguments":{}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "super-secret") || strings.Contains(output.String(), "do-not-return") {
		t.Fatalf("backend secret leaked: %s", output.String())
	}
	if !strings.Contains(output.String(), "game operation failed") {
		t.Fatalf("sanitized error missing: %s", output.String())
	}

	fake = &fakeBackend{wrongID: true}
	server, _ = testServer(t, fake)
	output.Reset()
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "another-character") {
		t.Fatalf("foreign character leaked in output: %s", output.String())
	}
}

func TestRemoteBackendBindsCapabilityAndRejectsRedirect(t *testing.T) {
	var gotAuth string
	var gotBody string
	token := strings.Repeat("a", 43)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotAuth = request.Header.Get("Authorization")
		data, _ := ioReadAll(request.Body)
		gotBody = string(data)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"result":{"revision":3,"character_id":"char-fixed","connected":true,"ready":true,"character":{"id":"char-fixed","level":1,"alive":true},"gold":0,"unlimited_funds":true}}`))
	}))
	defer server.Close()
	endpoint := server.URL + "/v1/game"
	backend, err := NewRemoteBackend(RemoteBackendConfig{Endpoint: endpoint, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{AccountID: "hidden", CharacterID: "char-fixed", Generation: 2}
	observation, err := backend.Observe(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.UnlimitedFunds || observation.Gold != 0 {
		t.Fatalf("remote observation funding = %+v", observation)
	}
	if gotAuth != "Bearer "+token || strings.Contains(gotBody, "char-fixed") || strings.Contains(gotBody, "hidden") || strings.Contains(gotBody, "2") {
		t.Fatalf("remote request leaked binding/auth: auth=%q body=%q", gotAuth, gotBody)
	}

	redirect := httptest.NewServer(http.RedirectHandler(endpoint, http.StatusTemporaryRedirect))
	defer redirect.Close()
	redirectBackend, err := NewRemoteBackend(RemoteBackendConfig{Endpoint: redirect.URL + "/v1/game", Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := redirectBackend.Observe(context.Background(), binding); !errors.Is(err, ErrBackend) {
		t.Fatalf("redirect error = %v, want ErrBackend", err)
	}
}

func TestRemoteBackendNormalizesRequestTimeout(t *testing.T) {
	token := strings.Repeat("a", 43)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"result":{"revision":1,"character_id":"char-fixed","connected":true,"ready":true,"character":{"id":"char-fixed","level":1,"alive":true}}}`))
	}))
	defer server.Close()
	defaultBackend, err := NewRemoteBackend(RemoteBackendConfig{Endpoint: server.URL + "/v1/game", Token: token})
	if err != nil {
		t.Fatal(err)
	}
	if defaultBackend.client.Timeout != defaultRemoteTimeout {
		t.Fatalf("default remote timeout = %v, want %v", defaultBackend.client.Timeout, defaultRemoteTimeout)
	}
	shortBackend, err := NewRemoteBackend(RemoteBackendConfig{Endpoint: server.URL + "/v1/game", Token: token, RequestTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if shortBackend.client.Timeout != 2*time.Second {
		t.Fatalf("explicit remote timeout = %v", shortBackend.client.Timeout)
	}
	if _, err := NewRemoteBackend(RemoteBackendConfig{Endpoint: server.URL + "/v1/game", Token: token, RequestTimeout: maxRemoteTimeout + time.Second}); !errors.Is(err, ErrBackend) {
		t.Fatalf("oversized remote timeout = %v, want ErrBackend", err)
	}
}

// Keep the test independent of Go versions where httptest response bodies
// expose a differently named helper in generated docs.
func ioReadAll(reader io.Reader) ([]byte, error) {
	var buffer bytes.Buffer
	_, err := buffer.ReadFrom(reader)
	return buffer.Bytes(), err
}
