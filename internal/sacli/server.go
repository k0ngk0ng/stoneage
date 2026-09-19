package sacli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aigame"
	"github.com/k0ngk0ng/stoneage/internal/aiknowledge"
	"github.com/k0ngk0ng/stoneage/internal/ainavigation"
	"github.com/k0ngk0ng/stoneage/internal/aiplanner"
)

// BuildVersion is the release tag this binary was built from; main sets
// it so the daemon can report it. Archived clients carry their tag.
var BuildVersion = "dev"

// DefaultRequestTimeout bounds one CLI command so a terminal agent never
// hangs on a wedged session. `wait` sets its own deadline.
const DefaultRequestTimeout = 60 * time.Second

// Server owns the game session and answers CLI requests on a private socket.
type Server struct {
	config Config
	events *eventLog

	mu         sync.Mutex
	game       Game
	generation uint64
	lastError  string
	listener   net.Listener
	stopping   bool
	// character is the character this daemon keeps entered. It starts from
	// the config and is updated by `enter`, so a reconnect returns to the
	// world instead of stopping at the character list.
	character string

	navigationOnce sync.Once
	navigationMu   sync.Mutex
	navigation     *ainavigation.Navigator
	navigationErr  error

	knowledgeOnce sync.Once
	knowledgeData *aiknowledge.Knowledge
	knowledgeErr  error

	warpOnce sync.Once
	warps    *aiplanner.WarpGraph
	warpErr  error

	// The auto battle loop runs alongside the command socket, so its state is
	// kept separate from mu: commands must keep answering while it runs.
	autoMu       sync.Mutex
	autoRunning  bool
	autoCancel   context.CancelFunc
	autoLastLine string

	recoveryOnce sync.Once
	recoveryData *aiknowledge.RecoveryTables
	recoveryErr  error
}

// NewServer builds an idle daemon. It connects on first use so that a
// failing login is reported to the command that needed it.
func NewServer(config Config) *Server {
	return &Server{config: config, events: newEventLog(512), character: config.Character}
}

// desiredCharacter returns the character that must be entered after every
// (re)connect.
func (s *Server) desiredCharacter() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.character
}

// setDesiredCharacter remembers the character for this daemon's lifetime.
func (s *Server) setDesiredCharacter(name string) {
	s.mu.Lock()
	s.character = name
	s.mu.Unlock()
}

// Serve listens on the configured Unix socket until ctx is cancelled or a
// `stop` request arrives.
func (s *Server) Serve(ctx context.Context) error {
	listener, err := s.listen()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()
	defer func() {
		_ = listener.Close()
		s.shutdown()
	}()
	s.preloadMaps()

	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || s.isStopping() {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) listen() (net.Listener, error) {
	path := s.config.SocketPath
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create socket directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if conn, dialErr := net.DialTimeout("unix", path, 200*time.Millisecond); dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("another sactl daemon is already listening on %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("set socket permissions: %w", err)
	}
	return listener, nil
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Minute))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var request Request
	if err := json.Unmarshal(line, &request); err != nil {
		_ = writeResponse(conn, failure(KindUsage, "invalid request: %v", err))
		return
	}
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response := s.Dispatch(requestCtx, request)
	_ = writeResponse(conn, response)
}

func writeResponse(conn net.Conn, response Response) error {
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(payload, '\n'))
	return err
}

func (s *Server) isStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping
}

// Stop ends the daemon after the current response is written.
func (s *Server) Stop() {
	s.mu.Lock()
	s.stopping = true
	listener := s.listener
	s.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
}

func (s *Server) shutdown() {
	s.stopAutoBattle()
	s.mu.Lock()
	game := s.game
	s.game = nil
	s.mu.Unlock()
	if game != nil {
		_ = game.Close()
	}
}

// session returns the live game session, connecting on first use. A failed
// login is reported verbatim; swallowing that error is what made the
// previous AI chain hard to debug.
func (s *Server) session(ctx context.Context) (Game, error) {
	s.mu.Lock()
	if s.game != nil {
		game := s.game
		s.mu.Unlock()
		return game, nil
	}
	s.mu.Unlock()

	// Connect outside the lock: dialing and entering the world takes seconds
	// and must not block `status` from reporting the attempt.
	game, err := connect(ctx, s.config, s.desiredCharacter())
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastError = err.Error()
		return nil, err
	}
	if s.game != nil { // another command connected first
		_ = game.Close()
		return s.game, nil
	}
	s.game = game
	s.generation++
	s.lastError = ""
	go s.pump(game, s.generation)
	return game, nil
}

// pump copies server events into the ring buffer and marks the session
// disconnected when the connection ends.
func (s *Server) pump(game Game, generation uint64) {
	for event := range game.Events() {
		s.events.append(event)
	}
	s.mu.Lock()
	if s.generation == generation && s.game == game {
		s.game = nil
		s.lastError = "game session closed"
	}
	s.mu.Unlock()
}

func (s *Server) recordError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.lastError = err.Error()
	s.mu.Unlock()
}

// snapshot observes the current session state.
func (s *Server) snapshot(ctx context.Context) (aigame.Snapshot, error) {
	game, err := s.session(ctx)
	if err != nil {
		return aigame.Snapshot{}, err
	}
	snapshot, err := game.Observe(ctx)
	if err != nil {
		s.recordError(err)
		return aigame.Snapshot{}, err
	}
	return snapshot, nil
}

// navigator lazily loads the 2.5 map data used by `goto`.
func (s *Server) navigator() (*ainavigation.Navigator, error) {
	if strings.TrimSpace(s.config.MapDirectory) == "" {
		return nil, fmt.Errorf("routing needs the server's 2.5 data directory: set map_directory in the sactl config (for example a copy of the deployment's data/ or the repository's runtime/legacy-server/gmsv/data)")
	}
	s.navigationOnce.Do(func() {
		navigator, err := ainavigation.LoadPath(s.config.MapDirectory)
		s.navigationMu.Lock()
		s.navigation, s.navigationErr = navigator, err
		s.navigationMu.Unlock()
	})
	s.navigationMu.Lock()
	defer s.navigationMu.Unlock()
	if s.navigationErr != nil {
		return nil, fmt.Errorf("load maps from %s: %w", s.config.MapDirectory, s.navigationErr)
	}
	return s.navigation, nil
}

// preloadMaps loads the map data in the background so that a later
// observation can name the current floor without a first-call stall.
func (s *Server) preloadMaps() {
	go func() { _, _ = s.navigator() }()
}

// floorName returns the map display name for a floor, or "" when the map data
// is not loaded yet. The LS2MAP label is CP936 bytes in the server's own
// "name|field" format; both are handled here so callers see plain UTF-8.
func (s *Server) floorName(floor int) string {
	if floor < 0 {
		return ""
	}
	s.navigationMu.Lock()
	navigator := s.navigation
	s.navigationMu.Unlock()
	if navigator == nil {
		return ""
	}
	floorMap, ok := navigator.Floor(floor)
	if !ok {
		return ""
	}
	label := floorMap.ShowString
	if index := strings.IndexByte(label, '|'); index >= 0 {
		label = label[:index]
	}
	return strings.TrimSpace(decodeLegacy([]byte(label)))
}

func (s *Server) statusReport() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := "connected"
	if s.game == nil {
		state = "not connected"
	}
	report := fmt.Sprintf("version: %s\nsession: %s\naccount: %s\ncharacter: %s\nsocket: %s\naddress: %s",
		BuildVersion, state, s.config.Account, s.config.Character, s.config.SocketPath, s.config.Address)
	if s.lastError != "" {
		report += "\nlast error: " + s.lastError
	}
	return report
}
