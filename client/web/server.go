// Command web is the HTTP side of the browser port of sa_2903.exe.
//
// A browser is not allowed to open the legacy game's raw TCP socket.  This
// process therefore owns one TCP connection per browser session and exposes a
// deliberately small HTTP long-polling API.  Packets are never decoded or
// rewritten here: the page implements the machine-code protocol and this
// process only moves newline-delimited bytes between HTTP and TCP.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

// The executable is intentionally self-contained: index.html is the only
// runtime asset beside this forwarding service.
//
//go:embed index.html
var page []byte

// Keep the worker beside the self-contained page so `go run ./client/web`
// and the production binary expose the exact same cache/update behavior.
// The worker is intentionally not bundled into index.html: browsers need a
// stable same-origin /sw.js URL in order to install it.
//
//go:embed sw.js
var serviceWorker []byte

// Installed mobile shortcuts must not force a particular orientation. The
// page keeps the executable's 640x480 surface and scales it to the limiting
// viewport axis, so both portrait and landscape remain playable.
var webManifest = []byte(`{
  "name": "StoneAge 2.5",
  "short_name": "StoneAge",
  "id": "/",
  "start_url": "/",
  "scope": "/",
  "display": "fullscreen",
  "orientation": "any",
  "background_color": "#000000",
  "theme_color": "#101820"
}`)

// A local checkout does not run the object-storage publisher, but the page
// still probes the publication marker so the same Service Worker update path
// is exercised during development.  Keep a valid, no-cache marker on the Web
// origin instead of returning a noisy 404; a CDN-configured page resolves the
// same URL against its public static root and never uses this fallback.
var localAssetVersion = []byte(`{
  "revision": "local-dev",
  "delta_known": false,
  "changed_all": true,
  "changed_objects": [],
  "removed_objects": []
}`)

const (
	// The browser client is intended to be usable from another device on the
	// same LAN.  Keep the game gateway itself bound to localhost below; only
	// this HTTP bridge is exposed, and it can still be narrowed with
	// STONEAGE_WEB_LISTEN when needed.
	defaultListenAddress   = "0.0.0.0:8088"
	defaultTCPUpstream     = "127.0.0.1:9065"
	defaultAssetsDirectory = "assets/original"
	// The original client keeps map DAT/MAP files beside sa_2903.exe.  They
	// are read-only source data for the native 54x54 auto-map window; serving
	// them separately keeps the generated bitmap manifest small and lets the
	// browser use the same floor data as the Windows client.
	defaultMapDirectory = "runtime/legacy-client/map"
	// The preserved sa_2903 data keeps the original WAVs beside the executable
	// rather than inside the generated bitmap pack.  Serve that read-only
	// directory through /audio/ so the browser can use the same BGM/SE files
	// without copying 38 MB of binary data into client/web/assets.
	defaultAudioDirectory = "runtime/legacy-client/data"
	// NPC create files are read-only metadata used to show fixed server NPCs
	// before a 2.5 C visibility delta reaches the browser.  Interaction still
	// goes through the real TK/WN packets; this endpoint never emulates NPC
	// state or answers a quest locally.
	defaultNPCDirectory = "runtime/legacy-server/gmsv/data/npc"
	defaultPacketLimit  = 4 * 1024 * 1024
	defaultMaxSessions  = 64
	defaultPollTimeout  = 25 * time.Second
	defaultIdleTimeout  = 10 * time.Minute
	defaultDialTimeout  = 5 * time.Second
	// An in-place logout is represented by the stock 2.5 disconnect path,
	// not by a newer CharLogout flag.  Give GMSV a short window to observe the
	// TCP FIN, run CHAR_logout(), and close its side before the browser is told
	// that the logout has completed.
	defaultGracefulCloseTimeout = 3 * time.Second
	// An unpatched 2.5 _CONNECT_endOne() closes the client socket immediately
	// after handing CHAR_logout()'s save to SAAC.  Its peer close proves that
	// GMSV accepted the native in-place logout, but not that the asynchronous
	// SAAC write reached the character file.  Keep this bridge-side drain for
	// compatibility with those original servers; the repository's durable EOF
	// patch additionally keeps GMSV open until the SAAC acknowledgement.
	defaultLegacyLogoutSaveDrain = 500 * time.Millisecond
	maxQueuedEvents              = 256
	maxQueuedBytes               = 16 * 1024 * 1024
)

// Config controls both the HTTP endpoint and its fixed TCP destination.  The
// destination is configured by the operator, never by a browser request; this
// avoids turning the small bridge into an SSRF service.
type Config struct {
	ListenAddress   string
	TCPUpstream     string
	AssetsDirectory string
	MapDirectory    string
	AudioDirectory  string
	NPCDirectory    string
	PacketLimit     int
	MaxSessions     int
	PollTimeout     time.Duration
	IdleTimeout     time.Duration
	DialTimeout     time.Duration
	AllowedOrigin   string
	// CDNBaseURL is the public static root which contains assets/, maps/ and
	// audio/.  It changes only browser static-resource URLs; the account, NPC
	// and game-session APIs always remain on this process.
	CDNBaseURL string
	// OSS describes the stable object-storage origin used by deployment to
	// hold assets/, maps/ and audio/. Runtime browser URLs still prefer the
	// CDN base above; this process never uploads to OSS or receives its keys.
	OSS OSSConfig
}

type OSSConfig struct {
	Provider string
	Endpoint string
	Region   string
	Bucket   string
	Prefix   string
}

func DefaultConfig() Config {
	return Config{
		ListenAddress:   defaultListenAddress,
		TCPUpstream:     defaultTCPUpstream,
		AssetsDirectory: defaultAssetsDirectory,
		MapDirectory:    defaultMapDirectory,
		AudioDirectory:  defaultAudioDirectory,
		NPCDirectory:    defaultNPCDirectory,
		PacketLimit:     defaultPacketLimit,
		MaxSessions:     defaultMaxSessions,
		PollTimeout:     defaultPollTimeout,
		IdleTimeout:     defaultIdleTimeout,
		DialTimeout:     defaultDialTimeout,
		OSS: OSSConfig{
			Provider: "aliyun-oss",
			Prefix:   "stoneage",
		},
	}
}

