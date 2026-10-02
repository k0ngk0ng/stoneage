package admin

import (
	"context"
	"errors"
	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/internal/ladder"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type arenaFixture struct {
	calls, offset int
	err           error
}

func (a *arenaFixture) ArenaStatus(_ context.Context, offset int) (ladder.AdminSnapshot, error) {
	a.calls++
	a.offset = offset
	return ladder.AdminSnapshot{Schema: 1, PageSize: 8, QueuedTotal: 1, Queues: []ladder.AdminQueue{{ID: "queue", Mode: 1, WaitMS: 2500, Members: []ladder.AdminMember{{Name: "测试玩家", Online: true}}}}, Matches: []ladder.AdminMatch{}}, a.err
}
func TestArenaMonitorAuthenticationReadOnlyAndUnavailable(t *testing.T) {
	ctx := context.Background()
	store, e := auth.Open(":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	if e = store.Migrate(ctx); e != nil {
		t.Fatal(e)
	}
	admin, e := store.CreateAdmin(ctx, "fixture", []byte("test-secret"))
	if e != nil {
		t.Fatal(e)
	}
	token, _, e := store.CreateSession(ctx, admin.ID, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	fixture := &arenaFixture{}
	s, e := NewServer(store, Options{Arena: fixture})
	if e != nil {
		t.Fatal(e)
	}
	send := func(method, path string, logged bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if logged {
			r.AddCookie(&http.Cookie{Name: "stoneage_admin_session", Value: token})
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	if w := send("GET", "/api/arena", false); w.Code != 401 || fixture.calls != 0 {
		t.Fatal(w.Code, fixture.calls)
	}
	if w := send("POST", "/api/arena", true); w.Code != 405 || fixture.calls != 0 {
		t.Fatal(w.Code, fixture.calls)
	}
	for _, q := range []string{"-1", "1", "257", "invalid"} {
		if w := send("GET", "/api/arena?offset="+q, true); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	if w := send("GET", "/api/arena?offset=8", true); w.Code != 200 || fixture.offset != 8 || !strings.Contains(w.Body.String(), "测试玩家") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := send("GET", "/arena", true); w.Code != 200 || !strings.Contains(w.Body.String(), "/static/arena.js") {
		t.Fatal(w.Code, w.Body.String())
	}
	fixture.err = errors.New("private path must not leak")
	if w := send("GET", "/api/arena", true); w.Code != 503 || strings.Contains(w.Body.String(), "private path") {
		t.Fatal(w.Code, w.Body.String())
	}
}
