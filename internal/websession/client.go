package websession

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
)

const (
	defaultRequestTimeout = 90 * time.Second
	defaultPollTimeout    = 30 * time.Second
	maxResponseBytes      = 8 << 20
	maxSessionIDBytes     = 256
)

// Client owns the two transport boundaries used by Connector. Public game
// session creation and the browser-compatible event stream use BaseURL. Agent
// lease operations use a separate HTTP client whose dialer is pinned to the
// Unix socket in Config.SocketPath.
type Client struct {
	baseURL     *url.URL
	serverID    string
	web         *http.Client
	control     *http.Client
	controlBase *url.URL
	pollTimeout time.Duration
}

// New constructs the Web client and private Unix transport. The production
// path requires SocketPath; tests may use a Unix listener serving the same
// HTTP API rather than a TCP test server.
func New(config Config) (*Client, error) {
	base, err := normalizeBaseURL(config.BaseURL)
	if err != nil {
		return nil, err
	}
	publicClient := &http.Client{
		Transport:     cloneNoRedirectTransport(http.DefaultTransport),
		Timeout:       defaultRequestTimeout,
		CheckRedirect: noRedirect,
	}
	client := &Client{
		baseURL: base, serverID: strings.TrimSpace(config.ServerID),
		web:         publicClient,
		pollTimeout: defaultPollTimeout,
	}
	// The private control socket carries the agent lease. A caller that only
	// plays through the public Web session (a remote headless client) has no
	// access to it, so the client is built without one and every lease call
	// reports that instead of dialing a path that cannot exist.
	if socket := strings.TrimSpace(config.SocketPath); socket != "" {
		client.control = newUnixHTTPClient(socket)
		client.controlBase = &url.URL{Scheme: "http", Host: "stoneage-web-agent"}
	}
	return client, nil
}

// requiresControl reports whether this client can reach the private agent API.
func (client *Client) requiresControl() error {
	if client == nil || client.control == nil {
		return fmt.Errorf("%w: this client has no private control socket; agent leases are unavailable", ErrInvalidConfig)
	}
	return nil
}

func (client *Client) Close() error {
	if client == nil {
		return nil
	}
	if transport, ok := client.web.Transport.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
	if client.control != nil {
		if transport, ok := client.control.Transport.(interface{ CloseIdleConnections() }); ok {
			transport.CloseIdleConnections()
		}
	}
	return nil
}

// Dial creates the browser-compatible HTTP net.Conn used by the native
// aigame login protocol. The greeting is seeded from the /api/sessions JSON
// response, then Read is backed by the session's long-poll events endpoint.
func (client *Client) Dial(ctx context.Context, address string) (net.Conn, error) {
	if client == nil || client.web == nil || client.baseURL == nil {
		return nil, ErrInvalidConfig
	}
	if ctx == nil {
		ctx = context.Background()
	}
	serverID := strings.TrimSpace(address)
	if client.serverID != "" {
		serverID = client.serverID
	}
	if serverID == "" {
		return nil, fmt.Errorf("%w: server ID is required", ErrInvalidRequest)
	}
	requestBody := struct {
		ServerID string `json:"server_id,omitempty"`
	}{ServerID: serverID}
	var response createSessionResponse
	if err := client.publicJSON(ctx, http.MethodPost, "/api/sessions", requestBody, &response); err != nil {
		return nil, err
	}
	if err := response.Validate(); err != nil {
		return nil, err
	}
	greeting, err := base64.StdEncoding.DecodeString(response.Greeting)
	if err != nil || len(greeting) != 2 || greeting[0] != 'L' || greeting[1] != 0 {
		return nil, fmt.Errorf("%w: invalid Web greeting", ErrProtocol)
	}
	return newHTTPConn(client, response.ID, serverID, greeting), nil
}

// Attach claims one existing Web session for this exact identity. Web
// performs the final identity check and Gate compare-and-swap.
func (client *Client) Attach(ctx context.Context, request AttachRequest) (AttachResponse, error) {
	if err := client.requiresControl(); err != nil {
		return AttachResponse{}, err
	}
	if err := request.Validate(); err != nil {
		return AttachResponse{}, err
	}
	var response AttachResponse
	if err := client.privateJSON(ctx, http.MethodPost, AttachPath, "", request, &response); err != nil {
		return AttachResponse{}, err
	}
	if err := response.Validate(); err != nil {
		return AttachResponse{}, err
	}
	return response, nil
}

