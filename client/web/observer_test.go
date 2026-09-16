package main

import (
	"bufio"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

func webServerPacket(t *testing.T, id uint32, function string, fields ...string) []byte {
	t.Helper()
	encoded := make([]string, len(fields))
	for index, field := range fields {
		encoded[index] = namedproto.EncodeString([]byte(field))
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

func webClientPacket(t *testing.T, id uint32, function string, fields ...string) []byte {
	t.Helper()
	encoded := make([]string, len(fields))
	for index, field := range fields {
		encoded[index] = namedproto.EncodeString([]byte(field))
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

func TestAutomationObserverTracksWebLoginIdentity(t *testing.T) {
	left, right := net.Pipe()
	session := newTCPSession("observer-identity", left, 64*1024)
	defer right.Close()
	defer session.close()

	session.applyAuthoritativeClientPacket(webClientPacket(t, 1, "ClientLogin", "account", "ignored"))
	session.applyAuthoritativeClientPacket(webClientPacket(t, 2, "CharLogin", "Hero"))
	session.applyAuthoritativePacket(webServerPacket(t, 3, "CharLogin", "successful", ""))

	snapshot, err := session.observeAuthoritative(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Account != "account" || snapshot.Character != "Hero" || snapshot.Phase != aigame.PhaseWorld {
		t.Fatalf("shared observer identity=%+v", snapshot)
	}
}

func TestTypedTradeWriteDoesNotReenterManualObserver(t *testing.T) {
	left, right := net.Pipe()
	session := newTCPSession("observer-trade", left, 64*1024)
	defer right.Close()
	defer session.close()
	session.applyAuthoritativePacket(webServerPacket(t, 1, "CharLogin", "successful", ""))
	session.applyAuthoritativePacket(webServerPacket(t, 2, "TD", "C|7|Peer|1"))
	before, err := session.observeAuthoritative(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The unsolicited window is fenced, but typed cancel remains legal. Its
	// write holds the game state lock and must not recursively invoke the
	// manual observer on the same session.
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		done <- session.authoritative.ExecuteExpected(ctx, before.Revision, aigame.Action{Kind: aigame.ActionTrade, Command: "cancel"})
	}()
	_ = right.SetReadDeadline(time.Now().Add(time.Second))
	packet, err := bufio.NewReader(right).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	event, err := decodeWebPacket(packet)
	if err != nil || event.Function != "TD" || event.Fields[0] != "W|7|Peer" {
		t.Fatalf("cancel: %+v %v", event, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("typed writer reentered manual observer")
	}
	session.applyAuthoritativePacket(webServerPacket(t, 3, "TD", "W|7|Peer"))
	after, err := session.observeAuthoritative(context.Background())
	if err != nil || !after.Trade.Closed {
		t.Fatalf("close: %+v %v", after.Trade, err)
	}
}

func TestAutomationSessionObservesExistingTCPStreamAndExecutesOnIt(t *testing.T) {
	left, right := net.Pipe()
	session := newTCPSession("observer", left, 64*1024)
	defer right.Close()
	defer session.close()

	// The browser's existing login response is fed to the shared parser. No
	// second socket or credentials are involved in creating the observer.
	session.applyAuthoritativePacket(webServerPacket(t, 1, "CharLogin", "successful", ""))
	state, _, err := session.gate.Switch(1, aicontrol.Quest, "test")
	if err != nil {
		t.Fatal(err)
	}
	automation := &AutomationSession{ID: session.id, session: session, mode: aicontrol.Quest, generation: state.Generation}

	observation, err := automation.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if observation.Phase != aigame.PhaseWorld || observation.Revision == 0 {
		t.Fatalf("shared observer did not apply existing packet: %+v", observation)
	}

	writeResult := make(chan error, 1)
	go func() {
		writeResult <- automation.ExecuteExpected(context.Background(), observation.Revision, aigame.Action{Kind: aigame.ActionStatus, Command: "AI"})
	}()
	if err := right.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	packet, err := bufio.NewReader(right).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	event, err := decodeWebPacket(packet)
	if err != nil {
		t.Fatal(err)
	}
	if event.Function != "S" || len(event.Fields) != 1 || event.Fields[0] != "AI" {
		t.Fatalf("typed automation action was not written on existing socket: %+v", event)
	}
	if err := <-writeResult; err != nil {
		t.Fatal(err)
	}
}

type decodedWebPacket struct {
	Function string
	Fields   []string
}

func decodeWebPacket(packet []byte) (decodedWebPacket, error) {
	raw, err := namedproto.DecodePacket(packet)
	if err != nil {
		return decodedWebPacket{}, err
	}
	message, err := namedproto.ParseMessage(raw)
	if err != nil {
		return decodedWebPacket{}, err
	}
	fields := make([]string, len(message.Fields))
	for index, value := range message.Fields {
		decoded, err := namedproto.DecodeString(value)
		if err != nil {
			return decodedWebPacket{}, err
		}
		fields[index] = string(decoded)
	}
	return decodedWebPacket{Function: message.Function, Fields: fields}, nil
}

func TestAutomationSessionRejectsStaleOwnerAfterTakeover(t *testing.T) {
	left, right := net.Pipe()
	session := newTCPSession("observer-stale", left, 64*1024)
	defer right.Close()
	defer session.close()
	state, _, err := session.gate.Switch(1, aicontrol.Quest, "test")
	if err != nil {
		t.Fatal(err)
	}
	automation := &AutomationSession{session: session, mode: aicontrol.Quest, generation: state.Generation}
	if _, err := session.gate.Takeover("human"); err != nil {
		t.Fatal(err)
	}
	err = automation.ExecuteExpected(context.Background(), 1, aigame.Action{Kind: aigame.ActionStatus, Command: "AI"})
	if err == nil || !errors.Is(err, aicontrol.ErrStale) {
		t.Fatalf("stale automation action error=%v", err)
	}
}

func TestOldAutomationSessionCannotBorrowNewRunLease(t *testing.T) {
	left, right := net.Pipe()
	session := newTCPSession("observer-replaced-lease", left, 64*1024)
	defer right.Close()
	defer session.close()
	session.applyAuthoritativePacket(webServerPacket(t, 1, "CharLogin", "successful", ""))
	first, _, err := session.gate.Switch(1, aicontrol.Quest, "first run")
	if err != nil {
		t.Fatal(err)
	}
	old := &AutomationSession{session: session, mode: aicontrol.Quest, generation: first.Generation}
	if _, err := session.gate.Takeover("human"); err != nil {
		t.Fatal(err)
	}
	second, _, err := session.gate.Switch(session.gate.State().Generation, aicontrol.Quest, "second run")
	if err != nil {
		t.Fatal(err)
	}
	if err := old.ExecuteExpected(context.Background(), 1, aigame.Action{Kind: aigame.ActionStatus, Command: "AI"}); !errors.Is(err, aicontrol.ErrStale) {
		t.Fatalf("old session borrowed new lease: %v", err)
	}
	current := &AutomationSession{session: session, mode: aicontrol.Quest, generation: second.Generation}
	observation, err := current.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- current.ExecuteExpected(context.Background(), observation.Revision, aigame.Action{Kind: aigame.ActionStatus, Command: "AI"})
	}()
	if err := right.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(right).ReadBytes('\n'); err != nil {
		t.Fatalf("new session did not write: %v", err)
	}
	if err := <-result; err != nil {
		t.Fatalf("new session rejected its lease: %v", err)
	}
}
