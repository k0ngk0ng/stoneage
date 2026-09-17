package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

func agentSessionFixture(t *testing.T) (*Handler, *tcpSession, net.Conn, automationIdentity) {
	t.Helper()
	h, err := NewHandler(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	s := newTCPSession("agent-session", left, 64*1024)
	s.serverID = "line-a"
	seedWebAutomationState(t, s, 10, 100)
	s.applyAuthoritativePacket(webServerPacket(t, 6, "CharList", "successful", `AutomationHero|0\z0\z1\z10\z100\z20\z30\z4\z0\z50\z50\z50\z50\z0\zAutomationHero\zhome`))
	s.applyAuthoritativePacket(webServerPacket(t, 7, "CharLogin", "successful", ""))
	if err = h.sessions.add(s); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.observeAuthoritative(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := automationIdentityFromSnapshot(snapshot, s.serverLineID())
	if !ok {
		t.Fatal("missing authoritative identity")
	}
	t.Cleanup(func() { h.Close(); right.Close() })
	return h, s, right, identity
}

func agentRequest(t *testing.T, h http.Handler, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func attachTestAgent(t *testing.T, h *Handler, s *tcpSession, identity automationIdentity) string {
	t.Helper()
	response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/attach", "", map[string]any{"session_id": s.id, "generation": s.gate.State().Generation, "identity": identity})
	if response.Code != http.StatusCreated {
		t.Fatalf("attach %d %s", response.Code, response.Body.String())
	}
	var result struct{ Token string }
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Token
}

func TestAgentInternalBoundaryAndIdentity(t *testing.T) {
	h, s, _, identity := agentSessionFixture(t)
	valid := map[string]any{"session_id": s.id, "generation": uint64(1), "identity": identity}
	if response := agentRequest(t, h, "/internal/agent/attach", "", valid); response.Code != 404 {
		t.Fatalf("internal API public: %d", response.Code)
	}
	for _, field := range []string{"account", "slot", "name", "line", "incarnation"} {
		wrong := identity
		switch field {
		case "account":
			wrong.AccountID = "other"
			wrong.CharacterID = "other:0"
		case "slot":
			wrong.CharacterID = "automation-account:2"
		case "name":
			wrong.CharacterName = "other"
		case "line":
			wrong.ServerID = "line-b"
		case "incarnation":
			wrong.PersistentCharacterID = "other"
		}
		response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/attach", "", map[string]any{"session_id": s.id, "generation": uint64(1), "identity": wrong})
		if response.Code != 409 {
			t.Fatalf("%s mismatch accepted: %d", field, response.Code)
		}
	}
	valid["unlimited_funds"] = true
	if response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/attach", "", valid); response.Code != 400 {
		t.Fatalf("client privilege accepted: %d", response.Code)
	}
	delete(valid, "unlimited_funds")
	token := attachTestAgent(t, h, s, identity)
	if response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/attach", "", valid); response.Code != 409 {
		t.Fatalf("stale attach: %d", response.Code)
	}
	if response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/observe", "", nil); response.Code != 401 {
		t.Fatalf("unauthorized observe: %d", response.Code)
	}
	if response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/observe", token, nil); response.Code != 200 {
		t.Fatalf("observe: %d", response.Code)
	}
}

