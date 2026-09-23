package gamewiki

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
)

func TestNativeCatalog(t *testing.T) {
	root := "../../server/legacy/source/2.5/gmsv/data"
	if _, err := os.Stat(root); err != nil {
		t.Skip("native archive unavailable")
	}
	c, err := Load(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("counts: %+v", c.Counts)
	if c.Counts["battle_npc"] != 278 {
		t.Fatalf("battle placements: %d", c.Counts["battle_npc"])
	}
	for _, kind := range []string{"pet", "equipment", "item", "skill", "map", "npc", "encounter", "quest"} {
		if c.Counts[kind] == 0 {
			t.Error("empty category", kind)
		}
	}
	boss := c.Entries["enemy:1690"]
	if boss == nil || boss.HP != "6535–7446" {
		t.Fatalf("dark king: %+v", boss)
	}
	for _, e := range c.Entries {
		for _, l := range e.Links {
			if l.Key != "" && c.Entries[l.Key] == nil {
				t.Error("broken link", l.Key)
			}
		}
	}
	raw, _ := json.Marshal(c.List)
	if strings.Contains(string(raw), "setup.cf") {
		t.Fatal("setup file exposed")
	}
	h := NewHandler()
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "/wiki/data/catalog.json", nil))
	if r.Code != 200 {
		t.Fatal(r.Code)
	}
}
func TestNativeStatsSharedAllocation(t *testing.T) {
	p := aiknowledge.EnemyBase{InitNum: 100, LevelUpPoint: 4, BaseVital: 10, BaseStr: 10, BaseTough: 10, BaseDex: 10}
	hp, a, d, s, ok := NativeStats(p, aiknowledge.Range{Min: 1, Max: 1})
	if !ok || hp.Min != 66 || hp.Max != 124 || a.Min != 10 || a.Max != 25 || d != a || s.Min != 8 || s.Max != 22 {
		t.Fatalf("%+v %+v %+v %+v %v", hp, a, d, s, ok)
	}
	if _, _, _, _, ok := NativeStats(p, aiknowledge.Range{Min: 0, Max: 1}); ok {
		t.Fatal("invalid level accepted")
	}
}
func TestQuestCoverage(t *testing.T) {
	var qs []Quest
	if err := json.Unmarshal(questData, &qs); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	sources := map[string]bool{}
	commissions := 0
	for _, q := range qs {
		if seen[q.ID] {
			t.Fatal("duplicate", q.ID)
		}
		seen[q.ID] = true
		if q.Name == "" || len(q.Steps) < 2 || len(q.Sources) == 0 {
			t.Fatal("incomplete quest", q.ID)
		}
		if strings.HasPrefix(q.ID, "wt") {
			commissions++
		}
		for _, s := range q.Sources {
			sources[s.URL] = true
		}
	}
	if commissions != 48 {
		t.Fatal("commissions", commissions)
	}
	for prefix, n := range map[string]int{"n": 14, "b": 11, "j": 5, "s": 2, "z": 5, "big5-": 7} {
		for i := 1; i <= n; i++ {
			url := "https://news.17173.com/z/stoneage/renwu/" + prefix + text(i) + ".htm"
			if !sources[url] {
				t.Error("missing source", url)
			}
		}
	}
	for _, id := range []string{"sa25_01", "sa25_02", "sa25_03", "sa25_04", "sa25_05", "sa25_06", "news_01", "news_02", "news_03", "news_09", "news_10", "faq01", "wt01", "wt02", "wt03", "wt04", "jot01", "jot02", "jot03", "jot04"} {
		if !sources["https://news.17173.com/z/stoneage/renwu/"+id+".htm"] {
			t.Error("missing source", id)
		}
	}
	originalSources := 0
	for source := range sources {
		if strings.HasPrefix(source, "https://news.17173.com/z/stoneage/renwu/") {
			originalSources++
		}
	}
	if originalSources != 64 {
		t.Fatal("original source count", originalSources)
	}
}
func TestHTTPReadOnly(t *testing.T) {
	h := NewHandler()
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"GET", "/wiki", 200}, {"HEAD", "/wiki/app.js", 200}, {"POST", "/wiki/api", 405}, {"GET", "/wiki/../setup.cf", 404}, {"GET", "/wiki/api?entry=../../setup.cf", 404}} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest(tc.method, tc.path, nil))
		if r.Code != tc.status {
			t.Error(tc, r.Code)
		}
		if tc.method == http.MethodHead && r.Body.Len() != 0 {
			t.Fatal("HEAD body")
		}
	}
}
