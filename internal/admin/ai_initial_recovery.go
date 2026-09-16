package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/aiprovision"
	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

// AIInitialRecoveryProvider is the optional server-owned boundary for
// interrupted AI-player creation. The provider owns the initialization
// journal, game-state verification, identity checks, publication lock and
// profile persistence; the admin package only authenticates and audits the
// request. RecoverInitial must publish an already verified initialization and
// must leave the resulting profile stopped.
type AIInitialRecoveryProvider interface {
	ListInitialRecoveries(context.Context) ([]aiprovision.InitialRecoveryView, error)
	RecoverInitial(context.Context, string, string, *int64) (airuntime.Profile, error)
}

func (server *Server) aiInitialRecoveryProvider() (AIInitialRecoveryProvider, bool) {
	if server == nil || server.aiProvisioner == nil {
		return nil, false
	}
	provider, ok := server.aiProvisioner.(AIInitialRecoveryProvider)
	if !ok || provider == nil {
		return nil, false
	}
	return provider, true
}

// aiInitializationsAPI lists journal entries, including entries whose profile
// publication did not complete. A nil provider is reported as unavailable so
// a missing recovery adapter cannot look like an empty journal.
func (server *Server) aiInitializationsAPI(response http.ResponseWriter, request *http.Request, _ *pageData) {
	if request.Method != http.MethodGet {
		playerJSONError(response, http.StatusMethodNotAllowed, "请求方式不支持")
		return
	}
	provider, ok := server.aiInitialRecoveryProvider()
	if !ok {
		playerJSONError(response, http.StatusServiceUnavailable, "AI 初始化恢复服务尚未配置")
		return
	}
	initializations, err := provider.ListInitialRecoveries(request.Context())
	if err != nil {
		// The provider may wrap database paths, account names or other private
		// state. Keep all such details server-side.
		playerJSONError(response, http.StatusInternalServerError, "AI 初始化记录读取失败")
		return
	}
	if initializations == nil {
		initializations = []aiprovision.InitialRecoveryView{}
	}
	playerJSON(response, http.StatusOK, map[string]any{"initializations": initializations})
}

func (server *Server) aiInitialRecoveryAPI(response http.ResponseWriter, request *http.Request, data *pageData, suffix string) {
	parts := strings.Split(strings.Trim(suffix, "/"), "/")
	if len(parts) != 2 || parts[1] != "recover" || !aiIDPattern.MatchString(parts[0]) {
		playerJSONError(response, http.StatusNotFound, "初始化记录不存在")
		return
	}
	id := parts[0]
	if request.Method != http.MethodPost {
		playerJSONError(response, http.StatusMethodNotAllowed, "恢复发布必须使用 POST")
		return
	}
	if !server.aiWriteRequest(response, request, data) {
		return
	}
	provider, ok := server.aiInitialRecoveryProvider()
	if !ok {
		_ = server.recordAIAudit(request.Context(), data, "ai_initialization_recovery_failed", id, map[string]any{"result": "unavailable"})
		playerJSONError(response, http.StatusServiceUnavailable, "AI 初始化恢复服务尚未配置")
		return
	}
	profile, err := provider.RecoverInitial(request.Context(), id, data.Session.Username, adminID(data))
	if err != nil {
		_ = server.recordAIAudit(request.Context(), data, "ai_initialization_recovery_failed", id, map[string]any{"result": "failed"})
		aiInitialRecoveryError(response, err)
		return
	}
	// A malformed provider result must never cause the admin page to claim that
	// another identity was recovered. It also keeps the response shape aligned
	// with the normal provisioning endpoint without exposing any credentials.
	if profile.ID != id || strings.TrimSpace(profile.Account.ID) == "" || strings.TrimSpace(profile.Account.Username) == "" ||
		strings.TrimSpace(profile.Character.ID) == "" || strings.TrimSpace(profile.Character.Name) == "" {
		_ = server.recordAIAudit(request.Context(), data, "ai_initialization_recovery_failed", id, map[string]any{"result": "invalid_result"})
		playerJSONError(response, http.StatusInternalServerError, "AI 初始化恢复失败")
		return
	}
	_ = server.recordAIAudit(request.Context(), data, "ai_initialization_recovered", id, map[string]any{"result": "published", "version": profile.Version})
	playerJSON(response, http.StatusOK, map[string]any{"profile": server.aiProfileView(request.Context(), profile)})
}

func aiInitialRecoveryError(response http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	message := "AI 初始化恢复失败"
	switch {
	case errors.Is(err, airuntime.ErrNotFound), errors.Is(err, aiprovision.ErrBindingNotFound):
		status = http.StatusNotFound
		message = "初始化记录不存在"
	case errors.Is(err, airuntime.ErrConflict), errors.Is(err, aiprovision.ErrBindingConflict), errors.Is(err, aiprovision.ErrInitialUnconfirmed):
		status = http.StatusConflict
		message = "初始化状态未确认或已发生变化，请刷新后重试"
	case errors.Is(err, aiprovision.ErrInvalidConfig):
		status = http.StatusServiceUnavailable
		message = "AI 初始化恢复服务不可用"
	}
	playerJSONError(response, status, message)
}