func applyEnvironmentConfig(cfg Config) Config {
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_LISTEN")); value != "" {
		cfg.ListenAddress = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_TCP_UPSTREAM")); value != "" {
		cfg.TCPUpstream = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_ASSETS")); value != "" {
		cfg.AssetsDirectory = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_MAPS")); value != "" {
		cfg.MapDirectory = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_AUDIO")); value != "" {
		cfg.AudioDirectory = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_NPCS")); value != "" {
		cfg.NPCDirectory = value
	}
	if value := positiveIntEnv("STONEAGE_WEB_PACKET_LIMIT"); value > 0 {
		cfg.PacketLimit = value
	}
	if value := positiveIntEnv("STONEAGE_WEB_MAX_SESSIONS"); value > 0 {
		cfg.MaxSessions = value
	}
	if value := positiveDurationEnv("STONEAGE_WEB_POLL_TIMEOUT"); value > 0 {
		cfg.PollTimeout = value
	}
	if value := positiveDurationEnv("STONEAGE_WEB_IDLE_TIMEOUT"); value > 0 {
		cfg.IdleTimeout = value
	}
	if value := positiveDurationEnv("STONEAGE_WEB_DIAL_TIMEOUT"); value > 0 {
		cfg.DialTimeout = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_ALLOWED_ORIGIN")); value != "" {
		cfg.AllowedOrigin = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_CDN_BASE_URL")); value != "" {
		cfg.CDNBaseURL = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_OSS_ENDPOINT")); value != "" {
		cfg.OSS.Endpoint = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_OSS_REGION")); value != "" {
		cfg.OSS.Region = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_OSS_BUCKET")); value != "" {
		cfg.OSS.Bucket = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_OSS_PREFIX")); value != "" {
		cfg.OSS.Prefix = value
	}
	return cfg
}

func configFromEnvironment() Config { return applyEnvironmentConfig(DefaultConfig()) }

func normalizeCDNBaseURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	// This value is interpolated into CSS, HTML attributes and JavaScript
	// string/template literals.  Require a plain URL rather than trying to
	// escape one attacker-controlled spelling three different ways.
	if strings.ContainsAny(value, "\"'`<>\\\r\n\t ") {
		return "", errors.New("CDN base URL contains unsafe characters")
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("CDN base URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	return strings.TrimRight(value, "/"), nil
}

func pageWithCDNBase(source []byte, baseURL string) []byte {
	if baseURL == "" {
		return source
	}
	result := append([]byte(nil), source...)
	// Rewrite the origin-relative static roots in two passes.  Replacing them
	// one at a time can rewrite the CDN root that was inserted by an earlier
	// pass when the root itself contains another tree name (for example
	// https://cdn.example/game/maps).  Keep the markers deliberately opaque,
	// then expand them only after all source references have been isolated.
	const (
		assetsMarker = "__STONEAGE_STATIC_ASSETS_ROOT__"
		mapsMarker   = "__STONEAGE_STATIC_MAPS_ROOT__"
		audioMarker  = "__STONEAGE_STATIC_AUDIO_ROOT__"
	)
	markers := []struct {
		prefix string
		marker string
	}{
		{prefix: "/assets/", marker: assetsMarker},
		{prefix: "/maps/", marker: mapsMarker},
		{prefix: "/audio/", marker: audioMarker},
	}
	for _, item := range markers {
		result = bytes.ReplaceAll(result, []byte(item.prefix), []byte(item.marker))
	}
	for _, item := range markers {
		result = bytes.ReplaceAll(result, []byte(item.marker), []byte(baseURL+item.prefix))
	}
	return result
}

var autoMapFilenamePattern = regexp.MustCompile(`^([0-9]+)\.(?i:dat|map)$`)

// autoMapFilesForDirectory returns the exact case-sensitive filenames which
// can be requested from the browser for each numeric floor.  The legacy map
// tree came from Windows and contains both .DAT/.MAP and lower-case variants;
// a CDN/object store serves those names case-sensitively, so the browser must
// not guess one spelling and then retry a long chain of 404s.  Directory
// entries and symlinks are deliberately excluded from the public index.
func autoMapFilesForDirectory(directory string) map[string][]string {
	files := make(map[string][]string)
	if strings.TrimSpace(directory) == "" {
		return files
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return files
	}
	for _, entry := range entries {
		name := entry.Name()
		match := autoMapFilenamePattern.FindStringSubmatch(name)
		if len(match) != 2 || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := os.Lstat(filepath.Join(directory, name))
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		floor, err := strconv.Atoi(match[1])
		if err != nil || floor < 0 {
			continue
		}
		key := strconv.Itoa(floor)
		files[key] = append(files[key], name)
	}
	for key, names := range files {
		sort.Slice(names, func(left, right int) bool {
			leftDAT := strings.EqualFold(filepath.Ext(names[left]), ".dat")
			rightDAT := strings.EqualFold(filepath.Ext(names[right]), ".dat")
			if leftDAT != rightDAT {
				return leftDAT
			}
			leftLower, rightLower := strings.ToLower(names[left]), strings.ToLower(names[right])
			if leftLower != rightLower {
				return leftLower < rightLower
			}
			return names[left] < names[right]
		})
		files[key] = names
	}
	return files
}

func pageWithAutoMapFiles(source []byte, files map[string][]string) []byte {
	if files == nil {
		files = make(map[string][]string)
	}
	payload, err := json.Marshal(files)
	if err != nil || len(payload) == 0 {
		payload = []byte("{}")
	}
	result := append([]byte(nil), source...)
	const emptyDeclaration = "const AUTO_MAP_FILES={};"
	declaration := append([]byte("const AUTO_MAP_FILES="), payload...)
	declaration = append(declaration, ';')
	return bytes.Replace(result, []byte(emptyDeclaration), declaration, 1)
}

func positiveIntEnv(name string) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func positiveDurationEnv(name string) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0
	}
	return duration
}

type packetEvent struct {
	packet []byte
	closed bool
	err    string
}

type tcpSession struct {
	id     string
	conn   net.Conn
	limit  int
	closed chan struct{}

	mu           sync.Mutex
	writeMu      sync.Mutex
	events       []packetEvent
	eventBytes   int
	lastActivity time.Time
	closeOnce    sync.Once
	notify       chan struct{}
	polling      bool
	closing      bool
}

func newTCPSession(id string, conn net.Conn, packetLimit int) *tcpSession {
	return &tcpSession{
		id:           id,
		conn:         conn,
		limit:        packetLimit,
		closed:       make(chan struct{}),
		notify:       make(chan struct{}, 1),
		lastActivity: time.Now(),
	}
}

func (session *tcpSession) touchLocked() {
	session.lastActivity = time.Now()
}

func (session *tcpSession) touch() {
	session.mu.Lock()
	session.touchLocked()
	session.mu.Unlock()
}

