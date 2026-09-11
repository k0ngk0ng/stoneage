package admin

import (
	"net/http/httptest"
	"testing"
)

func TestRequestSourceIP(t *testing.T) {
	prefixes, err := parseTrustedProxies([]string{"192.0.2.1/32", "127.0.0.1", "::1/128"})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{trustedProxies: prefixes}
	tests := []struct{ name, peer, forwarded, real, want string }{
		{"docker nginx", "192.0.2.1:50000", "203.0.113.7", "", "203.0.113.7"},
		{"untrusted direct", "198.51.100.4:50000", "203.0.113.7", "203.0.113.8", "198.51.100.4"},
		{"untrusted private", "192.0.2.21:50000", "203.0.113.7", "", "192.0.2.21"},
		{"ignore supplied prefix", "192.0.2.1:50000", "192.0.2.88, 203.0.113.7", "", "203.0.113.7"},
		{"trusted chain", "192.0.2.1:50000", "203.0.113.7, 127.0.0.1", "", "203.0.113.7"},
		{"ipv6", "[::1]:50000", "2001:db8::7", "", "2001:db8::7"},
		{"mapped peer", "[::ffff:192.0.2.1]:50000", "203.0.113.7", "", "203.0.113.7"},
		{"real fallback", "192.0.2.1:50000", "", "203.0.113.7", "203.0.113.7"},
		{"no headers", "192.0.2.1:50000", "", "", "192.0.2.1"},
		{"invalid closest hop", "192.0.2.1:50000", "203.0.113.7, invalid", "192.0.2.8", "192.0.2.1"},
		{"private client", "192.0.2.1:50000", "192.168.1.8", "", "192.168.1.8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/", nil)
			request.RemoteAddr = tt.peer
			if tt.forwarded != "" {
				request.Header.Set("X-Forwarded-For", tt.forwarded)
			}
			if tt.real != "" {
				request.Header.Set("X-Real-IP", tt.real)
			}
			if got := server.requestSourceIP(request); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
	request := httptest.NewRequest("GET", "/", nil)
	request.RemoteAddr = "192.0.2.1:50000"
	request.Header.Add("X-Forwarded-For", "192.0.2.88")
	request.Header.Add("X-Forwarded-For", "203.0.113.7")
	if got := server.requestSourceIP(request); got != "203.0.113.7" {
		t.Fatalf("multiple headers: %s", got)
	}
}

func TestInvalidTrustedProxy(t *testing.T) {
	if _, err := parseTrustedProxies([]string{"not-an-ip"}); err == nil {
		t.Fatal("invalid proxy config accepted")
	}
}
