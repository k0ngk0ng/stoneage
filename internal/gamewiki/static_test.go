package gamewiki

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http/httptest"
	"runtime"
	"testing"
)

func staticJSON(t *testing.T, name string, out any) {
	t.Helper()
	f, err := content.Open("snapshot/" + name + ".gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	if err = json.NewDecoder(z).Decode(out); err != nil {
		t.Fatal(err)
	}
}
func TestStaticSnapshotCoverage(t *testing.T) {
	var index, catalog StaticIndex
	staticJSON(t, "catalog.json", &catalog)
	staticJSON(t, catalog.Revision+"/index.json", &index)
	if len(catalog.Rows) != 0 || catalog.Revision != index.Revision {
		t.Fatal("landing catalog includes full index")
	}
	if len(index.Rows) != 22034 {
		t.Fatal(len(index.Rows))
	}
	seen := map[string]bool{}
	shards := map[string]map[string]Entry{}
	for _, row := range index.Rows {
		if len(row) != 11 || seen[row[0]] {
			t.Fatal("invalid index row", row[0])
		}
		seen[row[0]] = true
		if shardFor(row[0]) != row[10] {
			t.Fatal("shard mismatch")
		}
		if shards[row[10]] == nil {
			var shard map[string]Entry
			staticJSON(t, index.Revision+"/"+row[10]+".json", &shard)
			shards[row[10]] = shard
		}
		entry, ok := shards[row[10]][row[0]]
		if !ok || entry.Name != row[2] {
			t.Fatal("missing detail", row[0])
		}
	}
	for _, cat := range catalog.Categories {
		var part StaticIndex
		staticJSON(t, catalog.Revision+"/category-"+cat.ID+".json", &part)
		if len(part.Rows) != cat.Count {
			t.Fatal(cat.ID, len(part.Rows))
		}
		for _, row := range part.Rows {
			if row[1] != cat.ID {
				t.Fatal("cross-category index")
			}
		}
	}
}
func TestStaticServingHasNoSearchOrCatalogCache(t *testing.T) {
	h := NewHandler()
	var catalog StaticIndex
	staticJSON(t, "catalog.json", &catalog)
	for _, path := range []string{"/wiki", "/wiki/data/catalog.json", "/wiki/data/" + catalog.Revision + "/category-quest.json", "/wiki/search-worker.js"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Accept-Encoding", "gzip")
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(path, w.Code)
		}
		if path == "/wiki/data/catalog.json" && w.Body.Len() > 2048 {
			t.Fatal("landing payload too large", w.Body.Len())
		}
	}
	for _, path := range []string{"/wiki/api?q=石", "/wiki/data/../../quests.json", "/wiki/data/no-such-file.json"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 200; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/wiki/data/"+catalog.Revision+"/category-quest.json", nil)
		r.Header.Set("Accept-Encoding", "gzip")
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	growth := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	t.Logf("retained heap growth after 200 static requests: %d bytes", growth)
	if growth > 2<<20 {
		t.Fatal("unexpected resident cache", growth)
	}
}
func BenchmarkStaticCategory(b *testing.B) {
	h := NewHandler()
	f, _ := content.Open("snapshot/catalog.json.gz")
	z, _ := gzip.NewReader(f)
	var catalog StaticIndex
	json.NewDecoder(z).Decode(&catalog)
	z.Close()
	f.Close()
	r := httptest.NewRequest("GET", "/wiki/data/"+catalog.Revision+"/category-quest.json", nil)
	r.Header.Set("Accept-Encoding", "gzip")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		io.Copy(io.Discard, w.Body)
	}
}
