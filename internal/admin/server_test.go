package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/auth"
)

type fakeOperator struct {
	gameServers     []GameServerStatus
	restarts        int
	gatewayRestarts int
	gameRestarts    int
	gmsvRestarts    int
	saacRestarts    int
	stops           int
	gatewayStops    int
	gameStops       int
	gmsvStops       int
	saacStops       int
	serviceStatus   ServiceStatus
	notifications   []string
	deployments     []string
	deployment      DeploymentStatus
	assetSyncStarts int
	assetSync       AssetSyncStatus
}

func (operator *fakeOperator) Status(context.Context) (ServiceStatus, error) {
	if operator.serviceStatus == (ServiceStatus{}) {
		return ServiceStatus{Gateway: "running", GMSV: "running", SAAC: "running", Database: "ready"}, nil
	}
	return operator.serviceStatus, nil
}

func (operator *fakeOperator) Restart(context.Context) error {
	operator.restarts++
	return nil
}

func (operator *fakeOperator) RestartGateway(context.Context) error {
	operator.gatewayRestarts++
	return nil
}

func (operator *fakeOperator) RestartGame(context.Context) error {
	operator.gameRestarts++
	return nil
}

func (operator *fakeOperator) RestartGMSV(context.Context) error {
	operator.gmsvRestarts++
	return nil
}

func (operator *fakeOperator) RestartSAAC(context.Context) error {
	operator.saacRestarts++
	return nil
}

func (operator *fakeOperator) Stop(context.Context) error {
	operator.stops++
	return nil
}

func (operator *fakeOperator) StopGateway(context.Context) error {
	operator.gatewayStops++
	return nil
}

func (operator *fakeOperator) StopGame(context.Context) error {
	operator.gameStops++
	return nil
}

func (operator *fakeOperator) StopGMSV(context.Context) error {
	operator.gmsvStops++
	return nil
}

func (operator *fakeOperator) StopSAAC(context.Context) error {
	operator.saacStops++
	return nil
}

func (operator *fakeOperator) Notify(_ context.Context, message string) error {
	operator.notifications = append(operator.notifications, message)
	return nil
}

func (operator *fakeOperator) DeployVersion(_ context.Context, version string) error {
	operator.deployments = append(operator.deployments, version)
	return nil
}

func (operator *fakeOperator) DeploymentStatus(context.Context) (DeploymentStatus, error) {
	if operator.deployment.Phase == "" {
		return DeploymentStatus{Phase: "idle"}, nil
	}
	return operator.deployment, nil
}

func (operator *fakeOperator) SyncAssets(context.Context) error {
	operator.assetSyncStarts++
	operator.assetSync = AssetSyncStatus{Phase: "running", Message: "syncing"}
	return nil
}

func (operator *fakeOperator) AssetSyncStatus(context.Context) (AssetSyncStatus, error) {
	if operator.assetSync.Phase == "" {
		return AssetSyncStatus{Phase: "idle", Message: "idle"}, nil
	}
	return operator.assetSync, nil
}

func newAdminTestServer(t *testing.T) (*auth.Store, *fakeOperator, *httptest.Server) {
	t.Helper()
	store, err := auth.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := store.CreateAdmin(context.Background(), "admin", []byte("secret123")); err != nil {
		store.Close()
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "setup.cf")
	if err := os.WriteFile(configPath, []byte("debuglevel=1\nenable_nu_flow_control=0\n"), 0o640); err != nil {
		store.Close()
		t.Fatal(err)
	}
	saacConfigPath := filepath.Join(filepath.Dir(configPath), "acserv.cf")
	if err := os.WriteFile(saacConfigPath, []byte("# comment\nport 9300\nrotate_interval 604800\nSameIpMun 10\n"), 0o640); err != nil {
		store.Close()
		t.Fatal(err)
	}
	operator := &fakeOperator{}
	control, err := NewServer(store, Options{Operator: operator, Config: ConfigManager{Path: configPath}, SAACConfig: ConfigManager{Path: saacConfigPath, Service: "saac"}, CSRFSecret: []byte("test-secret")})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	server := httptest.NewServer(control.Handler())
	t.Cleanup(func() {
		server.Close()
		store.Close()
	})
	return store, operator, server
}

