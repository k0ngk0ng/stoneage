package main

import (
	"context"
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
)

func TestWebAgentFrontdoorForwardsOnlyFixedTargetsAndHeaders(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var queries []string
	var auth, contentType, cookie, forwarded string
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if string(body) != `{"ok":true}` {
			t.Errorf("upstream body=%q", body)
		}
		mu.Lock()
		paths = append(paths, request.URL.Path)
		queries = append(queries, request.URL.RawQuery)
		auth, contentType, cookie, forwarded = request.Header.Get("Authorization"), request.Header.Get("Content-Type"), request.Header.Get("Cookie"), request.Header.Get("X-Forwarded-Host")
		mu.Unlock()
		response.Header().Set("X-Upstream", "must-not-cross")
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusAccepted)
		_, _ = response.Write([]byte(`{"accepted":true}`))
	}))
	defer upstream.Close()
	frontdoor, err := newWebAgentFrontdoor(upstream.URL+"/v1/game", upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "http://public.invalid/v1/game?target=admin", strings.NewReader(`{"ok":true}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Cookie", "session=must-not-cross")
	request.Header.Set("X-Forwarded-Host", "admin.invalid")
	response := httptest.NewRecorder()
	frontdoor.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Header().Get("X-Upstream") != "" || response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response status=%d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
	mu.Lock()
	gotPath, gotQuery, gotAuth, gotContentType, gotCookie, gotForwarded := paths[0], queries[0], auth, contentType, cookie, forwarded
	mu.Unlock()
	if gotPath != "/v1/game" || gotQuery != "" {
		t.Fatalf("fixed target path/query=%q/%q", gotPath, gotQuery)
	}
	if gotAuth != "Bearer secret" || gotContentType != "application/json" {
		t.Fatalf("allowed headers auth=%q content-type=%q", gotAuth, gotContentType)
	}
	if gotCookie != "" || gotForwarded != "" {
		t.Fatalf("sensitive headers crossed boundary cookie=%q forwarded=%q", gotCookie, gotForwarded)
	}

	request = httptest.NewRequest(http.MethodPost, "http://public.invalid/api/ai/worker/poll?upstream=http://attacker.invalid", strings.NewReader(`{"ok":true}`))
	request.Header.Set("Authorization", "Bearer worker")
	request.Header.Set("X-StoneAge-Worker-ID", "worker-1")
	request.Header.Set("X-StoneAge-Profile-ID", "profile-1")
	request.Header.Set("X-StoneAge-Worker-Epoch", "2")
	response = httptest.NewRecorder()
	frontdoor.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("worker response status=%d body=%q", response.Code, response.Body.String())
	}
	mu.Lock()
	gotPath, gotQuery = paths[1], queries[1]
	mu.Unlock()
	if gotPath != "/api/ai/worker/poll" || gotQuery != "" {
		t.Fatalf("worker target path/query=%q/%q", gotPath, gotQuery)
	}
}

func TestWebAgentFrontdoorRejectsBrowserAndUnknownRoutes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()
	frontdoor, err := newWebAgentFrontdoor(upstream.URL+"/v1/game", upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		method string
		path   string
		origin string
		want   int
	}{
		{name: "get", method: http.MethodGet, path: "/v1/game", want: http.StatusMethodNotAllowed},
		{name: "put", method: http.MethodPut, path: "/api/ai/worker/poll", want: http.StatusMethodNotAllowed},
		{name: "origin", method: http.MethodPost, path: "/v1/game", origin: "https://evil.invalid", want: http.StatusForbidden},
		{name: "admin", method: http.MethodPost, path: "/login", want: http.StatusNotFound},
		{name: "unknown-worker", method: http.MethodPost, path: "/api/ai/worker/admin", want: http.StatusNotFound},
		{name: "invalid-profile", method: http.MethodPost, path: "/api/ai/worker/game/profile%2Fother", want: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "http://public.invalid"+test.path, strings.NewReader(`{}`))
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()
			frontdoor.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d body=%q, want %d", response.Code, response.Body.String(), test.want)
			}
		})
	}
}

func TestNormalizeAgentUpstreams(t *testing.T) {
	if got, err := normalizeAgentGameUpstream("http://admin:8081"); err != nil || got.Path != "/v1/game" {
		t.Fatalf("host-only game upstream=%v err=%v", got, err)
	}
	for _, value := range []string{
		"",
		"ftp://admin:8081/v1/game",
		"http://user:pass@admin:8081/v1/game",
		"http://admin:8081/v1/game?x=1",
		"http://admin:8081/admin",
	} {
		if value == "" {
			continue
		}
		if _, err := normalizeAgentGameUpstream(value); err == nil {
			t.Errorf("normalizeAgentGameUpstream(%q) unexpectedly succeeded", value)
		}
	}
	if _, err := normalizeAgentWorkerUpstream("http://admin:8080/api/ai/worker"); err == nil {
		t.Fatal("worker upstream with a path unexpectedly succeeded")
	}
}

func TestWebAgentConfigLoadsAndEnvironmentOverrides(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "web.toml")
	content := `[agent]
socket_path = "/run/stoneage-web/agent.sock"
game_upstream = "http://admin:8081/v1/game"
worker_upstream = "http://admin:8080"
`
	if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"STONEAGE_WEB_AGENT_SOCKET", "STONEAGE_WEB_AGENT_GAME_UPSTREAM", "STONEAGE_WEB_AGENT_WORKER_UPSTREAM"} {
		t.Setenv(name, "")
	}
	cfg, _, err := configFromCommandLine([]string{"-config", filename})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentSocketPath != "/run/stoneage-web/agent.sock" || cfg.AgentGameUpstream != "http://admin:8081/v1/game" || cfg.AgentWorkerUpstream != "http://admin:8080" {
		t.Fatalf("agent config=%+v", cfg)
	}
	t.Setenv("STONEAGE_WEB_AGENT_GAME_UPSTREAM", "http://override:8081/v1/game")
	got, _, err := configFromCommandLine([]string{"-config", filename})
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentGameUpstream != "http://override:8081/v1/game" {
		t.Fatalf("agent environment override=%q", got.AgentGameUpstream)
	}
}

func newAgentListenerTestHandler() *Handler {
	return &Handler{sessions: newSessionStore(1), stop: make(chan struct{})}
}

func shortAgentSocketRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../build")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(root, "as-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func TestAgentListenerProtectsAndCleansUnixSocket(t *testing.T) {
	path := filepath.Join(shortAgentSocketRoot(t), "agent.sock")
	listener, err := newAgentListenerTestHandler().StartAgentListener(path)
	if err != nil {
		t.Fatal(err)
	}
	if listener == nil {
		t.Fatal("listener is nil")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket permissions=%o, want 600", info.Mode().Perm())
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket path after close err=%v", err)
	}
}

func TestAgentListenerRejectsOccupiedPaths(t *testing.T) {
	root := shortAgentSocketRoot(t)
	regular := filepath.Join(root, "regular")
	if err := os.WriteFile(regular, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newAgentListenerTestHandler().StartAgentListener(regular); err == nil {
		t.Fatal("regular file was accepted as agent socket")
	}
	if content, err := os.ReadFile(regular); err != nil || string(content) != "keep" {
		t.Fatalf("regular file changed: %q err=%v", content, err)
	}

	path := filepath.Join(root, "active.sock")
	first, err := newAgentListenerTestHandler().StartAgentListener(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := newAgentListenerTestHandler().StartAgentListener(path); err == nil {
		t.Fatal("active socket was accepted by a second listener")
	}
}

func TestAgentListenerRemovesConfirmedStaleSocket(t *testing.T) {
	path := filepath.Join(shortAgentSocketRoot(t), "stale.sock")
	old, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	listener, err := newAgentListenerTestHandler().StartAgentListener(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestHandlerCloseClosesAgentListener(t *testing.T) {
	path := filepath.Join(shortAgentSocketRoot(t), "agent.sock")
	handler := newAgentListenerTestHandler()
	if _, err := handler.StartAgentListener(path); err != nil {
		t.Fatal(err)
	}
	handler.Close()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent socket still exists after handler close")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestStartAgentListenerEmptyPathIsNoop(t *testing.T) {
	listener, err := newAgentListenerTestHandler().StartAgentListener(" ")
	if err != nil || listener != nil {
		t.Fatalf("empty path listener=%v err=%v", listener, err)
	}
}

func TestAgentListenerPrivateHandlerRejectsOrigin(t *testing.T) {
	path := filepath.Join(shortAgentSocketRoot(t), "agent.sock")
	handler := newAgentListenerTestHandler()
	listener, err := handler.StartAgentListener(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", path)
	}}}
	request, err := http.NewRequest(http.MethodPost, "http://agent.invalid/internal/agent/attach", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "https://browser.invalid")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("private origin status=%d", response.StatusCode)
	}
	_, _ = io.Copy(io.Discard, response.Body)
}
