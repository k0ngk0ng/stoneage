package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/gameservers"
	"github.com/k0ngk0ng/stoneage/internal/websession"
	"github.com/k0ngk0ng/stoneage/server/go/namedproto"
)

const agentIntegrationServerID = "line-a"

type agentIntegrationPacket struct {
	Connection int
	Packet     []byte
	Message    namedproto.Message
}

type agentIntegrationGame struct {
	listener net.Listener

	mu          sync.Mutex
	connections map[int]net.Conn
	closed      map[int]chan struct{}
	packets     []agentIntegrationPacket
	characters  []string
	nextID      int
	stopOnce    sync.Once
	done        chan struct{}
	wg          sync.WaitGroup
}

func newAgentIntegrationGame(t *testing.T) *agentIntegrationGame {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	game := &agentIntegrationGame{
		listener:    listener,
		connections: make(map[int]net.Conn),
		closed:      make(map[int]chan struct{}),
		characters:  []string{"Hero"},
		done:        make(chan struct{}),
	}
	game.wg.Add(1)
	go game.acceptLoop()
	t.Cleanup(func() {
		game.close()
	})
	return game
}

func (game *agentIntegrationGame) acceptLoop() {
	defer game.wg.Done()
	for {
		connection, err := game.listener.Accept()
		if err != nil {
			select {
			case <-game.done:
				return
			default:
			}
			return
		}
		game.mu.Lock()
		game.nextID++
		id := game.nextID
		game.connections[id] = connection
		game.closed[id] = make(chan struct{})
		game.mu.Unlock()
		game.wg.Add(1)
		go game.serve(id, connection)
	}
}

func (game *agentIntegrationGame) serve(id int, connection net.Conn) {
	defer game.wg.Done()
	defer func() {
		_ = connection.Close()
		game.mu.Lock()
		if done := game.closed[id]; done != nil {
			select {
			case <-done:
			default:
				close(done)
			}
		}
		game.mu.Unlock()
	}()
	if _, err := connection.Write([]byte{'L', 0}); err != nil {
		return
	}
	reader := bufio.NewReader(connection)
	for {
		packet, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		raw, err := namedproto.DecodePacket(packet)
		if err != nil {
			return
		}
		message, err := namedproto.ParseMessage(raw)
		if err != nil {
			return
		}
		game.mu.Lock()
		game.packets = append(game.packets, agentIntegrationPacket{
			Connection: id,
			Packet:     append([]byte(nil), packet...),
			Message:    message,
		})
		game.mu.Unlock()
		game.reply(id, connection, message)
	}
}

func (game *agentIntegrationGame) reply(_ int, connection net.Conn, request namedproto.Message) {
	var fields []string
	switch request.Function {
	case "ClientLogin":
		fields = []string{namedproto.EncodeString([]byte("ok"))}
	case "CharList":
		game.mu.Lock()
		characters := append([]string(nil), game.characters...)
		game.mu.Unlock()
		records := make([]string, 0, len(characters))
		for _, character := range characters {
			// The second protocol escape layer is the one used by the native
			// character list parser. namedproto.EncodeString adds the outer
			// transport escaping below.
			records = append(records, character+"|0\\z")
		}
		fields = []string{
			namedproto.EncodeString([]byte("successful")),
			namedproto.EncodeString([]byte(strings.Join(records, " "))),
		}
	case "CreateNewChar":
		if len(request.Fields) >= 2 {
			if rawName, err := namedproto.DecodeString(request.Fields[1]); err == nil && strings.TrimSpace(string(rawName)) != "" {
				game.mu.Lock()
				game.characters = append(game.characters, string(rawName))
				game.mu.Unlock()
			}
		}
		fields = []string{namedproto.EncodeString([]byte("successful"))}
	case "CharLogin":
		fields = []string{
			namedproto.EncodeString([]byte("successful")),
			namedproto.EncodeString(nil),
		}
	default:
		// Gameplay writes do not have a request/response handshake. The
		// packet remains recorded above so callers can assert its transport.
		return
	}

	packet, err := namedproto.RawMessage(request.ID, request.Function, fields)
	if err != nil {
		return
	}
	framed, err := namedproto.EncodePacket(packet)
	if err != nil {
		return
	}
	_, _ = connection.Write(framed)
	if request.Function == "CharLogin" {
		// C establishes a valid world position for typed actions. It is sent
		// on the same connection immediately after the login response.
		world := agentIntegrationServerPacket(100, "S", "C100|30|30|4|5")
		_, _ = connection.Write(world)
	}
}

