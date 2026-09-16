package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
)

func boundAutomationSession(session *tcpSession) *AutomationSession {
	state := session.gate.State()
	return &AutomationSession{ID: session.id, session: session, mode: state.Mode, generation: state.Generation}
}
func (handler *Handler) recoveryOffer(ctx context.Context, session *tcpSession) (*AutomationRecovery, error) {
	provider, ok := handler.automationExecutor().(automationRecoveryProvider)
	if !ok || session == nil || session.gate == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, defaultWriteTimeout)
	defer cancel()
	return provider.Recovery(ctx, boundAutomationSession(session))
}
func (handler *Handler) checkedRecovery(ctx context.Context, session *tcpSession, input controlRequest) (automationRecoveryProvider, *AutomationRecovery, error) {
	provider, ok := handler.automationExecutor().(automationRecoveryProvider)
	if !ok || session == nil || session.gate == nil {
		return nil, nil, errors.New("任务恢复服务不可用")
	}
	state := session.gate.State()
	if input.Generation == nil || *input.Generation == 0 || state.Generation != *input.Generation {
		return nil, nil, aicontrol.ErrStale
	}
	if handle, _, _ := session.automationStatus(); handle != nil || state.Mode != aicontrol.Manual {
		return nil, nil, errors.New("请先停止当前自动任务")
	}
	offer, err := handler.recoveryOffer(ctx, session)
	if err != nil {
		return nil, nil, err
	}
	if offer == nil || offer.Handle != input.RecoveryHandle {
		return nil, nil, errors.New("待恢复任务已变化，请刷新状态")
	}
	if (offer.Mode != aicontrol.Quest && offer.Mode != aicontrol.Leveling) || (input.Mode != "" && input.Mode != string(offer.Mode)) {
		return nil, nil, errors.New("待恢复任务类型不匹配")
	}
	return provider, offer, nil
}
func (handler *Handler) resumeRecoveredAutomation(response http.ResponseWriter, request *http.Request, session *tcpSession, input controlRequest) {
	provider, offer, err := handler.checkedRecovery(request.Context(), session, input)
	if err != nil {
		controlError(response, err)
		return
	}
	state, lease, err := session.gate.Switch(*input.Generation, offer.Mode, "玩家恢复断线前的任务")
	if err != nil {
		controlError(response, err)
		return
	}
	bound := &AutomationSession{ID: session.id, session: session, mode: offer.Mode, generation: state.Generation}
	handle, err := provider.Recover(lease, bound, offer.Handle)
	if err != nil || handle == nil {
		// Compare generations: an intervening human takeover must not be revoked
		// by a late recovery failure. Keep the old checkpoint available for review.
		_, _, _ = session.gate.Switch(state.Generation, aicontrol.Manual, "任务恢复失败，原进度保留")
		if err == nil {
			err = errors.New("任务恢复没有返回执行句柄")
		}
		http.Error(response, err.Error(), http.StatusConflict)
		return
	}
	if !session.setAutomation(handle, offer.Mode, state.Generation) {
		stopAutomationHandle(handle)
		http.Error(response, "恢复期间控制权已变化", http.StatusConflict)
		return
	}
	activateAutomationHandle(handle)
	handler.writeJSON(response, http.StatusOK, handler.controlSnapshot(session))
}
func (handler *Handler) discardRecoveredAutomation(response http.ResponseWriter, request *http.Request, session *tcpSession, input controlRequest) {
	provider, offer, err := handler.checkedRecovery(request.Context(), session, input)
	if err != nil {
		controlError(response, err)
		return
	}
	if _, _, err = session.gate.Switch(*input.Generation, aicontrol.Manual, "玩家取消断线前的任务"); err != nil {
		controlError(response, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), defaultWriteTimeout)
	defer cancel()
	if err = provider.DiscardRecovery(ctx, boundAutomationSession(session), offer.Handle); err != nil {
		controlError(response, err)
		return
	}
	handler.writeJSON(response, http.StatusOK, handler.controlSnapshot(session))
}
