package admin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/k0ngk0ng/stoneage/internal/auth"
)

//go:embed templates/*.html static/*
var webFiles embed.FS

type Options struct {
	CookieSecure bool
	SetupToken   string
	Operator     Operator
	Config       ConfigManager
	SAACConfig   ConfigManager
	CSRFSecret   []byte
}

type Server struct {
	store        *auth.Store
	templates    map[string]*template.Template
	cookieSecure bool
	setupToken   string
	operator     Operator
	gmsvConfig   ConfigManager
	saacConfig   ConfigManager
	csrfSecret   []byte
}

type pageData struct {
	Title              string
	Session            *auth.Session
	CSRF               string
	Error              string
	Message            string
	Query              string
	FormUsername       string
	Accounts           []auth.Account
	Account            auth.Account
	Events             []auth.AuditEvent
	SetupTokenRequired bool
	Status             ServiceStatus
	Config             map[string]string
	ConfigGroups       []ConfigGroup
	ConfigError        string
	OperatorError      string
	Code               int
	ConfigService      string
	ConfigTitle        string
	ConfigDescription  string
	ConfigPath         string
	ConfigEditable     bool
	GatewayRunning     bool
	GatewayStopped     bool
	GameStatus         string
	GameStatusLabel    string
	GameRunning        bool
	GameStopped        bool
	GameAnyRunning     bool
	GameKnown          bool
	GMSVRunning        bool
	GMSVStopped        bool
	SAACRunning        bool
	SAACStopped        bool
	AnyServiceRunning  bool
	AllServicesKnown   bool
	AllServicesStopped bool
}

func NewServer(store *auth.Store, options Options) (*Server, error) {
	if store == nil {
		return nil, errors.New("auth store is required")
	}
	if len(options.CSRFSecret) == 0 {
		options.CSRFSecret = make([]byte, 32)
		if _, err := rand.Read(options.CSRFSecret); err != nil {
			return nil, fmt.Errorf("generate CSRF secret: %w", err)
		}
	}
	templates := make(map[string]*template.Template)
	for name, file := range map[string]string{
		"login": "login.html", "setup": "setup.html", "accounts": "accounts.html",
		"account_new": "account_new.html", "account_detail": "account_detail.html",
		"audit": "audit.html", "server": "server.html", "notification": "notification.html",
		"config": "config.html", "error": "error.html",
	} {
		parsed, parseErr := template.ParseFS(webFiles, "templates/layout.html", "templates/"+file)
		if parseErr != nil {
			return nil, fmt.Errorf("parse admin template %s: %w", name, parseErr)
		}
		templates[name] = parsed
	}
	return &Server{
		store:        store,
		templates:    templates,
		cookieSecure: options.CookieSecure,
		setupToken:   options.SetupToken,
		operator:     options.Operator,
		gmsvConfig:   options.Config,
		saacConfig:   options.SAACConfig,
		csrfSecret:   options.CSRFSecret,
	}, nil
}

func (server *Server) Handler() http.Handler {
	static, _ := fs.Sub(webFiles, "static")
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.FS(static)))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'")
		if strings.HasPrefix(request.URL.Path, "/static/") {
			staticHandler.ServeHTTP(response, request)
			return
		}
		if request.Body != nil {
			request.Body = http.MaxBytesReader(response, request.Body, 64<<10)
		}
		server.route(response, request)
	})
}

