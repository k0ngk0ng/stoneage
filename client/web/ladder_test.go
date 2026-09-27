package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/battleauto"
	"github.com/k0ngk0ng/stoneage/internal/ladder"
)

func ladderHTTPRequest(t *testing.T, generation uint64) *http.Request {
	t.Helper()
	body, err := json.Marshal(ladderHTTPAction{generation, ladder.Request{ID: "create_a", Revision: 10, Operation: "create", Argument: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewRequest(http.MethodPost, "/api/sessions/battle-auto/ladder", bytes.NewReader(body))
}

func seedLadderAutoState(t *testing.T, s *tcpSession, snapshot ladder.Snapshot) {
	t.Helper()
	observed, _ := s.observeAuthoritative(context.Background())
	revision := uint64(30)
	if observed.Ladder != nil {
		revision = observed.Ladder.Revision + 1
	}
	e := ladder.Envelope{Version: 1, OK: true, Revision: revision, Sequence: revision, Snapshot: snapshot}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	s.applyAuthoritativePacket(webServerPacket(t, 30, "S", "LADDER|"+string(b)))
}

// Send through the real HTTP handler and pipe, and provide the authority's
// correlated reply only after reading the outgoing request.
func ladderAutoPost(t *testing.T, h *Handler, s *tcpSession, peer net.Conn, op, arg string, snapshot ladder.Snapshot, decorate ...func(*ladder.Envelope)) *httptest.ResponseRecorder {
	t.Helper()
	observed, _ := s.observeAuthoritative(context.Background())
	revision := uint64(30)
	if observed.Ladder != nil {
		revision = observed.Ladder.Revision
	}
	id, err := ladder.NewRequestID()
	if err != nil {
		t.Fatal(err)
	}
	r := ladder.Request{ID: id, Revision: revision, Operation: op, Argument: arg}
	b, err := json.Marshal(ladderHTTPAction{Generation: s.gate.State().Generation, Request: r})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(response, httptest.NewRequest("POST", "/api/sessions/"+s.id+"/ladder", bytes.NewReader(b)))
		close(done)
	}()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	packet, err := bufio.NewReader(peer).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	written, err := decodeWebPacket(packet)
	wire, _ := r.Wire()
	if err != nil || written.Function != "S" || written.Fields[0] != wire {
		t.Fatalf("write=%+v err=%v", written, err)
	}
	e := ladder.Envelope{Version: 1, RequestID: r.ID, RequestWire: wire, OK: true, Code: "ok", Revision: revision + 1, Sequence: revision + 1, Snapshot: snapshot}
	for _, fn := range decorate {
		fn(&e)
	}
	b, err = json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	s.applyAuthoritativePacket(webServerPacket(t, 31, "S", "LADDER|"+string(b)))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ladder request did not finish")
	}
	if response.Code != http.StatusOK {
		t.Fatalf("response=%d %s", response.Code, response.Body)
	}
	return response
}

func TestWebHistoricalReceiptDoesNotRenewAutomation(t *testing.T) {
	for _, op := range []string{"ready", "strategy"} {
		t.Run(op, func(t *testing.T) {
			h, s, peer := battleSessionFixture(t)
			s.ladderAutoSuppressed = true
			state := ladder.Snapshot{Phase: "lobby", Self: ladder.Player{ID: "self", Strategy: "basic", Ready: true}}
			arg := ""
			if op == "strategy" {
				arg = "basic"
			}
			ladderAutoPost(t, h, s, peer, op, arg, state, func(e *ladder.Envelope) {
				e.Replay, e.ServerBoot, e.ReceiptBoot = true, "current", "previous"
			})
			s.automationMu.Lock()
			suppressed := s.ladderAutoSuppressed
			s.automationMu.Unlock()
			handle, _, _ := s.automationStatus()
			if !suppressed || handle != nil {
				t.Fatal("historical receipt renewed stopped automation")
			}
		})
	}
}

type webLocalLadderStrategy struct{ tier string }

func (s webLocalLadderStrategy) Info() battleauto.StrategyInfo {
	return battleauto.StrategyInfo{ID: "local_guard", Name: "Local guard", Tier: s.tier}
}

func (s webLocalLadderStrategy) Decide(ctx context.Context, snapshot aigame.Snapshot) (battleauto.Decision, bool, error) {
	if err := ctx.Err(); err != nil {
		return battleauto.Decision{}, false, err
	}
	return battleauto.Decision{Action: aigame.Action{Kind: aigame.ActionBattle, Command: "G"}}, snapshot.Battle.CommandReady, nil
}

func TestWebLocalLadderStrategyHostAndManualTakeover(t *testing.T) {
	for _, tier := range []string{"intermediate", "advanced"} {
		t.Run(tier, func(t *testing.T) {
			h, s, peer := battleSessionFixture(t)
			h.config.LadderStrategies = []battleauto.Strategy{webLocalLadderStrategy{tier: tier}}
			state := ladder.Snapshot{Phase: "lobby", Self: ladder.Player{ID: "self", Strategy: "local_guard", Ready: true}}
			response := ladderAutoPost(t, h, s, peer, "strategy", "local_guard", state)
			if response.Code != http.StatusOK {
				t.Fatal(response.Body.String())
			}
			handle, _, _ := s.automationStatus()
			runner, ok := handle.(battleauto.Runner)
			if !ok || !runner.LadderOnly || !runner.Strategies.Has("local_guard") {
				t.Fatal("extension not installed in host runner")
			}
			state.Phase = "battle"
			state.Match = &ladder.Match{ID: "local_match"}
			seedLadderAutoState(t, s, state)
			seedWebBattle(t, s)
			if command := readWebBattleCommandOrFail(t, peer, "local strategy"); command != "G" {
				t.Fatalf("extension emitted %q", command)
			}
			state.Self.Strategy = "manual"
			ladderAutoPost(t, h, s, peer, "strategy", "manual", state)
			openWebTurn(t, s, 41)
			if command, err := readWebBattleCommand(t, peer, 250*time.Millisecond); err == nil {
				t.Fatalf("extension continued after takeover: %q", command)
			}
		})
	}
}

func TestWebLadderDefaultPolicyManualAndNextMatch(t *testing.T) {
	h, s, peer := battleSessionFixture(t)
	state := ladder.Snapshot{Phase: "lobby", Self: ladder.Player{ID: "self", Strategy: "basic", Ready: true}}
	ladderAutoPost(t, h, s, peer, "ready", "", state)
	handle, _, generation := s.automationStatus()
	runner, ok := handle.(battleauto.Runner)
	if !ok || !runner.LadderOnly || runner.Policy.SeekEncounters || s.gate.State().Mode != aicontrol.Battle {
		t.Fatal("ready did not install ladder-only loop")
	}
	if err := h.ensureLadderAuto(context.Background(), s, generation); err != nil {
		t.Fatal(err)
	}
	if s.gate.State().Generation != generation {
		t.Fatal("second policy started")
	}
	state.Phase = "battle"
	state.Match = &ladder.Match{ID: "match_web"}
	seedLadderAutoState(t, s, state)
	seedWebBattle(t, s)
	if command := readWebBattleCommandOrFail(t, peer, "default ladder policy"); command != "H|A" {
		t.Fatal(command)
	}
	state.Self.Strategy = "manual"
	ladderAutoPost(t, h, s, peer, "strategy", "manual", state)
	if s.gate.State().Mode != aicontrol.Manual {
		t.Fatal("strategy did not hand controls back")
	}
	openWebTurn(t, s, 41)
	if command, err := readWebBattleCommand(t, peer, 250*time.Millisecond); err == nil {
		t.Fatalf("manual turn received %s", command)
	}
	state.Self.Strategy = "basic"
	ladderAutoPost(t, h, s, peer, "strategy", "basic", state)
	if command := readWebBattleCommandOrFail(t, peer, "reselected basic policy"); command != "H|A" {
		t.Fatal(command)
	}
	state.Phase = "lobby"
	state.Self.Ready = false
	state.Match = nil
	ladderAutoPost(t, h, s, peer, "ack", "", state)
	if s.gate.State().Mode != aicontrol.Manual {
		t.Fatal("ack left ladder loop running")
	}
	seedWebBattle(t, s)
	if command, err := readWebBattleCommand(t, peer, 250*time.Millisecond); err == nil {
		t.Fatalf("ordinary battle received %s", command)
	}
}

func TestRawLadderPacketsNeverStartBridgePolicy(t *testing.T) {
	_, s, peer := battleSessionFixture(t)
	seedLadderAutoState(t, s, ladder.Snapshot{Phase: "battle", Self: ladder.Player{Strategy: "basic"}, Match: &ladder.Match{ID: "cli_match"}})
	seedWebBattle(t, s)
	if command, err := readWebBattleCommand(t, peer, 250*time.Millisecond); err == nil {
		t.Fatalf("bridge competed with sactl: %s", command)
	}
	if handle, _, _ := s.automationStatus(); handle != nil {
		t.Fatal("raw protocol implicitly installed policy")
	}
}

func TestRetiredWebBattleLoopCannotOverwriteNewState(t *testing.T) {
	h, s, _ := battleSessionFixture(t)
	if err := h.startBattleLoop(s, 1, aicontrol.Battle, "old", false, true); err != nil {
		t.Fatal(err)
	}
	handle, _, oldGeneration := s.automationStatus()
	old := handle.(battleauto.Runner)
	manual, err := s.gate.Takeover("replace")
	if err != nil {
		t.Fatal(err)
	}
	s.clearAutomation(oldGeneration)
	if err := h.startBattleLoop(s, manual.Generation, aicontrol.Battle, "new", false, true); err != nil {
		t.Fatal(err)
	}
	handle, _, newGeneration := s.automationStatus()
	current := handle.(battleauto.Runner)
	current.Log("current")
	current.State(battleauto.State{Battles: 2})
	old.Log("old")
	old.State(battleauto.State{Battles: 99})
	s.clearAutomation(oldGeneration)
	if _, _, generation := s.automationStatus(); generation != newGeneration {
		t.Fatal("old exit cleared new loop")
	}
	state, known := s.automationStateSnapshot()
	if !known || state.Battles != 2 || s.automationNoteText() != "current" {
		t.Fatalf("retired callbacks overwrote state: %+v", state)
	}
}

func TestLadderTakeoverRetainsUnconfirmedNativeRequest(t *testing.T) {
	h, s, peer := battleSessionFixture(t)
	seedLadderAutoState(t, s, ladder.Snapshot{Phase: "battle", Self: ladder.Player{Strategy: "basic"}, Match: &ladder.Match{ID: "active"}})
	if err := h.startBattleLoop(s, 1, aicontrol.Battle, "ladder", false, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(response, httptest.NewRequest("POST", "/api/sessions/"+s.id+"/takeover", strings.NewReader(`{"generation":2}`)).WithContext(ctx))
		close(done)
	}()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	packet, err := bufio.NewReader(peer).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	written, err := decodeWebPacket(packet)
	if err != nil || written.Function != "S" {
		t.Fatalf("%+v %v", written, err)
	}
	cancel() // The server may have applied the request; no receipt arrived.
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("takeover did not finish")
	}
	var reply struct {
		controlResponse
		Code    string         `json:"code"`
		Request ladder.Request `json:"request"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	wire, _ := reply.Request.Wire()
	if response.Code != 502 || reply.Code != "outcome_unknown" || wire != written.Fields[0] || reply.Request.Operation != "strategy" || reply.Request.Argument != "manual" {
		t.Fatalf("lost original request: %d %s", response.Code, response.Body)
	}
	if reply.AutomationActive || reply.Control.Mode != aicontrol.Manual || reply.Control.Generation != 3 {
		t.Fatalf("local takeover hidden: %+v", reply)
	}
	if err := h.ensureLadderAuto(context.Background(), s, 3); err != nil {
		t.Fatal(err)
	}
	if handle, _, _ := s.automationStatus(); handle != nil {
		t.Fatal("unconfirmed takeover was auto-started again")
	}
}

func TestLadderHTTPWaitsForAuthorityWithoutHoldingControlFence(t *testing.T) {
	h, s, peer := battleSessionFixture(t)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(response, ladderHTTPRequest(t, 1)); close(done) }()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	packet, err := bufio.NewReader(peer).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	written, err := decodeWebPacket(packet)
	if err != nil || written.Function != "S" || written.Fields[0] != "LADDER|1|create_a|10|create|1" {
		t.Fatalf("write=%+v err=%v", written, err)
	}
	select {
	case <-done:
		t.Fatal("socket write was mistaken for confirmed mutation")
	default:
	}
	taken := make(chan error, 1)
	go func() { _, err := s.gate.Takeover("human takeover while waiting"); taken <- err }()
	select {
	case err := <-taken:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("receipt wait held control fence")
	}
	s.applyAuthoritativePacket(webServerPacket(t, 20, "S", `LADDER|{"version":1,"request_id":"create_a","request_wire":"LADDER|1|create_a|10|create|1","ok":true,"code":"ok","revision":11,"sequence":11,"snapshot":{"phase":"lobby"}}`))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("receipt waiter did not finish")
	}
	var reply ladder.Envelope
	if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil || response.Code != http.StatusOK || reply.RequestID != "create_a" || !reply.OK {
		t.Fatalf("response=%d %s err=%v", response.Code, response.Body, err)
	}
}

func TestLadderHTTPRejectsStaleGenerationAndAutomationOwner(t *testing.T) {
	for _, mode := range []aicontrol.Mode{aicontrol.Manual, aicontrol.Quest} {
		t.Run(string(mode), func(t *testing.T) {
			h, s, peer := battleSessionFixture(t)
			state, _, err := s.gate.Switch(1, mode, "test")
			if err != nil {
				t.Fatal(err)
			}
			generation := uint64(1)
			if mode == aicontrol.Quest {
				generation = state.Generation
			}
			response := httptest.NewRecorder()
			h.ServeHTTP(response, ladderHTTPRequest(t, generation))
			if response.Code != http.StatusConflict {
				t.Fatalf("%d %s", response.Code, response.Body)
			}
			_ = peer.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
			if n, _ := peer.Read(make([]byte, 128)); n != 0 {
				t.Fatal("rejected action wrote to upstream")
			}
		})
	}
}

func TestLadderHTTPRecoveryAndSnapshotRequired(t *testing.T) {
	h, s, _ := battleSessionFixture(t)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/sessions/battle-auto/ladder", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("missing initial status: %d", response.Code)
	}
	s.applyAuthoritativePacket(webServerPacket(t, 20, "S", `LADDER|{"version":1,"ok":true,"revision":7,"sequence":7,"snapshot":{"phase":"result"}}`))
	response = httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/sessions/battle-auto/ladder?cursor=7&stream=old-connection", nil))
	var batch ladder.Events
	if err := json.Unmarshal(response.Body.Bytes(), &batch); err != nil || !batch.Gap || batch.TimedOut || batch.Snapshot.Snapshot.Phase != "result" {
		t.Fatalf("recovery=%s err=%v", response.Body, err)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("character data may be cached")
	}
	if !s.ladderBlocksWorldAutomation(context.Background()) {
		t.Fatal("pending result did not reserve character")
	}
}

func TestLadderScriptAvailableLocally(t *testing.T) {
	h, _, _ := battleSessionFixture(t)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/ladder.js", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("StoneAgeLadder")) {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
}
