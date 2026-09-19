package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

// webBattleRoster is a full-width (thirteen field) roster: the character's row
// and one enemy, with no row in the pet slot the server reserves. A battle
// without a live pet row needs exactly one command per turn, which is what
// makes the turn assertions below unambiguous.
const webBattleRoster = "BC|0|0|AutomationHero||100|1|23|23|4|0||0|0|0|A|Enemy||100|1|10|10|0|0||0|0|0"

// webServerIntPacket mirrors webServerPacket for functions whose fields are
// integers rather than strings.
func webServerIntPacket(t *testing.T, id uint32, function string, values ...int32) []byte {
	t.Helper()
	encoded := make([]string, len(values))
	for index, value := range values {
		encoded[index] = namedproto.EncodeInt(value)
	}
	raw, err := namedproto.RawMessage(id, function, encoded)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := namedproto.EncodePacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	return packet
}

// seedWebBattle makes the session observe a battle being opened: the encounter
// envelope, the player menu and the roster.
func seedWebBattle(t *testing.T, session *tcpSession) {
	t.Helper()
	session.applyAuthoritativePacket(webServerIntPacket(t, 10, "EN", 1, 218))
	session.applyAuthoritativePacket(webServerPacket(t, 11, "B", "BP|0|0|10"))
	session.applyAuthoritativePacket(webServerPacket(t, 12, "B", webBattleRoster))
}

// openWebTurn is the server opening the next command window.
func openWebTurn(t *testing.T, session *tcpSession, id uint32) {
	t.Helper()
	session.applyAuthoritativePacket(webServerPacket(t, id, "B", "BP|0|0|10"))
}

// readWebBattleCommand decodes the next battle command the session wrote to
// the connection the human is holding. It never invents state: a timeout is
// returned to the caller so a test can assert that nothing was sent.
func readWebBattleCommand(t *testing.T, peer net.Conn, timeout time.Duration) (string, error) {
	t.Helper()
	if err := peer.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4096)
	n, err := peer.Read(buffer)
	if err != nil {
		return "", err
	}
	raw, err := namedproto.DecodePacket(buffer[:n])
	if err != nil {
		t.Fatalf("decode battle command: %v", err)
	}
	message, err := namedproto.ParseMessage(raw)
	if err != nil {
		t.Fatalf("parse battle command: %v", err)
	}
	if message.Function != "B" || len(message.Fields) == 0 {
		t.Fatalf("unexpected battle envelope: %+v", message)
	}
	decoded, err := namedproto.DecodeString(message.Fields[0])
	if err != nil {
		t.Fatalf("decode battle command field: %v", err)
	}
	return string(decoded), nil
}

func readWebBattleCommandOrFail(t *testing.T, peer net.Conn, what string) string {
	t.Helper()
	command, err := readWebBattleCommand(t, peer, 3*time.Second)
	if err != nil {
		t.Fatalf("no command for %s: %v", what, err)
	}
	return command
}

// postBattleAuto drives the browser-facing endpoint the panel uses.
func postBattleAuto(t *testing.T, handler http.Handler, sessionID, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/"+path, bytes.NewReader([]byte(body)))
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(response, request)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		// A control call waits for the gate, which the loop holds while it
		// writes. A caller that never reads its own session would deadlock
		// here, so report it as a failure rather than hanging the suite.
		t.Fatalf("POST %s did not return", path)
	}
	return response
}

func decodeControlSnapshot(t *testing.T, response *httptest.ResponseRecorder) controlResponse {
	t.Helper()
	var snapshot controlResponse
	if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode control snapshot %q: %v", response.Body.String(), err)
	}
	return snapshot
}

func TestWebBattleAutoAnswersTurnsUntilTakeover(t *testing.T) {
	handler, session, peer, _ := agentSessionFixture(t)
	seedWebBattle(t, session)

	response := postBattleAuto(t, handler, session.id, "battle-auto", `{"generation":1}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("auto battle start: %d %s", response.Code, response.Body.String())
	}
	snapshot := decodeControlSnapshot(t, response)
	if !snapshot.AutomationActive || snapshot.AutomationMode != aicontrol.Battle || snapshot.Control.Mode != aicontrol.Battle {
		t.Fatalf("auto battle not published as battle control: %+v", snapshot)
	}

	// The enemy row is the only living opponent, and its battle id 10 is
	// written in hex exactly as the native client writes it.
	if command := readWebBattleCommandOrFail(t, peer, "the first turn"); command != "H|A" {
		t.Fatalf("first turn command = %q, want H|A", command)
	}

	// A second turn proves the loop keeps answering instead of firing once:
	// the server's next menu packet re-opens the command window.
	openWebTurn(t, session, 13)
	if command := readWebBattleCommandOrFail(t, peer, "the second turn"); command != "H|A" {
		t.Fatalf("second turn command = %q, want H|A", command)
	}

	response = postBattleAuto(t, handler, session.id, "takeover", `{"generation":2,"reason":"test takeover"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("takeover: %d %s", response.Code, response.Body.String())
	}
	snapshot = decodeControlSnapshot(t, response)
	if snapshot.AutomationActive || snapshot.Control.Mode != aicontrol.Manual {
		t.Fatalf("takeover did not return control: %+v", snapshot)
	}

	// Handing control back must stop the loop on the spot, not after the
	// current turn's timeout.
	if command, err := readWebBattleCommand(t, peer, 400*time.Millisecond); err == nil {
		t.Fatalf("command after takeover: %q", command)
	} else {
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("read after takeover: %v", err)
		}
	}
}

func TestWebBattleAutoRejectsInvalidRequests(t *testing.T) {
	handler, session, peer, _ := agentSessionFixture(t)
	seedWebBattle(t, session)

	request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+session.id+"/battle-auto", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "POST" {
		t.Fatalf("GET battle-auto: %d allow=%q", response.Code, response.Header().Get("Allow"))
	}

	for _, body := range []string{`{}`, `{"generation":0}`, `{"generation":1,"supply":true}`} {
		response = postBattleAuto(t, handler, session.id, "battle-auto", body)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("start %s: %d %s", body, response.Code, response.Body.String())
		}
	}
	response = postBattleAuto(t, handler, session.id, "battle-auto", `{"generation":9}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale generation accepted: %d %s", response.Code, response.Body.String())
	}

	response = postBattleAuto(t, handler, session.id, "battle-auto", `{"generation":1}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("auto battle start: %d %s", response.Code, response.Body.String())
	}
	// The running loop owns the session now, so a second start is a conflict
	// rather than a silent replacement.
	response = postBattleAuto(t, handler, session.id, "battle-auto", `{"generation":2}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("second start accepted: %d %s", response.Code, response.Body.String())
	}
	if command := readWebBattleCommandOrFail(t, peer, "the only turn"); command != "H|A" {
		t.Fatalf("command = %q, want H|A", command)
	}
	response = postBattleAuto(t, handler, session.id, "takeover", `{"generation":2}`)
	if response.Code != http.StatusOK {
		t.Fatalf("takeover: %d %s", response.Code, response.Body.String())
	}
}
