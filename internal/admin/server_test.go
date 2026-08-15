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
	if staticResponse.StatusCode != http.StatusOK || !strings.Contains(string(staticBody), "data-confirm") {
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
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "没有账号") {
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
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("create account response = %d", response.StatusCode)
	}
	response.Body.Close()
	if _, err := store.GetAccountByUsername(context.Background(), "probe"); err != nil {
		t.Fatalf("created account missing: %v", err)
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
	if response.StatusCode != http.StatusSeeOther || operator.gameRestarts != 0 {
		t.Fatalf("config response=%d game restarts=%d", response.StatusCode, operator.gameRestarts)
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
	if response.StatusCode != http.StatusSeeOther || operator.saacRestarts != 0 {
		t.Fatalf("SAAC config response=%d SAAC restarts=%d", response.StatusCode, operator.saacRestarts)
	}
	response, err = client.PostForm(server.URL+"/notifications", url.Values{
		"csrf":    {csrf[1]},
		"message": {"今晚 23:00 维护提醒 $(not-a-command)"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || len(operator.notifications) != 1 || operator.notifications[0] != "今晚 23:00 维护提醒 $(not-a-command)" {
		t.Fatalf("notification response=%d notifications=%#v", response.StatusCode, operator.notifications)
	}
	response, err = client.PostForm(server.URL+"/server/restart", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || operator.restarts != 1 {
		t.Fatalf("restart response=%d restarts=%d", response.StatusCode, operator.restarts)
	}
	response, err = client.PostForm(server.URL+"/server/restart-gateway", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || operator.gatewayRestarts != 1 {
		t.Fatalf("gateway restart response=%d restarts=%d", response.StatusCode, operator.gatewayRestarts)
	}
	response, err = client.PostForm(server.URL+"/server/restart-game", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || operator.gameRestarts != 1 {
		t.Fatalf("game restart response=%d restarts=%d", response.StatusCode, operator.gameRestarts)
	}
	response, err = client.PostForm(server.URL+"/server/restart-gmsv", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || operator.gmsvRestarts != 1 {
		t.Fatalf("GMSV restart response=%d restarts=%d", response.StatusCode, operator.gmsvRestarts)
	}
	response, err = client.PostForm(server.URL+"/server/restart-saac", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || operator.saacRestarts != 1 {
		t.Fatalf("SAAC restart response=%d restarts=%d", response.StatusCode, operator.saacRestarts)
	}
	response, err = client.PostForm(server.URL+"/server/stop", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || operator.stops != 1 {
		t.Fatalf("stop response=%d stops=%d", response.StatusCode, operator.stops)
	}
	response, err = client.PostForm(server.URL+"/server/stop-gateway", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || operator.gatewayStops != 1 {
		t.Fatalf("gateway stop response=%d stops=%d", response.StatusCode, operator.gatewayStops)
	}
	response, err = client.PostForm(server.URL+"/server/stop-game", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || operator.gameStops != 1 {
		t.Fatalf("game stop response=%d stops=%d", response.StatusCode, operator.gameStops)
	}
	response, err = client.PostForm(server.URL+"/server/stop-gmsv", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || operator.gmsvStops != 1 {
		t.Fatalf("GMSV stop response=%d stops=%d", response.StatusCode, operator.gmsvStops)
	}
	response, err = client.PostForm(server.URL+"/server/stop-saac", url.Values{"csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || operator.saacStops != 1 {
		t.Fatalf("SAAC stop response=%d stops=%d", response.StatusCode, operator.saacStops)
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
