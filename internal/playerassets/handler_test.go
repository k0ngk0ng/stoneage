package playerassets

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUsesWebAliasesAndRestrictsManifestPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(`{"bitmaps":{"91":{"file":"item.png"}},"bitmap_aliases":{"20033":91},"actor_bitmaps":{"100250":"unrelated-bitmap.png","100251":"../outside.png"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"item.png", "pet.png", "unrelated-bitmap.png"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	h := &Handler{Root: root}
	if err := os.WriteFile(filepath.Join(root, "sprites.json"), []byte(`{"100250":{"actions":[{"direction":1,"action":3,"frames":[{"file":"pet.png"}]}]},"100251":{"actions":[{"direction":1,"action":3,"frames":[{"file":"../outside.png"}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		url, body string
		status    int
	}{
		{"/api/player-assets/graphic/20033?kind=item", "item.png", 200},
		{"/api/player-assets/graphic/100250?kind=pet", "pet.png", 200},
		{"/api/player-assets/graphic/100250?kind=character", "pet.png", 200},
		{"/api/player-assets/graphic/100251?kind=pet", "", 404},
		{"/api/player-assets/graphic/999?kind=item", "", 404},
		{"/api/player-assets/graphic/../../manifest.json?kind=item", "", 404},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", tt.url, nil))
		if w.Code != tt.status || tt.body != "" && w.Body.String() != tt.body {
			t.Fatalf("%s: %d %s", tt.url, w.Code, w.Body)
		}
	}
}

func TestPetFrameChangeDoesNotReuseTimestampCachedImage(t *testing.T) {
	root := t.TempDir()
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", `{}`)
	write("old.png", "unrelated rabbit")
	write("correct.png", "native pig")
	stamp := time.Unix(1000000000, 0)
	for _, name := range []string{"old.png", "correct.png"} {
		if err := os.Chtimes(filepath.Join(root, name), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	write("sprites.json", `{"100250":{"actions":[{"direction":1,"action":3,"frames":[{"file":"old.png"}]}]}}`)
	h := &Handler{Root: root}
	url := "/api/player-assets/graphic/100250?kind=pet"
	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest("GET", url, nil))
	write("sprites.json", `{"100250":{"actions":[{"direction":1,"action":3,"frames":[{"file":"correct.png"}]}]}}`)
	request := httptest.NewRequest("GET", url, nil)
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))
	request.Header.Set("If-Modified-Since", stamp.UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT"))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 200 || response.Body.String() != "native pig" {
		t.Fatalf("stale preview: %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("ETag") == first.Header().Get("ETag") {
		t.Fatal("frame identity did not change ETag")
	}
}
