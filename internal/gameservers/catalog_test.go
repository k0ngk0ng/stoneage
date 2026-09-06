package gameservers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchCatalogValidation(t *testing.T) {
	one := Server{ID: "one", Name: "一线", Address: "gateway:9065", UpstreamAddress: "gmsv:9065"}
	for _, tc := range []struct {
		name    string
		status  int
		servers []Server
		raw     string
		valid   bool
	}{
		{name: "valid", status: 200, servers: []Server{one}, valid: true},
		{name: "duplicate", status: 200, servers: []Server{one, one}},
		{name: "empty", status: 200, servers: []Server{}},
		{name: "unavailable", status: 503},
		{name: "invalid", status: 200, raw: `{"servers":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/servers" {
					t.Errorf("path=%s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				if tc.raw != "" {
					w.Write([]byte(tc.raw))
				} else {
					json.NewEncoder(w).Encode(Catalog{Servers: tc.servers})
				}
			}))
			defer api.Close()
			_, err := Fetch(context.Background(), api.URL)
			if (err == nil) != tc.valid {
				t.Fatalf("err=%v valid=%v", err, tc.valid)
			}
		})
	}
}
