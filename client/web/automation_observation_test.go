package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/ladder"
)

func TestAutomaticQuestAllowsHeartbeatButRejectsGameplayFromManualClient(t *testing.T) {
	f := newAutomationExecutorFixture(t, 1, 100)
	handler := &Handler{config: Config{PacketLimit: 64 * 1024}}
	submit := func(packet []byte, generation uint64) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"packet": base64.StdEncoding.EncodeToString(packet), "generation": generation})
		request := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.sendPacket(response, request, f.tcp, false)
		return response
	}
	for _, query := range []struct{ function, value string }{{"Echo", "sactl"}, {"S", "AI"}, {"S", "LADDER|1|health|0|status|"}} {
		packet := webClientPacket(t, 42, query.function, query.value)
		read := make(chan error, 1)
		go func() {
			_ = f.peer.SetReadDeadline(time.Now().Add(time.Second))
			got := make([]byte, len(packet))
			_, err := io.ReadFull(f.peer, got)
			if err == nil && !bytes.Equal(got, packet) {
				err = io.ErrUnexpectedEOF
			}
			read <- err
		}()
		if response := submit(packet, 0); response.Code != http.StatusAccepted {
			t.Fatal(response.Code, response.Body.String())
		}
		if err := <-read; err != nil {
			t.Fatal(err)
		}
	}
	wire, _ := (ladder.Request{ID: "mutation", Revision: 1, Operation: "queue"}).Wire()
	for _, packet := range [][]byte{webClientPacket(t, 43, "S", wire), webClientPacket(t, 44, "W", "1", "1", "a")} {
		for _, gen := range []uint64{0, f.session.generation} {
			response := submit(packet, gen)
			if response.Code != http.StatusConflict {
				t.Fatal("manual gameplay bypassed automatic owner", response.Code)
			}
		}
	}
	// Read-only sends must leave the original lease unchanged.
	if state := f.tcp.gate.State(); state.Generation != f.session.generation || state.Mode != f.session.mode {
		t.Fatal("observation changed ownership", state)
	}
}
