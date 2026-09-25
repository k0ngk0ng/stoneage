package main

import (
	"encoding/json"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBattleJournalRequiresExistingSession(t *testing.T) {
	fake := newFakeTCP(t, []byte{'L', 0}, nil)
	handler, err := NewHandler(testConfig(fake.address()))
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	create := httptest.NewRecorder()
	handler.ServeHTTP(create, httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader("{}")))
	var created createResponse
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("create: %s", create.Body.String())
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/api/sessions/unknown/battle-log", 404}, {"POST", "/api/sessions/" + created.ID + "/battle-log", 405}, {"GET", "/api/sessions/" + created.ID + "/battle-log", 200}} {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(tc.method, tc.path, nil))
		if r.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.path, r.Code, r.Body.String())
		}
		if r.Code == 200 {
			var journal aigame.BattleJournal
			if err := json.Unmarshal(r.Body.Bytes(), &journal); err != nil {
				t.Fatal(err)
			}
			if len(journal.Battles) != 0 || r.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("journal leakage/cache")
			}
		}
	}
}
