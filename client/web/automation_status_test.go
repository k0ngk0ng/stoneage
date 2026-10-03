package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func TestQuestTerminalReceiptSurvivesHandleClearAndExcludesEvidence(t *testing.T) {
	handler, session, provider := recoveryHTTPFixture(t)
	provider.offer = nil
	state, _, err := session.gate.Switch(1, aicontrol.Quest, "test")
	if err != nil {
		t.Fatal(err)
	}
	handle := &webAutomationHandle{session: boundAutomationSession(session), mode: aicontrol.Quest, generation: state.Generation}
	if !session.setAutomation(handle, aicontrol.Quest, state.Generation) {
		t.Fatal("set failed")
	}
	handle.finishReceipt(aimcp.TaskReceipt{Handle: "q", Status: aimcp.ReceiptConfirmed, State: "completed", Evidence: json.RawMessage(`{"private":"plan evidence"}`), Progress: &aimcp.TaskProgress{Step: 14, Steps: 14}})
	result := handler.controlSnapshot(session)
	if result.AutomationActive || result.Task == nil || result.Task.Status != aimcp.ReceiptConfirmed || result.Task.Progress.Step != 14 || len(result.Task.Evidence) != 0 {
		t.Fatalf("result=%+v task=%+v", result, result.Task)
	}
	next, _, err := session.gate.Switch(result.Control.Generation, aicontrol.Quest, "next")
	if err != nil {
		t.Fatal(err)
	}
	if !session.setAutomation(&recoveryHTTPHandle{}, aicontrol.Quest, next.Generation) {
		t.Fatal("next set failed")
	}
	session.rememberTaskReceipt(handle, aimcp.TaskReceipt{Handle: "old"})
	if result := handler.controlSnapshot(session); result.Task != nil {
		t.Fatalf("old receipt leaked to new run: %+v", result.Task)
	}
}

type cancelTestHandle struct {
	recoveryHTTPHandle
	err error
}

func (h *cancelTestHandle) Stop(context.Context) error { h.stops++; return h.err }

func TestQuestCancelFencesStaleRequestsAndRetainsUnconfirmedHandle(t *testing.T) {
	handler, session, provider := recoveryHTTPFixture(t)
	provider.offer = nil
	state, _, err := session.gate.Switch(1, aicontrol.Quest, "test")
	if err != nil {
		t.Fatal(err)
	}
	handle := &cancelTestHandle{err: errors.New("checkpoint unavailable")}
	session.setAutomation(handle, aicontrol.Quest, state.Generation)
	call := func(g uint64, mode string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		handler.cancelTaskAutomation(response, httptest.NewRequest("POST", "/automation/cancel", strings.NewReader(fmt.Sprintf(`{"generation":%d,"mode":%q}`, g, mode))), session)
		return response
	}
	if r := call(state.Generation-1, "quest"); r.Code != 409 || handle.stops != 0 {
		t.Fatalf("stale cancel=%d stops=%d", r.Code, handle.stops)
	}
	if r := call(state.Generation, "leveling"); r.Code != 409 || handle.stops != 0 {
		t.Fatal("wrong task type cancelled")
	}
	if r := call(state.Generation, "quest"); r.Code != 502 || handle.stops != 1 {
		t.Fatalf("failed cancel=%d %s", r.Code, r.Body.String())
	}
	if current, _, _ := session.automationStatus(); current != handle || session.gate.State().Mode != aicontrol.Paused {
		t.Fatal("unconfirmed cancellation erased handle")
	}
	handle.err = nil
	if r := call(session.gate.State().Generation, "quest"); r.Code != 200 || session.gate.State().Mode != aicontrol.Manual {
		t.Fatalf("retry cancel=%d %s", r.Code, r.Body.String())
	}
	if current, _, _ := session.automationStatus(); current != nil {
		t.Fatal("cancelled handle retained")
	}
}

func TestAutomationStopFailureCannotBecomeFalseSuccess(t *testing.T) {
	fixture := newAutomationExecutorFixture(t, 1, 0)
	store := &cancelFailStore{Store: fixture.plans}
	fixture.executor.config.Plans = store
	value, err := fixture.executor.Start(fixture.lease, fixture.session, levelingAutomationRequest(fixture.session.State().Generation, 2, 0))
	if err != nil {
		t.Fatal(err)
	}
	handle := value.(*webAutomationHandle)
	if _, err := fixture.tcp.gate.Takeover("stop test"); err != nil {
		t.Fatal(err)
	}
	waitAutomationReceipt(t, handle, func(receipt aimcp.TaskReceipt) bool { return receipt.State == string(automation.Paused) })
	store.fail.Store(true)
	if err := handle.Stop(context.Background()); err == nil {
		t.Fatal("failed persistence reported success")
	}
	if handle.stopReceipt != nil {
		t.Fatal("failed cancellation has receipt")
	}
	store.fail.Store(false)
	if err := handle.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if handle.stopReceipt == nil {
		t.Fatal("second stop skipped persistence")
	}
	receipt, err := handle.status(context.Background())
	if err != nil || receipt.Status != aimcp.ReceiptCancelled {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
}

type cancelFailStore struct {
	automation.Store
	fail atomic.Bool
}

func (s *cancelFailStore) Save(ctx context.Context, checkpoint automation.Checkpoint, revision uint64) error {
	if s.fail.Load() {
		return errors.New("checkpoint storage unavailable")
	}
	return s.Store.Save(ctx, checkpoint, revision)
}