func (session *tcpSession) signal() {
	select {
	case session.notify <- struct{}{}:
	default:
	}
}

func (session *tcpSession) enqueue(event packetEvent) {
	session.mu.Lock()
	if len(session.events) >= maxQueuedEvents || session.eventBytes+len(event.packet) > maxQueuedBytes {
		session.mu.Unlock()
		session.finish("HTTP event queue overflow")
		return
	}
	session.events = append(session.events, event)
	session.eventBytes += len(event.packet)
	session.touchLocked()
	session.mu.Unlock()
	session.signal()
}

func (session *tcpSession) readLoop() {
	reader := bufio.NewReaderSize(session.conn, 64*1024)
	for {
		packet, err := readDelimitedPacket(reader, session.limit)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				session.finish("")
			} else {
				session.finish(err.Error())
			}
			return
		}
		session.enqueue(packetEvent{packet: packet})
	}
}

func readDelimitedPacket(reader *bufio.Reader, limit int) ([]byte, error) {
	packet := make([]byte, 0, minInt(limit, 64*1024))
	for {
		fragment, err := reader.ReadSlice('\n')
		packet = append(packet, fragment...)
		if len(packet) > limit {
			return nil, fmt.Errorf("TCP packet exceeds %d bytes", limit)
		}
		if err == nil {
			return packet, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, err
	}
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func (session *tcpSession) finish(reason string) {
	session.closeOnce.Do(func() {
		session.mu.Lock()
		session.touchLocked()
		session.events = append(session.events, packetEvent{closed: true, err: reason})
		session.mu.Unlock()
		close(session.closed)
		_ = session.conn.Close()
		session.signal()
	})
}

func (session *tcpSession) close() {
	session.finish("")
}

// closeGracefully half-closes the browser-to-GMSV direction and waits for
// GMSV to close the other half.  The stock 2.5 server treats that EOF as the
// native close-button path and saves the character without rewriting its
// floor/x/y to the record point.  Waiting for the peer close removes the
// rapid-relogin race that exists when net.Conn.Close returns before GMSV has
// even entered _CONNECT_endOne().
func (session *tcpSession) closeGracefully(timeout time.Duration) error {
	if timeout <= 0 {
		session.close()
		return nil
	}

	// Serialize the FIN with all preceding packet writes.  Once closing is
	// visible, a request that already resolved this session can no longer put
	// bytes on the half-closed connection.
	session.writeMu.Lock()
	if session.isClosed() {
		session.writeMu.Unlock()
		return nil
	}
	session.mu.Lock()
	session.closing = true
	session.touchLocked()
	session.mu.Unlock()
	writer, ok := session.conn.(interface{ CloseWrite() error })
	if !ok {
		session.writeMu.Unlock()
		session.close()
		return errors.New("upstream connection cannot be half-closed")
	}
	err := writer.CloseWrite()
	session.writeMu.Unlock()
	if err != nil {
		session.finish(err.Error())
		return err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-session.closed:
		// Stock 2.5 has no response packet for the EOF-based in-place logout.
		// Unpatched servers close before saacproto_ACCharSave_recv() finishes,
		// so this bounded bridge-side fallback keeps their wire schema intact
		// instead of introducing the later CharLogout(Flg) field.
		drain := time.NewTimer(defaultLegacyLogoutSaveDrain)
		defer drain.Stop()
		<-drain.C
		return nil
	case <-timer.C:
		// A broken or non-2.5 upstream must not hold an HTTP handler forever.
		err := errors.New("graceful disconnect timeout")
		session.finish(err.Error())
		return err
	}
}

func (session *tcpSession) isClosed() bool {
	select {
	case <-session.closed:
		return true
	default:
		return false
	}
}

func (session *tcpSession) write(packet []byte) error {
	if len(packet) == 0 || packet[len(packet)-1] != '\n' {
		return errors.New("packet must contain a trailing newline")
	}
	if len(packet) > session.limit {
		return fmt.Errorf("packet exceeds %d bytes", session.limit)
	}
	if session.isClosed() {
		return net.ErrClosed
	}
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	if session.isClosed() {
		return net.ErrClosed
	}
	session.mu.Lock()
	closing := session.closing
	session.mu.Unlock()
	if closing {
		return net.ErrClosed
	}
	remaining := packet
	for len(remaining) > 0 {
		written, err := session.conn.Write(remaining)
		if err != nil {
			session.finish(err.Error())
			return err
		}
		if written == 0 {
			session.finish("TCP write made no progress")
			return io.ErrShortWrite
		}
		remaining = remaining[written:]
	}
	session.touch()
	return nil
}

// poll returns all currently queued packets, waiting at most timeout when the
// queue is empty.  Only one outstanding poll is allowed for a session so that
// event ordering remains exactly the TCP ordering seen by the original client.
func (session *tcpSession) poll(timeout time.Duration) ([]packetEvent, error) {
	return session.pollContext(context.Background(), timeout)
}

// pollContext is the request-aware form used by the HTTP handler.  A browser
// tab can disappear while /events is waiting; honoring that cancellation is
// important because otherwise the bridge keeps the TCP socket (and its
// transient legacy-account lock) alive until the idle watchdog fires.
func (session *tcpSession) pollContext(ctx context.Context, timeout time.Duration) ([]packetEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	session.mu.Lock()
	if session.polling {
		session.mu.Unlock()
		return nil, errors.New("another events poll is already pending")
	}
	session.polling = true
	session.touchLocked()
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		session.polling = false
		session.mu.Unlock()
	}()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		session.mu.Lock()
		if len(session.events) > 0 {
			events := append([]packetEvent(nil), session.events...)
			session.events = nil
			session.eventBytes = 0
			session.touchLocked()
			session.mu.Unlock()
			return events, nil
		}
		closed := session.isClosed()
		session.mu.Unlock()
		if closed {
			return []packetEvent{{closed: true}}, nil
		}
		select {
		case <-session.notify:
			continue
		case <-session.closed:
			continue
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, nil
		}
	}
}

type sessionStore struct {
	mu          sync.RWMutex
	sessions    map[string]*tcpSession
	maxSessions int
}

func newSessionStore(maxSessions int) *sessionStore {
	if maxSessions <= 0 {
		maxSessions = defaultMaxSessions
	}
	return &sessionStore{sessions: make(map[string]*tcpSession), maxSessions: maxSessions}
}

func (store *sessionStore) add(session *tcpSession) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.sessions) >= store.maxSessions {
		return errors.New("maximum browser sessions reached")
	}
	store.sessions[session.id] = session
	return nil
}