func TestAgentObservationPreservesBrowserEventsAndIncarnation(t *testing.T) {
	h, s, _, identity := agentSessionFixture(t)
	token := attachTestAgent(t, h, s, identity)
	s.enqueue(packetEvent{packet: []byte("browser packet\n")})
	s.mu.Lock()
	queued := len(s.events)
	s.mu.Unlock()
	response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/observe", token, nil)
	if response.Code != 200 {
		t.Fatalf("observe: %d", response.Code)
	}
	var result struct {
		Snapshot     aigame.Snapshot `json:"snapshot"`
		SessionToken string          `json:"session_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	before, _ := s.observeAuthoritative(context.Background())
	if result.Snapshot.Account != identity.AccountID || result.SessionToken == "" || result.SessionToken != before.SessionToken {
		t.Fatal("observation lost identity/incarnation")
	}
	s.mu.Lock()
	remaining := len(s.events)
	s.mu.Unlock()
	if remaining != queued {
		t.Fatal("Agent consumed browser events")
	}
	// A new login of even the same role invalidates the old lease.
	s.applyAuthoritativePacket(webServerPacket(t, 8, "CharLogin", "successful", ""))
	if response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/observe", token, nil); response.Code != 410 {
		t.Fatalf("new incarnation inherited lease: %d", response.Code)
	}
}

func TestAgentLeaseCompletesDelayedPersistentIdentity(t *testing.T) {
	h, s, _, identity := agentSessionFixture(t)
	// The initial attach races the optional S(AI) response, so the lease is
	// intentionally bound without a persistent character ID.
	identity.PersistentCharacterID = ""
	token := attachTestAgent(t, h, s, identity)
	const firstID = "pc1_0123456789abcdef0123456789abcdef"
	s.applyAuthoritativePacket(webServerPacket(t, 8, "S", "AI|v=1|chara=12|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0|character_id="+firstID))
	if response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/observe", token, nil); response.Code != http.StatusOK {
		t.Fatalf("delayed persistent identity revoked lease: %d", response.Code)
	}
	h.agentMu.Lock()
	lease := h.agentLeases[token]
	h.agentMu.Unlock()
	if lease == nil {
		t.Fatal("lease disappeared after persistent identity completion")
	}
	lease.identityMu.Lock()
	gotID := lease.identity.PersistentCharacterID
	lease.identityMu.Unlock()
	if gotID != firstID {
		t.Fatalf("lease persistent identity = %q, want %q", gotID, firstID)
	}

	const replacementID = "pc1_fedcba9876543210fedcba9876543210"
	s.applyAuthoritativePacket(webServerPacket(t, 9, "S", "AI|v=1|chara=12|end=0,0,0,0,0,0|now=0,0,0,0,0,0|ride=0|character_id="+replacementID))
	if response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/observe", token, nil); response.Code != http.StatusGone {
		t.Fatalf("replacement persistent identity retained lease: %d", response.Code)
	}
}

func TestAgentTakeoverRevokesWatchAndEveryOperation(t *testing.T) {
	h, s, _, identity := agentSessionFixture(t)
	token := attachTestAgent(t, h, s, identity)
	generation := s.gate.State().Generation
	if err := s.dispatch(context.Background(), generation, aicontrol.Manual, func(context.Context) error { t.Error("manual bypass"); return nil }); err == nil {
		t.Fatal("manual bypass accepted")
	}
	watch := httptest.NewRequest(http.MethodGet, "/internal/agent/watch", nil)
	watch.Header.Set("Authorization", "Bearer "+token)
	done := make(chan int, 1)
	go func() { w := httptest.NewRecorder(); h.InternalAgentHandler().ServeHTTP(w, watch); done <- w.Code }()
	if _, err := s.gate.Takeover("human"); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-done:
		if status != 410 {
			t.Fatalf("watch: %d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("watch did not revoke")
	}
	for _, path := range []string{"observe", "execute", "detach"} {
		response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/"+path, token, map[string]any{"revision": 0, "action": aigame.Chat("hi", 0, 3)})
		if response.Code != 410 {
			t.Fatalf("stale %s: %d", path, response.Code)
		}
	}
	if state := s.gate.State(); state.Mode != aicontrol.Manual || state.Generation != generation+1 {
		t.Fatalf("late revoke changed takeover: %+v", state)
	}
}

func TestAgentDetachRetainsHumanConnectionAndFencesAction(t *testing.T) {
	h, s, peer, identity := agentSessionFixture(t)
	token := attachTestAgent(t, h, s, identity)
	snapshot, _ := s.observeAuthoritative(context.Background())
	// Real typed action must reach the existing connection, not another login.
	read := make(chan error, 1)
	go func() { buffer := make([]byte, 4096); _, err := peer.Read(buffer); read <- err }()
	response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/execute", token, map[string]any{"revision": snapshot.Revision, "action": aigame.Chat("hello", 0, 3)})
	if response.Code != 204 {
		t.Fatalf("execute: %d %s", response.Code, response.Body.String())
	}
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	response = agentRequest(t, h.InternalAgentHandler(), "/internal/agent/detach", token, nil)
	if response.Code != 204 {
		t.Fatalf("detach: %d", response.Code)
	}
	select {
	case <-s.closed:
		t.Fatal("detached human was logged out")
	default:
	}
	if state := s.gate.State(); state.Mode != aicontrol.Manual {
		t.Fatalf("control not returned: %+v", state)
	}
	second := attachTestAgent(t, h, s, identity)
	if second == token {
		t.Fatal("lease reused")
	}
	response = agentRequest(t, h.InternalAgentHandler(), "/internal/agent/detach", token, nil)
	if response.Code != 410 || s.gate.State().Mode != aicontrol.Agent {
		t.Fatal("old detach revoked replacement")
	}
	s.close()
	if response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/observe", second, nil); response.Code != 410 {
		t.Fatalf("closed session still authorized: %d", response.Code)
	}
}

func TestAgentExecutePreservesDefiniteRejection(t *testing.T) {
	h, s, _, identity := agentSessionFixture(t)
	token := attachTestAgent(t, h, s, identity)
	snapshot, _ := s.observeAuthoritative(context.Background())
	response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/execute", token, map[string]any{"revision": snapshot.Revision - 1, "action": aigame.Chat("hello", 0, 3)})
	var result struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != 409 || result.Code != "stale_revision" {
		t.Fatalf("lost rejection classification: %d %s", response.Code, response.Body.String())
	}
	if response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/observe", token, nil); response.Code != 200 {
		t.Fatalf("revision conflict revoked valid lease: %d", response.Code)
	}
}

func TestAgentDelayedPersistentIdentityKeepsSameLoginLease(t *testing.T) {
	for _, arrivesBeforeAttach := range []bool{false, true} {
		t.Run(map[bool]string{false: "after", true: "before"}[arrivesBeforeAttach], func(t *testing.T) {
			h, s, _, identity := agentSessionFixture(t)
			if arrivesBeforeAttach {
				seedDurableRecoveryIdentity(t, s, durableRecoveryPlayerID)
			}
			token := attachTestAgent(t, h, s, identity)
			if !arrivesBeforeAttach {
				seedDurableRecoveryIdentity(t, s, durableRecoveryPlayerID)
			}
			if response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/observe", token, nil); response.Code != 200 {
				t.Fatalf("late metadata revoked same login: %d", response.Code)
			}
			seedDurableRecoveryIdentity(t, s, "pc1_1123456789abcdef0123456789abcdef")
			if response := agentRequest(t, h.InternalAgentHandler(), "/internal/agent/observe", token, nil); response.Code != 410 {
				t.Fatalf("changed known identity accepted: %d", response.Code)
			}
		})
	}
}