func (server *Server) route(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/healthz" {
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(response, "ok\n")
		return
	}
	if request.URL.Path == "/login" {
		server.login(response, request)
		return
	}
	if request.URL.Path == "/setup" {
		server.setup(response, request)
		return
	}
	session, token := server.session(request)
	if session == nil {
		if request.Method == http.MethodGet {
			http.Redirect(response, request, "/login", http.StatusSeeOther)
		} else {
			server.renderError(response, http.StatusUnauthorized, "登录已过期，请重新登录")
		}
		return
	}
	data := &pageData{Session: session, CSRF: server.csrfToken(token)}
	if request.URL.Path == "/logout" {
		server.logout(response, request, token, data)
		return
	}
	if request.URL.Path == "/" {
		http.Redirect(response, request, "/accounts", http.StatusSeeOther)
		return
	}
	if request.URL.Path == "/accounts" {
		server.accounts(response, request, data)
		return
	}
	if request.URL.Path == "/accounts/new" {
		if request.Method != http.MethodGet {
			server.renderError(response, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		data.Title = "新建账号"
		server.render(response, "account_new", data)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/accounts/") {
		server.account(response, request, data)
		return
	}
	if request.URL.Path == "/audit" {
		server.audit(response, request, data)
		return
	}
	if request.URL.Path == "/server" || request.URL.Path == "/server/restart" || request.URL.Path == "/server/restart-game" || request.URL.Path == "/server/restart-gateway" || request.URL.Path == "/server/restart-gmsv" || request.URL.Path == "/server/restart-saac" || request.URL.Path == "/server/stop" || request.URL.Path == "/server/stop-game" || request.URL.Path == "/server/stop-gateway" || request.URL.Path == "/server/stop-gmsv" || request.URL.Path == "/server/stop-saac" {
		server.server(response, request, data)
		return
	}
	if request.URL.Path == "/notifications" || request.URL.Path == "/server/notify" {
		server.notification(response, request, data)
		return
	}
	if request.URL.Path == "/server/config" || request.URL.Path == "/services/gateway/config" || request.URL.Path == "/services/gmsv/config" || request.URL.Path == "/services/saac/config" {
		server.configPage(response, request, data)
		return
	}
	server.renderError(response, http.StatusNotFound, "页面不存在")
}

func (server *Server) login(response http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		server.render(response, "login", &pageData{Title: "管理员登录"})
		return
	}
	if request.Method != http.MethodPost {
		server.renderError(response, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	_ = request.ParseForm()
	username := request.FormValue("username")
	password := []byte(request.FormValue("password"))
	admin, err := server.store.AuthenticateAdmin(request.Context(), username, password, requestSourceIP(request))
	if err != nil {
		server.render(response, "login", &pageData{Title: "管理员登录", Error: "账号或密码错误"})
		return
	}
	token, expires, err := server.store.CreateSession(request.Context(), admin.ID, 12*time.Hour)
	if err != nil {
		server.render(response, "login", &pageData{Title: "管理员登录", Error: "无法创建登录会话"})
		return
	}
	http.SetCookie(response, &http.Cookie{Name: "stoneage_admin_session", Value: token, Path: "/", Expires: expires, MaxAge: int(time.Until(expires).Seconds()), HttpOnly: true, Secure: server.cookieSecure, SameSite: http.SameSiteLaxMode})
	http.Redirect(response, request, "/accounts", http.StatusSeeOther)
}

func (server *Server) setup(response http.ResponseWriter, request *http.Request) {
	hasAdmins, err := server.store.HasAdmins(request.Context())
	if err != nil {
		server.renderError(response, http.StatusInternalServerError, err.Error())
		return
	}
	if hasAdmins {
		http.Redirect(response, request, "/login", http.StatusSeeOther)
		return
	}
	if server.setupToken == "" {
		server.renderError(response, http.StatusServiceUnavailable, "尚未初始化管理员，请在服务器上运行 create-admin 子命令")
		return
	}
	data := &pageData{Title: "初始化管理员", SetupTokenRequired: true}
	if request.Method == http.MethodGet {
		server.render(response, "setup", data)
		return
	}
	if request.Method != http.MethodPost {
		server.renderError(response, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	_ = request.ParseForm()
	if !hmac.Equal([]byte(request.FormValue("setup_token")), []byte(server.setupToken)) {
		data.Error = "初始化令牌错误"
		server.render(response, "setup", data)
		return
	}
	if _, err := server.store.CreateAdmin(request.Context(), request.FormValue("username"), []byte(request.FormValue("password"))); err != nil {
		data.Error = publicError(err)
		server.render(response, "setup", data)
		return
	}
	http.Redirect(response, request, "/login", http.StatusSeeOther)
}

func (server *Server) logout(response http.ResponseWriter, request *http.Request, token string, data *pageData) {
	if request.Method != http.MethodPost {
		server.renderError(response, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !server.verifyCSRF(request, token) {
		server.renderError(response, http.StatusForbidden, "CSRF 校验失败")
		return
	}
	_ = server.store.DeleteSession(request.Context(), token)
	http.SetCookie(response, &http.Cookie{Name: "stoneage_admin_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: server.cookieSecure, SameSite: http.SameSiteLaxMode})
	http.Redirect(response, request, "/login", http.StatusSeeOther)
}

func (server *Server) accounts(response http.ResponseWriter, request *http.Request, data *pageData) {
	if request.Method != http.MethodGet {
		if request.Method == http.MethodPost {
			server.createAccount(response, request, data)
			return
		}
		server.renderError(response, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	query := request.URL.Query().Get("q")
	accounts, err := server.store.ListAccounts(request.Context(), query)
	if err != nil {
		server.renderError(response, http.StatusInternalServerError, err.Error())
		return
	}
	data.Title, data.Query, data.Accounts = "账号管理", query, accounts
	data.Message = request.URL.Query().Get("message")
	server.render(response, "accounts", data)
}

func (server *Server) createAccount(response http.ResponseWriter, request *http.Request, data *pageData) {
	if !server.verifyCSRF(request, cookieValue(request, "stoneage_admin_session")) {
		server.renderError(response, http.StatusForbidden, "CSRF 校验失败")
		return
	}
	_ = request.ParseForm()
	username := request.FormValue("username")
	if _, err := server.store.CreateAccountAs(request.Context(), adminID(data), username, []byte(request.FormValue("password"))); err != nil {
		data.Title, data.FormUsername, data.Error = "新建账号", username, publicError(err)
		server.render(response, "account_new", data)
		return
	}
	http.Redirect(response, request, "/accounts?message="+urlEscape("账号已创建"), http.StatusSeeOther)
}

func (server *Server) account(response http.ResponseWriter, request *http.Request, data *pageData) {
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(parts) < 2 {
		server.renderError(response, http.StatusNotFound, "账号不存在")
		return
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		server.renderError(response, http.StatusNotFound, "账号不存在")
		return
	}
	if len(parts) == 2 && request.Method == http.MethodGet {
		account, err := server.store.GetAccount(request.Context(), id)
		if err != nil {
			server.renderError(response, http.StatusNotFound, "账号不存在")
			return
		}
		data.Title, data.Account = "账号详情", account
		server.render(response, "account_detail", data)
		return
	}
	if len(parts) != 3 || request.Method != http.MethodPost || !server.verifyCSRF(request, cookieValue(request, "stoneage_admin_session")) {
		server.renderError(response, http.StatusForbidden, "请求无效")
		return
	}
	switch parts[2] {
	case "password":
		_ = request.ParseForm()
		mustChange := request.FormValue("must_change") == "1"
		err = server.store.SetAccountPasswordAs(request.Context(), adminID(data), id, []byte(request.FormValue("password")), mustChange)
	case "status":
		_ = request.ParseForm()
		err = server.store.SetAccountStatusAs(request.Context(), adminID(data), id, request.FormValue("status"))
	default:
		server.renderError(response, http.StatusNotFound, "操作不存在")
		return
	}
	if err != nil {
		server.renderError(response, http.StatusBadRequest, publicError(err))
		return
	}
	http.Redirect(response, request, "/accounts/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (server *Server) audit(response http.ResponseWriter, request *http.Request, data *pageData) {
	if request.Method != http.MethodGet {
		server.renderError(response, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	events, err := server.store.RecentAudit(request.Context(), 100)
	if err != nil {
		server.renderError(response, http.StatusInternalServerError, err.Error())
		return
	}
	data.Title, data.Events = "审计日志", events
	server.render(response, "audit", data)
}

func (server *Server) server(response http.ResponseWriter, request *http.Request, data *pageData) {
	if request.Method == http.MethodGet {
		server.renderService(response, request, data)
		return
	}
	if request.Method != http.MethodPost || !server.verifyCSRF(request, cookieValue(request, "stoneage_admin_session")) {
		server.renderError(response, http.StatusForbidden, "请求无效")
		return
	}
	switch request.URL.Path {
	case "/server/restart", "/server/restart-game", "/server/restart-gateway", "/server/restart-gmsv", "/server/restart-saac":
		target := "all"
		if request.URL.Path == "/server/restart-game" {
			target = "game"
		} else if request.URL.Path == "/server/restart-gateway" {
			target = "gateway"
		} else if request.URL.Path == "/server/restart-gmsv" {
			target = "gmsv"
		} else if request.URL.Path == "/server/restart-saac" {
			target = "saac"
		}
		if err := server.restartTarget(request.Context(), target); err != nil {
			server.renderError(response, http.StatusBadGateway, err.Error())
			return
		}
		_ = server.store.RecordAudit(request.Context(), adminID(data), "server_restarted_"+target, "", requestSourceIP(request), "")
		http.Redirect(response, request, "/server?message="+urlEscape(restartMessage(target)), http.StatusSeeOther)
	case "/server/stop", "/server/stop-game", "/server/stop-gateway", "/server/stop-gmsv", "/server/stop-saac":
		target := "all"
		if request.URL.Path == "/server/stop-game" {
			target = "game"
		} else if request.URL.Path == "/server/stop-gateway" {
			target = "gateway"
		} else if request.URL.Path == "/server/stop-gmsv" {
			target = "gmsv"
		} else if request.URL.Path == "/server/stop-saac" {
			target = "saac"
		}
		if err := server.stopTarget(request.Context(), target); err != nil {
			server.renderError(response, http.StatusBadGateway, err.Error())
			return
		}
		_ = server.store.RecordAudit(request.Context(), adminID(data), "server_stopped_"+target, "", requestSourceIP(request), "")
		http.Redirect(response, request, "/server?message="+urlEscape(stopMessage(target)), http.StatusSeeOther)
	default:
		server.renderError(response, http.StatusNotFound, "操作不存在")
	}
}

func (server *Server) notification(response http.ResponseWriter, request *http.Request, data *pageData) {
	if request.Method == http.MethodGet {
		data.Title = "通知"
		data.Message = request.URL.Query().Get("message")
		server.render(response, "notification", data)
		return
	}
	if request.Method != http.MethodPost || !server.verifyCSRF(request, cookieValue(request, "stoneage_admin_session")) {
		server.renderError(response, http.StatusForbidden, "请求无效")
		return
	}
	if request.URL.Path != "/notifications" && request.URL.Path != "/server/notify" {
		server.renderError(response, http.StatusNotFound, "操作不存在")
		return
	}
	message, err := normalizeNotification(request.FormValue("message"))
	if err != nil {
		server.renderError(response, http.StatusBadRequest, err.Error())
		return
	}
	notifier, ok := server.operator.(NotifyingOperator)
	if !ok {
		server.renderError(response, http.StatusServiceUnavailable, "当前运维接口不支持在线通知")
		return
	}
	if err := notifier.Notify(request.Context(), message); err != nil {
		_ = server.store.RecordAudit(request.Context(), adminID(data), "server_notification_failed", "", requestSourceIP(request), err.Error())
		server.renderError(response, http.StatusBadGateway, err.Error())
		return
	}
	_ = server.store.RecordAudit(request.Context(), adminID(data), "server_notification_sent", "", requestSourceIP(request), message)
	http.Redirect(response, request, "/notifications?message="+urlEscape("通知已发送给在线玩家"), http.StatusSeeOther)
}

func (server *Server) configPage(response http.ResponseWriter, request *http.Request, data *pageData) {
	service, manager, title, description, editable, ok := server.configSpec(request.URL.Path)
	if !ok {
		server.renderError(response, http.StatusNotFound, "配置页面不存在")
		return
	}
	data.ConfigService = service
	data.ConfigTitle = title
	data.ConfigDescription = description
	data.ConfigPath = configPathLabel(service)
	data.ConfigEditable = editable
	data.Message = request.URL.Query().Get("message")
	if editable {
		values, err := manager.Load()
		if err != nil {
			data.ConfigError = err.Error()
		} else {
			data.ConfigGroups = manager.groups(values)
		}
	}
	if request.Method == http.MethodGet {
		data.Title = title
		server.render(response, "config", data)
		return
	}
	if request.Method != http.MethodPost || !editable || !server.verifyCSRF(request, cookieValue(request, "stoneage_admin_session")) {
		server.renderError(response, http.StatusForbidden, "请求无效")
		return
	}
	_ = request.ParseForm()
	values := make(map[string]string, len(manager.definitions()))
	for _, field := range manager.definitions() {
		if field.Kind == "bool" {
			values[field.Key] = "0"
			if request.FormValue(field.Key) == "1" {
				values[field.Key] = "1"
			}
			continue
		}
		if request.Form.Has(field.Key) {
			values[field.Key] = strings.TrimSpace(request.FormValue(field.Key))
		}
	}
	if err := manager.Update(values); err != nil {
		server.renderError(response, http.StatusBadRequest, publicError(err))
		return
	}
	_ = server.store.RecordAudit(request.Context(), adminID(data), service+"_config_changed", "", requestSourceIP(request), configAuditDetailsFor(values, manager.definitions()))
	http.Redirect(response, request, configPathForService(service)+"?message="+urlEscape("配置已保存；重启对应服务后生效"), http.StatusSeeOther)
}

func (server *Server) configSpec(path string) (string, ConfigManager, string, string, bool, bool) {
	switch path {
	case "/server/config", "/services/gmsv/config":
		return "gmsv", server.gmsvConfig, "GMSV 配置", "编辑 GMSV 的 setup.cf。保存不会自动重启 GMSV。", true, true
	case "/services/saac/config":
		return "saac", server.saacConfig, "SAAC 配置", "编辑 SAAC 的 acserv.cf。密码、目录和协议连接参数仍保留在服务器文件中。保存不会自动重启 SAAC。", true, true
	case "/services/gateway/config":
		return "gateway", ConfigManager{}, "游戏网关配置", "游戏网关由启动参数和部署环境管理，目前没有可安全在线修改的配置项。", false, true
	default:
		return "", ConfigManager{}, "", "", false, false
	}
}

func configPathForService(service string) string {
	switch service {
	case "gmsv":
		return "/services/gmsv/config"
	case "saac":
		return "/services/saac/config"
	default:
		return "/services/gateway/config"
	}
}

func configPathLabel(service string) string {
	switch service {
	case "gmsv":
		return "setup.cf"
	case "saac":
		return "acserv.cf"
	default:
		return "启动参数"
	}
}

func (server *Server) renderService(response http.ResponseWriter, request *http.Request, data *pageData) {
	data.Title = "服务"
	data.Message = request.URL.Query().Get("message")
	if server.operator != nil {
		status, err := server.operator.Status(request.Context())
		if err != nil {
			data.OperatorError = err.Error()
			data.Status = ServiceStatus{Gateway: "未知", GMSV: "未知", SAAC: "未知", Database: "未知"}
		} else {
			data.Status = status
		}
	} else {
		data.Status = ServiceStatus{Gateway: "未配置运维接口", GMSV: "未配置运维接口", SAAC: "未配置运维接口", Database: "正常"}
	}
	data.GatewayRunning = data.Status.Gateway == "running"
	data.GatewayStopped = data.Status.Gateway == "stopped"
	data.GMSVRunning = data.Status.GMSV == "running"
	data.GMSVStopped = data.Status.GMSV == "stopped"
	data.SAACRunning = data.Status.SAAC == "running"
	data.SAACStopped = data.Status.SAAC == "stopped"
	data.GameStatus, data.GameStatusLabel = gameServiceStatus(data.Status.GMSV, data.Status.SAAC)
	data.GameRunning = data.GameStatus == "running"
	data.GameStopped = data.GameStatus == "stopped"
	data.GameAnyRunning = data.GMSVRunning || data.SAACRunning
	data.GameKnown = isServiceStatusKnown(data.Status.GMSV) && isServiceStatusKnown(data.Status.SAAC)
	data.AnyServiceRunning = data.GatewayRunning || data.GameAnyRunning
	data.AllServicesKnown = isServiceStatusKnown(data.Status.Gateway) && data.GameKnown
	data.AllServicesStopped = data.AllServicesKnown && !data.AnyServiceRunning
	server.render(response, "server", data)
}

// gameServiceStatus is the user-facing status of the legacy game service. The
// implementation still probes GMSV and SAAC independently so their config and
// emergency restart actions remain available, but they are one deployable game
// service from an operator's point of view.
func gameServiceStatus(gmsv, saac string) (string, string) {
	switch {
	case gmsv == "running" && saac == "running":
		return "running", "运行中"
	case gmsv == "stopped" && saac == "stopped":
		return "stopped", "已停止"
	case isServiceStatusKnown(gmsv) && isServiceStatusKnown(saac):
		return "degraded", "部分运行"
	default:
		return "unknown", "未知"
	}
}

func isServiceStatusKnown(value string) bool {
	return value == "running" || value == "stopped"
}

func normalizeNotification(value string) (string, error) {
	message := strings.TrimSpace(value)
	if message == "" {
		return "", errors.New("通知内容不能为空")
	}
	if strings.ContainsAny(message, "\r\n\x00") {
		return "", errors.New("通知请填写为单行文字")
	}
	if utf8.RuneCountInString(message) > 240 {
		return "", errors.New("通知最多 240 个字符")
	}
	return message, nil
}

func (server *Server) restartTarget(ctx context.Context, target string) error {
	if server.operator == nil {
		return errors.New("未配置受限运维接口")
	}
	if targeted, ok := server.operator.(TargetedOperator); ok {
		switch target {
		case "gateway":
			return targeted.RestartGateway(ctx)
		case "gmsv":
			return targeted.RestartGMSV(ctx)
		case "saac":
			return targeted.RestartSAAC(ctx)
		case "game":
			return targeted.RestartGame(ctx)
		}
	}
	if target == "gmsv" || target == "saac" {
		return fmt.Errorf("当前运维接口不支持独立重启 %s", strings.ToUpper(target))
	}
	return server.operator.Restart(ctx)
}

func (server *Server) stopTarget(ctx context.Context, target string) error {
	if server.operator == nil {
		return errors.New("未配置受限运维接口")
	}
	stopping, ok := server.operator.(StoppingOperator)
	if !ok {
		return errors.New("当前运维接口不支持停止服务")
	}
	switch target {
	case "gateway":
		return stopping.StopGateway(ctx)
	case "gmsv":
		return stopping.StopGMSV(ctx)
	case "saac":
		return stopping.StopSAAC(ctx)
	case "game":
		return stopping.StopGame(ctx)
	default:
		return stopping.Stop(ctx)
	}
}

func restartMessage(target string) string {
	switch target {
	case "gateway":
		return "游戏网关已重启"
	case "gmsv":
		return "GMSV 已重启"
	case "saac":
		return "SAAC 已重启"
	case "game":
		return "游戏服务（GMSV + SAAC）已重启"
	default:
		return "全部服务已重启"
	}
}

func stopMessage(target string) string {
	switch target {
	case "gateway":
		return "游戏网关已停止"
	case "gmsv":
		return "GMSV 已停止"
	case "saac":
		return "SAAC 已停止"
	case "game":
		return "游戏服务（GMSV + SAAC）已停止"
	default:
		return "全部服务已停止"
	}
}

func configAuditDetailsFor(values map[string]string, definitions []configFieldDefinition) string {
	parts := make([]string, 0, len(values))
	for _, definition := range definitions {
		key := definition.Key
		if value, ok := values[key]; ok {
			parts = append(parts, key+"="+value)
		}
	}
	return strings.Join(parts, " ")
}

func adminID(data *pageData) *int64 {
	if data == nil || data.Session == nil || data.Session.AdminUserID <= 0 {
		return nil
	}
	id := data.Session.AdminUserID
	return &id
}

func (server *Server) session(request *http.Request) (*auth.Session, string) {
	token := cookieValue(request, "stoneage_admin_session")
	if token == "" {
		return nil, ""
	}
	session, err := server.store.ValidateSession(request.Context(), token)
	if err != nil {
		return nil, ""
	}
	return &session, token
}

func (server *Server) csrfToken(sessionToken string) string {
	mac := hmac.New(sha256.New, server.csrfSecret)
	_, _ = mac.Write([]byte(sessionToken))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (server *Server) verifyCSRF(request *http.Request, sessionToken string) bool {
	if sessionToken == "" {
		return false
	}
	provided := request.FormValue("csrf")
	want := server.csrfToken(sessionToken)
	return hmac.Equal([]byte(provided), []byte(want))
}

func (server *Server) render(response http.ResponseWriter, page string, data *pageData) {
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	templates, ok := server.templates[page]
	if !ok {
		http.Error(response, "template not found", http.StatusInternalServerError)
		return
	}
	name := page
	if page != "login" && page != "setup" {
		name = "layout"
	}
	if err := templates.ExecuteTemplate(response, name, data); err != nil {
		http.Error(response, "template error", http.StatusInternalServerError)
	}
}

func (server *Server) renderError(response http.ResponseWriter, code int, message string) {
	response.WriteHeader(code)
	data := &pageData{Title: "错误", Error: message, Code: code}
	if err := server.templates["error"].ExecuteTemplate(response, "layout", data); err != nil {
		http.Error(response, message, code)
	}
}

func cookieValue(request *http.Request, name string) string {
	cookie, err := request.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func requestSourceIP(request *http.Request) string {
	if host, _, err := net.SplitHostPort(request.RemoteAddr); err == nil {
		return host
	}
	return request.RemoteAddr
}

func publicError(err error) string {
	if errors.Is(err, auth.ErrInvalidUsername) {
		return "账号格式不正确（仅支持 1–15 位 ASCII 字符）"
	}
	if errors.Is(err, auth.ErrInvalidPassword) {
		return "密码长度必须为 1–15 位可打印字符"
	}
	return err.Error()
}

func urlEscape(value string) string { return url.QueryEscape(value) }
