package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWikiIndependentOfGameSession(t *testing.T) {
	fake := newFakeTCP(t, []byte{'L', 0}, nil)
	h, err := NewHandler(testConfig(fake.address()))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	for _, path := range []string{"/wiki", "/wiki/", "/wiki/app.js", "/wiki/style.css"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		if w.Header().Get("Set-Cookie") != "" {
			t.Fatal("wiki created game session")
		}
		if path == "/wiki" && !strings.Contains(w.Body.String(), "石器百科") {
			t.Fatal("wiki routed to game page")
		}
	}
}