func TestAdminLoginAccountAndCSRF(t *testing.T) {
	store, _, server := newAdminTestServer(t)
	staticResponse, err := http.Get(server.URL + "/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	staticBody, _ := io.ReadAll(staticResponse.Body)
	staticResponse.Body.Close()
	staticText := string(staticBody)
	if staticResponse.StatusCode != http.StatusOK || !strings.Contains(staticText, "data-confirm") || !strings.Contains(staticText, "confirm-modal") || strings.Contains(staticText, "window.confirm") {
		t.Fatalf("static app response = %d %s", staticResponse.StatusCode, staticBody)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	response, err := client.PostForm(server.URL+"/login", url.Values{"username": {"admin"}, "password": {"secret123"}})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/accounts" {
		t.Fatalf("login response = %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	response.Body.Close()

	response, err = client.Get(server.URL + "/accounts")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "没有账号") || !strings.Contains(string(body), "id=\"confirm-modal\"") {
		t.Fatalf("accounts page = %d %s", response.StatusCode, body)
	}
	csrf := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(string(body))
	if len(csrf) != 2 {
		t.Fatal("accounts page did not contain CSRF token")
	}

	response, err = client.PostForm(server.URL+"/accounts", url.Values{"csrf": {csrf[1]}, "username": {"probe"}, "password": {"local"}})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/accounts" {
		t.Fatalf("create account response = %d location=%q", response.StatusCode, response.Header.Get("Location"))
	}
	response.Body.Close()
	if _, err := store.GetAccountByUsername(context.Background(), "probe"); err != nil {
		t.Fatalf("created account missing: %v", err)
	}
	response, err = client.Get(server.URL + "/accounts")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "账号已创建") || strings.Contains(response.Request.URL.RawQuery, "message") {
		t.Fatalf("account flash response = %d url=%s body=%s", response.StatusCode, response.Request.URL, body)
	}
	response, err = client.Get(server.URL + "/accounts")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if strings.Contains(string(body), "账号已创建") {
		t.Fatal("account flash message was shown more than once")
	}

	response, err = client.PostForm(server.URL+"/accounts", url.Values{"username": {"forged"}, "password": {"local"}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("CSRF response = %d, want 403", response.StatusCode)
	}
}

func TestAdminConfigAndRestart(t *testing.T) {
	_, operator, server := newAdminTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.PostForm(server.URL+"/login", url.Values{"username": {"admin"}, "password": {"secret123"}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response, err = client.Get(server.URL + "/services/gmsv/config")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	csrf := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(string(body))
	if len(csrf) != 2 {
		t.Fatal("GMSV config page did not contain CSRF token")
	}
	response, err = client.PostForm(server.URL+"/services/gmsv/config", url.Values{
		"csrf":                   {csrf[1]},
		"enable_nu_flow_control": {"1"},
		"debuglevel":             {"3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/services/gmsv/config" || operator.gameRestarts != 0 {
		t.Fatalf("config response=%d location=%q game restarts=%d", response.StatusCode, response.Header.Get("Location"), operator.gameRestarts)
	}
	response, err = client.Get(server.URL + "/services/saac/config")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	csrf = regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(string(body))
	if len(csrf) != 2 {
		t.Fatal("SAAC config page did not contain CSRF token")
	}
	response, err = client.PostForm(server.URL+"/services/saac/config", url.Values{
		"csrf":      {csrf[1]},
		"SameIpMun": {"12"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/services/saac/config" || operator.saacRestarts != 0 {
		t.Fatalf("SAAC config response=%d location=%q SAAC restarts=%d", response.StatusCode, response.Header.Get("Location"), operator.saacRestarts)
	}
	response, err = client.PostForm(server.URL+"/notifications", url.Values{
		"csrf":    {csrf[1]},
		"message": {"今晚 23:00 维护提醒 $(not-a-command)"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/notifications" || len(operator.notifications) != 1 || operator.notifications[0] != "今晚 23:00 维护提醒 $(not-a-command)" {
		t.Fatalf("notification response=%d location=%q notifications=%#v", response.StatusCode, response.Header.Get("Location"), operator.notifications)
	}
	response, err = client.Get(server.URL + "/notifications")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "通知已发送给在线玩家") || len(operator.notifications) != 1 {
		t.Fatalf("notification flash response=%d notifications=%d body=%s", response.StatusCode, len(operator.notifications), body)
	}
	response, err = client.Get(server.URL + "/notifications?message=伪造消息")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if strings.Contains(string(body), "伪造消息") || strings.Contains(string(body), "通知已发送给在线玩家") || len(operator.notifications) != 1 {
		t.Fatalf("notification GET unexpectedly used query/message or sent a notification: %s", body)
	}
	response, err = client.PostForm(server.URL+"/server/restart", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/server" || operator.restarts != 1 {
		t.Fatalf("restart response=%d location=%q restarts=%d", response.StatusCode, response.Header.Get("Location"), operator.restarts)
	}
	response, err = client.PostForm(server.URL+"/server/restart-gateway", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/server" || operator.gatewayRestarts != 1 {
		t.Fatalf("gateway restart response=%d location=%q restarts=%d", response.StatusCode, response.Header.Get("Location"), operator.gatewayRestarts)
	}
	response, err = client.PostForm(server.URL+"/server/restart-game", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/server" || operator.gameRestarts != 1 {
		t.Fatalf("game restart response=%d location=%q restarts=%d", response.StatusCode, response.Header.Get("Location"), operator.gameRestarts)
	}
	response, err = client.PostForm(server.URL+"/server/restart-gmsv", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/server" || operator.gmsvRestarts != 1 {
		t.Fatalf("GMSV restart response=%d location=%q restarts=%d", response.StatusCode, response.Header.Get("Location"), operator.gmsvRestarts)
	}
	response, err = client.PostForm(server.URL+"/server/restart-saac", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/server" || operator.saacRestarts != 1 {
		t.Fatalf("SAAC restart response=%d location=%q restarts=%d", response.StatusCode, response.Header.Get("Location"), operator.saacRestarts)
	}
	response, err = client.PostForm(server.URL+"/server/stop", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/server" || operator.stops != 1 {
		t.Fatalf("stop response=%d location=%q stops=%d", response.StatusCode, response.Header.Get("Location"), operator.stops)
	}
	response, err = client.PostForm(server.URL+"/server/stop-gateway", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/server" || operator.gatewayStops != 1 {
		t.Fatalf("gateway stop response=%d location=%q stops=%d", response.StatusCode, response.Header.Get("Location"), operator.gatewayStops)
	}
	response, err = client.PostForm(server.URL+"/server/stop-game", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/server" || operator.gameStops != 1 {
		t.Fatalf("game stop response=%d location=%q stops=%d", response.StatusCode, response.Header.Get("Location"), operator.gameStops)
	}
	response, err = client.PostForm(server.URL+"/server/stop-gmsv", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/server" || operator.gmsvStops != 1 {
		t.Fatalf("GMSV stop response=%d location=%q stops=%d", response.StatusCode, response.Header.Get("Location"), operator.gmsvStops)
	}
	response, err = client.PostForm(server.URL+"/server/stop-saac", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/server" || operator.saacStops != 1 {
		t.Fatalf("SAAC stop response=%d location=%q stops=%d", response.StatusCode, response.Header.Get("Location"), operator.saacStops)
	}
}

func TestAdminReleaseVersionValidationAndCSRF(t *testing.T) {
	_, operator, server := newAdminTestServer(t)
	operator.deployment = DeploymentStatus{Version: "v1.2.3", Phase: "succeeded", Message: "已发布"}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.PostForm(server.URL+"/login", url.Values{"username": {"admin"}, "password": {"secret123"}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	response, err = client.Get(server.URL + "/releases")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "v1.2.3") || !strings.Contains(string(body), "已发布") {
		t.Fatalf("release page = %d %s", response.StatusCode, body)
	}
	csrf := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(string(body))
	if len(csrf) != 2 {
		t.Fatal("release page did not contain CSRF token")
	}

	response, err = client.PostForm(server.URL+"/releases/deploy", url.Values{"csrf": {csrf[1]}, "version": {"latest"}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "版本号必须是 v1.2.3 格式") || len(operator.deployments) != 0 {
		t.Fatalf("invalid release response = %d deployments=%#v body=%s", response.StatusCode, operator.deployments, body)
	}

	response, err = client.PostForm(server.URL+"/releases/deploy", url.Values{"csrf": {csrf[1]}, "version": {"v1.3.0"}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/releases" || len(operator.deployments) != 1 || operator.deployments[0] != "v1.3.0" {
		t.Fatalf("valid release response = %d location=%q deployments=%#v", response.StatusCode, response.Header.Get("Location"), operator.deployments)
	}
	response, err = client.Get(server.URL + "/releases")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "已开始下发 v1.3.0") || strings.Contains(response.Request.URL.RawQuery, "message") {
		t.Fatalf("release flash response = %d url=%s body=%s", response.StatusCode, response.Request.URL, body)
	}

	response, err = client.PostForm(server.URL+"/releases/deploy", url.Values{"version": {"v1.3.1"}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("release CSRF response = %d", response.StatusCode)
	}
}

func TestAdminAssetSyncIsBulkAndCSRFProtected(t *testing.T) {
	_, operator, server := newAdminTestServer(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.PostForm(server.URL+"/login", url.Values{"username": {"admin"}, "password": {"secret123"}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response, err = client.Get(server.URL + "/assets")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "批量同步") || !strings.Contains(string(body), "assets/") || !strings.Contains(string(body), "maps/") || !strings.Contains(string(body), "audio/") {
		t.Fatalf("asset page = %d %s", response.StatusCode, body)
	}
	csrf := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(string(body))
	if len(csrf) != 2 {
		t.Fatal("asset page did not contain CSRF token")
	}
	response, err = client.PostForm(server.URL+"/assets/sync", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/assets" || operator.assetSyncStarts != 1 {
		t.Fatalf("asset sync response = %d location=%q starts=%d", response.StatusCode, response.Header.Get("Location"), operator.assetSyncStarts)
	}
	response, err = client.PostForm(server.URL+"/assets/sync", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("asset sync without CSRF status=%d", response.StatusCode)
	}
}

func TestAdminServiceButtonsFollowStatus(t *testing.T) {
	_, operator, server := newAdminTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.PostForm(server.URL+"/login", url.Values{"username": {"admin"}, "password": {"secret123"}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()

	operator.serviceStatus = ServiceStatus{Gateway: "stopped", GMSV: "running", SAAC: "stopped", Database: "ready"}
	response, err = client.Get(server.URL + "/server")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	page := string(body)
	if response.StatusCode != http.StatusOK || !strings.Contains(page, `action="/server/restart-gateway"`) || !strings.Contains(page, `action="/server/restart-game"`) || !strings.Contains(page, `>启动</button>`) {
		t.Fatalf("stopped service page = %d %s", response.StatusCode, body)
	}
	if strings.Count(page, `<article class="service-row">`) != 2 || !strings.Contains(page, "游戏服务") || !strings.Contains(page, "部分运行") {
		t.Fatalf("service page should expose gateway and one composite game service: %s", body)
	}
	if !strings.Contains(page, `<details class="service-components" open>`) || !strings.Contains(page, "分别管理 GMSV 和 SAAC") {
		t.Fatalf("service page should expose independent GMSV/SAAC controls by default: %s", body)
	}
	if strings.Contains(page, `action="/server/stop-gateway"`) {
		t.Fatalf("stopped gateway should not have an enabled stop form: %s", body)
	}
	if !strings.Contains(page, `action="/server/stop-gmsv"`) || strings.Contains(page, `action="/server/stop-saac"`) {
		t.Fatalf("service stop forms did not follow running/stopped state: %s", body)
	}

	operator.serviceStatus = ServiceStatus{Gateway: "unknown", GMSV: "unknown", SAAC: "unknown", Database: "ready"}
	response, err = client.Get(server.URL + "/server")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	page = string(body)
	if strings.Count(page, `<article class="service-row">`) != 2 || strings.Contains(page, `action="/server/stop-gateway"`) || strings.Contains(page, `action="/server/stop-game"`) || strings.Contains(page, `action="/server/stop-gmsv"`) || strings.Contains(page, `action="/server/stop-saac"`) {
		t.Fatalf("unknown services should not have enabled stop forms: %s", body)
	}
	if !strings.Contains(page, `暂时无法确认全部服务状态`) {
		t.Fatalf("unknown services should disable all-service controls: %s", body)
	}
}

func (operator *fakeOperator) GameServers(context.Context) ([]GameServerStatus, error) {
	return operator.gameServers, nil
}

func TestAdminGameServerList(t *testing.T) {
	_, operator, server := newAdminTestServer(t)
	count := int32(7)
	zero := int32(0)
	operator.gameServers = []GameServerStatus{
		{Name: "一线", Address: "gmsv:9065", Online: &count, CheckedAt: "2026-09-06T08:00:00Z"},
		{Name: "二线", Address: "gmsv2:9065", Online: &zero, CheckedAt: "2026-09-06T08:00:00Z"},
		{Name: "三线", Address: "gmsv3:9065", Error: "无法获取在线人数", CheckedAt: "2026-09-06T08:00:00Z"},
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	response, err := client.PostForm(server.URL+"/login", url.Values{"username": {"admin"}, "password": {"secret123"}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response, err = client.Get(server.URL + "/server")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	for _, want := range []string{"服务器：<strong>3</strong>", "已知在线人数：<strong>7</strong>", "1 个服务器人数未知", "<td>0</td>", "<td>未知</td>", `data-local-time="2026-09-06T08:00:00Z"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
	operator.gameServers = operator.gameServers[2:]
	response, err = client.Get(server.URL + "/server")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(body), "在线人数：<strong>未知</strong>") {
		t.Fatalf("all unknown servers must not show a zero total: %s", body)
	}

}
