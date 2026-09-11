package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/auth"
	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

type testPlayerManager struct {
	calls    int
	account  string
	slot     int
	mutation playerdata.Mutation
	err      error
}

func (m *testPlayerManager) List(_ context.Context, account string) ([]playerdata.Character, error) {
	m.account = account
	return []playerdata.Character{{Slot: 1, Name: "角色乙", Online: true}}, m.err
}
func (m *testPlayerManager) Get(_ context.Context, account string, slot int) (playerdata.Snapshot, error) {
	m.account, m.slot = account, slot
	return playerdata.Snapshot{Name: "角色乙", Revision: strings.Repeat("a", 64)}, m.err
}
func (m *testPlayerManager) Apply(_ context.Context, account string, slot int, mutation playerdata.Mutation) (playerdata.Snapshot, error) {
	m.calls++
	m.account, m.slot, m.mutation = account, slot, mutation
	return playerdata.Snapshot{Name: "角色乙", Revision: strings.Repeat("b", 64)}, m.err
}

func TestPlayerAPIAccountBindingCSRFAndAudit(t *testing.T) {
	ctx := context.Background()
	store, err := auth.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	administrator, err := store.CreateAdmin(ctx, "admin", []byte("secret123"))
	if err != nil {
		t.Fatal(err)
	}
	account, err := store.CreateAccount(ctx, "target", []byte("local"))
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := store.CreateSession(ctx, administrator.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	manager := &testPlayerManager{}
	server, err := NewServer(store, Options{Players: manager, PlayerCatalog: &gamecatalog.Catalog{Items: []gamecatalog.Item{{Entry: gamecatalog.Entry{Kind: gamecatalog.KindItem, ID: 0, Name: "小斧头"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/accounts/" + strconv.FormatInt(account.ID, 10) + "/players"
	send := func(method, path, body, csrf string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "198.51.100.9:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: "stoneage_admin_session", Value: token})
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w
	}
	body := `{"revision":"` + strings.Repeat("a", 64) + `","action":"set_character","field":"gld","value":900}`
	if w := send("GET", base, "", "", false); w.Code != 401 || !strings.Contains(w.Body.String(), "error") {
		t.Fatalf("unauth: %d %s", w.Code, w.Body)
	}
	if w := send("GET", base, "", "", true); w.Code != 200 || !strings.Contains(w.Body.String(), "角色乙") || manager.account != "target" {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	if w := send("POST", base+"/1", body, "wrong", true); w.Code != 403 || manager.calls != 0 {
		t.Fatalf("csrf: %d calls=%d", w.Code, manager.calls)
	}
	csrf := server.csrfToken(token)
	if w := send("POST", base+"/1", strings.TrimSuffix(body, "}")+`,"username":"other"}`, csrf, true); w.Code != 400 || manager.calls != 0 {
		t.Fatalf("injected account: %d", w.Code)
	}
	if w := send("POST", base+"/2", body, csrf, true); w.Code != 404 || manager.calls != 0 {
		t.Fatalf("invalid slot: %d", w.Code)
	}
	if w := send("POST", base+"/1", body, csrf, true); w.Code != 200 {
		t.Fatalf("mutation: %d %s", w.Code, w.Body)
	}
	if manager.account != "target" || manager.slot != 1 || manager.calls != 1 || manager.mutation.Value != 900 {
		t.Fatalf("wrong target: %+v", manager)
	}
	events, err := store.RecentAudit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Event != "player_changed" {
			continue
		}
		found = true
		if event.Username != "target" || event.SourceIP != "198.51.100.9" || !strings.Contains(event.Detail, "\"value\":900") {
			t.Fatalf("audit: %+v", event)
		}
	}
	if !found {
		t.Fatal("missing completed mutation audit")
	}
	manager.err = playerdata.ErrConflict
	if w := send("POST", base+"/1", body, csrf, true); w.Code != 409 {
		t.Fatalf("conflict: %d %s", w.Code, w.Body)
	}
	if w := send("GET", "/api/player-catalog?kind=item&q=斧&limit=1", "", "", true); w.Code != 200 {
		t.Fatalf("catalog: %d %s", w.Code, w.Body)
	} else {
		var result struct {
			Total   int
			Entries []gamecatalog.Entry
		}
		if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Total != 1 || result.Entries[0].ID != 0 {
			t.Fatalf("catalog result: %s %v", w.Body, err)
		}
	}
}

func TestPlayerCatalogLoaderRetriesAndCaches(t *testing.T) {
	ctx := context.Background()
	store, err := auth.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	administrator, err := store.CreateAdmin(ctx, "admin", []byte("secret123"))
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := store.CreateSession(ctx, administrator.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	want := &gamecatalog.Catalog{Items: []gamecatalog.Item{{Entry: gamecatalog.Entry{
		Kind: gamecatalog.KindItem, ID: 777, Name: "懒加载物品",
	}}}}
	server, err := NewServer(store, Options{
		PlayerCatalogLoader: func() (*gamecatalog.Catalog, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("GMSV data is not ready")
			}
			return want, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/player-catalog?kind=item&q=懒加载", nil)
		r.AddCookie(&http.Cookie{Name: "stoneage_admin_session", Value: token})
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		return w
	}
	if w := request(); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("first catalog request status = %d, want 503", w.Code)
	}
	if w := request(); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "懒加载物品") {
		t.Fatalf("second catalog request = %d %s", w.Code, w.Body)
	}
	if w := request(); w.Code != http.StatusOK {
		t.Fatalf("cached catalog request status = %d, want 200", w.Code)
	}
	if calls != 2 {
		t.Fatalf("catalog loader calls = %d, want 2", calls)
	}
}
