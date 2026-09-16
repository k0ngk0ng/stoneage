package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
)

type recoveryHTTPHandle struct {
	stops     int
	detaches  int
	resumeErr error
	pauseErr  error
}

func (h *recoveryHTTPHandle) Stop(context.Context) error   { h.stops++; return nil }
func (h *recoveryHTTPHandle) Detach(context.Context) error { h.detaches++; return nil }
func (h *recoveryHTTPHandle) Resume(context.Context) error { return h.resumeErr }

type recoveryHTTPProvider struct {
	offer  *AutomationRecovery
	handle *recoveryHTTPHandle
	calls  int
	fail   bool
	before func(*AutomationSession)
}

func (p *recoveryHTTPProvider) Start(context.Context, *AutomationSession, AutomationStartRequest) (AutomationHandle, error) {
	return nil, errors.New("unexpected start")
}
func (p *recoveryHTTPProvider) Recovery(context.Context, *AutomationSession) (*AutomationRecovery, error) {
	return p.offer, nil
}
func (p *recoveryHTTPProvider) Recover(ctx context.Context, s *AutomationSession, id string) (AutomationHandle, error) {
	p.calls++
	if ctx.Err() != nil || s.mode != aicontrol.Quest || s.generation != s.State().Generation {
		return nil, errors.New("invalid recovery lease")
	}
	if p.before != nil {
		p.before(s)
	}
	if p.fail {
		return nil, errors.New("delivery remains unknown")
	}
	p.offer = nil
	return p.handle, nil
}
func (p *recoveryHTTPProvider) DiscardRecovery(context.Context, *AutomationSession, string) error {
	p.offer = nil
	return nil
}
func recoveryHTTPFixture(t *testing.T) (*Handler, *tcpSession, *recoveryHTTPProvider) {
	t.Helper()
	left, right := net.Pipe()
	session := newTCPSession("reconnected", left, 64*1024)
	t.Cleanup(func() { session.close(); right.Close() })
	provider := &recoveryHTTPProvider{offer: &AutomationRecovery{Handle: "old-task", Mode: aicontrol.Quest}, handle: &recoveryHTTPHandle{}}
	handler := &Handler{}
	handler.SetAutomation(provider)
	return handler, session, provider
}
func TestRecoveredAutomationRequiresExplicitCurrentOffer(t *testing.T) {
	handler, session, provider := recoveryHTTPFixture(t)
	if got := handler.controlSnapshot(session); got.Recovery == nil || got.AutomationActive {
		t.Fatalf("recovery was not offered as inactive: %+v", got)
	}
	generation := session.gate.State().Generation
	for _, body := range []string{fmt.Sprintf(`{"generation":%d,"recovery_handle":"other"}`, generation), fmt.Sprintf(`{"generation":%d,"recovery_handle":"old-task"}`, generation+1)} {
		response := httptest.NewRecorder()
		handler.resumeAutomation(response, httptest.NewRequest("POST", "/resume", strings.NewReader(body)), session)
		if response.Code != 409 || provider.calls != 0 {
			t.Fatalf("stale offer accepted: %d", response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.resumeAutomation(response, httptest.NewRequest("POST", "/resume", strings.NewReader(fmt.Sprintf(`{"generation":%d,"recovery_handle":"old-task","mode":"quest"}`, generation))), session)
	if response.Code != 200 || provider.calls != 1 || session.gate.State().Mode != aicontrol.Quest {
		t.Fatalf("explicit recovery failed: %d %s", response.Code, response.Body.String())
	}
	if handle, _, _ := session.automationStatus(); handle != provider.handle {
		t.Fatal("recovered handle not installed")
	}
}
func TestRecoveryFailurePreservesOfferAndConcurrentTakeover(t *testing.T) {
	for _, takeover := range []bool{false, true} {
		t.Run(fmt.Sprint(takeover), func(t *testing.T) {
			handler, session, provider := recoveryHTTPFixture(t)
			provider.fail = true
			var taken uint64
			if takeover {
				provider.before = func(s *AutomationSession) {
					state, err := s.session.gate.Takeover("human")
					if err != nil {
						t.Fatal(err)
					}
					taken = state.Generation
				}
			}
			generation := session.gate.State().Generation
			response := httptest.NewRecorder()
			handler.resumeAutomation(response, httptest.NewRequest("POST", "/resume", strings.NewReader(fmt.Sprintf(`{"generation":%d,"recovery_handle":"old-task"}`, generation))), session)
			if response.Code != 409 || provider.offer == nil || provider.handle.stops != 0 || session.gate.State().Mode != aicontrol.Manual {
				t.Fatal("failure cancelled checkpoint or retained game control")
			}
			if takeover && session.gate.State().Generation != taken {
				t.Fatal("late recovery failure revoked newer takeover")
			}
		})
	}
}
func TestSessionDisconnectDetachesButExplicitTakeoverCancels(t *testing.T) {
	handler, session, provider := recoveryHTTPFixture(t)
	state, _, err := session.gate.Switch(session.gate.State().Generation, aicontrol.Quest, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !session.setAutomation(provider.handle, aicontrol.Quest, state.Generation) {
		t.Fatal("install")
	}
	session.closeGate()
	if provider.handle.detaches != 1 || provider.handle.stops != 0 {
		t.Fatal("disconnect cancelled recoverable run")
	}
	handler, session, provider = recoveryHTTPFixture(t)
	state, _, _ = session.gate.Switch(session.gate.State().Generation, aicontrol.Quest, "test")
	session.setAutomation(provider.handle, aicontrol.Quest, state.Generation)
	response := httptest.NewRecorder()
	handler.takeover(response, httptest.NewRequest("POST", "/takeover", strings.NewReader(`{}`)), session)
	if response.Code != 200 || provider.handle.stops != 1 || provider.handle.detaches != 0 {
		t.Fatal("explicit takeover retained old run")
	}
}
func TestSameSessionUnconfirmedResumeKeepsCheckpoint(t *testing.T) {
	handler, session, provider := recoveryHTTPFixture(t)
	provider.offer = nil
	provider.handle.resumeErr = errors.New("unknown delivery")
	state, _, _ := session.gate.Switch(session.gate.State().Generation, aicontrol.Quest, "test")
	session.setAutomation(provider.handle, aicontrol.Quest, state.Generation)
	state, _, _ = session.gate.Switch(state.Generation, aicontrol.Paused, "pause")
	response := httptest.NewRecorder()
	handler.resumeAutomation(response, httptest.NewRequest("POST", "/resume", strings.NewReader(fmt.Sprintf(`{"generation":%d}`, state.Generation))), session)
	if response.Code != 502 || provider.handle.stops != 0 || session.gate.State().Mode != aicontrol.Paused {
		t.Fatal("unconfirmed resume cancelled task")
	}
	if handle, _, _ := session.automationStatus(); handle != provider.handle {
		t.Fatal("unconfirmed handle disappeared")
	}
}

func TestDiscardRequiresMatchingOfferAndStartCannotBypassRecovery(t *testing.T) {
	handler, session, provider := recoveryHTTPFixture(t)
	generation := session.gate.State().Generation
	response := httptest.NewRecorder()
	handler.startAutomation(response, httptest.NewRequest("POST", "/automation/start", strings.NewReader(fmt.Sprintf(`{"generation":%d,"mode":"quest","task_id":"quest","maximum_seconds":60,"budget":{"maximum_spend":0}}`, generation))), session)
	if response.Code != 409 || session.gate.State().Generation != generation || provider.offer == nil {
		t.Fatalf("new task bypassed recovery: %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.takeover(response, httptest.NewRequest("POST", "/takeover", strings.NewReader(fmt.Sprintf(`{"generation":%d,"recovery_handle":"wrong"}`, generation))), session)
	if response.Code != 409 || provider.offer == nil {
		t.Fatal("stale cancellation removed recovery")
	}
	response = httptest.NewRecorder()
	handler.takeover(response, httptest.NewRequest("POST", "/takeover", strings.NewReader(fmt.Sprintf(`{"generation":%d,"recovery_handle":"old-task"}`, generation))), session)
	if response.Code != 200 || provider.offer != nil || session.gate.State().Mode != aicontrol.Manual {
		t.Fatalf("explicit discard failed: %d", response.Code)
	}
}

func (h *recoveryHTTPHandle) Pause(context.Context) error { return h.pauseErr }
func TestUnconfirmedPauseDoesNotCancelCheckpoint(t *testing.T) {
	handler, session, provider := recoveryHTTPFixture(t)
	provider.offer = nil
	provider.handle.pauseErr = errors.New("pause confirmation timed out")
	state, _, _ := session.gate.Switch(session.gate.State().Generation, aicontrol.Quest, "test")
	session.setAutomation(provider.handle, aicontrol.Quest, state.Generation)
	response := httptest.NewRecorder()
	handler.pauseAutomation(response, httptest.NewRequest("POST", "/pause", strings.NewReader(fmt.Sprintf(`{"generation":%d}`, state.Generation))), session)
	if response.Code != 502 || provider.handle.stops != 0 || session.gate.State().Mode != aicontrol.Paused {
		t.Fatal("unconfirmed pause cancelled task")
	}
	if handle, _, _ := session.automationStatus(); handle != provider.handle {
		t.Fatal("unconfirmed pause lost checkpoint handle")
	}
}
