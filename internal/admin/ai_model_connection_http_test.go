package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airuntime"
	"github.com/k0ngk0ng/stoneage/internal/auth"
)

type aiConnectionTestFunc func(context.Context, string) error

func (f aiConnectionTestFunc) TestModelConfig(ctx context.Context, id string) error {
	return f(ctx, id)
}

func TestAIModelConnectionHTTPReportsSafeResultAndRequiresCSRF(t *testing.T) {
	root := t.TempDir()
	accounts, err := auth.Open(filepath.Join(root, "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer accounts.Close()
	ctx := context.Background()
	if err = accounts.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = accounts.CreateAdmin(ctx, "admin", []byte("secret123")); err != nil {
		t.Fatal(err)
	}
	models, err := airuntime.OpenStore(filepath.Join(root, "ai.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer models.Close()
	var calls atomic.Int32
	control, err := NewServer(accounts, Options{AIStore: models, CSRFSecret: []byte("probe-http-test"), AIConnectionTester: aiConnectionTestFunc(func(ctx context.Context, id string) error {
		calls.Add(1)
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 120*time.Second {
			t.Error("missing probe deadline")
		}
		if id == "model-failure" {
			return classifiedProbeFailure("authentication")
		}
		return nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(control.Handler())
	defer server.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login, err := client.PostForm(server.URL+"/login", map[string][]string{"username": {"admin"}, "password": {"secret123"}})
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	page, err := client.Get(server.URL + "/ai/models")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(page.Body)
	page.Body.Close()
	csrf := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindSubmatch(raw)
	if len(csrf) != 2 {
		t.Fatal("missing CSRF")
	}
	for _, tc := range []struct {
		id     string
		csrf   bool
		status int
		code   string
	}{{"model-ok", false, 403, ""}, {"model-ok", true, 200, ""}, {"model-failure", true, 502, "authentication"}} {
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/ai/models/"+tc.id+"/test", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if tc.csrf {
			req.Header.Set("X-CSRF-Token", string(csrf[1]))
		}
		r, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != tc.status || strings.Contains(string(body), "private-key-and-upstream-body") {
			t.Fatalf("status=%d body=%s", r.StatusCode, body)
		}
		if tc.csrf {
			var result map[string]any
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatal(err)
			}
			if _, ok := result["duration_ms"].(float64); !ok {
				t.Fatal("missing duration")
			}
			if tc.code != "" && result["code"] != tc.code {
				t.Fatal("missing stable error code")
			}
			if tc.status == 200 && result["ok"] != true {
				t.Fatal("success unconfirmed")
			}
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("unexpected test calls: %d", calls.Load())
	}
}
