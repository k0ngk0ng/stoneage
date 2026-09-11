package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/clientip"
)

func TestSessionForwardsClientIPToGateway(t *testing.T) {
	for _, tt := range []struct{ name, peer, forwarded, want string }{
		{"trusted nginx", "192.0.2.1:4321", "198.51.100.88, 203.0.113.7", "203.0.113.7"},
		{"direct client", "198.51.100.4:4321", "203.0.113.7", "198.51.100.4"},
		{"ipv6 client", "192.0.2.1:4321", "2001:db8::7", "2001:db8::7"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := newFakeTCP(t, []byte{'L', 0}, nil)
			cfg := testConfig(upstream.address())
			cfg.ForwardClientIP = true
			cfg.TrustedProxies = []string{"192.0.2.1/32"}
			handler, err := NewHandler(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer handler.Close()
			request := httptest.NewRequest(http.MethodPost, "/api/sessions", strings.NewReader("{}"))
			request.RemoteAddr = tt.peer
			request.Header.Set("X-Forwarded-For", tt.forwarded)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusCreated {
				t.Fatalf("session status=%d body=%s", response.Code, response.Body.String())
			}
			select {
			case header := <-upstream.received:
				got, err := clientip.ParseProxyHeader(header)
				if err != nil || got != tt.want {
					t.Fatalf("forwarded %q, want %q: %v", got, tt.want, err)
				}
			case <-time.After(time.Second):
				t.Fatal("gateway did not receive source IP")
			}
		})
	}
}
