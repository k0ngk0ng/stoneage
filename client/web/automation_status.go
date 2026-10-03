package main

import (
	"context"
	"net/http"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aimcp"
)

// Only a summary crosses the public boundary, never the full evidence/plan.
func publicTaskReceipt(receipt aimcp.TaskReceipt) *aimcp.TaskReceipt {
	receipt.Evidence = nil
	return &receipt
}

func (s *tcpSession) rememberTaskReceipt(handle *webAutomationHandle, receipt aimcp.TaskReceipt) {
	s.automationMu.Lock()
	defer s.automationMu.Unlock()
	if s.automationHandle == handle {
		s.automationReceipt = publicTaskReceipt(receipt)
	}
}

func (s *tcpSession) taskReceiptSnapshot() *aimcp.TaskReceipt {
	s.automationMu.Lock()
	handle, receipt := s.automationHandle, s.automationReceipt
	s.automationMu.Unlock()
	if provider, ok := handle.(interface {
		status(context.Context) (aimcp.TaskReceipt, error)
	}); ok {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if current, err := provider.status(ctx); err == nil {
			return publicTaskReceipt(current)
		}
	}
	return receipt
}

// Unlike manual takeover, cancel is scoped to the observed task generation.
// A delayed CLI request must not cancel a newer task or an Arena controller.
func (handler *Handler) cancelTaskAutomation(response http.ResponseWriter, request *http.Request, session *tcpSession) {
	input, err := decodeControlRequest(response, request)
	if err != nil || input.Generation == nil || *input.Generation == 0 || (input.Mode != "quest" && input.Mode != "leveling") {
		http.Error(response, "task mode and current control generation are required", http.StatusBadRequest)
		return
	}
	if input.RecoveryHandle != "" {
		_, offer, err := handler.checkedRecovery(request.Context(), session, input)
		if err != nil || offer == nil || string(offer.Mode) != input.Mode {
			http.Error(response, "task recovery changed", http.StatusConflict)
			return
		}
		handler.discardRecoveredAutomation(response, request, session, input)
		return
	}
	handle, mode, generation := session.automationStatus()
	state := session.gate.State()
	stopper, ok := handle.(automationStopper)
	if !ok || generation == 0 || string(mode) != input.Mode || (state.Mode != mode && state.Mode != aicontrol.Paused) {
		http.Error(response, "matching task automation is not active", http.StatusConflict)
		return
	}
	stopped, _, err := session.gate.Switch(*input.Generation, aicontrol.Paused, "正在取消自动任务")
	if err != nil {
		controlError(response, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), defaultWriteTimeout)
	defer cancel()
	if err := stopper.Stop(ctx); err != nil {
		// Keep the handle visible: cancellation was not durably confirmed.
		http.Error(response, "task stopped sending actions but cancellation is unconfirmed: "+err.Error(), http.StatusBadGateway)
		return
	}
	if native, ok := handle.(*webAutomationHandle); ok {
		if receipt, err := native.status(ctx); err == nil {
			session.rememberTaskReceipt(native, receipt)
		}
	}
	if _, _, err := session.gate.Switch(stopped.Generation, aicontrol.Manual, "玩家取消自动任务"); err != nil {
		controlError(response, err)
		return
	}
	session.clearAutomation(generation)
	handler.writeJSON(response, http.StatusOK, handler.controlSnapshot(session))
}