func (client *Client) Observe(ctx context.Context, token string) (ObserveResponse, error) {
	if err := client.requiresControl(); err != nil {
		return ObserveResponse{}, err
	}
	if strings.TrimSpace(token) == "" {
		return ObserveResponse{}, ErrLeaseUnavailable
	}
	var response ObserveResponse
	if err := client.privateJSON(ctx, http.MethodPost, ObservePath, token, struct{}{}, &response); err != nil {
		return ObserveResponse{}, err
	}
	if response.Snapshot.SessionToken == "" {
		response.Snapshot.SessionToken = strings.TrimSpace(response.SessionToken)
	}
	return response, nil
}

func (client *Client) Execute(ctx context.Context, token string, request ExecuteRequest) error {
	if err := client.requiresControl(); err != nil {
		return err
	}

	if strings.TrimSpace(token) == "" {
		return ErrLeaseUnavailable
	}
	if err := request.Validate(); err != nil {
		return err
	}
	return client.privateNoContent(ctx, http.MethodPost, ExecutePath, token, request)
}

func (client *Client) Detach(ctx context.Context, token string) error {
	if err := client.requiresControl(); err != nil {
		return err
	}

	if strings.TrimSpace(token) == "" {
		return nil
	}
	return client.privateNoContent(ctx, http.MethodPost, DetachPath, token, struct{}{})
}

// watch returns nil for an active 204 response and ErrLeaseRevoked when Web
// returns 410. The caller owns the retry loop and cancellation policy.
func (client *Client) watch(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return ErrLeaseUnavailable
	}
	request, err := client.newPrivateRequest(ctx, http.MethodGet, WatchPath, token, nil)
	if err != nil {
		return err
	}
	response, err := client.control.Do(request)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrLeaseUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusOK {
		return nil
	}
	return statusError(response.StatusCode)
}

func (client *Client) controlGeneration(ctx context.Context, sessionID string) (uint64, error) {
	if !validSessionID(sessionID) {
		return 0, ErrInvalidRequest
	}
	var response controlResponse
	if err := client.publicJSON(ctx, http.MethodGet, "/api/sessions/"+url.PathEscape(sessionID)+"/control", nil, &response); err != nil {
		return 0, err
	}
	if response.Control.Generation == 0 {
		return 0, fmt.Errorf("%w: control generation is missing", ErrProtocol)
	}
	return response.Control.Generation, nil
}

