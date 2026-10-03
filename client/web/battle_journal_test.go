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
	// Feed the existing session's shared observer, then read the actual HTTP
	// endpoint. This adds structured data only, without changing Web UI labels.
	session, ok := handler.sessions.get(created.ID)
	if !ok {
		t.Fatal("created session disappeared")
	}
	session.applyAuthoritativePacket(webServerIntPacket(t, 1, "EN", 1, 218))
	session.applyAuthoritativePacket(webServerPacket(t, 2, "B", "BP|0|0|14"))
	session.applyAuthoritativePacket(webServerPacket(t, 3, "B", "BH|a0|rA|f602|dA|gF|FF|"))
	r := httptest.NewRecorder()
	handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/battle-log", nil))
	var journal aigame.BattleJournal
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &journal) != nil || len(journal.Battles) != 1 {
		t.Fatalf("journal: %s", r.Body.String())
	}
	logs := journal.Battles[0].Logs
	entry := logs[len(logs)-1]
	if entry.Recipient == nil || *entry.Recipient != 0 || entry.Guardian == nil || *entry.Guardian != 15 {
		t.Fatalf("recipient not exposed: %+v", entry)
	}
	// The Web panel reads the same combo and human-round projection as sactl.
	session.applyAuthoritativePacket(webServerPacket(t, 4, "B", "BA|0|1|"))
	session.applyAuthoritativePacket(webServerPacket(t, 5, "B", "BY|rA|a0|f2|d3|p0|a5|f2|d1|p0|FF|"))
	r = httptest.NewRecorder()
	handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/sessions/"+created.ID+"/battle-log", nil))
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &journal) != nil {
		t.Fatal(r.Body.String())
	}
	logs = journal.Battles[0].Logs
	for i, actor := range []int{0, 5} {
		hit := logs[len(logs)-2+i]
		if hit.Kind != "attack" || hit.Actor != actor || hit.Turn != 2 || hit.Hits != 2 || !strings.Contains(hit.Text, "合击") {
			t.Fatal(hit)
		}
	}

}
