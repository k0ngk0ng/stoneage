package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The public AI entry points are deliberately a small allow-list.  The Web
// bridge has a larger, authenticated UI surface, so forwarding an arbitrary
// path or URL here would accidentally turn it into an admin proxy.
const (
	agentFrontdoorMaxRequestBytes  = 8 << 20
	agentFrontdoorMaxResponseBytes = 16 << 20
	agentFrontdoorTimeout          = 60 * time.Second
)

var agentFrontdoorWorkerPaths = map[string]struct{}{
	"/api/ai/worker/connect":   {},
	"/api/ai/worker/poll":      {},
	"/api/ai/worker/result":    {},
	"/api/ai/worker/heartbeat": {},
	"/api/ai/worker/v1/game":   {},
}

// webAgentFrontdoor forwards only the worker protocol and the fixed game
// gateway endpoint.  It never derives an upstream from the incoming Host,
// path or query string.
type webAgentFrontdoor struct {
	gameUpstream   url.URL
	workerUpstream url.URL
	client         *http.Client
}

func newWebAgentFrontdoor(gameUpstream, workerUpstream string) (*webAgentFrontdoor, error) {
	if strings.TrimSpace(gameUpstream) == "" && strings.TrimSpace(workerUpstream) == "" {
		return nil, nil
	}
	frontdoor := &webAgentFrontdoor{
		client: &http.Client{
			Transport: func() http.RoundTripper {
				// Do not honor HTTP_PROXY/HTTPS_PROXY for this server-owned
				// hop. A proxy would receive the worker's bearer credential.
				transport := http.DefaultTransport.(*http.Transport).Clone()
				transport.Proxy = nil
				return transport
			}(),
			Timeout: agentFrontdoorTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	var err error
	if strings.TrimSpace(gameUpstream) != "" {
		frontdoor.gameUpstream, err = normalizeAgentGameUpstream(gameUpstream)
		if err != nil {
			return nil, fmt.Errorf("invalid web agent game upstream: %w", err)
		}
	}
	if strings.TrimSpace(workerUpstream) != "" {
		frontdoor.workerUpstream, err = normalizeAgentWorkerUpstream(workerUpstream)
		if err != nil {
			return nil, fmt.Errorf("invalid web agent worker upstream: %w", err)
		}
	}
	return frontdoor, nil
}

func normalizeAgentUpstream(value string) (url.URL, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return url.URL{}, nil
	}
	if strings.ContainsAny(value, "\x00\r\n\t ") {
		return url.URL{}, errors.New("upstream contains unsafe characters")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return url.URL{}, errors.New("upstream must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	return *parsed, nil
}

func normalizeAgentGameUpstream(value string) (url.URL, error) {
	parsed, err := normalizeAgentUpstream(value)
	if err != nil {
		return url.URL{}, err
	}
	// A host-only value is convenient in tests and means the fixed default
	// path. Any other path remains operator-controlled but is required to be
	// the game gateway entry point; a request can never replace it.
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/v1/game"
	}
	if parsed.Path != "/v1/game" {
		return url.URL{}, errors.New("game upstream path must be /v1/game")
	}
	parsed.RawPath = ""
	return parsed, nil
}

func normalizeAgentWorkerUpstream(value string) (url.URL, error) {
	parsed, err := normalizeAgentUpstream(value)
	if err != nil {
		return url.URL{}, err
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return url.URL{}, errors.New("worker upstream must be an origin without a path")
	}
	parsed.Path, parsed.RawPath = "", ""
	return parsed, nil
}

func (frontdoor *webAgentFrontdoor) owns(path string) bool {
	if frontdoor == nil {
		return false
	}
	if path == "/v1/game" {
		return true
	}
	if _, ok := agentFrontdoorWorkerPaths[path]; ok {
		return true
	}
	return strings.HasPrefix(path, "/api/ai/worker/")
}

func (frontdoor *webAgentFrontdoor) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if frontdoor == nil {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Header.Get("Origin") != "" {
		http.Error(response, "browser access forbidden", http.StatusForbidden)
		return
	}
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	target, ok := frontdoor.targetFor(request.URL.Path)
	if !ok {
		http.NotFound(response, request)
		return
	}
	if request.ContentLength > agentFrontdoorMaxRequestBytes {
		http.Error(response, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	body := request.Body
	if body == nil {
		body = http.NoBody
	}
	proxied, err := http.NewRequestWithContext(request.Context(), http.MethodPost, target.String(), io.LimitReader(body, agentFrontdoorMaxRequestBytes+1))
	if err != nil {
		// Do not include the target or request headers in a diagnostic. The
		// target may contain operator-only network names and the headers carry
		// worker/session credentials.
		http.Error(response, "agent upstream unavailable", http.StatusBadGateway)
		return
	}
	copyAgentRequestHeaders(proxied.Header, request.Header)
	if request.ContentLength >= 0 && request.ContentLength <= agentFrontdoorMaxRequestBytes {
		proxied.ContentLength = request.ContentLength
	}
	upstreamResponse, err := frontdoor.client.Do(proxied)
	if err != nil {
		http.Error(response, "agent upstream unavailable", http.StatusBadGateway)
		return
	}
	defer upstreamResponse.Body.Close()
	copyAgentResponseHeaders(response.Header(), upstreamResponse.Header)
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(upstreamResponse.StatusCode)
	_, _ = io.Copy(response, io.LimitReader(upstreamResponse.Body, agentFrontdoorMaxResponseBytes))
}

func (frontdoor *webAgentFrontdoor) targetFor(path string) (url.URL, bool) {
	if path == "/v1/game" {
		if frontdoor.gameUpstream.Host == "" {
			return url.URL{}, false
		}
		target := frontdoor.gameUpstream
		target.RawQuery, target.Fragment = "", ""
		return target, true
	}
	if _, ok := agentFrontdoorWorkerPaths[path]; ok {
		if frontdoor.workerUpstream.Host == "" {
			return url.URL{}, false
		}
		target := frontdoor.workerUpstream
		target.Path = path
		target.RawPath, target.RawQuery, target.Fragment = "", "", ""
		return target, true
	}
	const profilePrefix = "/api/ai/worker/game/"
	if strings.HasPrefix(path, profilePrefix) && frontdoor.workerUpstream.Host != "" {
		profile := strings.TrimPrefix(path, profilePrefix)
		if !validAgentProfilePath(profile) {
			return url.URL{}, false
		}
		target := frontdoor.workerUpstream
		target.Path = profilePrefix + profile
		target.RawPath, target.RawQuery, target.Fragment = "", "", ""
		return target, true
	}
	return url.URL{}, false
}

func validAgentProfilePath(profile string) bool {
	if profile == "" || len(profile) > 128 || strings.ContainsAny(profile, "/\\%\x00\r\n") {
		return false
	}
	for index, char := range profile {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || (index > 0 && strings.ContainsRune("._:-", char)) {
			continue
		}
		return false
	}
	return true
}

// Only headers used by the worker protocol and JSON gateway cross the public
// boundary. In particular cookies, forwarded-host headers and arbitrary
// tracing values never reach the internal admin process.
func copyAgentRequestHeaders(destination, source http.Header) {
	for _, name := range []string{
		"Accept",
		"Authorization",
		"Content-Type",
		"X-StoneAge-Worker-ID",
		"X-StoneAge-Profile-ID",
		"X-StoneAge-Worker-Epoch",
	} {
		if values, ok := source[http.CanonicalHeaderKey(name)]; ok {
			for _, value := range values {
				destination.Add(name, value)
			}
		}
	}
}

func copyAgentResponseHeaders(destination, source http.Header) {
	// Responses are intentionally narrower than a general reverse proxy. In
	// particular, Location and Set-Cookie could disclose an admin origin or
	// establish a browser session on behalf of the caller.
	for _, name := range []string{"Content-Type", "Retry-After"} {
		for _, value := range source.Values(name) {
			destination.Add(name, value)
		}
	}
}

// agentListener owns exactly one Unix socket path. The path is removed on
// close only when it still refers to the socket created by this instance.
type agentListener struct {
	server    *http.Server
	listener  net.Listener
	handler   *Handler
	path      string
	ownerInfo os.FileInfo
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func (listener *agentListener) Close() error {
	if listener == nil {
		return nil
	}
	listener.closeOnce.Do(func() {
		listener.closeErr = listener.server.Close()
		<-listener.done
		if current, err := os.Lstat(listener.path); err == nil && os.SameFile(listener.ownerInfo, current) {
			if err := os.Remove(listener.path); err != nil && !errors.Is(err, os.ErrNotExist) && listener.closeErr == nil {
				listener.closeErr = err
			}
		}
	})
	return listener.closeErr
}

// StartAgentListener mounts the private agent API on a Unix socket. It does
// not expose the handler through the public HTTP server.
func (handler *Handler) StartAgentListener(path string) (io.Closer, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	if handler == nil {
		return nil, errors.New("web handler is nil")
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("agent socket path must be absolute")
	}
	if err := prepareAgentSocketPath(path); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on agent socket: %w", err)
	}
	if unixListener, ok := listener.(*net.UnixListener); ok {
		// We own cleanup and protect against a path being replaced while the
		// listener is alive. The default net/http unlink-on-close would remove
		// a replacement path before Close could perform that check.
		unixListener.SetUnlinkOnClose(false)
	}
	ownerInfo, err := os.Lstat(path)
	if err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("stat agent socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("set agent socket permissions: %w", err)
	}
	server := &http.Server{
		Handler:           handler.InternalAgentHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	result := &agentListener{server: server, listener: listener, handler: handler, path: path, ownerInfo: ownerInfo, done: make(chan struct{})}
	go func() {
		defer close(result.done)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// A failed private listener must revoke all leases. Otherwise a
			// runtime could retain a control generation that can no longer be
			// observed or detached.
			handler.Close()
		}
	}()
	if handler.stop != nil {
		go func() {
			select {
			case <-handler.stop:
				_ = result.Close()
			case <-result.done:
			}
		}()
	}
	return result, nil
}

func prepareAgentSocketPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect agent socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("agent socket path is occupied by a non-socket")
	}
	active, err := agentSocketIsActive(path)
	if err != nil {
		return fmt.Errorf("check existing agent socket: %w", err)
	}
	if active {
		return errors.New("agent socket is already in use")
	}
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("recheck stale agent socket: %w", err)
	}
	if !os.SameFile(info, current) {
		return errors.New("agent socket path changed while checking stale socket")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale agent socket: %w", err)
	}
	return nil
}

func agentSocketIsActive(path string) (bool, error) {
	connection, err := net.DialTimeout("unix", path, 250*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		return true, nil
	}
	var operationError *net.OpError
	if errors.As(err, &operationError) {
		var syscallError syscall.Errno
		if errors.As(operationError.Err, &syscallError) {
			switch syscallError {
			case syscall.ECONNREFUSED, syscall.ENOENT, syscall.ECONNRESET:
				return false, nil
			case syscall.EACCES, syscall.EPERM, syscall.ETIMEDOUT:
				return false, err
			}
		}
	}
	// An unknown connect error is not proof that the socket is stale. Keep the
	// path intact so a permission or namespace problem fails closed.
	return false, err
}
