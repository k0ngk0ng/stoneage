package admin

import (
	"context"
	"errors"
	"net/http"
)

// Only reviewed categories cross the HTTP boundary. Runtime/provider error
// strings can contain request material and must never be shown to the browser.
func aiModelConnectionFailure(err error) (int, string, string) {
	code := "failed"
	var classified interface{ ConnectionTestCode() string }
	if errors.As(err, &classified) {
		code = classified.ConnectionTestCode()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = "timeout"
	}
	switch code {
	case "missing_key":
		return http.StatusBadRequest, code, "尚未配置 API Key，请编辑模型并保存密钥后再测试。"
	case "timeout":
		return http.StatusGatewayTimeout, code, "模型测试超时，请检查 API 地址、网络连接或模型服务响应速度。"
	case "authentication":
		return http.StatusBadGateway, code, "模型服务拒绝认证，请检查 API Key 及其访问权限。"
	case "rate_limit":
		return http.StatusBadGateway, code, "模型服务返回限流或额度不足，请检查服务商额度后重试。"
	case "model_unavailable":
		return http.StatusBadGateway, code, "模型或 Responses API 不可用，请检查模型名称、API 地址及访问权限。"
	case "network":
		return http.StatusBadGateway, code, "无法连接模型服务，请检查 API 地址和服务器网络。"
	case "invalid_response":
		return http.StatusBadGateway, code, "模型未返回有效的 Responses API 响应，请检查接口兼容性或更换模型。"
	case "runtime_unavailable":
		return http.StatusServiceUnavailable, code, "模型测试服务不可用，请检查管理端模型存储和服务配置。"
	case "busy":
		return http.StatusConflict, code, "已有模型测试正在进行，请等待完成后再试。"
	default:
		return http.StatusBadGateway, "failed", "AI 模型连接失败，请检查 API 地址、模型名称和密钥后重试。"
	}
}
