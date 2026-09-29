package websession

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestInPlaceCloseWaitsAndReportsFailure(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusGatewayTimeout} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "DELETE" || r.URL.Path != "/api/sessions/fixture" || r.URL.Query().Get("wait") != "1" {
					t.Errorf("wrong close request: %s %s", r.Method, r.URL)
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			conn := newHTTPConn(client, "fixture", "local", nil)
			err = conn.CloseAndWait(context.Background())
			if (err == nil) != (status == http.StatusNoContent) {
				t.Fatalf("close status %d: %v", status, err)
			}
			conn.Close()
			if calls.Load() != 1 {
				t.Fatalf("close mutation repeated %d times", calls.Load())
			}
		})
	}
}