func (client *Client) publicJSON(ctx context.Context, method, endpoint string, body any, result any) error {
	request, err := client.newPublicRequest(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	response, err := client.web.Do(request)
	if err != nil {
		logHTTPFailure(endpoint, 0, err)
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrLeaseUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		logHTTPFailure(endpoint, response.StatusCode, statusError(response.StatusCode))
		return statusError(response.StatusCode)
	}
	err = decodeJSON(response.Body, result)
	if err != nil {
		logHTTPFailure(endpoint, response.StatusCode, err)
	}
	return err
}

func (client *Client) privateJSON(ctx context.Context, method, endpoint, token string, body any, result any) error {
	if err := client.requiresControl(); err != nil {
		return err
	}
	request, err := client.newPrivateRequest(ctx, method, endpoint, token, body)
	if err != nil {
		return err
	}
	response, err := client.control.Do(request)
	if err != nil {
		logHTTPFailure(endpoint, 0, err)
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrLeaseUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		logHTTPFailure(endpoint, response.StatusCode, statusError(response.StatusCode))
		return statusError(response.StatusCode)
	}
	err = decodeJSON(response.Body, result)
	if err != nil {
		logHTTPFailure(endpoint, response.StatusCode, err)
	}
	return err
}

func (client *Client) privateNoContent(ctx context.Context, method, endpoint, token string, body any) error {
	if err := client.requiresControl(); err != nil {
		return err
	}
	request, err := client.newPrivateRequest(ctx, method, endpoint, token, body)
	if err != nil {
		return err
	}
	response, err := client.control.Do(request)
	if err != nil {
		logHTTPFailure(endpoint, 0, err)
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrLeaseUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		logHTTPFailure(endpoint, response.StatusCode, statusError(response.StatusCode))
		if endpoint == ExecutePath && response.StatusCode == http.StatusConflict {
			return decodeExecuteError(response.Body)
		}
		return statusError(response.StatusCode)
	}
	_, _ = io.CopyN(io.Discard, response.Body, maxResponseBytes)
	return nil
}

// decodeExecuteError maps only the Web bridge's proven pre-submission
// rejection codes. Any missing, malformed or unknown code remains an
// unknown write outcome: a transport response alone cannot prove that the
// typed packet was not already submitted.
func decodeExecuteError(reader io.Reader) error {
	var response ErrorResponse
	if err := decodeJSON(reader, &response); err != nil {
		return ErrWriteOutcomeUnknown
	}
	switch strings.TrimSpace(response.Code) {
	case "stale_revision":
		return aigame.ErrStaleRevision
	case "invalid_action":
		return aigame.ErrInvalidAction
	case "wrong_phase":
		return aigame.ErrWrongPhase
	case "battle_not_ready":
		return aigame.ErrBattleNotReady
	default:
		return ErrWriteOutcomeUnknown
	}
}

func (client *Client) newPublicRequest(ctx context.Context, method, endpoint string, body any) (*http.Request, error) {
	return newJSONRequest(ctx, client.baseURL, method, endpoint, "", body)
}

func (client *Client) newPrivateRequest(ctx context.Context, method, endpoint, token string, body any) (*http.Request, error) {
	return newJSONRequest(ctx, client.controlBase, method, endpoint, token, body)
}

func newJSONRequest(ctx context.Context, base *url.URL, method, endpoint, token string, body any) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if base == nil || !strings.HasPrefix(endpoint, "/") || strings.ContainsAny(endpoint, "\x00\r\n") {
		return nil, ErrInvalidConfig
	}
	u := *base
	u.Path = path.Join(strings.TrimSuffix(base.Path, "/"), endpoint)
	if strings.HasSuffix(endpoint, "/") && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil || len(encoded) > maxResponseBytes {
			return nil, ErrInvalidRequest
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, ErrInvalidConfig
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		if !safeHeaderValue(token) {
			return nil, ErrInvalidRequest
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return request, nil
}

func decodeJSON(reader io.Reader, target any) error {
	if target == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(reader, maxResponseBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrProtocol
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrProtocol
	}
	return nil
}

func statusError(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	case http.StatusConflict:
		return ErrLeaseConflict
	case http.StatusGone:
		return ErrLeaseRevoked
	default:
		return ErrLeaseUnavailable
	}
}

func normalizeBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf("%w: Web base URL must be an HTTP(S) origin", ErrInvalidConfig)
	}
	parsed.Path = ""
	return parsed, nil
}

func validSessionID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len([]byte(value)) > maxSessionIDBytes || strings.ContainsAny(value, "/\\\x00\r\n") {
		return false
	}
	return true
}

type createSessionResponse struct {
	ID                  string       `json:"id"`
	Greeting            string       `json:"greeting"`
	EventAck            bool         `json:"event_ack"`
	Control             controlState `json:"control"`
	AutomationAvailable bool         `json:"automation_available"`
}

func (response createSessionResponse) Validate() error {
	if !validSessionID(response.ID) || response.Greeting == "" {
		return fmt.Errorf("%w: invalid Web session response", ErrProtocol)
	}
	return nil
}

type controlState struct {
	Mode       string `json:"mode"`
	Generation uint64 `json:"generation"`
	Reason     string `json:"reason,omitempty"`
}

type controlResponse struct {
	Control             controlState    `json:"control"`
	Recovery            json.RawMessage `json:"automation_recovery"`
	RecoveryUnavailable bool            `json:"automation_recovery_unavailable,omitempty"`
	AutomationAvailable bool            `json:"automation_available"`
	AutomationActive    bool            `json:"automation_active"`
	AutomationMode      string          `json:"automation_mode,omitempty"`
}

type eventsResponse struct {
	Events       []eventResponse `json:"events"`
	Closed       bool            `json:"closed"`
	Control      json.RawMessage `json:"control"`
	Acknowledged bool            `json:"acknowledged,omitempty"`
}

