package websession

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func unixSocketPath(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "build"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(root, "ws-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return filepath.Join(directory, "agent.sock")
}

func TestIdentityFromSnapshotRequiresCanonicalSlot(t *testing.T) {
	snapshot := aigame.Snapshot{Connected: true, Account: "ai", Character: "Hero", Characters: []aigame.Character{{Slot: 1, Name: "Hero"}}}
	identity, err := identityFromSnapshot(snapshot, "local-line")
	if err != nil {
		t.Fatalf("identityFromSnapshot: %v", err)
	}
	if identity.CharacterID != "ai:1" || identity.ServerID != "local-line" {
		t.Fatalf("identity = %+v", identity)
	}
	snapshot.Characters = append(snapshot.Characters, aigame.Character{Slot: 0, Name: "Hero"})
	if _, err := identityFromSnapshot(snapshot, "local-line"); err == nil {
		t.Fatal("ambiguous character name was accepted")
	}
}

func TestClientPrivateLeaseAPIOverUnixSocket(t *testing.T) {
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer public.Close()

	socketPath := unixSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var mu sync.Mutex
	var seen []string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case AttachPath:
			var input AttachRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.Generation != 1 || input.Identity.CharacterID != "ai:0" {
				t.Errorf("attach request = %+v", input)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(AttachResponse{Token: strings.Repeat("t", 43), Generation: 2, Identity: input.Identity})
		case ObservePath:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ObserveResponse{Snapshot: aigame.Snapshot{Revision: 7}, SessionToken: "incarnation"})
		case ExecutePath, DetachPath:
			w.WriteHeader(http.StatusNoContent)
		case WatchPath:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Shutdown(context.Background())

	client, err := New(Config{BaseURL: public.URL, SocketPath: socketPath, ServerID: "local-line"})
	if err != nil {
		t.Fatal(err)
	}
	identity := Identity{AccountID: "ai", CharacterID: "ai:0", CharacterName: "Hero", ServerID: "local-line"}
	attached, err := client.Attach(context.Background(), AttachRequest{SessionID: "session", Generation: 1, Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	if attached.Generation != 2 || attached.Token == "" {
		t.Fatalf("attach response = %+v", attached)
	}
	observed, err := client.Observe(context.Background(), attached.Token)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Snapshot.SessionToken != "incarnation" || observed.Snapshot.Revision != 7 {
		t.Fatalf("observe response = %+v", observed)
	}
	if err := client.Execute(context.Background(), attached.Token, ExecuteRequest{Revision: 7, Action: aigame.Chat("hello", 0, 3)}); err != nil {
		t.Fatal(err)
	}
	if err := client.Detach(context.Background(), attached.Token); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	joined := strings.Join(seen, "\n")
	mu.Unlock()
	if !strings.Contains(joined, "POST "+AttachPath) || !strings.Contains(joined, "Bearer "+attached.Token) {
		t.Fatalf("private requests = %q", joined)
	}
}

func TestClientBindReturnsLeaseOnlyBoundSession(t *testing.T) {
	publicCalls := make(chan string, 1)
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicCalls <- r.Method + " " + r.URL.Path
		http.NotFound(w, r)
	}))
	defer public.Close()

	socketPath := unixSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	identity := Identity{AccountID: "ai", CharacterID: "ai:0", CharacterName: "Hero", ServerID: "line"}
	watchStarted := make(chan struct{})
	var watchOnce sync.Once
	var mu sync.Mutex
	var seen []string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case AttachPath:
			var input AttachRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(AttachResponse{Token: strings.Repeat("b", 43), Generation: 2, Identity: input.Identity})
		case ObservePath:
			_ = json.NewEncoder(w).Encode(ObserveResponse{Snapshot: aigame.Snapshot{Revision: 11}, SessionToken: "bound-incarnation"})
		case ExecutePath, DetachPath:
			w.WriteHeader(http.StatusNoContent)
		case WatchPath:
			watchOnce.Do(func() { close(watchStarted) })
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Shutdown(context.Background())

	client, err := New(Config{BaseURL: public.URL, SocketPath: socketPath, ServerID: "line"})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := client.Bind(context.Background(), AttachRequest{SessionID: "human-session", Generation: 1, Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-watchStarted:
	case <-time.After(time.Second):
		t.Fatal("bound session did not start lease watch")
	}
	observed, err := bound.Observe(context.Background())
	if err != nil || observed.Revision != 11 || observed.SessionToken != "bound-incarnation" {
		t.Fatalf("bound observe = %+v, err=%v", observed, err)
	}
	if err := bound.ExecuteExpected(context.Background(), observed.Revision, aigame.Chat("hello", 0, 3)); err != nil {
		t.Fatal(err)
	}
	if err := bound.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-bound.LeaseDone():
	case <-time.After(time.Second):
		t.Fatal("bound lease did not close")
	}
	if err := bound.Close(); err != nil {
		t.Fatalf("second bound close = %v", err)
	}
	select {
	case request := <-publicCalls:
		t.Fatalf("bound session touched public Web API: %s", request)
	default:
	}
	mu.Lock()
	joined := strings.Join(seen, "\n")
	mu.Unlock()
	for _, want := range []string{"POST " + AttachPath, "POST " + ObservePath, "POST " + ExecutePath, "POST " + DetachPath, "GET " + WatchPath} {
		if !strings.Contains(joined, want) {
			t.Fatalf("bound requests = %q, missing %q", joined, want)
		}
	}
}

