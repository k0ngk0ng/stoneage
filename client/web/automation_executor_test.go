package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiservice"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

type automationExecutorFixture struct {
	executor *AutomationExecutor
	session  *AutomationSession
	tcp      *tcpSession
	peer     net.Conn
	lease    context.Context

	plans    *automation.SQLiteStore
	receipts *aiservice.ReceiptStore
}

// flatAutomationTiles is only used by lifecycle tests. The executor still
// obtains the current position from the authoritative observer; this adapter
// supplies one reviewed, zero-cost route so the test never needs to calculate
// a path itself.
type flatAutomationTiles struct{}

func (flatAutomationTiles) RouteContext(_ context.Context, floor int, from, to ainavigation.Point) (ainavigation.Route, error) {
	if floor < 0 || from == to {
		return ainavigation.Route{Floor: floor, From: from, To: to}, nil
	}
	return ainavigation.Route{Floor: floor, From: from, To: to, Points: []ainavigation.Point{to}, Directions: "a"}, nil
}

func newAutomationExecutorFixture(t *testing.T, level, gold int32) automationExecutorFixture {
	t.Helper()
	left, right := net.Pipe()
	tcp := newTCPSession("automation-executor", left, 64*1024)
	t.Cleanup(func() {
		tcp.close()
		_ = right.Close()
	})
	seedWebAutomationState(t, tcp, level, gold)
	state, lease, err := tcp.gate.Switch(1, aicontrol.Leveling, "automation test")
	if err != nil {
		t.Fatal(err)
	}
	plans, err := automation.OpenStore(filepath.Join(t.TempDir(), "plans.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plans.Close() })
	receipts, err := aiservice.OpenReceiptStore(filepath.Join(t.TempDir(), "receipts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receipts.Close() })
	executor, err := NewAutomationExecutor(AutomationExecutorConfig{
		Knowledge:    &aiknowledge.Knowledge{Digest: "automation-test-knowledge"},
		Tiles:        flatAutomationTiles{},
		Plans:        plans,
		Receipts:     receipts,
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return automationExecutorFixture{
		executor: executor,
		session:  &AutomationSession{ID: tcp.id, session: tcp, mode: aicontrol.Leveling, generation: state.Generation},
		tcp:      tcp,
		peer:     right,
		lease:    lease,
		plans:    plans,
		receipts: receipts,
	}
}

func seedWebAutomationState(t *testing.T, session *tcpSession, level, gold int32) {
	t.Helper()
	session.applyAuthoritativeClientPacket(webClientPacket(t, 1, "ClientLogin", "automation-account", "ignored"))
	session.applyAuthoritativeClientPacket(webClientPacket(t, 2, "CharLogin", "AutomationHero"))
	session.applyAuthoritativePacket(webServerPacket(t, 3, "CharLogin", "successful", ""))
	session.applyAuthoritativePacket(webServerPacket(t, 4, "S", "C100|30|30|4|5"))
	values := []string{
		"100", "100", "20", "20", "5", "5", "5", "5", "0", "1000",
		strconv.FormatInt(int64(level), 10), "10", "10", "10", "10", "10", "10", "10", "10", "10",
		strconv.FormatInt(int64(gold), 10), "0", "0", "0", "0", "0", "0", "0",
		"AutomationHero", "",
	}
	session.applyAuthoritativePacket(webServerPacket(t, 5, "S", "P1|"+strings.Join(values, "|")))
}

func levelingAutomationRequest(generation uint64, targetLevel int, reserve int64) AutomationStartRequest {
	return AutomationStartRequest{
		SessionID:  "automation-executor",
		Mode:       aicontrol.Leveling,
		Generation: generation,
		Config: AutomationConfig{
			Targets:        []AutomationTarget{{Kind: "character", Level: targetLevel}},
			TargetPolicy:   "all",
			MaximumSeconds: 30,
			MaximumDeaths:  0,
			Budget:         AutomationBudget{Reserve: reserve},
		},
	}
}

func waitAutomationReceipt(t *testing.T, handle *webAutomationHandle, want func(aimcp.TaskReceipt) bool) aimcp.TaskReceipt {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		receipt, err := handle.status(context.Background())
		if err == nil && want(receipt) {
			return receipt
		}
		time.Sleep(time.Millisecond)
	}
	receipt, err := handle.status(context.Background())
	t.Fatalf("automation receipt did not reach expected state: %+v err=%v", receipt, err)
	return aimcp.TaskReceipt{}
}

func TestAutomationExecutorPreviewUsesAuthoritativeBudgetWithoutWriting(t *testing.T) {
	fixture := newAutomationExecutorFixture(t, 1, 0)
	state := fixture.session.State()
	request := levelingAutomationRequest(state.Generation, 2, 100)
	before := fixture.tcp.authoritativeSnapshot()
	preview, err := fixture.executor.Preview(context.Background(), fixture.session, request)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Ready || preview.AlreadyComplete {
		t.Fatalf("insufficient budget preview was accepted: %+v", preview)
	}
	if preview.Budget.Reserve != 100 || !containsAutomationProblem(preview.Problems, "可用石币不足保留金额") {
		t.Fatalf("preview budget/problem mismatch: %+v", preview)
	}
	after := fixture.tcp.authoritativeSnapshot()
	if before.Revision != after.Revision {
		t.Fatalf("preview changed authoritative state: before=%+v after=%+v", before, after)
	}
	fixture.peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err := bufio.NewReader(fixture.peer).ReadBytes('\n'); err == nil {
		t.Fatal("preview wrote a game packet")
	}
}

func TestAutomationExecutorStartCompletedReleasesLease(t *testing.T) {
	fixture := newAutomationExecutorFixture(t, 5, 0)
	fixture.executor.config.NoProgressTimeout = 17 * time.Second
	state := fixture.session.State()
	request := levelingAutomationRequest(state.Generation, 5, 0)
	handleValue, err := fixture.executor.Start(fixture.lease, fixture.session, request)
	if err != nil {
		t.Fatal(err)
	}
	handle, ok := handleValue.(*webAutomationHandle)
	if !ok || handle == nil {
		t.Fatalf("unexpected automation handle: %#v", handleValue)
	}
	levelingRequest, err := handle.levelingRequest(request.Config)
	if err != nil || levelingRequest.NoProgressTimeout != 17*time.Second || handle.leveling.NoProgressTimeout != 17*time.Second {
		t.Fatalf("server no-progress timeout was not wired: request=%+v controller=%v err=%v", levelingRequest, handle.leveling.NoProgressTimeout, err)
	}
	if got := fixture.session.State(); got.Mode != aicontrol.Leveling || got.Generation != state.Generation {
		t.Fatalf("start changed control before activation: before=%+v after=%+v", state, got)
	}
	activateAutomationHandle(handle)
	if got := fixture.session.State(); got.Mode != aicontrol.Manual || got.Generation <= state.Generation {
		t.Fatalf("completed run did not release control: %+v", got)
	}
	if active, _, generation := fixture.tcp.automationStatus(); active != nil || generation != 0 {
		t.Fatalf("completed run left session automation marker: handle=%v generation=%d", active, generation)
	}
}

func TestAutomationExecutorLeaseCancellationStopsRunAndRejectsResume(t *testing.T) {
	fixture := newAutomationExecutorFixture(t, 1, 0)
	state := fixture.session.State()
	request := levelingAutomationRequest(state.Generation, 2, 0)
	handleValue, err := fixture.executor.Start(fixture.lease, fixture.session, request)
	if err != nil {
		t.Fatal(err)
	}
	handle := handleValue.(*webAutomationHandle)
	if _, err := fixture.tcp.gate.Takeover("human takeover"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fixture.lease.Done():
	case <-time.After(time.Second):
		t.Fatal("takeover did not cancel the automation lease")
	}
	receipt := waitAutomationReceipt(t, handle, func(receipt aimcp.TaskReceipt) bool {
		return receipt.State == string(automation.Paused)
	})
	if receipt.Status != aimcp.ReceiptFailed {
		t.Fatalf("lease cancellation receipt=%+v", receipt)
	}
	if err := handle.Resume(context.Background()); !errors.Is(err, aicontrol.ErrOwner) {
		t.Fatalf("stale automation handle resumed after takeover: %v", err)
	}
	if err := handle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func containsAutomationProblem(problems []string, want string) bool {
	for _, problem := range problems {
		if problem == want {
			return true
		}
	}
	return false
}

func (session *tcpSession) authoritativeSnapshot() aigame.Snapshot {
	if session == nil {
		return aigame.Snapshot{}
	}
	session.authoritativeMu.RLock()
	observer := session.authoritative
	session.authoritativeMu.RUnlock()
	if observer == nil {
		return aigame.Snapshot{}
	}
	return observer.Snapshot()
}

func TestHumanQuestPetSelectionReachesTaskRequest(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/", strings.NewReader(`{"mode":"quest","task_id":"quest-a","selected_pet_id":"owned-pet"}`))
	input, err := decodeAutomationStart(response, request)
	if err != nil {
		t.Fatal(err)
	}
	handle := &webAutomationHandle{}
	task, err := handle.questRequest(AutomationConfig{TaskID: input.TaskID, SelectedPetID: input.SelectedPetID})
	if err != nil {
		t.Fatal(err)
	}
	var pet string
	if err := json.Unmarshal(task.Parameters["selected_pet_id"], &pet); err != nil || pet != "owned-pet" {
		t.Fatalf("pet=%q err=%v", pet, err)
	}
}

func TestHumanQuestPreviewRejectsForeignPetBeforeCompilation(t *testing.T) {
	fixture := newAutomationExecutorFixture(t, 10, 5000)
	request := AutomationStartRequest{
		Generation: fixture.session.generation,
		Mode:       aicontrol.Quest,
		Config:     AutomationConfig{TaskID: "missing-task", SelectedPetID: "foreign-pet"},
	}
	preview, err := fixture.executor.Preview(context.Background(), fixture.session, request)
	if err == nil || !strings.Contains(err.Error(), "unique owned stable identity") || preview.Ready {
		t.Fatalf("foreign pet reached compilation or a ready preview: %+v %v", preview, err)
	}
}