func (store *sessionStore) hasCapacity() bool {
	store.mu.RLock()
	available := len(store.sessions) < store.maxSessions
	store.mu.RUnlock()
	return available
}

func (store *sessionStore) get(id string) (*tcpSession, bool) {
	store.mu.RLock()
	session, ok := store.sessions[id]
	store.mu.RUnlock()
	return session, ok
}

// removeIf drops a session only when the map still points at expected.  A
// browser can reconnect while the old read loop is unwinding; the old loop
// must never remove the replacement session that happens to reuse its id
// (ids are random today, but keeping this identity check makes the ownership
// contract explicit and race-safe).
func (store *sessionStore) removeIf(id string, expected *tcpSession) bool {
	store.mu.Lock()
	current, ok := store.sessions[id]
	if ok && current == expected {
		delete(store.sessions, id)
	}
	store.mu.Unlock()
	return ok && current == expected
}

func (store *sessionStore) delete(id string) {
	store.mu.Lock()
	session := store.sessions[id]
	delete(store.sessions, id)
	store.mu.Unlock()
	if session != nil {
		session.close()
	}
}

func (store *sessionStore) deleteGracefully(id string, timeout time.Duration) error {
	store.mu.Lock()
	session := store.sessions[id]
	delete(store.sessions, id)
	store.mu.Unlock()
	if session != nil {
		return session.closeGracefully(timeout)
	}
	return nil
}

func (store *sessionStore) closeIdle(idle time.Duration) {
	if idle <= 0 {
		return
	}
	now := time.Now()
	store.mu.RLock()
	type staleSession struct {
		id      string
		session *tcpSession
	}
	stale := make([]staleSession, 0)
	for id, session := range store.sessions {
		session.mu.Lock()
		last := session.lastActivity
		session.mu.Unlock()
		if now.Sub(last) > idle {
			stale = append(stale, staleSession{id: id, session: session})
		}
	}
	store.mu.RUnlock()
	for _, item := range stale {
		if store.removeIf(item.id, item.session) {
			item.session.finish("session idle timeout")
		}
	}
}

func (store *sessionStore) closeAll() {
	store.mu.Lock()
	sessions := make([]*tcpSession, 0, len(store.sessions))
	for id, session := range store.sessions {
		delete(store.sessions, id)
		sessions = append(sessions, session)
	}
	store.mu.Unlock()
	for _, session := range sessions {
		session.close()
	}
}

type Handler struct {
	config   Config
	sessions *sessionStore
	page     []byte
	assets   http.Handler
	maps     http.Handler
	audio    http.Handler
	npcDir   string
	npcMu    sync.RWMutex
	npcData  map[int][]npcMetadata
	npcErr   error
	npcDone  bool
	stop     chan struct{}
	stopOnce sync.Once
}

