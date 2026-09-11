package clientip

import "testing"

func TestProxyHeaderRoundTrip(t *testing.T) {
	for _, ip := range []string{"203.0.113.7", "2001:db8::7"} {
		header, err := ProxyHeader(ip)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseProxyHeader(header)
		if err != nil || got != ip {
			t.Fatalf("%q: got %q, %v", ip, got, err)
		}
	}
	for _, header := range []string{"PROXY UNKNOWN\r\n", "PROXY TCP4 2001:db8::7 0.0.0.0 0 0\r\n", "PROXY TCP4 203.0.113.7 0.0.0.0 0 65536\r\n", "PROXY TCP4 invalid 0.0.0.0 0 0\r\n", "PROXY TCP4 203.0.113.7 0.0.0.0 0 0\n"} {
		if _, err := ParseProxyHeader([]byte(header)); err == nil {
			t.Fatalf("accepted malformed header %q", header)
		}
	}
}
