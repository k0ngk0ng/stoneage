package websession

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicAutomationUsesBoundSessionAndDoesNotRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/sessions/current/automation/start" || r.Method != "POST" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["generation"] != float64(3) {
			t.Errorf("body=%v err=%v", body, err)
		}
		http.Error(w, "task evidence is unverified", http.StatusConflict)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	conn := newHTTPConn(client, "current", "line", nil)
	defer conn.cancel()
	_, err = conn.AutomationCall(context.Background(), "start", map[string]any{"generation": 3, "task_id": "q"})
	if err == nil || !strings.Contains(err.Error(), "unverified") || calls != 1 {
		t.Fatal(err, calls)
	}
	if _, err = conn.AutomationCall(context.Background(), "../other/send", nil); err == nil || calls != 1 {
		t.Fatal("arbitrary operation accepted")
	}
	conn.mu.Lock()
	conn.closed = true
	conn.mu.Unlock()
	if _, err = conn.AutomationCall(context.Background(), "start", nil); err == nil || calls != 1 {
		t.Fatal("closed connection sent mutation")
	}
}

func TestAutomationControlGenerationIsRetainedForSubsequentWrites(t *testing.T) {
	sends := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/sessions/current/control":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"control":{"mode":"manual","generation":4}}`))
		case "/api/sessions/current/pause":
			// A late response must not rewind an already observed token.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"control":{"mode":"paused","generation":3}}`))
		case "/api/sessions/current/send":
			sends++
			var body struct {
				Packet     string `json:"packet"`
				Generation uint64 `json:"generation"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Generation != 4 || body.Packet != "cGFja2V0" {
				t.Errorf("bad fenced write: %+v %v", body, err)
			}
			http.Error(w, "stale generation", http.StatusConflict)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	conn := newHTTPConn(client, "current", "line", nil)
	defer conn.cancel()
	for _, op := range []string{"status", "pause"} {
		if _, err := conn.AutomationCall(context.Background(), op, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Write([]byte("packet")); err == nil || sends != 1 {
		t.Fatal("write rejection retried or ignored", err, sends)
	}
}