func TestExecuteErrorCodesPreserveWriteOutcome(t *testing.T) {
	public := httptest.NewServer(http.NotFoundHandler())
	defer public.Close()

	socketPath := unixSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var mu sync.RWMutex
	code := ""
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ExecutePath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.RLock()
		current := code
		mu.RUnlock()
		w.WriteHeader(http.StatusConflict)
		if current == "malformed" {
			_, _ = io.WriteString(w, "not-json")
			return
		}
		_ = json.NewEncoder(w).Encode(ErrorResponse{Code: current})
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Shutdown(context.Background())

	client, err := New(Config{BaseURL: public.URL, SocketPath: socketPath, ServerID: "line"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		code string
		want error
	}{
		{code: "stale_revision", want: aigame.ErrStaleRevision},
		{code: "invalid_action", want: aigame.ErrInvalidAction},
		{code: "wrong_phase", want: aigame.ErrWrongPhase},
		{code: "battle_not_ready", want: aigame.ErrBattleNotReady},
		{code: "outcome_unknown", want: ErrWriteOutcomeUnknown},
		{code: "malformed", want: ErrWriteOutcomeUnknown},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			mu.Lock()
			code = test.code
			mu.Unlock()
			err := client.Execute(context.Background(), "lease-token", ExecuteRequest{Revision: 1, Action: aigame.Chat("hello", 0, 3)})
			if !errors.Is(err, test.want) {
				t.Fatalf("Execute(%q) = %v, want %v", test.code, err, test.want)
			}
			if test.want == ErrWriteOutcomeUnknown && (errors.Is(err, aigame.ErrStaleRevision) || errors.Is(err, aigame.ErrInvalidAction)) {
				t.Fatalf("unknown execute error became retryable: %v", err)
			}
		})
	}
}

func TestHTTPConnUsesWebGreetingAndEvents(t *testing.T) {
	packet := []byte("1|TK|hello\n")
	var deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/sessions":
			_ = json.NewEncoder(w).Encode(createSessionResponse{ID: "session", Greeting: base64.StdEncoding.EncodeToString([]byte{'L', 0}), EventAck: true})
		case r.Method == http.MethodGet && r.URL.Path == "/api/sessions/session/events":
			_ = json.NewEncoder(w).Encode(eventsResponse{Events: []eventResponse{{Packet: base64.StdEncoding.EncodeToString(packet)}}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/sessions/session/send":
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/sessions/session":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	socketPath := unixSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := New(Config{BaseURL: server.URL, SocketPath: socketPath, ServerID: "local-line"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := client.Dial(context.Background(), "ignored")
	if err != nil {
		t.Fatal(err)
	}
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(connection, greeting); err != nil || string(greeting) != "L\x00" {
		t.Fatalf("greeting = %q, err=%v", greeting, err)
	}
	buffer := make([]byte, len(packet))
	if _, err := io.ReadFull(connection, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != string(packet) {
		t.Fatalf("event packet = %q", buffer)
	}
	if _, err := connection.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("closing HTTP connection did not delete Web session")
	}
}

func TestStatusErrorsDoNotExposeBody(t *testing.T) {
	for status, expected := range map[int]error{
		http.StatusUnauthorized: ErrUnauthorized,
		http.StatusConflict:     ErrLeaseConflict,
		http.StatusGone:         ErrLeaseRevoked,
	} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			if !errors.Is(statusError(status), expected) {
				t.Fatalf("statusError(%d) = %v", status, statusError(status))
			}
		})
	}
}

func TestNewRequiresPrivateSocket(t *testing.T) {
	if _, err := New(Config{BaseURL: "http://127.0.0.1:1"}); err == nil {
		t.Fatal("missing private socket was accepted")
	}
	if _, err := New(Config{BaseURL: "file:///tmp/web", SocketPath: os.DevNull}); err == nil {
		t.Fatal("non-HTTP Web URL was accepted")
	}
}

func TestPublicClientDisablesRedirectsAndProxy(t *testing.T) {
	socketPath := unixSocketPath(t)
	client, err := New(Config{BaseURL: "http://127.0.0.1:1", SocketPath: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	if client.web.CheckRedirect == nil {
		t.Fatal("public client allows redirects")
	}
	if err := client.web.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect = %v", err)
	}
	transport, ok := client.web.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("public transport type = %T", client.web.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("public client inherited an environment proxy")
	}
}

func TestSessionLeaseDoneIsClosedOnNil(t *testing.T) {
	var session *Session
	select {
	case <-session.LeaseDone():
	case <-time.After(time.Second):
		t.Fatal("nil session lease did not return a closed channel")
	}
}
