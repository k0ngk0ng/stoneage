package admin

import (
	"context"
	"errors"
	"net/http"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/aisupervisor"
)

type AIUnknownRecovery interface {
	UnknownRecovery(context.Context, string) (aisupervisor.UnknownRecoveryStatus, error)
	ReviewUnknown(context.Context, aisupervisor.UnknownRecoveryRequest) error
}

func (adapter *AISupervisorRuntimeAdapter) UnknownRecovery(ctx context.Context, id string) (aisupervisor.UnknownRecoveryStatus, error) {
	runtime, ok := adapter.supervisor.(AIUnknownRecovery)
	if !ok {
		return aisupervisor.UnknownRecoveryStatus{}, ErrAIRuntimeUnavailable
	}
	return runtime.UnknownRecovery(ctx, id)
}
func (adapter *AISupervisorRuntimeAdapter) ReviewUnknown(ctx context.Context, request aisupervisor.UnknownRecoveryRequest) error {
	runtime, ok := adapter.supervisor.(AIUnknownRecovery)
	if !ok {
		return ErrAIRuntimeUnavailable
	}
	return runtime.ReviewUnknown(ctx, request)
}

func (server *Server) aiUnknownRecoveryAPI(response http.ResponseWriter, request *http.Request, data *pageData, id string) {
	if !aiIDPattern.MatchString(id) {
		playerJSONError(response, http.StatusNotFound, "AI 玩家不存在")
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		playerJSONError(response, http.StatusMethodNotAllowed, "请求方式不支持")
		return
	}
	if request.Method == http.MethodPost && !server.aiWriteRequest(response, request, data) {
		return
	}
	runtime, ok := server.aiRuntime.(AIUnknownRecovery)
	if !ok {
		playerJSONError(response, http.StatusServiceUnavailable, "当前运行时不支持异常轮次核查")
		return
	}
	if request.Method == http.MethodGet {
		status, err := runtime.UnknownRecovery(request.Context(), id)
		if errors.Is(err, airuntime.ErrNotFound) {
			playerJSON(response, http.StatusOK, map[string]any{"recovery": nil})
			return
		}
		if err != nil {
			playerJSONError(response, http.StatusConflict, "暂时无法核查；请先停止 AI 玩家并稍后刷新")
			return
		}
		playerJSON(response, http.StatusOK, map[string]any{"recovery": status})
		return
	}
	var input aisupervisor.UnknownRecoveryRequest
	if err := decodeAIJSON(request, &input); err != nil {
		aiDecodeError(response, err)
		return
	}
	if input.ProfileID != id || input.AttemptID == "" || input.Reason != airuntime.UnknownReviewReason {
		playerJSONError(response, http.StatusBadRequest, "核查信息无效")
		return
	}
	input.Actor = data.Session.Username
	if err := runtime.ReviewUnknown(request.Context(), input); err != nil {
		_ = server.recordAIAudit(request.Context(), data, "ai_unknown_review_failed", id, map[string]any{"attempt_id": input.AttemptID, "result": "not_accepted"})
		playerJSONError(response, http.StatusConflict, "核查未完成或状态已变化，请刷新后重试；旧请求不会自动重发")
		return
	}
	playerJSON(response, http.StatusOK, map[string]any{"ok": true, "message": "已保留未知记录并完成核查；AI 玩家保持停止，可手动启动新轮次"})
}