type eventResponse struct {
	Packet string `json:"packet,omitempty"`
	Closed bool   `json:"closed,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Session is both the provisioning HeadlessSession and the game session used
// by aiservice. Login/character selection use the public protocol stream;
// once EnterCharacter succeeds, gameplay reads and writes use the private
// typed lease RPCs.
type Session struct {
	client     *Client
	protocol   *aigame.Session
	connection *httpConn
	serverID   string

	mu          sync.Mutex
	leaseToken  string
	generation  uint64
	identity    Identity
	leaseDone   chan struct{}
	leaseClosed bool
	watchCancel context.CancelFunc
	closeOnce   sync.Once
	closeErr    error
}

func newSession(client *Client, protocol *aigame.Session, connection *httpConn, serverID string) *Session {
	return &Session{client: client, protocol: protocol, connection: connection, serverID: serverID, leaseDone: make(chan struct{})}
}

func (session *Session) Characters() []aigame.Character {
	if session == nil || session.protocol == nil {
		return nil
	}
	return session.protocol.Characters()
}

func (session *Session) RefreshCharacters(ctx context.Context) ([]aigame.Character, error) {
	if session == nil || session.protocol == nil {
		return nil, ErrClosed
	}
	return session.protocol.RefreshCharacters(ctx)
}

func (session *Session) CreateCharacter(ctx context.Context, create aigame.CharacterCreate) error {
	if session == nil || session.protocol == nil {
		return ErrClosed
	}
	return session.protocol.CreateCharacter(ctx, create)
}

func (session *Session) EnterCharacter(ctx context.Context, name string) error {
	if session == nil || session.protocol == nil {
		return ErrClosed
	}
	enterDone := traceStage(ctx, "enter_character")
	enterErr := session.protocol.EnterCharacter(ctx, name)
	enterDone(enterErr)
	if err := enterErr; err != nil {
		return err
	}
	snapshot := session.protocol.Snapshot()
	identity, err := identityFromSnapshot(snapshot, session.serverID)
	if err != nil {
		return err
	}
	controlDone := traceStage(ctx, "read_control_generation")
	generation, err := session.client.controlGeneration(ctx, session.connection.sessionID)
	controlDone(err)
	if err != nil {
		return err
	}
	attachDone := traceStage(ctx, "attach_agent_lease")
	response, err := session.client.Attach(ctx, AttachRequest{SessionID: session.connection.sessionID, Generation: generation, Identity: identity})
	attachDone(err)
	if err != nil {
		return err
	}
	if !sameIdentity(identity, response.Identity) {
		return fmt.Errorf("%w: Web returned a different identity", ErrProtocol)
	}
	session.mu.Lock()
	session.leaseToken = response.Token
	session.generation = response.Generation
	session.identity = response.Identity
	session.mu.Unlock()
	session.startWatch()
	return nil
}

func (session *Session) Events() <-chan aigame.Event {
	if session == nil || session.protocol == nil {
		return nil
	}
	return session.protocol.Events()
}

func (session *Session) Observe(ctx context.Context) (aigame.Snapshot, error) {
	if session == nil || session.protocol == nil {
		return aigame.Snapshot{}, ErrClosed
	}
	token, active := session.lease()
	if !active {
		return aigame.Snapshot{}, ErrLeaseUnavailable
	}
	response, err := session.client.Observe(ctx, token)
	if err != nil {
		session.handleLeaseError(err)
		return aigame.Snapshot{}, err
	}
	if response.SessionToken != "" {
		response.Snapshot.SessionToken = response.SessionToken
	}
	return response.Snapshot, nil
}

func (session *Session) ExecuteExpected(ctx context.Context, revision uint64, action aigame.Action) error {
	if session == nil || session.protocol == nil {
		return ErrClosed
	}
	token, active := session.lease()
	if !active {
		return ErrLeaseUnavailable
	}
	err := session.client.Execute(ctx, token, ExecuteRequest{Revision: revision, Action: action})
	if err != nil {
		session.handleLeaseError(err)
	}
	return err
}

// LeaseDone closes when Web revokes or the caller detaches the agent lease.
// It is deliberately separate from the game Events channel so supervisors
// can fence pending work even while no game packet is arriving.
func (session *Session) LeaseDone() <-chan struct{} {
	if session == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return session.leaseDone
}

func (session *Session) Generation() uint64 {
	if session == nil {
		return 0
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.generation
}

func (session *Session) Identity() Identity {
	if session == nil {
		return Identity{}
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.identity
}

func (session *Session) Close() error {
	if session == nil {
		return nil
	}
	session.closeOnce.Do(func() {
		session.mu.Lock()
		token := session.leaseToken
		cancel := session.watchCancel
		session.leaseToken = ""
		if !session.leaseClosed {
			session.leaseClosed = true
			close(session.leaseDone)
		}
		session.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if token != "" {
			ctx, cancelDetach := context.WithTimeout(context.Background(), 5*time.Second)
			_ = session.client.Detach(ctx, token)
			cancelDetach()
		}
		if session.protocol != nil {
			session.closeErr = session.protocol.Close()
		}
	})
	return session.closeErr
}

func (session *Session) lease() (string, bool) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.leaseClosed || session.leaseToken == "" {
		return "", false
	}
	return session.leaseToken, true
}

func (session *Session) startWatch() {
	ctx, cancel := context.WithCancel(context.Background())
	session.mu.Lock()
	if session.watchCancel != nil {
		session.watchCancel()
	}
	session.watchCancel = cancel
	token := session.leaseToken
	session.mu.Unlock()
	go func() {
		for {
			err := session.client.watch(ctx, token)
			if err == nil {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			// A failed watch cannot prove that the lease is still fenced. Do
			// not keep driving model turns while the private boundary is
			// unavailable; the Web side independently expires idle leases.
			// Known revocation and transport failures therefore share the same
			// fail-closed path, while cancellation remains a normal shutdown.
			session.markLeaseLost()
			return
		}
	}()
}

func (session *Session) handleLeaseError(err error) {
	if errors.Is(err, ErrLeaseRevoked) || errors.Is(err, ErrUnauthorized) {
		session.markLeaseLost()
	}
}

func (session *Session) markLeaseLost() {
	session.mu.Lock()
	if session.leaseClosed {
		session.mu.Unlock()
		return
	}
	session.leaseClosed = true
	session.leaseToken = ""
	close(session.leaseDone)
	cancel := session.watchCancel
	session.watchCancel = nil
	session.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Login creates a process-owned Web session. A revoked lease must close
	// that session instead of leaving an unaffiliated game connection alive
	// until the Web idle timeout. A human-attached session, if supported by a
	// separate adapter, must own its cleanup policy independently.
	if session.connection != nil {
		session.connection.close(false, ErrLeaseRevoked)
	}
	if session.protocol != nil {
		_ = session.protocol.Close()
	}
}

func sameIdentity(left, right Identity) bool {
	left, right = left.normalized(), right.normalized()
	if left.PersistentCharacterID == "" {
		left.PersistentCharacterID = right.PersistentCharacterID
	}
	return left == right
}

func identityFromSnapshot(snapshot aigame.Snapshot, serverID string) (Identity, error) {
	account := strings.TrimSpace(snapshot.Account)
	character := strings.TrimSpace(snapshot.Character)
	serverID = strings.TrimSpace(serverID)
	if account == "" || character == "" || serverID == "" {
		return Identity{}, fmt.Errorf("%w: character identity is unavailable", ErrProtocol)
	}
	slot, matches := -1, 0
	for _, candidate := range snapshot.Characters {
		if strings.TrimSpace(candidate.Name) != character {
			continue
		}
		matches++
		if candidate.Slot >= 0 {
			slot = candidate.Slot
		}
	}
	if matches != 1 || slot < 0 {
		return Identity{}, fmt.Errorf("%w: canonical character slot is unavailable", ErrProtocol)
	}
	identity := Identity{AccountID: account, CharacterID: fmt.Sprintf("%s:%d", account, slot), CharacterName: character, ServerID: serverID}
	if snapshot.Connected && snapshot.AI.Received && aigame.ValidPersistentCharacterID(snapshot.AI.PersistentCharacterID) {
		identity.PersistentCharacterID = snapshot.AI.PersistentCharacterID
	}
	if err := identity.Validate(); err != nil {
		return Identity{}, err
	}
	return identity, nil
}