func agentIntegrationServerPacket(id uint32, function string, fields ...string) []byte {
	encoded := make([]string, len(fields))
	for index, field := range fields {
		encoded[index] = namedproto.EncodeString([]byte(field))
	}
	raw, err := namedproto.RawMessage(id, function, encoded)
	if err != nil {
		return nil
	}
	packet, err := namedproto.EncodePacket(raw)
	if err != nil {
		return nil
	}
	return packet
}

func (game *agentIntegrationGame) address() string {
	return game.listener.Addr().String()
}

func (game *agentIntegrationGame) packetCount() int {
	game.mu.Lock()
	defer game.mu.Unlock()
	return len(game.packets)
}

func (game *agentIntegrationGame) packetAfter(index int, match func(agentIntegrationPacket) bool) (agentIntegrationPacket, bool) {
	game.mu.Lock()
	defer game.mu.Unlock()
	if index < 0 {
		index = 0
	}
	for _, packet := range game.packets[index:] {
		if match(packet) {
			packet.Packet = append([]byte(nil), packet.Packet...)
			return packet, true
		}
	}
	return agentIntegrationPacket{}, false
}

func (game *agentIntegrationGame) waitPacket(ctx context.Context, index int, match func(agentIntegrationPacket) bool) (agentIntegrationPacket, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if packet, ok := game.packetAfter(index, match); ok {
			return packet, nil
		}
		select {
		case <-ctx.Done():
			return agentIntegrationPacket{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (game *agentIntegrationGame) waitConnectionCount(ctx context.Context, count int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		game.mu.Lock()
		current := len(game.connections)
		game.mu.Unlock()
		if current >= count {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (game *agentIntegrationGame) connectionClosed(id int) bool {
	game.mu.Lock()
	done := game.closed[id]
	game.mu.Unlock()
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func (game *agentIntegrationGame) waitConnectionClosed(ctx context.Context, id int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	game.mu.Lock()
	done := game.closed[id]
	game.mu.Unlock()
	if done == nil {
		return fmt.Errorf("unknown fake connection %d", id)
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (game *agentIntegrationGame) close() {
	game.stopOnce.Do(func() {
		close(game.done)
		_ = game.listener.Close()
		game.mu.Lock()
		connections := make([]net.Conn, 0, len(game.connections))
		for _, connection := range game.connections {
			connections = append(connections, connection)
		}
		game.mu.Unlock()
		for _, connection := range connections {
			_ = connection.Close()
		}
		game.wg.Wait()
	})
}

type agentIntegrationFixture struct {
	game       *agentIntegrationGame
	handler    *Handler
	public     *httptest.Server
	directory  *httptest.Server
	private    *http.Server
	privateNet net.Listener
	socketPath string
	connector  *websession.Connector
}

func newAgentIntegrationFixture(t *testing.T) *agentIntegrationFixture {
	t.Helper()
	game := newAgentIntegrationGame(t)
	directory := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/servers" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(gameservers.Catalog{Servers: []gameservers.Server{{
			ID:              agentIntegrationServerID,
			Name:            "集成测试线",
			Address:         game.address(),
			UpstreamAddress: game.address(),
		}}})
	}))
	cfg := DefaultConfig()
	cfg.TCPUpstream = game.address()
	cfg.GatewayAPIURL = directory.URL
	cfg.PacketLimit = 64 * 1024
	cfg.MaxSessions = 8
	cfg.PollTimeout = 20 * time.Millisecond
	cfg.IdleTimeout = 2 * time.Second
	cfg.DialTimeout = time.Second
	handler, err := NewHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	public := httptest.NewServer(handler)
	// Unix socket paths have a small platform limit. Keep the test socket in
	// the repository's build directory instead of t.TempDir(), whose test
	// name prefix can exceed that limit.
	buildDir, err := filepath.Abs("../../build")
	if err != nil {
		handler.Close()
		directory.Close()
		public.Close()
		t.Fatal(err)
	}
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		handler.Close()
		directory.Close()
		public.Close()
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp(buildDir, "as-")
	if err != nil {
		handler.Close()
		directory.Close()
		public.Close()
		t.Fatal(err)
	}
	socketPath := filepath.Join(socketDir, "a.sock")
	privateNet, err := net.Listen("unix", socketPath)
	if err != nil {
		_ = os.RemoveAll(socketDir)
		handler.Close()
		directory.Close()
		public.Close()
		t.Fatal(err)
	}
	private := &http.Server{Handler: handler.InternalAgentHandler()}
	go func() { _ = private.Serve(privateNet) }()
	connector, err := websession.NewConnector(websession.Config{
		BaseURL:    public.URL,
		ServerID:   agentIntegrationServerID,
		SocketPath: socketPath,
	})
	if err != nil {
		_ = privateNet.Close()
		_ = private.Shutdown(context.Background())
		_ = os.RemoveAll(socketDir)
		handler.Close()
		directory.Close()
		public.Close()
		t.Fatal(err)
	}
	fixture := &agentIntegrationFixture{
		game: game, handler: handler, public: public, directory: directory,
		private: private, privateNet: privateNet, socketPath: socketPath, connector: connector,
	}
	t.Cleanup(func() {
		_ = connector.Close()
		_ = private.Shutdown(context.Background())
		_ = privateNet.Close()
		_ = os.RemoveAll(socketDir)
		public.Close()
		handler.Close()
		directory.Close()
	})
	return fixture
}

func (fixture *agentIntegrationFixture) sessionIDs() []string {
	fixture.handler.sessions.mu.RLock()
	ids := make([]string, 0, len(fixture.handler.sessions.sessions))
	for id := range fixture.handler.sessions.sessions {
		ids = append(ids, id)
	}
	fixture.handler.sessions.mu.RUnlock()
	sort.Strings(ids)
	return ids
}

func (fixture *agentIntegrationFixture) waitSessionCount(ctx context.Context, count int) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		ids := fixture.sessionIDs()
		if len(ids) >= count {
			return ids, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (fixture *agentIntegrationFixture) sessionByID(id string) (*tcpSession, error) {
	session, ok := fixture.handler.sessions.get(id)
	if !ok {
		return nil, fmt.Errorf("unknown Web session %q", id)
	}
	return session, nil
}

func agentIntegrationContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func agentIntegrationJSON(t *testing.T, method, endpoint string, body any, target any) int {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if target != nil {
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
	}
	return response.StatusCode
}

func agentIntegrationGeneration(t *testing.T, fixture *agentIntegrationFixture, sessionID string) uint64 {
	t.Helper()
	var response struct {
		Control struct {
			Generation uint64 `json:"generation"`
		} `json:"control"`
	}
	status := agentIntegrationJSON(t, http.MethodGet, fixture.public.URL+"/api/sessions/"+url.PathEscape(sessionID)+"/control", nil, &response)
	if status != http.StatusOK || response.Control.Generation == 0 {
		t.Fatalf("control status=%d response=%+v", status, response)
	}
	return response.Control.Generation
}

func agentIntegrationPostPacket(t *testing.T, fixture *agentIntegrationFixture, sessionID string, packet []byte) int {
	t.Helper()
	return agentIntegrationJSON(t, http.MethodPost, fixture.public.URL+"/api/sessions/"+url.PathEscape(sessionID)+"/send", map[string]string{
		"packet": base64.StdEncoding.EncodeToString(packet),
	}, nil)
}

func agentIntegrationPoll(t *testing.T, fixture *agentIntegrationFixture, sessionID string) []agentIntegrationPacket {
	t.Helper()
	endpoint := fixture.public.URL + "/api/sessions/" + url.PathEscape(sessionID) + "/events?timeout=1000"
	response, err := http.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("events status=%d body=%s", response.StatusCode, body)
	}
	var payload struct {
		Events []struct {
			Packet string `json:"packet"`
			Closed bool   `json:"closed"`
		} `json:"events"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	result := make([]agentIntegrationPacket, 0, len(payload.Events))
	for _, event := range payload.Events {
		if event.Packet == "" {
			continue
		}
		packet, err := base64.StdEncoding.DecodeString(event.Packet)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := namedproto.DecodePacket(packet)
		if err != nil {
			t.Fatal(err)
		}
		message, err := namedproto.ParseMessage(raw)
		if err != nil {
			t.Fatal(err)
		}
		result = append(result, agentIntegrationPacket{Packet: packet, Message: message})
	}
	return result
}

func agentIntegrationBrowserLogin(t *testing.T, fixture *agentIntegrationFixture) (string, *tcpSession) {
	t.Helper()
	var created struct {
		ID string `json:"id"`
	}
	status := agentIntegrationJSON(t, http.MethodPost, fixture.public.URL+"/api/sessions", map[string]string{"server_id": agentIntegrationServerID}, &created)
	if status != http.StatusCreated || created.ID == "" {
		t.Fatalf("browser session status=%d response=%+v", status, created)
	}
	steps := []struct {
		name   string
		packet []byte
		want   string
	}{
		{name: "ClientLogin", packet: webClientPacket(t, 1, "ClientLogin", "browser-account", "browser-password"), want: "ClientLogin"},
		{name: "CharList", packet: webClientPacket(t, 2, "CharList"), want: "CharList"},
		{name: "CharLogin", packet: webClientPacket(t, 3, "CharLogin", "Hero"), want: "CharLogin"},
	}
	for _, step := range steps {
		if status := agentIntegrationPostPacket(t, fixture, created.ID, step.packet); status != http.StatusAccepted {
			t.Fatalf("browser %s status=%d", step.name, status)
		}
		events := agentIntegrationPoll(t, fixture, created.ID)
		found := false
		for _, event := range events {
			if event.Message.Function == step.want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("browser %s response missing in %+v", step.name, events)
		}
	}
	session, err := fixture.sessionByID(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	return created.ID, session
}

func TestAgentIntegrationLoginCharacterAndFencing(t *testing.T) {
	fixture := newAgentIntegrationFixture(t)
	ctx, cancel := agentIntegrationContext(t)
	defer cancel()

	headless, err := fixture.connector.Login(ctx, aigame.Config{Address: agentIntegrationServerID}, aigame.Credentials{
		Account:  "int-account",
		Password: "integration-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	session, ok := headless.(*websession.Session)
	if !ok {
		t.Fatalf("connector returned %T, want *websession.Session", headless)
	}
	defer session.Close()

	characters, err := session.RefreshCharacters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(characters) != 1 || characters[0].Name != "Hero" {
		t.Fatalf("initial character list=%+v", characters)
	}
	if err := session.CreateCharacter(ctx, aigame.CharacterCreate{Name: "CreatedHero", Hometown: 1}); err != nil {
		t.Fatal(err)
	}
	characters, err = session.RefreshCharacters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(characters) != 2 || characters[1].Name != "CreatedHero" {
		t.Fatalf("created character list=%+v", characters)
	}
	if err := session.EnterCharacter(ctx, "CreatedHero"); err != nil {
		t.Fatal(err)
	}

	ids, err := fixture.waitSessionCount(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || fixture.game.packetCount() < 6 {
		t.Fatalf("login did not use one Web connection: sessions=%v packets=%d", ids, fixture.game.packetCount())
	}
	snapshot, err := session.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Account != "int-account" || snapshot.Character != "CreatedHero" || snapshot.Phase != aigame.PhaseWorld || snapshot.Position.Floor != 100 {
		t.Fatalf("agent observation=%+v", snapshot)
	}

	webID := ids[0]
	blockedStart := fixture.game.packetCount()
	blocked := webClientPacket(t, 90, "S", "blocked")
	status := agentIntegrationJSON(t, http.MethodPost, fixture.public.URL+"/api/sessions/"+url.PathEscape(webID)+"/send", map[string]string{
		"packet": base64.StdEncoding.EncodeToString(blocked),
	}, nil)
	if status != http.StatusConflict {
		t.Fatalf("raw browser write while Agent-owned status=%d", status)
	}
	if err := waitAgentIntegrationNoPacketGrowth(fixture.game, blockedStart, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	executeStart := fixture.game.packetCount()
	if err := session.ExecuteExpected(ctx, snapshot.Revision, aigame.Chat("hello", 0, 3)); err != nil {
		t.Fatal(err)
	}
	packet, err := fixture.game.waitPacket(ctx, executeStart, func(packet agentIntegrationPacket) bool {
		return packet.Connection == 1 && packet.Message.Function == "TK"
	})
	if err != nil {
		t.Fatalf("typed action did not reach original connection: %v", err)
	}
	if packet.Message.Function != "TK" {
		t.Fatalf("typed action packet=%+v", packet.Message)
	}

	leaseDone := session.LeaseDone()
	takeoverURL := fixture.public.URL + "/api/sessions/" + url.PathEscape(webID) + "/takeover"
	if status := agentIntegrationJSON(t, http.MethodPost, takeoverURL, map[string]string{"reason": "integration takeover"}, nil); status != http.StatusOK {
		t.Fatalf("takeover status=%d", status)
	}
	select {
	case <-leaseDone:
	case <-time.After(2 * time.Second):
		t.Fatal("LeaseDone did not close after Web takeover")
	}
	if _, err := session.Observe(ctx); !errors.Is(err, websession.ErrLeaseUnavailable) {
		t.Fatalf("observe after takeover=%v, want lease unavailable", err)
	}
	if err := session.ExecuteExpected(ctx, snapshot.Revision, aigame.Chat("late", 0, 3)); !errors.Is(err, websession.ErrLeaseUnavailable) {
		t.Fatalf("execute after takeover=%v, want lease unavailable", err)
	}
	if err := waitAgentIntegrationNoPacketGrowth(fixture.game, executeStart+1, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestAgentIntegrationAttachReusesBrowserSessionAndCloseIsolated(t *testing.T) {
	fixture := newAgentIntegrationFixture(t)
	ctx, cancel := agentIntegrationContext(t)
	defer cancel()

	browserID, browserSession := agentIntegrationBrowserLogin(t, fixture)
	if err := fixture.game.waitConnectionCount(ctx, 1); err != nil {
		t.Fatal(err)
	}
	attachClient, err := websession.New(websession.Config{
		BaseURL:    fixture.public.URL,
		ServerID:   agentIntegrationServerID,
		SocketPath: fixture.socketPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer attachClient.Close()

	generation := agentIntegrationGeneration(t, fixture, browserID)
	identity := websession.Identity{
		AccountID:     "browser-account",
		CharacterID:   "browser-account:0",
		CharacterName: "Hero",
		ServerID:      agentIntegrationServerID,
	}
	bound, err := attachClient.Bind(ctx, websession.AttachRequest{
		SessionID: browserID, Generation: generation, Identity: identity,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()
	browserSession.enqueue(packetEvent{packet: []byte("browser-event\n")})
	browserSession.mu.Lock()
	queued := len(browserSession.events)
	browserSession.mu.Unlock()
	observed, err := bound.Observe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Account != "browser-account" || observed.Character != "Hero" {
		t.Fatalf("attached observation=%+v", observed)
	}
	browserSession.mu.Lock()
	remaining := len(browserSession.events)
	browserSession.mu.Unlock()
	if remaining != queued {
		t.Fatalf("agent observe consumed browser events: before=%d after=%d", queued, remaining)
	}
	if fixture.game.packetCount() != 3 {
		t.Fatalf("Client.Attach opened or logged in another TCP stream: packets=%d", fixture.game.packetCount())
	}
	if err := bound.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-bound.LeaseDone():
	case <-time.After(time.Second):
		t.Fatal("BoundSession.Close did not close LeaseDone")
	}
	if state := browserSession.gate.State(); string(state.Mode) != "manual" {
		t.Fatalf("browser session control after detach=%+v", state)
	}

	headless, err := fixture.connector.Login(ctx, aigame.Config{Address: agentIntegrationServerID}, aigame.Credentials{
		Account:  "ai-account",
		Password: "ai-password",
	})
	if err != nil {
		t.Fatal(err)
	}
	aiSession, ok := headless.(*websession.Session)
	if !ok {
		t.Fatalf("connector returned %T, want *websession.Session", headless)
	}
	if err := aiSession.EnterCharacter(ctx, "Hero"); err != nil {
		t.Fatal(err)
	}
	ids, err := fixture.waitSessionCount(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	var aiID string
	for _, id := range ids {
		if id != browserID {
			aiID = id
		}
	}
	if aiID == "" {
		t.Fatalf("could not identify AI Web session among %v", ids)
	}
	aiConnID := 2
	if err := aiSession.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.game.waitConnectionClosed(ctx, aiConnID); err != nil {
		t.Fatalf("AI Session.Close did not close its own connection: %v", err)
	}
	if fixture.game.connectionClosed(1) {
		t.Fatal("AI Session.Close closed the independent browser connection")
	}
	if _, err := fixture.sessionByID(browserID); err != nil {
		t.Fatalf("browser Web session was removed by AI close: %v", err)
	}
	if _, err := fixture.sessionByID(aiID); err == nil {
		t.Fatal("AI Web session remained after Session.Close")
	}
}

func waitAgentIntegrationNoPacketGrowth(game *agentIntegrationGame, start int, duration time.Duration) error {
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if current := game.packetCount(); current != start {
			return fmt.Errorf("unexpected upstream packet growth: started at %d, now %d", start, current)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}
