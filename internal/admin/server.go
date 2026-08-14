package admin

import (
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

	"github.com/k0ngk0ng/stoneage/internal/auth"
)

//go:embed templates/*.html static/*
var webFiles embed.FS

type Options struct {
	CookieSecure bool
	SetupToken   string
	Operator     Operator
	Config       ConfigManager
	CSRFSecret   []byte
}

type Server struct {
	store        *auth.Store
	templates    map[string]*template.Template
	cookieSecure bool
	setupToken   string
	operator     Operator
	config       ConfigManager
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
	ConfigError        string
	OperatorError      string
	Code               int
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
		"audit": "audit.html", "server": "server.html", "error": "error.html",
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
		config:       options.Config,
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
	if request.URL.Path == "/server" || request.URL.Path == "/server/restart" || request.URL.Path == "/server/config" {
		server.server(response, request, data)
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
		server.renderServer(response, request, data)
		return
	}
	if request.Method != http.MethodPost || !server.verifyCSRF(request, cookieValue(request, "stoneage_admin_session")) {
		server.renderError(response, http.StatusForbidden, "请求无效")
		return
	}
	switch request.URL.Path {
	case "/server/restart":
		if server.operator == nil {
			server.renderError(response, http.StatusServiceUnavailable, "未配置受限运维接口")
			return
		}
		if err := server.operator.Restart(request.Context()); err != nil {
			server.renderError(response, http.StatusBadGateway, err.Error())
			return
		}
		_ = server.store.RecordAudit(request.Context(), adminID(data), "server_restarted", "", requestSourceIP(request), "")
		http.Redirect(response, request, "/server", http.StatusSeeOther)
	case "/server/config":
		_ = request.ParseForm()
		values := map[string]string{"enable_nu_flow_control": "0", "debuglevel": request.FormValue("debuglevel")}
		if request.FormValue("enable_nu_flow_control") == "1" {
			values["enable_nu_flow_control"] = "1"
		}
		if err := server.config.Update(values); err != nil {
			server.renderError(response, http.StatusBadRequest, publicError(err))
			return
		}
		_ = server.store.RecordAudit(request.Context(), adminID(data), "server_config_changed", "", requestSourceIP(request), fmt.Sprintf("enable_nu_flow_control=%s debuglevel=%s", values["enable_nu_flow_control"], values["debuglevel"]))
		if server.operator != nil {
			if err := server.operator.Restart(request.Context()); err != nil {
				_ = server.store.RecordAudit(request.Context(), adminID(data), "server_restart_failed", "", requestSourceIP(request), err.Error())
				server.renderError(response, http.StatusBadGateway, err.Error())
				return
			}
		}
		http.Redirect(response, request, "/server", http.StatusSeeOther)
	default:
		server.renderError(response, http.StatusNotFound, "操作不存在")
	}
}

func (server *Server) renderServer(response http.ResponseWriter, request *http.Request, data *pageData) {
	data.Title = "服务端"
	data.Config = map[string]string{"enable_nu_flow_control": "0", "debuglevel": "0"}
	if values, err := server.config.Load(); err != nil {
		data.ConfigError = err.Error()
	} else {
		for key, value := range values {
			data.Config[key] = value
		}
	}
	if server.operator != nil {
		status, err := server.operator.Status(request.Context())
		if err != nil {
			data.OperatorError = err.Error()
			data.Status = ServiceStatus{Gateway: "未知", GMSV: "未知", Database: "未知"}
		} else {
			data.Status = status
		}
	} else {
		data.Status = ServiceStatus{Gateway: "未配置运维接口", GMSV: "未配置运维接口", Database: "正常"}
	}
	server.render(response, "server", data)
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
