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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
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

	"github.com/k0ngk0ng/stoneage/client/web/runtimeassets"
	"github.com/k0ngk0ng/stoneage/internal/aicontrol"
	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/battleauto"
	"github.com/k0ngk0ng/stoneage/internal/characterbuild"
	"github.com/k0ngk0ng/stoneage/internal/clientip"
	"github.com/k0ngk0ng/stoneage/internal/gameservers"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

var page = runtimeassets.SourcePage

// Injected from the release tag at build time; independent of asset revisions.
var releaseVersion = "dev"

func pageWithReleaseVersion(source []byte, version string) []byte {
	return bytes.ReplaceAll(source, []byte("<!--STONEAGE_RELEASE_VERSION-->dev"), []byte(html.EscapeString(version)))
}

// Production workers and modules are published under a content-addressed CDN root.
var serviceWorker, _ = runtimeassets.Files.ReadFile("sw.js")
var automationScript, _ = runtimeassets.Files.ReadFile("automation.js")
var runtimeScripts = runtimeassets.Files
var webManifest, _ = runtimeassets.Files.ReadFile("manifest.webmanifest")

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
	// Bound every bridge-to-game write so a control handoff cannot wait on a
	// stalled upstream socket forever. This is a bridge timeout only; it does
	// not alter the 2.5 server's turn or movement deadlines.
	defaultWriteTimeout = 5 * time.Second
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
// avoids turning the small bridge into an SSRF service. When GatewayAPIURL is
// configured, the gateway directory supplies the selected listener address
// and TCPUpstream remains only as a backwards-compatible single-line fallback.
type Config struct {
	TrustedProxies  []string
	ForwardClientIP bool
	ListenAddress   string
	TCPUpstream     string
	GatewayAPIURL   string
	// AgentSocketPath is a private Unix socket used by the AI runtime to
	// attach to a live browser session. AgentGameUpstream and
	// AgentWorkerUpstream are fixed internal HTTP destinations for the small
	// public AI frontdoor; request URLs never select either destination.
	AgentSocketPath     string
	AgentGameUpstream   string
	AgentWorkerUpstream string
	AssetsDirectory     string
	MapDirectory        string
	AudioDirectory      string
	NPCDirectory        string
	PacketLimit         int
	MaxSessions         int
	PollTimeout         time.Duration
	IdleTimeout         time.Duration
	DialTimeout         time.Duration
	AllowedOrigin       string
	// CDNBaseURL is the public static root which contains assets/, maps/ and
	// audio/.  It changes only browser static-resource URLs; the account, NPC
	// and game-session APIs always remain on this process.
	CDNBaseURL string
	// OSS describes the stable object-storage origin used by deployment to
	// hold assets/, maps/ and audio/. Runtime browser URLs still prefer the
	// CDN base above; this process never uploads to OSS or receives its keys.
	OSS OSSConfig
	// Automation is the server-owned deterministic executor. It is injected
	// by startup after loading the reviewed knowledge/map snapshots and
	// opening the durable plan/receipt stores. Browser requests cannot replace
	// this capability.
	Automation Automation
	// AutomationClose releases resources opened for Automation. It is kept
	// separate from the interface so tests and embedders can inject an
	// executor without requiring it to expose lifecycle methods.
	AutomationClose func() error
	// The following paths are server-owned runtime inputs. If any are set,
	// NewHandler loads all four dependencies and enables the real executor;
	// incomplete configuration fails closed.
	AutomationKnowledgeDataDir  string
	AutomationMapDataDir        string
	AutomationStockItems        string
	AutomationHealingItems      string
	AutomationNPCRegistry       string
	AutomationDB                string
	ReceiptDB                   string
	AutomationPollInterval      time.Duration
	AutomationNoProgressTimeout time.Duration
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
	if value, ok := os.LookupEnv("STONEAGE_WEB_TRUSTED_PROXIES"); ok {
		cfg.TrustedProxies = strings.Split(value, ",")
	}
	cfg.ForwardClientIP = strings.EqualFold(os.Getenv("STONEAGE_WEB_FORWARD_CLIENT_IP"), "true")
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_LISTEN")); value != "" {
		cfg.ListenAddress = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_TCP_UPSTREAM")); value != "" {
		cfg.TCPUpstream = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_GATEWAY_API_URL")); value != "" {
		cfg.GatewayAPIURL = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_AGENT_SOCKET")); value != "" {
		cfg.AgentSocketPath = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_AGENT_GAME_UPSTREAM")); value != "" {
		cfg.AgentGameUpstream = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_WEB_AGENT_WORKER_UPSTREAM")); value != "" {
		cfg.AgentWorkerUpstream = value
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
	if value := strings.TrimSpace(os.Getenv("STONEAGE_AI_KNOWLEDGE_DATA_DIR")); value != "" {
		cfg.AutomationKnowledgeDataDir = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_AI_MAP_DATA_DIR")); value != "" {
		cfg.AutomationMapDataDir = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_AI_STOCK_ITEMS")); value != "" {
		cfg.AutomationStockItems = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_AI_HEALING_ITEMS")); value != "" {
		cfg.AutomationHealingItems = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_AI_NPC_REGISTRY")); value != "" {
		cfg.AutomationNPCRegistry = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_AI_AUTOMATION_DB")); value != "" {
		cfg.AutomationDB = value
	}
	if value := strings.TrimSpace(os.Getenv("STONEAGE_AI_RECEIPT_DB")); value != "" {
		cfg.ReceiptDB = value
	}
	if value := positiveDurationEnv("STONEAGE_AI_AUTOMATION_POLL_INTERVAL"); value > 0 {
		cfg.AutomationPollInterval = value
	}
	if value := positiveDurationEnv("STONEAGE_AI_AUTOMATION_NO_PROGRESS_TIMEOUT"); value > 0 {
		cfg.AutomationNoProgressTimeout = value
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

func normalizeGatewayAPIURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("gateway API URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	if strings.ContainsAny(value, "\"'`<>\\\r\n\t ") {
		return "", errors.New("gateway API URL contains unsafe characters")
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
	entries, _ := runtimeassets.Files.ReadDir(".")
	for _, entry := range entries {
		if entry.Name() == "sw.js" {
			continue
		} // Registration must be same-origin.
		result = bytes.ReplaceAll(result, []byte("/"+entry.Name()), []byte(baseURL+"/"+runtimeassets.Root(baseURL)+entry.Name()))
	}
	return runtimeassets.Externalize(result, baseURL)
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
	// seq is assigned only after a session opts into the reliable events
	// protocol.  Keeping it on the queued event lets a reconnecting browser
	// replay the same packet until its acknowledgement is received, while the
	// legacy poll path remains a destructive read.
	seq uint64
}

type eventPollMode uint8

const (
	eventPollModeUnset eventPollMode = iota
	eventPollModeLegacy
	eventPollModeReliable
)

var (
	errEventsPollPending = errors.New("another events poll is already pending")
	errEventsPollMode    = errors.New("events poll mode cannot be changed")
	errEventsAckAhead    = errors.New("ack must not exceed the last delivered event")
)

type tcpSession struct {
	id string
	// serverID is the stable configured game-line identifier selected when
	// this TCP session was created.  Keep it alongside the socket so a
	// reconnect cannot recover a run from the same account on another line.
	serverID     string
	conn         net.Conn
	limit        int
	gate         *aicontrol.Gate
	writeTimeout time.Duration
	closed       chan struct{}

	mu                   sync.Mutex
	writeMu              sync.Mutex
	dispatchMu           sync.Mutex
	activeDispatch       int
	gateClosePending     bool
	gateCloseOnce        sync.Once
	automationMu         sync.Mutex
	automationHandle     AutomationHandle
	automationMode       aicontrol.Mode
	automationGen        uint64
	automationNote       string
	automationState      battleauto.State
	automationStateKnown bool
	authoritative        *aigame.Session
	authoritativeMu      sync.RWMutex
	authoritativeErr     error
	events               []packetEvent
	eventBytes           int
	lastActivity         time.Time
	closeOnce            sync.Once
	notify               chan struct{}
	polling              bool
	closing              bool
	pollMode             eventPollMode
	nextEventSeq         uint64
	lastDeliveredSeq     uint64
	lastAckedSeq         uint64
	finished             bool
}

func newTCPSession(id string, conn net.Conn, packetLimit int) *tcpSession {
	session := &tcpSession{
		id:           id,
		serverID:     legacyServerID,
		conn:         conn,
		limit:        packetLimit,
		gate:         aicontrol.New(),
		writeTimeout: defaultWriteTimeout,
		closed:       make(chan struct{}),
		notify:       make(chan struct{}, 1),
		lastActivity: time.Now(),
	}
	// The Web bridge already owns this TCP connection. Feed the same framed
	// packets into an aigame parser backed by a write-through adapter so an
	// automation executor can observe and submit typed actions on this exact
	// session without opening another login/socket.
	session.authoritative = aigame.NewSession(&webObserverConn{session: session}, aigame.Config{PacketLimit: packetLimit, EventBuffer: 1})
	return session
}

func (session *tcpSession) touchLocked() {
	session.lastActivity = time.Now()
}

func (session *tcpSession) touch() {
	session.mu.Lock()
	session.touchLocked()
	session.mu.Unlock()
}

// serverLineID returns the immutable configured game-line key associated with
// this session. Tests and embedders that construct a session directly use the
// legacy single-line default from newTCPSession.
func (session *tcpSession) serverLineID() string {
	if session == nil {
		return ""
	}
	return strings.TrimSpace(session.serverID)
}

func (session *tcpSession) signal() {
	select {
	case session.notify <- struct{}{}:
	default:
	}
}

// dispatch is the only path from an HTTP caller to the character socket. The
// small in-flight counter lets finish fence the Gate without attempting to
// acquire Gate's mutex from inside Gate.Dispatch (write errors can call
// finish while Dispatch still owns that mutex).
func (session *tcpSession) dispatch(ctx context.Context, generation uint64, owner aicontrol.Mode, send func(context.Context) error) error {
	if session == nil || session.gate == nil {
		return aicontrol.ErrClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session.dispatchMu.Lock()
	session.mu.Lock()
	finished := session.finished
	session.mu.Unlock()
	if finished || session.gateClosePending {
		session.dispatchMu.Unlock()
		return aicontrol.ErrClosed
	}
	session.activeDispatch++
	session.dispatchMu.Unlock()

	err := session.gate.Dispatch(ctx, generation, owner, send)
	session.dispatchMu.Lock()
	session.activeDispatch--
	closeGate := session.gateClosePending && session.activeDispatch == 0
	session.dispatchMu.Unlock()
	if closeGate {
		session.closeGate()
	}
	return err
}

func (session *tcpSession) closeGate() {
	if session.gate == nil {
		return
	}
	session.gateCloseOnce.Do(func() {
		session.gate.Close()
		handle := session.clearAutomation(0)
		detachAutomationHandle(handle)
	})
}

func (session *tcpSession) enqueue(event packetEvent) {
	session.mu.Lock()
	if session.finished {
		session.mu.Unlock()
		return
	}
	if len(session.events) >= maxQueuedEvents || session.eventBytes+len(event.packet) > maxQueuedBytes {
		session.mu.Unlock()
		session.finish("HTTP event queue overflow")
		return
	}
	session.assignEventSequenceLocked(&event)
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
		session.applyAuthoritativePacket(packet)
		session.enqueue(packetEvent{packet: packet})
	}
}

func (session *tcpSession) applyAuthoritativePacket(packet []byte) {
	if session == nil {
		return
	}
	session.authoritativeMu.RLock()
	observer := session.authoritative
	session.authoritativeMu.RUnlock()
	if observer == nil {
		return
	}
	if err := observer.ApplyServerPacket(packet); err != nil && !errors.Is(err, aigame.ErrClosed) {
		session.authoritativeMu.Lock()
		if session.authoritativeErr == nil {
			session.authoritativeErr = err
		}
		session.authoritativeMu.Unlock()
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
		session.finished = true
		event := packetEvent{closed: true, err: reason}
		session.assignEventSequenceLocked(&event)
		session.events = append(session.events, event)
		session.mu.Unlock()
		close(session.closed)
		_ = session.conn.Close()
		session.signal()
	})
	// Never call Gate.Close while dispatch holds Gate's mutex. A write failure
	// can reach this method from inside the Dispatch callback, so defer the
	// close until the wrapped dispatch has returned.
	session.dispatchMu.Lock()
	session.gateClosePending = true
	closeGate := session.activeDispatch == 0
	session.dispatchMu.Unlock()
	if closeGate {
		session.closeGate()
	}
	session.authoritativeMu.RLock()
	observer := session.authoritative
	session.authoritativeMu.RUnlock()
	if observer != nil {
		_ = observer.Close()
	}
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
	if err := session.writeUpstream(packet); err != nil {
		return err
	}
	// Release the transport lock before entering the game observer. Typed
	// actions take the game lock before the transport lock.
	session.applyAuthoritativeClientPacket(packet)
	return nil
}

// The aigame writer already records typed action submissions and may hold its
// state lock during this write. Only browser-originated packets need the raw
// client observation hook; invoking it twice would both lose provenance and
// recursively lock the shared observer.
func (session *tcpSession) writeUpstream(packet []byte) error {
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
	// net.Conn.Write has no useful cancellation hook on every supported
	// transport. A deadline is therefore part of the bridge's ownership
	// contract: takeover must not wait indefinitely for a blocked upstream.
	writeTimeout := session.writeTimeout
	if writeTimeout <= 0 {
		writeTimeout = defaultWriteTimeout
	}
	_ = session.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	defer func() { _ = session.conn.SetWriteDeadline(time.Time{}) }()
	remaining := packet
	for len(remaining) > 0 {
		written, err := session.conn.Write(remaining)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		remaining = remaining[written:]
	}
	session.touch()

	return nil
}

// assignEventSequenceLocked gives reliable-mode events their stable wire
// identity.  A session can receive its greeting before the first /events
// request chooses a mode, so pending events are assigned lazily when reliable
// mode is selected.
func (session *tcpSession) assignEventSequenceLocked(event *packetEvent) {
	if session.pollMode != eventPollModeReliable || event == nil || event.seq != 0 {
		return
	}
	if session.nextEventSeq == 0 {
		session.nextEventSeq = 1
	}
	event.seq = session.nextEventSeq
	session.nextEventSeq++
}

func (session *tcpSession) assignPendingEventSequencesLocked() {
	if session.pollMode != eventPollModeReliable {
		return
	}
	for index := range session.events {
		session.assignEventSequenceLocked(&session.events[index])
	}
}

// acknowledgeEventsLocked removes only events that the browser has already
// seen.  In particular, an ack cannot consume a packet that was queued after
// the last delivered response, even if that packet already has a sequence.
func (session *tcpSession) acknowledgeEventsLocked(ack uint64) {
	if ack <= session.lastAckedSeq {
		return
	}
	all := session.events
	kept := all[:0]
	bytes := 0
	for _, event := range all {
		if event.seq != 0 && event.seq <= ack {
			continue
		}
		kept = append(kept, event)
		bytes += len(event.packet)
	}
	// The compacted slice may keep references to acknowledged packet buffers in
	// its unused capacity.  Clear those slots so a busy session does not retain
	// megabytes of already acknowledged TCP data until its next allocation.
	for index := len(kept); index < len(all); index++ {
		all[index] = packetEvent{}
	}
	session.events = kept
	session.eventBytes = bytes
	session.lastAckedSeq = ack
}

// beginPollLocked fixes the session's event protocol on its first valid poll,
// then validates the acknowledgement and reserves the single poll slot.
func (session *tcpSession) beginPollLocked(mode eventPollMode, ack uint64) error {
	if session.polling {
		return errEventsPollPending
	}
	if session.pollMode != eventPollModeUnset && session.pollMode != mode {
		return errEventsPollMode
	}
	if mode == eventPollModeReliable && ack > session.lastDeliveredSeq {
		return errEventsAckAhead
	}
	if session.pollMode == eventPollModeUnset {
		session.pollMode = mode
	}
	if mode == eventPollModeReliable {
		session.assignPendingEventSequencesLocked()
		session.acknowledgeEventsLocked(ack)
	}
	session.polling = true
	session.touchLocked()
	return nil
}

func (session *tcpSession) finishPoll() {
	session.mu.Lock()
	session.polling = false
	session.mu.Unlock()
}

func (session *tcpSession) reliableClosed() bool {
	session.mu.Lock()
	closed := session.isClosed() && len(session.events) == 0
	session.mu.Unlock()
	return closed
}

// poll returns all currently queued packets, waiting at most timeout when the
// queue is empty.  Only one outstanding poll is allowed for a session so that
// event ordering remains exactly the TCP ordering seen by the original client.
// This is the legacy destructive-read API used by older browsers and tests.
func (session *tcpSession) poll(timeout time.Duration) ([]packetEvent, error) {
	return session.pollContext(context.Background(), timeout)
}

// pollContext is the request-aware legacy form used by the HTTP handler.  A
// canceled request releases this poll only; the TCP session remains alive so a
// transient browser/proxy cancellation cannot log the player out.
func (session *tcpSession) pollContext(ctx context.Context, timeout time.Duration) ([]packetEvent, error) {
	return session.pollModeContext(ctx, timeout, eventPollModeLegacy, 0)
}

// pollReliableContext implements the opt-in acknowledgement protocol.  Events
// remain in the queue after delivery and are replayed until a later request
// acknowledges them.
func (session *tcpSession) pollReliableContext(ctx context.Context, timeout time.Duration, ack uint64) ([]packetEvent, error) {
	return session.pollModeContext(ctx, timeout, eventPollModeReliable, ack)
}

func (session *tcpSession) pollModeContext(ctx context.Context, timeout time.Duration, mode eventPollMode, ack uint64) ([]packetEvent, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	session.mu.Lock()
	if err := session.beginPollLocked(mode, ack); err != nil {
		session.mu.Unlock()
		return nil, err
	}
	session.mu.Unlock()
	defer session.finishPoll()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		session.mu.Lock()
		if mode == eventPollModeReliable {
			session.assignPendingEventSequencesLocked()
			events := make([]packetEvent, 0, len(session.events))
			for _, event := range session.events {
				if event.seq <= session.lastAckedSeq {
					continue
				}
				events = append(events, event)
				if event.seq > session.lastDeliveredSeq {
					session.lastDeliveredSeq = event.seq
				}
			}
			if len(events) > 0 {
				session.touchLocked()
				session.mu.Unlock()
				return events, nil
			}
		} else if len(session.events) > 0 {
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
			if mode == eventPollModeReliable {
				// The close marker is retained and delivered with a sequence
				// before this point.  Once it has been acknowledged there is
				// no synthetic second marker to assign a bogus seq=0 to.
				return nil, nil
			}
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
	publicAssetBaseURL string
	agentMu            sync.Mutex
	agentLeases        map[string]*webAgentLease
	agentFrontdoor     *webAgentFrontdoor
	trustedProxies     []netip.Prefix
	config             Config
	sessions           *sessionStore
	page               []byte
	assets             http.Handler
	maps               http.Handler
	audio              http.Handler
	npcDir             string
	npcMu              sync.RWMutex
	npcData            map[int][]npcMetadata
	npcErr             error
	npcDone            bool
	stop               chan struct{}
	stopOnce           sync.Once
	automationMu       sync.RWMutex
	automation         Automation
	automationClose    func() error

	// Auto battle loads the game's recovery tables once and keeps the last
	// decision of the loop for the control panel.
	recoveryOnce sync.Once
	recoveryData *aiknowledge.RecoveryTables
	recoveryErr  error
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
	trustedProxies, err := clientip.ParseTrustedProxies(config.TrustedProxies)
	if err != nil {
		return nil, err
	}
	if config.ListenAddress == "" {
		config.ListenAddress = defaultListenAddress
	}
	gatewayAPIURL, err := normalizeGatewayAPIURL(config.GatewayAPIURL)
	if err != nil {
		return nil, fmt.Errorf("invalid gateway API URL: %w", err)
	}
	config.GatewayAPIURL = gatewayAPIURL
	if config.TCPUpstream == "" {
		config.TCPUpstream = defaultTCPUpstream
	}
	if config.GatewayAPIURL == "" {
		if _, _, err := net.SplitHostPort(config.TCPUpstream); err != nil {
			return nil, fmt.Errorf("invalid TCP upstream %q: %w", config.TCPUpstream, err)
		}
	}
	agentFrontdoor, err := newWebAgentFrontdoor(config.AgentGameUpstream, config.AgentWorkerUpstream)
	if err != nil {
		return nil, err
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
	if config.Automation == nil {
		automation, closeAutomation, err := configureAutomationRuntime(config)
		if err != nil {
			return nil, err
		}
		config.Automation = automation
		config.AutomationClose = closeAutomation
	}
	handler := &Handler{publicAssetBaseURL: publicAssetBaseURL, trustedProxies: trustedProxies, config: config, agentFrontdoor: agentFrontdoor, sessions: newSessionStore(config.MaxSessions), page: pageWithReleaseVersion(pageWithCDNBase(page, publicAssetBaseURL), releaseVersion), stop: make(chan struct{}), npcData: make(map[int][]npcMetadata), automation: config.Automation, automationClose: config.AutomationClose}
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
		if handler.automationClose != nil {
			_ = handler.automationClose()
		}
	})
}

func (handler *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if handler.agentFrontdoor != nil && handler.agentFrontdoor.owns(request.URL.Path) {
		handler.agentFrontdoor.ServeHTTP(response, request)
		return
	}
	handler.setHeaders(response, request)
	if request.Method == http.MethodOptions {
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if base := handler.publicAssetBaseURL; base != "" {
		name := strings.TrimPrefix(request.URL.Path, "/")
		_, embeddedErr := runtimeassets.Files.ReadFile(name)
		target := ""
		if (embeddedErr == nil || runtimeassets.Extra(base)[name] != nil) && name != "sw.js" {
			target = base + "/" + runtimeassets.Root(base) + name
		}
		if strings.HasPrefix(name, "assets/") || strings.HasPrefix(name, "maps/") || strings.HasPrefix(name, "audio/") || name == "_client-version.json" {
			target = base + request.URL.EscapedPath()
		}
		if target != "" {
			if request.URL.RawQuery != "" {
				target += "?" + request.URL.RawQuery
			}
			http.Redirect(response, request, target, http.StatusTemporaryRedirect)
			return
		}
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
		if handler.publicAssetBaseURL != "" {
			workerURL, _ := json.Marshal(handler.publicAssetBaseURL + "/" + runtimeassets.Root(handler.publicAssetBaseURL) + "sw.js")
			fmt.Fprintf(response, "importScripts(%s);\n", workerURL)
		} else {
			_, _ = response.Write(serviceWorker)
		}
		return
	}
	if request.URL.Path == "/world-resources.js" || request.URL.Path == "/map-pack.js" || request.URL.Path == "/resource-worker.js" || request.URL.Path == "/resource-client.js" || request.URL.Path == "/map-packs.json" {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		response.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		if strings.HasSuffix(request.URL.Path, ".json") {
			response.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		response.Header().Set("Cache-Control", "no-cache")
		if request.Method != http.MethodHead {
			script, _ := runtimeScripts.ReadFile(strings.TrimPrefix(request.URL.Path, "/"))
			_, _ = response.Write(script)
		}
		return
	}
	if request.URL.Path == "/automation.js" {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		response.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		response.Header().Set("Cache-Control", "no-cache")
		if request.Method == http.MethodHead {
			return
		}
		_, _ = response.Write(automationScript)
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
	if request.URL.Path == "/api/servers" {
		handler.listServers(response, request)
		return
	}
	if request.URL.Path == "/api/sessions" {
		if request.Method != http.MethodPost {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handler.createSession(response, request)
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
	if len(parts) == 3 && parts[1] == "automation" && parts[2] == "tasks" {
		// The catalog is tied to the current server knowledge revision and may
		// contain character-specific review notes; never cache success or error
		// responses in a browser or intermediary.
		response.Header().Set("Cache-Control", "no-store")
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
	if len(parts) == 2 && parts[1] == "automation" {
		http.NotFound(response, request)
		return
	}
	if len(parts) == 3 && parts[1] == "automation" && parts[2] == "tasks" {
		response.Header().Set("Cache-Control", "no-store")
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", "GET")
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handler.listAutomationTasks(response, request)
		return
	}
	if len(parts) == 3 && parts[1] == "automation" && (parts[2] == "start" || parts[2] == "preview") {
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", "POST")
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if parts[2] == "preview" {
			handler.previewAutomation(response, request, session)
		} else {
			handler.startAutomation(response, request, session)
		}
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
		/* Keep send-and-close atomic for compatible callers. Page unload uses
		   DELETE instead, preserving the native in-place disconnect behavior. */
		handler.sendPacket(response, request, session, request.URL.Query().Get("close") == "1")
	case "events":
		if request.Method != http.MethodGet {
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handler.pollEvents(response, request, session)
	case "control":
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", "GET")
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handler.control(response, request, session)
	case "battle-auto":
		handler.startBattleAuto(response, request, session)
	case "takeover":
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", "POST")
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handler.takeover(response, request, session)
	case "pause":
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", "POST")
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handler.pauseAutomation(response, request, session)
	case "resume":
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", "POST")
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handler.resumeAutomation(response, request, session)
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

func (handler *Handler) listAutomationTasks(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	provider, ok := handler.automationExecutor().(automationTaskDirectoryProvider)
	if !ok || provider == nil {
		http.Error(response, "automation task directory unavailable", http.StatusServiceUnavailable)
		return
	}
	directory, err := provider.TaskDirectory(request.Context())
	if err != nil {
		http.Error(response, "automation task directory unavailable", http.StatusServiceUnavailable)
		return
	}
	handler.writeJSON(response, http.StatusOK, directory)
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
		"status":          "ok",
		"sessions":        count,
		"upstream":        handler.config.TCPUpstream,
		"gateway_api_url": handler.config.GatewayAPIURL,
	})
}

const legacyServerID = "local-line"

type publicServer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
}

type publicServerListResponse struct {
	Servers []publicServer `json:"servers"`
}

func (handler *Handler) configuredServers(ctx context.Context) ([]gameservers.Server, error) {
	if handler.config.GatewayAPIURL == "" {
		return []gameservers.Server{{
			ID:              legacyServerID,
			Name:            "游戏服务器",
			Address:         handler.config.TCPUpstream,
			UpstreamAddress: handler.config.TCPUpstream,
		}}, nil
	}
	return gameservers.Fetch(ctx, handler.config.GatewayAPIURL)
}

func (handler *Handler) listServers(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", "GET")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	servers, err := handler.configuredServers(request.Context())
	if err != nil {
		http.Error(response, "server directory unavailable", http.StatusBadGateway)
		return
	}
	public := make([]publicServer, 0, len(servers))
	for _, server := range servers {
		public = append(public, publicServer{ID: server.ID, Name: server.Name, Disabled: server.Disabled})
	}
	handler.writeJSON(response, http.StatusOK, publicServerListResponse{Servers: public})
}

type createResponse struct {
	ID                  string          `json:"id"`
	Greeting            string          `json:"greeting"`
	EventAck            bool            `json:"event_ack"`
	Control             aicontrol.State `json:"control"`
	AutomationAvailable bool            `json:"automation_available"`
}

type createRequest struct {
	ServerID string `json:"server_id"`
}

func decodeCreateRequest(response http.ResponseWriter, request *http.Request) (createRequest, error) {
	var value createRequest
	if request.Body == nil {
		return value, nil
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		if errors.Is(err, io.EOF) {
			return value, nil
		}
		return createRequest{}, fmt.Errorf("invalid session request: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return createRequest{}, errors.New("session request contains multiple JSON values")
		}
		return createRequest{}, fmt.Errorf("invalid session request: %w", err)
	}
	value.ServerID = strings.TrimSpace(value.ServerID)
	return value, nil
}

func (handler *Handler) sessionAddress(ctx context.Context, serverID string) (string, int, error) {
	if handler.config.GatewayAPIURL == "" {
		if serverID != "" && serverID != legacyServerID {
			return "", http.StatusNotFound, errors.New("unknown server")
		}
		return handler.config.TCPUpstream, http.StatusOK, nil
	}
	if serverID == "" {
		return "", http.StatusBadRequest, errors.New("server_id is required")
	}
	servers, err := handler.configuredServers(ctx)
	if err != nil {
		return "", http.StatusBadGateway, err
	}
	for _, server := range servers {
		if server.ID != serverID {
			continue
		}
		if server.Disabled {
			return "", http.StatusServiceUnavailable, errors.New("server is disabled")
		}
		return server.Address, http.StatusOK, nil
	}
	return "", http.StatusNotFound, errors.New("unknown server")
}

func (handler *Handler) createSession(response http.ResponseWriter, request *http.Request) {
	if !handler.sessions.hasCapacity() {
		http.Error(response, "maximum browser sessions reached", http.StatusServiceUnavailable)
		return
	}
	input, err := decodeCreateRequest(response, request)
	if err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	address, status, err := handler.sessionAddress(request.Context(), input.ServerID)
	if err != nil {
		if status == http.StatusBadGateway {
			http.Error(response, "server directory unavailable", status)
		} else {
			http.Error(response, err.Error(), status)
		}
		return
	}
	connection, err := net.DialTimeout("tcp", address, handler.config.DialTimeout)
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
	if handler.config.ForwardClientIP {
		header, err := clientip.ProxyHeader(clientip.RequestSourceIP(request, handler.trustedProxies))
		if err == nil {
			_ = connection.SetWriteDeadline(time.Now().Add(handler.config.DialTimeout))
			_, err = io.Copy(connection, bytes.NewReader(header))
			_ = connection.SetWriteDeadline(time.Time{})
		}
		if err != nil {
			_ = connection.Close()
			http.Error(response, "cannot forward client address", http.StatusBadGateway)
			return
		}
	}
	id, err := newSessionID()
	if err != nil {
		_ = connection.Close()
		http.Error(response, "cannot allocate session", http.StatusInternalServerError)
		return
	}
	session := newTCPSession(id, connection, handler.config.PacketLimit)
	// The legacy single-line mode has no request field, but it still needs a
	// stable identity component for automation recovery.  Gateway-backed
	// sessions always carry the caller-selected configured server ID.
	session.serverID = input.ServerID
	if session.serverID == "" {
		session.serverID = legacyServerID
	}
	if err := handler.sessions.add(session); err != nil {
		_ = connection.Close()
		http.Error(response, err.Error(), http.StatusServiceUnavailable)
		return
	}
	go session.readLoop()
	handler.writeJSON(response, http.StatusCreated, createResponse{
		ID:                  id,
		Greeting:            base64.StdEncoding.EncodeToString(greeting),
		EventAck:            true,
		Control:             session.gate.State(),
		AutomationAvailable: handler.automationExecutor() != nil,
	})
}

func newSessionID() (string, error) {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

type controlResponse struct {
	Recovery            *AutomationRecovery `json:"automation_recovery"`
	RecoveryUnavailable bool                `json:"automation_recovery_unavailable,omitempty"`
	Control             aicontrol.State     `json:"control"`
	AutomationAvailable bool                `json:"automation_available"`
	AutomationActive    bool                `json:"automation_active"`
	AutomationMode      aicontrol.Mode      `json:"automation_mode,omitempty"`
	// AutomationNote is the last decision an auto battle loop made, and
	// AutomationState its counters. Together they are the only view a player
	// gets of a mode that runs without a task to inspect.
	AutomationNote  string            `json:"automation_note,omitempty"`
	AutomationState *battleauto.State `json:"automation_state,omitempty"`
}

func (handler *Handler) controlSnapshot(session *tcpSession) controlResponse {
	state := aicontrol.State{}
	if session == nil {
		return controlResponse{AutomationAvailable: handler.automationExecutor() != nil}
	}
	if session.gate != nil {
		state = session.gate.State()
	}
	_, mode, generation := session.automationStatus()
	active := generation != 0 && (state.Mode == aicontrol.Quest || state.Mode == aicontrol.Leveling || state.Mode == aicontrol.Agent || state.Mode == aicontrol.Battle || state.Mode == aicontrol.Paused)
	if !active {
		mode = ""
	}
	var recovery *AutomationRecovery
	var recoveryErr error
	if !active && state.Mode == aicontrol.Manual {
		recovery, recoveryErr = handler.recoveryOffer(context.Background(), session)
	}
	note := ""
	var battleState *battleauto.State
	// The light loop can hold either claim: battle answers turns, leveling
	// walks between them. Both report through the same counters.
	if active && (state.Mode == aicontrol.Battle || state.Mode == aicontrol.Leveling) {
		note = session.automationNoteText()
		if snapshot, ok := session.automationStateSnapshot(); ok {
			battleState = &snapshot
		}
	}
	return controlResponse{
		Control:  state,
		Recovery: recovery, RecoveryUnavailable: recoveryErr != nil,
		AutomationAvailable: handler.automationExecutor() != nil,
		AutomationActive:    active,
		AutomationMode:      mode,
		AutomationNote:      note,
		AutomationState:     battleState,
	}
}

func (handler *Handler) control(response http.ResponseWriter, _ *http.Request, session *tcpSession) {
	if session == nil || session.gate == nil {
		http.Error(response, "session control unavailable", http.StatusGone)
		return
	}
	handler.writeJSON(response, http.StatusOK, handler.controlSnapshot(session))
}

// controlRequest is shared by takeover/pause/resume. An owner field is
// accepted only to produce a deliberate rejection for old integrations that
// tried to identify an AI owner over HTTP; it is never used for authorization.
type controlRequest struct {
	RecoveryHandle string  `json:"recovery_handle,omitempty"`
	Generation     *uint64 `json:"generation,omitempty"`
	Mode           string  `json:"mode,omitempty"`
	Reason         string  `json:"reason,omitempty"`
	Owner          string  `json:"owner,omitempty"`
}

func decodeControlRequest(response http.ResponseWriter, request *http.Request) (controlRequest, error) {
	var value controlRequest
	if request.Body == nil {
		return value, nil
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		if errors.Is(err, io.EOF) {
			return value, nil
		}
		return controlRequest{}, fmt.Errorf("invalid control request: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return controlRequest{}, errors.New("control request contains multiple JSON values")
		}
		return controlRequest{}, fmt.Errorf("invalid control request: %w", err)
	}
	if owner := strings.ToLower(strings.TrimSpace(value.Owner)); owner != "" && owner != string(aicontrol.Manual) {
		return controlRequest{}, errors.New("external automation owner is not accepted")
	}
	value.Mode = strings.ToLower(strings.TrimSpace(value.Mode))
	value.Reason = strings.TrimSpace(value.Reason)
	return value, nil
}

func controlError(response http.ResponseWriter, err error) {
	status := http.StatusConflict
	switch {
	case errors.Is(err, aicontrol.ErrClosed):
		status = http.StatusGone
	case errors.Is(err, aicontrol.ErrStale):
		status = http.StatusConflict
	case errors.Is(err, aicontrol.ErrMode):
		status = http.StatusBadRequest
	}
	http.Error(response, err.Error(), status)
}

func (handler *Handler) takeover(response http.ResponseWriter, request *http.Request, session *tcpSession) {
	input, err := decodeControlRequest(response, request)
	if err != nil {
		if strings.Contains(err.Error(), "external automation owner") {
			http.Error(response, err.Error(), http.StatusForbidden)
		} else {
			http.Error(response, err.Error(), http.StatusBadRequest)
		}
		return
	}
	if input.RecoveryHandle != "" {
		handler.discardRecoveredAutomation(response, request, session, input)
		return
	}
	_, err = session.gate.Takeover(input.Reason)
	if err != nil {
		controlError(response, err)
		return
	}
	handle := session.clearAutomation(0)
	stopAutomationHandle(handle)
	handler.writeJSON(response, http.StatusOK, handler.controlSnapshot(session))
}

func (handler *Handler) pauseAutomation(response http.ResponseWriter, request *http.Request, session *tcpSession) {
	input, err := decodeControlRequest(response, request)
	if err != nil {
		if strings.Contains(err.Error(), "external automation owner") {
			http.Error(response, err.Error(), http.StatusForbidden)
		} else {
			http.Error(response, err.Error(), http.StatusBadRequest)
		}
		return
	}
	if input.Generation == nil || *input.Generation == 0 {
		http.Error(response, "current control generation is required", http.StatusBadRequest)
		return
	}
	state := session.gate.State()
	if state.Mode != aicontrol.Quest && state.Mode != aicontrol.Leveling && state.Mode != aicontrol.Agent {
		http.Error(response, "automation is not running", http.StatusConflict)
		return
	}
	if _, _, generation := session.automationStatus(); generation == 0 {
		http.Error(response, "automation run is unavailable", http.StatusConflict)
		return
	}
	if _, _, err = session.gate.Switch(*input.Generation, aicontrol.Paused, input.Reason); err != nil {
		controlError(response, err)
		return
	}
	if handle, _, _ := session.automationStatus(); handle != nil {
		if pauser, ok := handle.(automationPauser); ok {
			ctx, cancel := context.WithTimeout(request.Context(), defaultWriteTimeout)
			err = pauser.Pause(ctx)
			cancel()
			if err != nil {
				// Gate has already revoked every outbound action. Failure to
				// confirm the durable pause must not turn a pause request into
				// cancellation or erase an uncertain submitted step.
				http.Error(response, err.Error(), http.StatusBadGateway)
				return
			}
		}
	}
	handler.writeJSON(response, http.StatusOK, handler.controlSnapshot(session))
}

func (handler *Handler) resumeAutomation(response http.ResponseWriter, request *http.Request, session *tcpSession) {
	input, err := decodeControlRequest(response, request)
	if err != nil {
		if strings.Contains(err.Error(), "external automation owner") {
			http.Error(response, err.Error(), http.StatusForbidden)
		} else {
			http.Error(response, err.Error(), http.StatusBadRequest)
		}
		return
	}
	if input.Generation == nil || *input.Generation == 0 {
		http.Error(response, "current control generation is required", http.StatusBadRequest)
		return
	}
	if input.RecoveryHandle != "" {
		handler.resumeRecoveredAutomation(response, request, session, input)
		return
	}
	state := session.gate.State()
	if state.Mode != aicontrol.Paused {
		http.Error(response, "automation is not paused", http.StatusConflict)
		return
	}
	handle, mode, generation := session.automationStatus()
	if handle == nil || generation == 0 {
		http.Error(response, "automation run is unavailable", http.StatusConflict)
		return
	}
	if input.Mode != "" && input.Mode != string(mode) {
		http.Error(response, "automation mode does not match the paused run", http.StatusConflict)
		return
	}
	if mode != aicontrol.Quest && mode != aicontrol.Leveling && mode != aicontrol.Agent {
		http.Error(response, "invalid automation mode", http.StatusBadRequest)
		return
	}
	newState, ctx, err := session.gate.Switch(*input.Generation, mode, input.Reason)
	if err != nil {
		controlError(response, err)
		return
	}
	if resumer, ok := handle.(automationResumer); ok {
		if err = resumer.Resume(ctx); err != nil {
			// A failed reconciliation is not a cancellation. Preserve the
			// checkpoint and its handle for a later observation or explicit
			// takeover, without undoing a concurrent control transition.
			_, _, _ = session.gate.Switch(newState.Generation, aicontrol.Paused, "恢复尚未确认，原任务保持暂停")
			http.Error(response, err.Error(), http.StatusBadGateway)
			return
		}
	}
	if !session.setAutomation(handle, mode, newState.Generation) {
		http.Error(response, "automation control changed during resume", http.StatusConflict)
		return
	}
	handler.writeJSON(response, http.StatusOK, handler.controlSnapshot(session))
}

type automationStartHTTP struct {
	Generation          *uint64                `json:"generation,omitempty"`
	Mode                string                 `json:"mode"`
	Reason              string                 `json:"reason,omitempty"`
	Owner               string                 `json:"owner,omitempty"`
	TaskID              string                 `json:"task_id,omitempty"`
	IncludeDependencies bool                   `json:"include_dependencies,omitempty"`
	SelectedPetID       string                 `json:"selected_pet_id,omitempty"`
	CharacterBuild      *characterbuild.Policy `json:"character_build,omitempty"`
	Targets             []AutomationTarget     `json:"targets,omitempty"`
	TargetPolicy        string                 `json:"target_policy,omitempty"`
	Budget              AutomationBudget       `json:"budget"`
	MaximumSeconds      int                    `json:"maximum_seconds"`
	MaximumDeaths       int                    `json:"maximum_deaths"`
	OfflineContinue     bool                   `json:"offline_continue"`
}

func decodeAutomationStart(response http.ResponseWriter, request *http.Request) (automationStartHTTP, error) {
	var value automationStartHTTP
	if request.Body == nil {
		return value, errors.New("automation start request is required")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return automationStartHTTP{}, fmt.Errorf("invalid automation start request: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return automationStartHTTP{}, errors.New("automation start request contains multiple JSON values")
	}
	if owner := strings.ToLower(strings.TrimSpace(value.Owner)); owner != "" && owner != string(aicontrol.Manual) {
		return automationStartHTTP{}, errors.New("external automation owner is not accepted")
	}
	value.Mode = strings.ToLower(strings.TrimSpace(value.Mode))
	value.Reason = strings.TrimSpace(value.Reason)
	value.TaskID = strings.TrimSpace(value.TaskID)
	value.TargetPolicy = strings.ToLower(strings.TrimSpace(value.TargetPolicy))
	if value.IncludeDependencies && value.Mode != string(aicontrol.Quest) {
		return automationStartHTTP{}, errors.New("自动前置任务仅适用于自动任务模式")
	}
	if err := validateWebCharacterBuild(aicontrol.Mode(value.Mode), value.CharacterBuild); err != nil {
		return automationStartHTTP{}, err
	}
	return value, nil
}

func (handler *Handler) startAutomation(response http.ResponseWriter, request *http.Request, session *tcpSession) {
	input, err := decodeAutomationStart(response, request)
	if err != nil {
		if strings.Contains(err.Error(), "external automation owner") {
			http.Error(response, err.Error(), http.StatusForbidden)
		} else {
			http.Error(response, err.Error(), http.StatusBadRequest)
		}
		return
	}
	if input.Generation == nil || *input.Generation == 0 {
		http.Error(response, "current control generation is required", http.StatusBadRequest)
		return
	}
	var mode aicontrol.Mode
	switch input.Mode {
	case string(aicontrol.Quest):
		mode = aicontrol.Quest
		if input.TaskID == "" {
			http.Error(response, "task_id is required for quest automation", http.StatusBadRequest)
			return
		}
	case string(aicontrol.Leveling):
		mode = aicontrol.Leveling
		if len(input.Targets) == 0 || (input.TargetPolicy != "all" && input.TargetPolicy != "any") {
			http.Error(response, "leveling targets and all/any policy are required", http.StatusBadRequest)
			return
		}
		for _, target := range input.Targets {
			if (target.Kind != "character" && target.Kind != "pet") || target.Level <= 0 || (target.Kind == "pet" && strings.TrimSpace(target.ID) == "") {
				http.Error(response, "leveling targets require stable identity and positive level", http.StatusBadRequest)
				return
			}
		}
	default:
		http.Error(response, "unsupported automation mode", http.StatusBadRequest)
		return
	}
	if input.MaximumSeconds <= 0 || input.MaximumDeaths < 0 || input.Budget.MaximumSpend < 0 || input.Budget.Reserve < 0 {
		http.Error(response, "invalid automation limits or budget", http.StatusBadRequest)
		return
	}
	executor := handler.automationExecutor()
	if executor == nil {
		http.Error(response, errAutomationUnavailable.Error(), http.StatusServiceUnavailable)
		return
	}
	state := session.gate.State()
	if state.Mode != aicontrol.Manual {
		http.Error(response, "character is already under automation control", http.StatusConflict)
		return
	}
	if offer, err := handler.recoveryOffer(request.Context(), session); err != nil {
		http.Error(response, err.Error(), http.StatusServiceUnavailable)
		return
	} else if offer != nil {
		http.Error(response, "请先恢复或取消断线前的自动任务", http.StatusConflict)
		return
	}
	started, runContext, err := session.gate.Switch(*input.Generation, mode, input.Reason)
	if err != nil {
		controlError(response, err)
		return
	}
	config := AutomationConfig{TaskID: input.TaskID, SelectedPetID: input.SelectedPetID, IncludeDependencies: input.IncludeDependencies, CharacterBuild: input.CharacterBuild.Clone(), Targets: append([]AutomationTarget(nil), input.Targets...), TargetPolicy: input.TargetPolicy, Budget: input.Budget, MaximumSeconds: input.MaximumSeconds, MaximumDeaths: input.MaximumDeaths, OfflineContinue: input.OfflineContinue}
	handle, err := executor.Start(runContext, &AutomationSession{ID: session.id, session: session, mode: mode, generation: started.Generation}, AutomationStartRequest{SessionID: session.id, Mode: mode, Generation: started.Generation, Reason: input.Reason, Config: config})
	if err != nil || handle == nil {
		_, _ = session.gate.Takeover("automation failed to start")
		if err == nil {
			err = errors.New("automation executor returned no run")
		}
		http.Error(response, err.Error(), http.StatusBadGateway)
		return
	}
	if !session.setAutomation(handle, mode, started.Generation) {
		stopAutomationHandle(handle)
		http.Error(response, "automation control changed during start", http.StatusConflict)
		return
	}
	activateAutomationHandle(handle)
	handler.writeJSON(response, http.StatusAccepted, handler.controlSnapshot(session))
}

func (handler *Handler) previewAutomation(response http.ResponseWriter, request *http.Request, session *tcpSession) {
	input, err := decodeAutomationStart(response, request)
	if err != nil {
		if strings.Contains(err.Error(), "external automation owner") {
			http.Error(response, err.Error(), http.StatusForbidden)
		} else {
			http.Error(response, err.Error(), http.StatusBadRequest)
		}
		return
	}
	if input.Generation == nil || *input.Generation == 0 {
		http.Error(response, "current control generation is required", http.StatusBadRequest)
		return
	}
	executor := handler.automationExecutor()
	previewer, ok := executor.(automationPreviewer)
	if executor == nil || !ok {
		http.Error(response, "automation budget preview unavailable", http.StatusServiceUnavailable)
		return
	}
	mode := aicontrol.Mode(input.Mode)
	config := AutomationConfig{TaskID: input.TaskID, SelectedPetID: input.SelectedPetID, IncludeDependencies: input.IncludeDependencies, CharacterBuild: input.CharacterBuild.Clone(), Targets: append([]AutomationTarget(nil), input.Targets...), TargetPolicy: input.TargetPolicy, Budget: input.Budget, MaximumSeconds: input.MaximumSeconds, MaximumDeaths: input.MaximumDeaths, OfflineContinue: input.OfflineContinue}
	value, err := previewer.Preview(request.Context(), &AutomationSession{ID: session.id, session: session, mode: mode, generation: *input.Generation}, AutomationStartRequest{SessionID: session.id, Mode: mode, Generation: *input.Generation, Reason: input.Reason, Config: config})
	if err != nil {
		http.Error(response, err.Error(), http.StatusBadGateway)
		return
	}
	handler.writeJSON(response, http.StatusOK, value)
}

type sendRequest struct {
	Packet     string  `json:"packet"`
	Generation *uint64 `json:"generation,omitempty"`
	Owner      string  `json:"owner,omitempty"`
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
		owner := strings.ToLower(strings.TrimSpace(encoded.Owner))
		// Browser HTTP is always the human owner. In particular, accepting an
		// owner=AI/agent field would let an external caller impersonate an
		// executor without going through the injected Automation boundary.
		if owner != "" && owner != string(aicontrol.Manual) {
			http.Error(response, "external automation owner is not accepted", http.StatusForbidden)
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
	generation := uint64(0)
	if encoded.Generation != nil {
		generation = *encoded.Generation
	}
	// A missing/zero generation is the old browser protocol. It remains valid
	// only for an untouched generation-1 manual session; once control changes,
	// every request must carry the current fencing token explicitly.
	if generation == 0 {
		state := session.gate.State()
		if state.Generation != 1 || state.Mode != aicontrol.Manual {
			http.Error(response, "control generation is required", http.StatusConflict)
			return
		}
		generation = 1
	}
	var writeErr error
	err = session.dispatch(request.Context(), generation, aicontrol.Manual, func(context.Context) error {
		writeErr = session.write(packet)
		return writeErr
	})
	if writeErr != nil {
		// write() deliberately leaves session finalization to this caller. That
		// keeps a write failure from calling Gate.Close while Dispatch still
		// owns Gate's mutex.
		session.finish(writeErr.Error())
		err = writeErr
	}
	if err != nil {
		if errors.Is(err, net.ErrClosed) || errors.Is(err, aicontrol.ErrClosed) {
			http.Error(response, "session is closed", http.StatusGone)
		} else if errors.Is(err, aicontrol.ErrStale) || errors.Is(err, aicontrol.ErrOwner) {
			http.Error(response, "control generation or owner is no longer valid", http.StatusConflict)
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
	Seq    uint64 `json:"seq,omitempty"`
}

func parseEventsAck(request *http.Request) (uint64, bool, error) {
	values, present := request.URL.Query()["ack"]
	if !present {
		return 0, false, nil
	}
	if len(values) != 1 {
		return 0, true, errors.New("ack must appear exactly once")
	}
	raw := strings.TrimSpace(values[0])
	if raw == "" {
		return 0, true, errors.New("ack must be a non-negative uint64")
	}
	ack, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, true, errors.New("ack must be a non-negative uint64")
	}
	return ack, true, nil
}

func (handler *Handler) pollEvents(response http.ResponseWriter, request *http.Request, session *tcpSession) {
	ack, reliable, err := parseEventsAck(request)
	if err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	timeout := handler.config.PollTimeout
	if raw := strings.TrimSpace(request.URL.Query().Get("timeout")); raw != "" {
		milliseconds, err := strconv.Atoi(raw)
		if err != nil || milliseconds < 1 || milliseconds > 60000 {
			http.Error(response, "timeout must be 1..60000 milliseconds", http.StatusBadRequest)
			return
		}
		timeout = time.Duration(milliseconds) * time.Millisecond
	}
	var events []packetEvent
	if reliable {
		events, err = session.pollReliableContext(request.Context(), timeout, ack)
	} else {
		events, err = session.pollContext(request.Context(), timeout)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// A canceled long poll is only one lost HTTP request.  Keep the
			// session and its TCP stream alive so a browser/proxy retry cannot
			// turn a transient map-transition cancellation into a logout.
			return
		}
		if errors.Is(err, errEventsAckAhead) {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(response, err.Error(), http.StatusConflict)
		return
	}
	encoded := make([]eventResponse, 0, len(events))
	for _, event := range events {
		item := eventResponse{Closed: event.closed, Error: event.err}
		if reliable {
			item.Seq = event.seq
		}
		if len(event.packet) > 0 {
			item.Packet = base64.StdEncoding.EncodeToString(event.packet)
		}
		encoded = append(encoded, item)
	}
	// Returning closed=true alongside a queued event lets a client skip the
	// event list and lose the final packets.  Only advertise the terminal
	// state once this response has no events left to deliver.
	closed := session.isClosed()
	if reliable {
		closed = session.reliableClosed()
	}
	body := map[string]any{
		"events":  encoded,
		"closed":  closed,
		"control": handler.controlSnapshot(session),
	}
	if reliable {
		body["acknowledged"] = true
	}
	handler.writeJSON(response, http.StatusOK, body)
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
	agentListener, err := handler.StartAgentListener(config.AgentSocketPath)
	if err != nil {
		return err
	}
	if agentListener != nil {
		defer agentListener.Close()
	}
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
