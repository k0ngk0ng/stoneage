package admin

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
)

type AILocalCommand struct {
	Command   string    `json:"command"`
	ExpiresAt time.Time `json:"expires_at"`
}

type AIExecutorStatus struct {
	Location  string     `json:"location"`
	Connected bool       `json:"connected"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	Message   string     `json:"message,omitempty"`
}

// AILocalExecutor issues profile-scoped invitations, never model credentials.
type AILocalExecutor interface {
	CreateCommand(context.Context, string, string) (AILocalCommand, error)
	ExecutorStatus(context.Context, string) (AIExecutorStatus, error)
}

func (server *Server) aiLocalExecutorAPI(w http.ResponseWriter, r *http.Request, data *pageData, id, action string) {
	if !aiIDPattern.MatchString(id) {
		playerJSONError(w, http.StatusNotFound, "AI 玩家不存在")
		return
	}
	profile, err := server.aiStore.GetProfile(r.Context(), id)
	if err != nil {
		aiStoreError(w, err)
		return
	}
	if action == "executor" {
		if r.Method != http.MethodGet {
			playerJSONError(w, http.StatusMethodNotAllowed, "请求方式不支持")
			return
		}
		if server.aiLocalExecutor == nil {
			playerJSON(w, http.StatusOK, AIExecutorStatus{Location: "server", Connected: server.aiRuntime != nil})
			return
		}
		status, err := server.aiLocalExecutor.ExecutorStatus(r.Context(), id)
		if err != nil {
			playerJSONError(w, http.StatusServiceUnavailable, "暂时无法读取执行器状态")
			return
		}
		playerJSON(w, http.StatusOK, status)
		return
	}
	if r.Method != http.MethodPost {
		playerJSONError(w, http.StatusMethodNotAllowed, "请求方式不支持")
		return
	}
	if !server.aiWriteRequest(w, r, data) {
		return
	}
	if server.aiLocalExecutor == nil {
		playerJSONError(w, http.StatusServiceUnavailable, "本地执行器接入尚未配置")
		return
	}
	if profile.Status != airuntime.ProfileStatusStopped && profile.Status != airuntime.ProfileStatusPaused {
		playerJSONError(w, http.StatusConflict, "请先暂停或停止玩家，再切换到本地运行")
		return
	}
	// Browser Origin preserves the external scheme through a reverse proxy.
	// It must match the current host; arbitrary callback destinations are not
	// accepted. The URL is only returned in the command, never fetched here.
	origin := r.Header.Get("Origin")
	if origin == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		origin = scheme + "://" + r.Host
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Host != r.Host || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(origin, "\r\n") {
		playerJSONError(w, http.StatusBadRequest, "管理端访问地址无效")
		return
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || (!ip.IsLoopback() && !ip.IsPrivate())) {
			playerJSONError(w, http.StatusBadRequest, "请通过 HTTPS 或局域网 IP 访问管理端后生成命令")
			return
		}
	}
	command, err := server.aiLocalExecutor.CreateCommand(r.Context(), id, strings.TrimSuffix(u.String(), "/"))
	if err != nil {
		playerJSONError(w, http.StatusConflict, "暂时无法生成本地运行命令，请确认旧执行已结束后重试")
		return
	}
	_ = server.recordAIAudit(r.Context(), data, "ai_local_invitation_created", id, map[string]any{"expires_at": command.ExpiresAt})
	result := map[string]any{"command": command.Command}
	if !command.ExpiresAt.IsZero() {
		result["expires_at"] = command.ExpiresAt
	}
	playerJSON(w, http.StatusOK, result)
}
