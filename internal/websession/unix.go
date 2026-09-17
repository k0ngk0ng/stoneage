package websession

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"
)

func newUnixHTTPClient(socketPath string) *http.Client {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _ string, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   0,
		ResponseHeaderTimeout: 90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{Transport: transport, Timeout: defaultRequestTimeout, CheckRedirect: noRedirect}
}

func cloneNoRedirectTransport(transport http.RoundTripper) http.RoundTripper {
	if transport == nil {
		transport = http.DefaultTransport
	}
	if base, ok := transport.(*http.Transport); ok {
		copy := base.Clone()
		// The public Web origin is process configuration and carries the
		// account login and character packets. Never let environment proxy
		// settings redirect those credentials to an untrusted intermediary.
		copy.Proxy = nil
		return copy
	}
	return transport
}

func noRedirect(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }

func safeHeaderValue(value string) bool {
	return !strings.ContainsAny(value, "\x00\r\n")
}