// npcMetadata is deliberately a description, not a second NPC protocol.
// The browser may use it to draw a fixed NPC and guide a player toward it,
// but the server remains authoritative for facing, dialogue, quests and WN
// selections.
type npcMetadata struct {
	ID        int64  `json:"id"`
	Floor     int    `json:"floor"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Direction int    `json:"direction"`
	Graphic   int    `json:"graphic"`
	Name      string `json:"name"`
	Source    string `json:"source,omitempty"`
	// Template/Interaction are read-only hints extracted from the same
	// generated create record that spawned the authoritative C object.  They
	// do not answer an NPC request; they only let the browser select the
	// native L (LOOK) or TK (TALK) entry point.  The 2.5 server remains the
	// authority and may still reject either packet.
	Template         string `json:"template,omitempty"`
	Interaction      string `json:"interaction,omitempty"`
	InteractionRange int    `json:"interactionRange,omitempty"`
}

type npcMetadataResponse struct {
	Floor int           `json:"floor"`
	NPCs  []npcMetadata `json:"npcs"`
}

func legacyNPCTextQuality(value string) int {
	score := 0
	for _, character := range value {
		switch {
		case character == '�':
			score -= 40
		case character < 32 && character != '\t' && character != '\n' && character != '\r':
			score -= 20
		case character >= 0xe000 && character <= 0xf8ff:
			score -= 16
		case character >= 0x3040 && character <= 0x30ff,
			character >= 0x31f0 && character <= 0x31ff:
			score -= 4
		case character >= 0x00a0 && character <= 0x024f:
			score -= 3
		case character >= 0x3400 && character <= 0x4dbf,
			character >= 0x4e00 && character <= 0x9fff:
			score += 2
		}
	}
	return score
}

func decodeLegacyTextSegment(data []byte) string {
	// The deployed data tree mixes generated CP936 fields with a few older
	// Big5 records.  In particular chatroom/bus.create stores
	// "長毛象公車" as Big5; GBK rejects those bytes, while the normal NPC
	// names in the same tree are CP936.  Decode one line/value at a time so a
	// malformed Japanese comment cannot force the entire file to Big5.
	simplifiedBytes, simplifiedErr := simplifiedchinese.GBK.NewDecoder().Bytes(data)
	traditionalBytes, traditionalErr := traditionalchinese.Big5.NewDecoder().Bytes(data)
	simplified := string(bytes.ToValidUTF8(simplifiedBytes, []byte("�")))
	traditional := string(bytes.ToValidUTF8(traditionalBytes, []byte("�")))
	// Keep GBK on a tie: it is the authoritative encoding for the generated
	// 2.5 NPC tree.  Big5 wins only when it is valid and materially cleaner.
	if traditionalErr == nil && (simplifiedErr != nil || legacyNPCTextQuality(traditional) > legacyNPCTextQuality(simplified)+4) {
		return traditional
	}
	if simplifiedErr == nil {
		return simplified
	}
	if traditionalErr == nil {
		return traditional
	}
	// A few local fixtures are already UTF-8.  Keeping valid source text on
	// two decoder failures preserves those development trees.
	return string(bytes.ToValidUTF8(data, []byte("�")))
}

func decodeLegacyNPCText(data []byte) string {
	// A .create file may contain several legacy encodings at once (comments
	// from the Japanese client, CP936 generated fields and Big5 bus names).
	// Selecting one decoder for the whole file lets an unrelated comment make
	// every NPC name mojibake.  Syntax is ASCII, so decoding each physical line
	// independently is safe and leaves braces/keys unchanged.
	if !bytes.Contains(data, []byte{'\n'}) {
		return decodeLegacyTextSegment(data)
	}
	lines := bytes.Split(data, []byte{'\n'})
	for index, line := range lines {
		lines[index] = []byte(decodeLegacyTextSegment(line))
	}
	return string(bytes.Join(lines, []byte{'\n'}))
}

func parseNPCInteger(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	// Create files occasionally carry an inline comment after a value.
	if index := strings.IndexAny(value, "#;"); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	parsed, err := strconv.Atoi(value)
	return parsed, err == nil
}

func parseNPCBornCorner(value string) (x, y int, fixed bool) {
	parts := strings.Split(strings.TrimSpace(value), ",")
	if len(parts) != 4 {
		return 0, 0, false
	}
	values := make([]int, len(parts))
	for index, part := range parts {
		parsed, ok := parseNPCInteger(part)
		if !ok {
			return 0, 0, false
		}
		values[index] = parsed
	}
	if values[0] != values[2] || values[1] != values[3] {
		// Randomly spawned enemies/NPCs cannot be represented by one stable
		// browser coordinate, so never invent a location for them.
		return 0, 0, false
	}
	return values[0], values[1], true
}

// Most town create files use borncenter=x,y,1,1 rather than the older
// borncorner=x1,y1,x2,y2 spelling.  A 1×1 borncenter is the same fixed cell;
// larger centres describe a random spawn area and must stay out of the
// deterministic browser metadata (the live C packet remains authoritative).
func parseNPCBornCenter(value string) (x, y int, fixed bool) {
	parts := strings.Split(strings.TrimSpace(value), ",")
	if len(parts) != 4 {
		return 0, 0, false
	}
	values := make([]int, len(parts))
	for index, part := range parts {
		parsed, ok := parseNPCInteger(part)
		if !ok {
			return 0, 0, false
		}
		values[index] = parsed
	}
	if values[2] != 1 || values[3] != 1 {
		return 0, 0, false
	}
	return values[0], values[1], true
}

func stableNPCMetadataID(source string, floor, x, y, graphic int, name string) int64 {
	hash := fnv.New32a()
	_, _ = io.WriteString(hash, source)
	_, _ = io.WriteString(hash, fmt.Sprintf("|%d|%d|%d|%d|%s", floor, x, y, graphic, name))
	// Keep all metadata IDs negative so a real CHAR object id can never be
	// mistaken for a static record when C/CD deltas arrive.
	return -int64(hash.Sum32()&0x3fffffff) - 1
}

// npcUsesLookInteraction mirrors the 2.5 npctemplate.c table.  These
// function sets register a CHAR_LOOKEDFUNC and therefore must be entered with
// lssproto_L_recv (the original client calls lookAtAround), not with a
// P|hi TK packet.  Keep the list deliberately explicit: many other NPCs have
// a WINDOWTALKEDFUNC but are still entered through their ordinary TALKEDFUNC.
func npcUsesLookInteraction(template string) bool {
	switch strings.ToLower(strings.TrimSpace(template)) {
	case "door", "npcgen_door", "npcgen_passdoor", "npcgen_titledoor",
		"msg", "npcgen_msg", "npcgen_msg2",
		"dengon", "npcgen_dengon", "fmdengon", "npcgen_fmdengon",
		"windowman", "windowhealer", "npcgen_winhealer", "duelranking", "signboard",
		"npcgen_signboard", "familyman", "bankman", "riderman", "fmletter":
		return true
	default:
		return false
	}
}

func npcTemplateFromEnemy(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if index := strings.IndexByte(value, '|'); index >= 0 {
		value = value[:index]
	}
	return strings.TrimSpace(value)
}

// npcInteractionRange exposes the distance enforced by the native healer
// callback.  Its final create-record argument is the range; native code uses
// one when the argument is absent or zero.  Other NPC templates do not have
// this interaction-specific hint.
func npcInteractionRange(template, enemy string) int {
	switch strings.ToLower(strings.TrimSpace(template)) {
	case "windowhealer", "npcgen_winhealer":
		interactionRange := 1
		arguments := strings.Split(enemy, "|")
		if len(arguments) > 4 {
			if parsed, ok := parseNPCInteger(arguments[4]); ok && parsed != 0 {
				interactionRange = parsed
			}
		}
		return interactionRange
	default:
		return 0
	}
}

func parseNPCCreateFile(path, source string, result map[int]map[string]npcMetadata) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := decodeLegacyNPCText(data)
	fields := make(map[string]string)
	flush := func() {
		floor, floorOK := parseNPCInteger(fields["floorid"])
		x, y, fixed := parseNPCBornCorner(fields["borncorner"])
		if !fixed {
			x, y, fixed = parseNPCBornCenter(fields["borncenter"])
		}
		graphic, graphicOK := parseNPCInteger(fields["graphicname"])
		if !floorOK || !fixed || !graphicOK || floor < 0 || x < 0 || y < 0 {
			fields = make(map[string]string)
			return
		}
		name := strings.TrimSpace(fields["name"])
		name = strings.ReplaceAll(name, `\n`, "\n")
		name = strings.ReplaceAll(name, `\t`, "\t")
		template := npcTemplateFromEnemy(fields["enemy"])
		interaction := "talk"
		if npcUsesLookInteraction(template) {
			interaction = "look"
		}
		interactionRange := npcInteractionRange(template, fields["enemy"])
		// Event/NPC records are all talkable from the browser's point of view;
		// the exact server subtype arrives later in the C object packet.
		item := npcMetadata{ID: stableNPCMetadataID(source, floor, x, y, graphic, name), Floor: floor, X: x, Y: y, Direction: 0, Graphic: graphic, Name: name, Source: source, Template: template, Interaction: interaction, InteractionRange: interactionRange}
		if direction, ok := parseNPCInteger(fields["dir"]); ok {
			item.Direction = direction
		}
		if result[floor] == nil {
			result[floor] = make(map[string]npcMetadata)
		}
		key := fmt.Sprintf("%d,%d,%d,%s", x, y, graphic, name)
		if _, exists := result[floor][key]; !exists {
			result[floor][key] = item
		}
		fields = make(map[string]string)
	}

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "{" {
			fields = make(map[string]string)
			continue
		}
		if line == "}" {
			flush()
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	flush()
	return nil
}

func loadNPCMetadata(root string) (map[int][]npcMetadata, error) {
	result := make(map[int]map[string]npcMetadata)
	if strings.TrimSpace(root) == "" {
		return map[int][]npcMetadata{}, nil
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".create") {
			return nil
		}
		relative, relativeErr := filepath.Rel(root, path)
		if relativeErr != nil {
			relative = entry.Name()
		}
		return parseNPCCreateFile(path, filepath.ToSlash(relative), result)
	})
	if err != nil {
		return nil, err
	}
	flattened := make(map[int][]npcMetadata, len(result))
	for floor, records := range result {
		items := make([]npcMetadata, 0, len(records))
		for _, item := range records {
			items = append(items, item)
		}
		sort.Slice(items, func(left, right int) bool {
			if items[left].Y != items[right].Y {
				return items[left].Y < items[right].Y
			}
			if items[left].X != items[right].X {
				return items[left].X < items[right].X
			}
			return items[left].Name < items[right].Name
		})
		flattened[floor] = items
	}
	return flattened, nil
}

func NewHandler(config Config) (*Handler, error) {
	if config.ListenAddress == "" {
		config.ListenAddress = defaultListenAddress
	}
	if config.TCPUpstream == "" {
		config.TCPUpstream = defaultTCPUpstream
	}
	if _, _, err := net.SplitHostPort(config.TCPUpstream); err != nil {
		return nil, fmt.Errorf("invalid TCP upstream %q: %w", config.TCPUpstream, err)
	}
	if config.PacketLimit <= 0 || config.PacketLimit > 64*1024*1024 {
		return nil, fmt.Errorf("packet limit must be between 1 and 67108864 bytes")
	}
	if config.MaxSessions <= 0 {
		return nil, errors.New("max sessions must be positive")
	}
	if config.PollTimeout <= 0 {
		config.PollTimeout = defaultPollTimeout
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = defaultIdleTimeout
	}
	if config.DialTimeout <= 0 {
		config.DialTimeout = defaultDialTimeout
	}
	cdnBaseURL, err := normalizeCDNBaseURL(config.CDNBaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid CDN base URL: %w", err)
	}
	config.CDNBaseURL = cdnBaseURL
	oss, err := normalizeOSSConfig(config.OSS)
	if err != nil {
		return nil, fmt.Errorf("invalid OSS configuration: %w", err)
	}
	config.OSS = oss
	publicAssetBaseURL := cdnBaseURL
	if publicAssetBaseURL == "" {
		/* A configured CDN is preferred. Without one, a public Aliyun OSS
		   bucket is still a valid external static origin, so the OSS settings
		   are operational rather than deployment-only metadata. */
		publicAssetBaseURL = ossPublicBaseURL(oss)
	}
	handler := &Handler{config: config, sessions: newSessionStore(config.MaxSessions), page: pageWithCDNBase(page, publicAssetBaseURL), stop: make(chan struct{}), npcData: make(map[int][]npcMetadata)}
	if strings.TrimSpace(config.AssetsDirectory) != "" {
		assetsDirectory := strings.TrimSpace(config.AssetsDirectory)
		/* ``go run ./client/web`` is normally launched from the repository
		   root, whereas the original default is relative to the web package.
		   Resolve that local layout without changing explicit operator paths. */
		if _, err := os.Stat(assetsDirectory); os.IsNotExist(err) && assetsDirectory == defaultAssetsDirectory {
			candidate := "client/web/" + defaultAssetsDirectory
			if _, candidateErr := os.Stat(candidate); candidateErr == nil {
				assetsDirectory = candidate
			}
		}
		handler.assets = http.StripPrefix("/assets/", http.FileServer(http.Dir(assetsDirectory)))
	}
	mapDirectory := strings.TrimSpace(config.MapDirectory)
	if mapDirectory != "" {
		/* Resolve the repository layout when the service is started from
		   client/web, while leaving an explicit operator path untouched. */
		if _, err := os.Stat(mapDirectory); os.IsNotExist(err) && mapDirectory == defaultMapDirectory {
			candidate := "client/web/../../" + defaultMapDirectory
			if _, candidateErr := os.Stat(candidate); candidateErr == nil {
				mapDirectory = candidate
			}
		}
		if _, err := os.Stat(mapDirectory); os.IsNotExist(err) {
			candidate := "../../" + defaultMapDirectory
			if _, candidateErr := os.Stat(candidate); candidateErr == nil {
				mapDirectory = candidate
			}
		}
		handler.maps = http.StripPrefix("/maps/", http.FileServer(http.Dir(mapDirectory)))
	}
	/* The browser's automatic-map loader must use the exact case-sensitive
	   object name published by the map tree.  Build this after the same path
	   fallback above so repository-root and client/web launches see the same
	   files.  An unavailable/empty directory simply contributes an empty index;
	   the normal live M-window path remains available. */
	handler.page = pageWithAutoMapFiles(handler.page, autoMapFilesForDirectory(mapDirectory))
	if strings.TrimSpace(config.AudioDirectory) != "" {
		audioDirectory := strings.TrimSpace(config.AudioDirectory)
		/* Match the asset path convenience above for both repository-root and
		   client/web working directories.  Explicit STONEAGE_WEB_AUDIO paths
		   remain untouched. */
		if _, err := os.Stat(audioDirectory); os.IsNotExist(err) && audioDirectory == defaultAudioDirectory {
			candidate := "client/web/../../" + defaultAudioDirectory
			if _, candidateErr := os.Stat(candidate); candidateErr == nil {
				audioDirectory = candidate
			}
		}
		if _, err := os.Stat(audioDirectory); os.IsNotExist(err) {
			candidate := "../../" + defaultAudioDirectory
			if _, candidateErr := os.Stat(candidate); candidateErr == nil {
				audioDirectory = candidate
			}
		}
		handler.audio = http.StripPrefix("/audio/", http.FileServer(http.Dir(audioDirectory)))
	}
	if strings.TrimSpace(config.NPCDirectory) != "" {
		npcDirectory := strings.TrimSpace(config.NPCDirectory)
		/* Match the repository layout convenience used by maps/audio.  An
		   explicit STONEAGE_WEB_NPCS path is left untouched. */
		if _, err := os.Stat(npcDirectory); os.IsNotExist(err) && npcDirectory == defaultNPCDirectory {
			candidate := "client/web/../../" + defaultNPCDirectory
			if _, candidateErr := os.Stat(candidate); candidateErr == nil {
				npcDirectory = candidate
			}
		}
		if _, err := os.Stat(npcDirectory); os.IsNotExist(err) {
			candidate := "../../" + defaultNPCDirectory
			if _, candidateErr := os.Stat(candidate); candidateErr == nil {
				npcDirectory = candidate
			}
		}
		if stat, err := os.Stat(npcDirectory); err == nil && stat.IsDir() {
			handler.npcDir = npcDirectory
		}
	}
	go handler.expiryLoop()
	return handler, nil
}

func (handler *Handler) expiryLoop() {
	interval := handler.config.IdleTimeout / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			handler.sessions.closeIdle(handler.config.IdleTimeout)
		case <-handler.stop:
			return
		}
	}
}

func (handler *Handler) Close() {
	handler.stopOnce.Do(func() {
		close(handler.stop)
		handler.sessions.closeAll()
	})
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	handler.setHeaders(response, request)
	if request.Method == http.MethodOptions {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if request.URL.Path == "/_client-version.json" {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		response.Header().Set("Content-Type", "application/json; charset=utf-8")
		response.Header().Set("Cache-Control", "no-cache")
		if request.Method == http.MethodHead {
			return
		}
		_, _ = response.Write(localAssetVersion)
		return
	}
	if request.URL.Path == "/" {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
		if request.Method == http.MethodHead {
			return
		}
		_, _ = response.Write(handler.page)
		return
	}
	if request.URL.Path == "/sw.js" {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		response.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		response.Header().Set("Cache-Control", "no-cache")
		response.Header().Set("Service-Worker-Allowed", "/")
		if request.Method == http.MethodHead {
			return
		}
		_, _ = response.Write(serviceWorker)
		return
	}
	if request.URL.Path == "/manifest.webmanifest" {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		response.Header().Set("Content-Type", "application/manifest+json; charset=utf-8")
		response.Header().Set("Cache-Control", "public, max-age=3600")
		if request.Method == http.MethodHead {
			return
		}
		_, _ = response.Write(webManifest)
		return
	}
	if request.URL.Path == "/healthz" {
		handler.health(response, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/assets/") {
		if handler.assets == nil {
			http.NotFound(response, request)
			return
		}
		// Asset manifests retain stable URLs between releases but can acquire
		// new logical-to-physical ADRN aliases.  Force revalidation so a browser
		// reload cannot keep an old index and silently hide newly deployed item
		// or sprite graphics.  The referenced binary assets remain cacheable.
		if strings.HasSuffix(strings.ToLower(request.URL.Path), ".json") {
			response.Header().Set("Cache-Control", "no-cache")
		}
		handler.assets.ServeHTTP(response, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/maps/") {
		if handler.maps == nil {
			http.NotFound(response, request)
			return
		}
		/* DAT/MAP files are immutable companion data.  The browser can cache a
		   floor while the player opens/closes the map window repeatedly. */
		response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		handler.maps.ServeHTTP(response, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/audio/") {
		if handler.audio == nil {
			http.NotFound(response, request)
			return
		}
		// Go's mime table uses the historical `audio/x-wav` alias.  Publish the
		// standards spelling so browsers and external CDNs agree on the media
		// type; the normal nosniff header remains in force for this read-only
		// subtree as well as for HTML, API, map and image responses.
		response.Header().Set("Content-Type", "audio/wav")
		// Audio filenames come from the preserved executable data and never
		// change during a local client session.  Let the browser keep the WAVs
		// between map/battle transitions instead of re-reading large BGM files.
		response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		handler.audio.ServeHTTP(response, request)
		return
	}
	if request.URL.Path == "/api/npcs" {
		handler.listNPCs(response, request)
		return
	}
	if request.URL.Path == "/api/sessions" {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handler.createSession(response)
		return
	}
	const prefix = "/api/sessions/"
	if !strings.HasPrefix(request.URL.Path, prefix) {
		http.NotFound(response, request)
		return
	}
	path := strings.TrimPrefix(request.URL.Path, prefix)
	parts := strings.Split(path, "/")
	if len(parts) < 1 || parts[0] == "" || strings.Contains(parts[0], "..") {
		http.NotFound(response, request)
		return
	}
	session, ok := handler.sessions.get(parts[0])
	if !ok {
		http.Error(response, "unknown session", http.StatusNotFound)
		return
	}
	if len(parts) == 1 && request.Method == http.MethodDelete {
		if request.URL.Query().Get("wait") == "1" {
			if err := handler.sessions.deleteGracefully(session.id, defaultGracefulCloseTimeout); err != nil {
				http.Error(response, err.Error(), http.StatusGatewayTimeout)
				return
			}
		} else {
			handler.sessions.delete(session.id)
		}
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) != 2 {
		http.NotFound(response, request)
		return
	}
	switch parts[1] {
	case "send":
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		/* The pagehide/beforeunload path uses close=1.  That request must be
		   self-contained: browsers may stop running promise continuations as
		   soon as the document is gone, so a second DELETE request is not a
		   reliable way to perform the native client's close-button cleanup. */
		handler.sendPacket(response, request, session, request.URL.Query().Get("close") == "1")
	case "events":
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handler.pollEvents(response, request, session)
	default:
		http.NotFound(response, request)
	}
}

func (handler *Handler) listNPCs(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	floorValue := strings.TrimSpace(request.URL.Query().Get("floor"))
	if floorValue == "" {
		http.Error(response, "floor is required", http.StatusBadRequest)
		return
	}
	floor, err := strconv.Atoi(floorValue)
	if err != nil || floor < 0 {
		http.Error(response, "invalid floor", http.StatusBadRequest)
		return
	}
	items, loadErr := handler.npcsForFloor(floor)
	if loadErr != nil {
		http.Error(response, "NPC metadata unavailable", http.StatusInternalServerError)
		return
	}
	response.Header().Set("Cache-Control", "public, max-age=60")
	handler.writeJSON(response, http.StatusOK, npcMetadataResponse{Floor: floor, NPCs: items})
}

func (handler *Handler) npcsForFloor(floor int) ([]npcMetadata, error) {
	handler.npcMu.RLock()
	if handler.npcDone {
		items := append([]npcMetadata(nil), handler.npcData[floor]...)
		err := handler.npcErr
		handler.npcMu.RUnlock()
		return items, err
	}
	handler.npcMu.RUnlock()

	handler.npcMu.Lock()
	defer handler.npcMu.Unlock()
	if handler.npcDone {
		return append([]npcMetadata(nil), handler.npcData[floor]...), handler.npcErr
	}
	if handler.npcDir == "" {
		handler.npcDone = true
		handler.npcData = make(map[int][]npcMetadata)
		return nil, nil
	}
	loaded, err := loadNPCMetadata(handler.npcDir)
	if err != nil {
		handler.npcErr = err
	} else {
		handler.npcData = loaded
	}
	handler.npcDone = true
	return append([]npcMetadata(nil), handler.npcData[floor]...), handler.npcErr
}

func (handler *Handler) setHeaders(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("Referrer-Policy", "no-referrer")
	if handler.config.AllowedOrigin != "" {
		response.Header().Set("Access-Control-Allow-Origin", handler.config.AllowedOrigin)
		response.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
		response.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		response.Header().Add("Vary", "Origin")
	}
	_ = request
}

func (handler *Handler) health(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	handler.sessions.mu.RLock()
	count := len(handler.sessions.sessions)
	handler.sessions.mu.RUnlock()
	handler.writeJSON(response, http.StatusOK, map[string]any{
		"status":   "ok",
		"sessions": count,
		"upstream": handler.config.TCPUpstream,
	})
}

type createResponse struct {
	ID       string `json:"id"`
	Greeting string `json:"greeting"`
}

func (handler *Handler) createSession(response http.ResponseWriter) {
	if !handler.sessions.hasCapacity() {
		http.Error(response, "maximum browser sessions reached", http.StatusServiceUnavailable)
		return
	}
	connection, err := net.DialTimeout("tcp", handler.config.TCPUpstream, handler.config.DialTimeout)
	if err != nil {
		http.Error(response, "TCP upstream unavailable", http.StatusBadGateway)
		return
	}
	_ = connection.SetReadDeadline(time.Now().Add(handler.config.DialTimeout))
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(connection, greeting); err != nil {
		_ = connection.Close()
		http.Error(response, "TCP upstream did not send a greeting", http.StatusBadGateway)
		return
	}
	_ = connection.SetReadDeadline(time.Time{})
	if greeting[0] != 'L' || greeting[1] != 0 {
		_ = connection.Close()
		http.Error(response, "TCP upstream sent an invalid greeting", http.StatusBadGateway)
		return
	}
	id, err := newSessionID()
	if err != nil {
		_ = connection.Close()
		http.Error(response, "cannot allocate session", http.StatusInternalServerError)
		return
	}
	session := newTCPSession(id, connection, handler.config.PacketLimit)
	if err := handler.sessions.add(session); err != nil {
		_ = connection.Close()
		http.Error(response, err.Error(), http.StatusServiceUnavailable)
		return
	}
	go session.readLoop()
	handler.writeJSON(response, http.StatusCreated, createResponse{
		ID:       id,
		Greeting: base64.StdEncoding.EncodeToString(greeting),
	})
}

func newSessionID() (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

type sendRequest struct {
	Packet string `json:"packet"`
}

func (handler *Handler) sendPacket(response http.ResponseWriter, request *http.Request, session *tcpSession, closeAfter bool) {
	if closeAfter {
		/* Always remove the bridge session after this one packet, including a
		   failed write.  A failed logout must have the same cleanup semantics as
		   the client's close button. */
		defer handler.sessions.delete(session.id)
	}
	body, err := io.ReadAll(http.MaxBytesReader(response, request.Body, int64(handler.config.PacketLimit*2)))
	if err != nil {
		http.Error(response, "request body is too large", http.StatusRequestEntityTooLarge)
		return
	}
	var encoded sendRequest
	if strings.HasPrefix(strings.TrimSpace(request.Header.Get("Content-Type")), "application/json") {
		if err := json.Unmarshal(body, &encoded); err != nil {
			http.Error(response, "invalid JSON packet", http.StatusBadRequest)
			return
		}
	} else {
		encoded.Packet = base64.StdEncoding.EncodeToString(body)
	}
	packet, err := base64.StdEncoding.DecodeString(encoded.Packet)
	if err != nil || len(packet) == 0 {
		http.Error(response, "packet must be non-empty base64", http.StatusBadRequest)
		return
	}
	if err := session.write(packet); err != nil {
		if errors.Is(err, net.ErrClosed) {
			http.Error(response, "session is closed", http.StatusGone)
		} else {
			http.Error(response, err.Error(), http.StatusBadGateway)
		}
		return
	}
	response.WriteHeader(http.StatusAccepted)
}

type eventResponse struct {
	Packet string `json:"packet,omitempty"`
	Closed bool   `json:"closed,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (handler *Handler) pollEvents(response http.ResponseWriter, request *http.Request, session *tcpSession) {
	timeout := handler.config.PollTimeout
	if raw := strings.TrimSpace(request.URL.Query().Get("timeout")); raw != "" {
		milliseconds, err := strconv.Atoi(raw)
		if err != nil || milliseconds < 1 || milliseconds > 60000 {
			http.Error(response, "timeout must be 1..60000 milliseconds", http.StatusBadRequest)
			return
		}
		timeout = time.Duration(milliseconds) * time.Millisecond
	}
	events, err := session.pollContext(request.Context(), timeout)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// The client is gone.  Remove the session before closing the TCP
			// side so a replacement login cannot race a dead bridge entry.
			handler.sessions.removeIf(session.id, session)
			session.finish("HTTP events request canceled")
			return
		}
		http.Error(response, err.Error(), http.StatusConflict)
		return
	}
	encoded := make([]eventResponse, 0, len(events))
	for _, event := range events {
		item := eventResponse{Closed: event.closed, Error: event.err}
		if len(event.packet) > 0 {
			item.Packet = base64.StdEncoding.EncodeToString(event.packet)
		}
		encoded = append(encoded, item)
	}
	handler.writeJSON(response, http.StatusOK, map[string]any{
		"events": encoded,
		"closed": session.isClosed(),
	})
}

