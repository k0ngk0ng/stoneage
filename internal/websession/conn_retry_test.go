package websession

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestReliablePollRetryAndReplay(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(204)
			return
		}
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(503)
			return
		}
		ack := r.URL.Query().Get("ack")
		if n == 2 && ack != "0" {
			t.Error("retry acknowledged undelivered event")
		}
		if n == 3 && ack != "1" {
			t.Error("received packet was not acknowledged")
		}
		events := []eventResponse{{Seq: 1, Ladder: json.RawMessage(`{"revision":1}`), Packet: base64.StdEncoding.EncodeToString([]byte("one\n"))}}
		if n >= 3 {
			events = append(events, eventResponse{Seq: 2, Packet: base64.StdEncoding.EncodeToString([]byte("two\n"))})
		}
		_ = json.NewEncoder(w).Encode(eventsResponse{Events: events, Acknowledged: true})
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	conn := newHTTPConn(client, "session", "line", nil)
	conn.reliable = true
	defer conn.Close()
	for _, want := range []string{"one\n", "two\n"} {
		packet := make([]byte, len(want))
		if _, err := io.ReadFull(conn, packet); err != nil || string(packet) != want {
			t.Fatalf("got %q, err=%v", packet, err)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("unexpected retries")
	}
	conn.cancel()
	if _, err := conn.Read(make([]byte, 1)); err != context.Canceled {
		t.Fatalf("cancelled read = %v", err)
	}
}
