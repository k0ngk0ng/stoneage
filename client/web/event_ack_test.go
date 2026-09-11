package main

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func eventHTTPHarness(t *testing.T) (*tcpSession, func(string, int) map[string]any) {
	t.Helper()
	left, right := net.Pipe()
	session := newTCPSession("events-test", left, defaultPacketLimit)
	t.Cleanup(func() { session.close(); _ = right.Close() })
	handler := &Handler{config: Config{PollTimeout: time.Millisecond}, sessions: newSessionStore(4)}
	if err := handler.sessions.add(session); err != nil {
		t.Fatal(err)
	}
	return session, func(query string, status int) map[string]any {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/sessions/events-test/events"+query, nil))
		if response.Code != status {
			t.Fatalf("GET events%s: status=%d want=%d body=%s", query, response.Code, status, response.Body.String())
		}
		if status != http.StatusOK {
			return nil
		}
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
}

func TestHTTPEventAcknowledgementContract(t *testing.T) {
	session, poll := eventHTTPHarness(t)
	session.enqueue(packetEvent{packet: []byte("one\n")})
	first := poll("?ack=0", http.StatusOK)
	if first["acknowledged"] != true || first["closed"] != false {
		t.Fatalf("reliable response flags: %v", first)
	}
	events := first["events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["seq"] != float64(1) || events[0].(map[string]any)["packet"] != base64.StdEncoding.EncodeToString([]byte("one\n")) {
		t.Fatalf("reliable wire events: %v", events)
	}
	// A later queued packet cannot be acknowledged merely by guessing its ID.
	session.enqueue(packetEvent{packet: []byte("two\n")})
	poll("?ack=2", http.StatusBadRequest)
	for _, query := range []string{"?ack=", "?ack=-1", "?ack=1.5", "?ack=18446744073709551616", "?ack=0&ack=1"} {
		poll(query, http.StatusBadRequest)
	}
	poll("", http.StatusConflict)
	replayed := poll("?ack=0", http.StatusOK)["events"].([]any)
	if len(replayed) != 2 || replayed[0].(map[string]any)["seq"] != float64(1) || replayed[1].(map[string]any)["seq"] != float64(2) {
		t.Fatalf("lost-response replay: %v", replayed)
	}
	remaining := poll("?ack=1", http.StatusOK)["events"].([]any)
	if len(remaining) != 1 || remaining[0].(map[string]any)["seq"] != float64(2) {
		t.Fatalf("partial ACK: %v", remaining)
	}
	if len(poll("?ack=2", http.StatusOK)["events"].([]any)) != 0 {
		t.Fatal("fully acknowledged packets were replayed")
	}
	if session.eventBytes != 0 || len(session.events) != 0 {
		t.Fatal("acknowledgement did not release the queue budget")
	}
}

func TestHTTPReliableCloseMarkerReplaysBeforeTerminalState(t *testing.T) {
	session, poll := eventHTTPHarness(t)
	session.enqueue(packetEvent{packet: []byte("final\n")})
	session.finish("upstream closed")
	for i := 0; i < 2; i++ {
		body := poll("?ack=0", http.StatusOK)
		events := body["events"].([]any)
		if body["closed"] != false || len(events) != 2 {
			t.Fatalf("terminal state skipped unacknowledged events: %v", body)
		}
		last := events[1].(map[string]any)
		if last["seq"] != float64(2) || last["closed"] != true || last["error"] != "upstream closed" {
			t.Fatalf("close marker: %v", last)
		}
	}
	acked := poll("?ack=2", http.StatusOK)
	if acked["closed"] != true || len(acked["events"].([]any)) != 0 {
		t.Fatalf("acknowledged EOF: %v", acked)
	}
}

func TestHTTPLegacyEventsKeepTheirWireSchema(t *testing.T) {
	session, poll := eventHTTPHarness(t)
	session.enqueue(packetEvent{packet: []byte("legacy\n")})
	body := poll("", http.StatusOK)
	if _, exists := body["acknowledged"]; exists {
		t.Fatal("legacy request opted into ACK mode")
	}
	event := body["events"].([]any)[0].(map[string]any)
	if _, exists := event["seq"]; exists {
		t.Fatal("legacy event gained a sequence field")
	}
	if len(poll("", http.StatusOK)["events"].([]any)) != 0 {
		t.Fatal("legacy destructive read was replayed")
	}
	poll("?ack=0", http.StatusConflict)
}

func TestReliableUnacknowledgedEventsStayBounded(t *testing.T) {
	session, poll := eventHTTPHarness(t)
	poll("?ack=0", http.StatusOK)
	for i := 0; i < maxQueuedEvents; i++ {
		session.enqueue(packetEvent{packet: []byte("pending\n")})
	}
	for i := 0; i < 2; i++ {
		if len(poll("?ack=0", http.StatusOK)["events"].([]any)) != maxQueuedEvents || session.isClosed() {
			t.Fatal("replay changed the unacknowledged queue budget")
		}
	}
	session.enqueue(packetEvent{packet: []byte("overflow\n")})
	body := poll("?ack=0", http.StatusOK)
	events := body["events"].([]any)
	if !session.isClosed() || len(events) != maxQueuedEvents+1 || events[len(events)-1].(map[string]any)["error"] != "HTTP event queue overflow" {
		t.Fatal("unacknowledged queue overflow did not terminate with its diagnostic")
	}
}