func (handler *Handler) writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func run() error {
	config, configPath, err := configFromCommandLine(os.Args[1:])
	if err != nil {
		return err
	}
	handler, err := NewHandler(config)
	if err != nil {
		return err
	}
	defer handler.Close()
	config = handler.config
	server := &http.Server{
		Addr:              config.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       70 * time.Second,
		WriteTimeout:      70 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	go func() {
		<-stop
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		handler.Close()
	}()
	if configPath != "" {
		log.Printf("web configuration loaded from %s", configPath)
	}
	if config.OSS.Endpoint != "" {
		log.Printf("static object-storage origin configured: provider=%s bucket=%s endpoint=%s prefix=%s", config.OSS.Provider, config.OSS.Bucket, config.OSS.Endpoint, config.OSS.Prefix)
	}
	if config.CDNBaseURL != "" {
		log.Printf("static CDN root configured: %s", config.CDNBaseURL)
	} else if publicOSSURL := ossPublicBaseURL(config.OSS); publicOSSURL != "" {
		log.Printf("static assets use public object-storage root: %s", publicOSSURL)
	} else if config.OSS.Provider == "cloudflare-r2" && config.OSS.Endpoint != "" {
		log.Printf("warning: Cloudflare R2 is configured without static.cdn.base_url; browser assets will use the local Web origin")
	}
	log.Printf("web listening on %s -> TCP %s", config.ListenAddress, config.TCPUpstream)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
