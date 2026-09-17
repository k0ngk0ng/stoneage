package airemote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/aibroker"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

var (
	remoteIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	remoteNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
)

var (
	ErrUnauthorized    = errors.New("airemote: worker is not authorized")
	ErrWorkerOffline   = errors.New("airemote: worker is offline")
	ErrProtocol        = errors.New("airemote: invalid worker protocol")
	ErrCommandUnknown  = errors.New("airemote: command is unknown")
	ErrCommandConflict = errors.New("airemote: command conflicts with an existing command")
)

// Enrollment binds one profile to one worker. TokenHash is preferred; Token is
// accepted only at registration so callers can avoid duplicating hash logic.
// The clear token is never copied into Hub's persistent Store.
type Enrollment struct {
	TokenHash string
	Token     string
	ExpiresAt time.Time
	Guard     func(context.Context, string) error
	Start     func(context.Context, string) error
	// PublicBaseURL is the profile-scoped control-plane origin used by MCP.
	PublicBaseURL string
}

// Config controls the transport. Docker is the legacy/local fallback used for
// profiles without a remote enrollment. GatewayURL is a fixed production
// endpoint used by the worker's MCP requests; it is never supplied by a job.
type Config struct {
	Docker     aibroker.Docker
	Fallback   aibroker.Docker
	Store      Store
	StatePath  string
	GatewayURL string
	// PublicBaseURL is the externally reachable control-plane origin used by
	// the worker runtime to reach the profile-scoped game proxy.
	PublicBaseURL string
	GameProxyURL  string
	Guard         func(context.Context, string) error
	Start         func(context.Context, string) error
	GameAuthorize func(context.Context, string, string) bool
	Now           func() time.Time
	PollTimeout   time.Duration
	LeaseTimeout  time.Duration
	// CommandTimeout bounds management commands, not model turns. Runs use
	// their caller's deadline, or a 30-minute fallback when none is supplied.
	CommandTimeout  time.Duration
	MaxPayloadBytes int
	MaxOutputBytes  int
}

type profileBinding struct {
	id            string
	tokenHash     string
	expiresAt     time.Time
	consumed      bool
	workerID      string
	sessionHash   string
	epoch         uint64
	publicBaseURL string
	guard         func(context.Context, string) error
	start         func(context.Context, string) error
	connecting    bool
}

type workerSession struct {
	id             string
	profileID      string
	epoch          uint64
	sessionHash    string
	connectedAt    time.Time
	lastSeenAt     time.Time
	leaseUntil     time.Time
	queue          chan *Command
	wake           chan struct{}
	closed         chan struct{}
	inFlight       string
	startRequested bool
	startRunning   bool
	startError     string
}

type commandWait struct {
	cmd  *Command
	done chan commandOutcome
}

type commandOutcome struct {
	result ResultRequest
	err    error
}

// Hub implements aibroker.Docker while routing each enrolled profile to its
// worker. A route remains remote once created; it never silently falls back to
// Docker after a worker disconnects.
type Hub struct {
	cfg        Config
	store      Store
	mu         sync.Mutex
	profiles   map[string]*profileBinding
	workers    map[string]*workerSession
	routes     map[string]RouteRecord
	commands   map[string]*commandWait
	gameClient *http.Client
	closed     bool
}

func New(cfg Config) (*Hub, error) {
	if cfg.Docker == nil {
		cfg.Docker = cfg.Fallback
	}
	if cfg.Store == nil {
		if strings.TrimSpace(cfg.StatePath) != "" {
			cfg.Store = NewFileStore(cfg.StatePath)
		} else {
			cfg.Store = NewMemoryStore()
		}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.PollTimeout <= 0 || cfg.PollTimeout > 90*time.Second {
		cfg.PollTimeout = 30 * time.Second
	}
	if cfg.LeaseTimeout <= 0 || cfg.LeaseTimeout > 5*time.Minute {
		cfg.LeaseTimeout = 90 * time.Second
	}
	if cfg.CommandTimeout <= 0 || cfg.CommandTimeout > 5*time.Minute {
		cfg.CommandTimeout = 2 * time.Minute
	}
	if cfg.MaxPayloadBytes <= 0 || cfg.MaxPayloadBytes > 8<<20 {
		cfg.MaxPayloadBytes = 1 << 20
	}
	if cfg.MaxOutputBytes <= 0 || cfg.MaxOutputBytes > 32<<20 {
		cfg.MaxOutputBytes = 8 << 20
	}
	if cfg.GameProxyURL == "" {
		cfg.GameProxyURL = cfg.GatewayURL
	}
	if cfg.GameProxyURL != "" && !validHTTPURL(cfg.GameProxyURL) {
		return nil, fmt.Errorf("%w: game gateway URL", aibroker.ErrInvalidConfig)
	}
	h := &Hub{cfg: cfg, store: cfg.Store, profiles: make(map[string]*profileBinding), workers: make(map[string]*workerSession), routes: make(map[string]RouteRecord), commands: make(map[string]*commandWait), gameClient: &http.Client{Timeout: 15 * time.Second}}
	h.gameClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	snapshot, err := h.store.Load(context.Background())
	if err != nil {
		return nil, fmt.Errorf("%w: load worker state", aibroker.ErrInvalidConfig)
	}
	seenRoutes := make(map[string]struct{}, len(snapshot.Routes))
	for _, route := range snapshot.Routes {
		if !validRouteRecord(route) {
			return nil, fmt.Errorf("%w: corrupt worker route", aibroker.ErrInvalidConfig)
		}
		if _, exists := seenRoutes[route.ContainerName]; exists {
			return nil, fmt.Errorf("%w: duplicate worker route", aibroker.ErrInvalidConfig)
		}
		seenRoutes[route.ContainerName] = struct{}{}
		h.routes[route.ContainerName] = route
	}
	seenProfiles := make(map[string]struct{}, len(snapshot.Profiles))
	for _, record := range snapshot.Profiles {
		if !validProfileRecord(record) {
			return nil, fmt.Errorf("%w: corrupt worker profile binding", aibroker.ErrInvalidConfig)
		}
		if _, exists := seenProfiles[record.ProfileID]; exists {
			return nil, fmt.Errorf("%w: duplicate worker profile", aibroker.ErrInvalidConfig)
		}
		seenProfiles[record.ProfileID] = struct{}{}
		h.profiles[record.ProfileID] = &profileBinding{id: record.ProfileID, tokenHash: record.TokenHash, consumed: record.Consumed, workerID: record.WorkerID, sessionHash: record.SessionHash, epoch: record.Epoch, expiresAt: record.ExpiresAt, publicBaseURL: record.PublicBaseURL, guard: cfg.Guard, start: cfg.Start}
	}
	return h, nil
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func validRouteRecord(route RouteRecord) bool {
	if !validRemoteID(route.ProfileID) || !validRemoteID(route.WorkerID) || !validRemoteID(route.RequestID) || !validRemoteName(route.ContainerName) || (route.VolumeName != "" && !validRemoteName(route.VolumeName)) || !validHash(route.PayloadHash) || route.UpdatedAt.IsZero() {
		return false
	}
	switch route.State {
	case string(aibroker.RunRunning), string(aibroker.RunCompleted), string(aibroker.RunUnknown), string(aibroker.DockerContainerExited), string(aibroker.DockerContainerDead), "stopped", "removed":
		return true
	}
	return false
}
func validProfileRecord(record ProfileRecord) bool {
	if !validRemoteID(record.ProfileID) || !validHash(record.TokenHash) {
		return false
	}
	if !record.Consumed {
		return record.WorkerID == "" && record.SessionHash == "" && record.Epoch == 0
	}
	return validRemoteID(record.WorkerID) && validHash(record.SessionHash) && record.Epoch > 0
}

func (h *Hub) RegisterProfile(ctx context.Context, profileID string, enrollment Enrollment) error {
	if h == nil {
		return aibroker.ErrInvalidConfig
	}
	if !validRemoteID(profileID) {
		return fmt.Errorf("%w: profile ID", aibroker.ErrInvalidRequest)
	}
	hash := strings.TrimSpace(enrollment.TokenHash)
	if hash == "" && enrollment.Token != "" {
		hash = tokenHash(enrollment.Token)
	}
	if hash == "" {
		h.mu.Lock()
		if existing := h.profiles[profileID]; existing != nil {
			hash = existing.tokenHash
		}
		h.mu.Unlock()
	}
	if len(hash) != 64 {
		return fmt.Errorf("%w: enrollment token hash", aibroker.ErrInvalidConfig)
	}
	now := h.now()
	if !enrollment.ExpiresAt.IsZero() && !enrollment.ExpiresAt.After(now) {
		return fmt.Errorf("%w: enrollment expired", aibroker.ErrInvalidRequest)
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return aibroker.ErrClosed
	}
	old := h.profiles[profileID]
	binding := &profileBinding{id: profileID, tokenHash: hash, expiresAt: enrollment.ExpiresAt, guard: enrollment.Guard, start: enrollment.Start, publicBaseURL: strings.TrimRight(enrollment.PublicBaseURL, "/")}
	if binding.guard == nil {
		binding.guard = h.cfg.Guard
	}
	if binding.start == nil {
		binding.start = h.cfg.Start
	}
	if old != nil && old.consumed {
		// Re-registering after a server restart must preserve the fenced session
		// and remote location. The clear enrollment token is never replaced.
		binding.consumed, binding.workerID, binding.sessionHash, binding.epoch, binding.publicBaseURL = old.consumed, old.workerID, old.sessionHash, old.epoch, old.publicBaseURL
		binding.tokenHash = old.tokenHash
	}
	h.profiles[profileID] = binding
	snapshot := h.snapshotLocked()
	if err := h.store.Save(ctx, snapshot); err != nil {
		if old != nil {
			h.profiles[profileID] = old
		} else {
			delete(h.profiles, profileID)
		}
		h.mu.Unlock()
		return fmt.Errorf("%w: persist enrollment", aibroker.ErrDocker)
	}
	h.mu.Unlock()
	return nil
}

func (h *Hub) UnregisterProfile(ctx context.Context, profileID string) error {
	if h == nil {
		return aibroker.ErrInvalidConfig
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return aibroker.ErrClosed
	}
	old := h.profiles[profileID]
	delete(h.profiles, profileID)
	if err := h.store.Save(ctx, h.snapshotLocked()); err != nil {
		if old != nil {
			h.profiles[profileID] = old
		}
		h.mu.Unlock()
		return fmt.Errorf("%w: persist profile removal", aibroker.ErrDocker)
	}
	h.mu.Unlock()
	return nil
}

// Invitation is a one-time enrollment credential. Callers must render Token
// only in the create response/command and never put it in a profile record.
type Invitation struct {
	ProfileID string    `json:"profile_id"`
	Token     string    `json:"token,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
	Endpoint  string    `json:"endpoint,omitempty"`
	Reconnect bool      `json:"reconnect,omitempty"`
}

func (h *Hub) Invite(ctx context.Context, profileID, publicBaseURL string) (Invitation, error) {
	if h == nil {
		return Invitation{}, aibroker.ErrInvalidConfig
	}
	if !validRemoteID(profileID) {
		return Invitation{}, fmt.Errorf("%w: profile ID", aibroker.ErrInvalidRequest)
	}
	endpoint := strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	if endpoint != "" {
		endpoint += "/api/ai/worker"
	}
	h.mu.Lock()
	if existing := h.profiles[profileID]; existing != nil && existing.consumed {
		// A connected worker owns the consumed enrollment and must keep using
		// its persisted session token. If it is offline, the old token cannot
		// be recovered (only its hash is stored), so rotate the enrollment and
		// issue a fresh token for a new local volume.
		if worker := h.workers[existing.workerID]; worker != nil && h.onlineLocked(worker) {
			status := Invitation{ProfileID: profileID, Endpoint: endpoint, Reconnect: true}
			if endpoint == "" {
				status.Endpoint = strings.TrimRight(existing.publicBaseURL, "/") + "/api/ai/worker"
			}
			h.mu.Unlock()
			return status, nil
		}
		secret, err := randomToken(32)
		if err != nil {
			h.mu.Unlock()
			return Invitation{}, err
		}
		expires := h.now().Add(30 * time.Minute)
		binding := *existing
		binding.tokenHash = tokenHash(secret)
		binding.expiresAt = expires
		binding.consumed = false
		binding.workerID = ""
		binding.sessionHash = ""
		binding.epoch++
		binding.connecting = false
		if endpoint != "" {
			binding.publicBaseURL = strings.TrimSuffix(endpoint, "/api/ai/worker")
		}
		h.profiles[profileID] = &binding
		if err := h.store.Save(ctx, h.snapshotLocked()); err != nil {
			h.profiles[profileID] = existing
			h.mu.Unlock()
			return Invitation{}, fmt.Errorf("%w: persist enrollment rotation", aibroker.ErrDocker)
		}
		h.mu.Unlock()
		return Invitation{ProfileID: profileID, Token: secret, ExpiresAt: expires, Endpoint: endpoint}, nil
	}
	h.mu.Unlock()
	secret, err := randomToken(32)
	if err != nil {
		return Invitation{}, err
	}
	expires := h.now().Add(30 * time.Minute)
	if err := h.RegisterProfile(ctx, profileID, Enrollment{Token: secret, ExpiresAt: expires, PublicBaseURL: strings.TrimRight(publicBaseURL, "/")}); err != nil {
		return Invitation{}, err
	}
	return Invitation{ProfileID: profileID, Token: secret, ExpiresAt: expires, Endpoint: endpoint}, nil
}
func (h *Hub) now() time.Time {
	if h != nil && h.cfg.Now != nil {
		return h.cfg.Now().UTC()
	}
	return time.Now().UTC()
}

func (h *Hub) profileFromSpec(spec aibroker.RunSpec) string {
	if value := spec.Environment["STONEAGE_AI_PROFILE_ID"]; validRemoteID(value) {
		return value
	}
	for i := 0; i+1 < len(spec.Command); i++ {
		if (spec.Command[i] == "-profile" || spec.Command[i] == "-profile-id") && validRemoteID(spec.Command[i+1]) {
			return spec.Command[i+1]
		}
	}
	return ""
}

func (h *Hub) profileMode(profileID string) (remote, connecting bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	binding, ok := h.profiles[profileID]
	if !ok {
		return false, false
	}
	return binding.consumed, binding.connecting
}
func (h *Hub) isRemoteProfile(profileID string) bool {
	remote, _ := h.profileMode(profileID)
	return remote
}

func (h *Hub) Run(ctx context.Context, spec aibroker.RunSpec, payload []byte) (aibroker.DockerResult, error) {
	profileID := h.profileFromSpec(spec)
	remote, connecting := h.profileMode(profileID)
	if connecting {
		return aibroker.DockerResult{}, aibroker.ErrProfileBusy
	}
	if profileID == "" || !remote {
		return h.fallbackRun(ctx, spec, payload)
	}
	if len(payload) > h.cfg.MaxPayloadBytes {
		return aibroker.DockerResult{}, aibroker.ErrRequestTooLarge
	}
	requestID, payloadHash := requestIdentity(payload)
	if requestID == "" {
		return aibroker.DockerResult{}, fmt.Errorf("%w: request identity", aibroker.ErrInvalidRequest)
	}
	worker, err := h.currentWorker(profileID)
	if err != nil {
		return aibroker.DockerResult{}, err
	}
	command := &Command{ProtocolVersion: ProtocolVersion, CommandID: newID("cmd"), Kind: KindRun, WorkerID: worker.id, WorkerEpoch: worker.epoch, ProfileID: profileID, RequestID: requestID, ContainerName: spec.ContainerName, VolumeName: spec.VolumeName, PayloadHash: payloadHash, Payload: append([]byte(nil), payload...), CreatedAt: h.now()}
	if h.cfg.GameProxyURL != "" {
		command.GameEndpoint = h.gameEndpoint(profileID)
	}
	if !validRemoteName(spec.ContainerName) {
		return aibroker.DockerResult{}, fmt.Errorf("%w: container name", aibroker.ErrInvalidRequest)
	}
	route := RouteRecord{ProfileID: profileID, WorkerID: worker.id, WorkerEpoch: worker.epoch, RequestID: requestID, ContainerName: spec.ContainerName, VolumeName: spec.VolumeName, PayloadHash: payloadHash, State: string(aibroker.RunRunning), UpdatedAt: h.now()}
	if err := h.saveRoute(route); err != nil {
		return aibroker.DockerResult{}, err
	}
	wait, err := h.enqueue(worker, command)
	if err != nil {
		h.updateRouteState(spec.ContainerName, string(aibroker.RunUnknown))
		return aibroker.DockerResult{}, err
	}
	outcome := h.wait(ctx, wait)
	if outcome.err != nil {
		return aibroker.DockerResult{}, outcome.err
	}
	if outcome.result.NotFound {
		return aibroker.DockerResult{}, aibroker.ErrContainerNotFound
	}
	if outcome.result.Error != "" {
		return aibroker.DockerResult{}, aibroker.ErrDocker
	}
	if outcome.result.PayloadHash != "" && outcome.result.PayloadHash != payloadHash {
		return aibroker.DockerResult{}, aibroker.ErrDocker
	}
	if err := h.updateRouteState(spec.ContainerName, outcome.result.State); err != nil {
		return aibroker.DockerResult{}, err
	}
	return aibroker.DockerResult{ExitCode: outcome.result.ExitCode, Stdout: clamp(outcome.result.Stdout, h.cfg.MaxOutputBytes), Stderr: clamp(outcome.result.Stderr, h.cfg.MaxOutputBytes)}, nil
}

func (h *Hub) fallbackRun(ctx context.Context, spec aibroker.RunSpec, payload []byte) (aibroker.DockerResult, error) {
	if h.cfg.Docker == nil {
		return aibroker.DockerResult{}, aibroker.ErrDocker
	}
	return h.cfg.Docker.Run(ctx, spec, payload)
}

func (h *Hub) Stop(ctx context.Context, containerName string) error {
	route, remote, err := h.routeForContainer(containerName)
	if err != nil {
		return err
	}
	if !remote {
		if h.cfg.Docker == nil {
			return aibroker.ErrDocker
		}
		return h.cfg.Docker.Stop(ctx, containerName)
	}
	worker, err := h.workerForRoute(route)
	if err != nil {
		return err
	}
	wait, err := h.enqueue(worker, &Command{ProtocolVersion: ProtocolVersion, CommandID: newID("cmd"), Kind: KindStop, WorkerID: worker.id, WorkerEpoch: worker.epoch, ProfileID: route.ProfileID, RequestID: route.RequestID, ContainerName: route.ContainerName, VolumeName: route.VolumeName, PayloadHash: route.PayloadHash, CreatedAt: h.now()})
	if err != nil {
		return err
	}
	outcome := h.wait(ctx, wait)
	if outcome.err != nil {
		return outcome.err
	}
	if outcome.result.Error != "" {
		return aibroker.ErrDocker
	}
	return nil
}

func (h *Hub) Inspect(ctx context.Context, containerName string) (aibroker.DockerContainerState, error) {
	route, remote, err := h.routeForContainer(containerName)
	if err != nil {
		return "", err
	}
	if !remote {
		if inspector, ok := h.cfg.Docker.(aibroker.DockerInspector); ok {
			return inspector.Inspect(ctx, containerName)
		}
		return "", aibroker.ErrDocker
	}
	worker, err := h.workerForRoute(route)
	if err != nil {
		return "", err
	}
	wait, err := h.enqueue(worker, &Command{ProtocolVersion: ProtocolVersion, CommandID: newID("cmd"), Kind: KindInspect, WorkerID: worker.id, WorkerEpoch: worker.epoch, ProfileID: route.ProfileID, RequestID: route.RequestID, ContainerName: route.ContainerName, VolumeName: route.VolumeName, PayloadHash: route.PayloadHash, CreatedAt: h.now()})
	if err != nil {
		return "", err
	}
	outcome := h.wait(ctx, wait)
	if outcome.err != nil {
		return "", outcome.err
	}
	if outcome.result.NotFound {
		return "", aibroker.ErrContainerNotFound
	}
	if outcome.result.Error != "" {
		return "", aibroker.ErrDocker
	}
	state := dockerState(outcome.result.State)
	if state == "" {
		return "", aibroker.ErrDocker
	}
	return state, nil
}

func (h *Hub) ReadResult(ctx context.Context, containerName string) (aibroker.DockerResult, error) {
	route, remote, err := h.routeForContainer(containerName)
	if err != nil {
		return aibroker.DockerResult{}, err
	}
	if !remote {
		if reader, ok := h.cfg.Docker.(aibroker.DockerResultReader); ok {
			return reader.ReadResult(ctx, containerName)
		}
		return aibroker.DockerResult{}, aibroker.ErrDocker
	}
	worker, err := h.workerForRoute(route)
	if err != nil {
		return aibroker.DockerResult{}, err
	}
	wait, err := h.enqueue(worker, &Command{ProtocolVersion: ProtocolVersion, CommandID: newID("cmd"), Kind: KindReadResult, WorkerID: worker.id, WorkerEpoch: worker.epoch, ProfileID: route.ProfileID, RequestID: route.RequestID, ContainerName: route.ContainerName, VolumeName: route.VolumeName, PayloadHash: route.PayloadHash, CreatedAt: h.now()})
	if err != nil {
		return aibroker.DockerResult{}, err
	}
	outcome := h.wait(ctx, wait)
	if outcome.err != nil {
		return aibroker.DockerResult{}, outcome.err
	}
	if outcome.result.NotFound {
		return aibroker.DockerResult{}, aibroker.ErrContainerNotFound
	}
	if outcome.result.Error != "" {
		return aibroker.DockerResult{}, aibroker.ErrDocker
	}
	return aibroker.DockerResult{ExitCode: outcome.result.ExitCode, Stdout: clamp(outcome.result.Stdout, h.cfg.MaxOutputBytes), Stderr: clamp(outcome.result.Stderr, h.cfg.MaxOutputBytes)}, nil
}

func (h *Hub) Remove(ctx context.Context, containerName string) error {
	route, remote, err := h.routeForContainer(containerName)
	if err != nil {
		return err
	}
	if !remote {
		if remover, ok := h.cfg.Docker.(aibroker.DockerRemover); ok {
			return remover.Remove(ctx, containerName)
		}
		return nil
	}
	worker, err := h.workerForRoute(route)
	if err != nil {
		return err
	}
	wait, err := h.enqueue(worker, &Command{ProtocolVersion: ProtocolVersion, CommandID: newID("cmd"), Kind: KindRemove, WorkerID: worker.id, WorkerEpoch: worker.epoch, ProfileID: route.ProfileID, RequestID: route.RequestID, ContainerName: route.ContainerName, VolumeName: route.VolumeName, PayloadHash: route.PayloadHash, CreatedAt: h.now()})
	if err != nil {
		return err
	}
	outcome := h.wait(ctx, wait)
	if outcome.err != nil {
		return outcome.err
	}
	if outcome.result.Error != "" && !outcome.result.NotFound {
		return aibroker.ErrDocker
	}
	return nil
}

func (h *Hub) RemoveVolume(ctx context.Context, volumeName string) error {
	if h.cfg.Docker != nil {
		if remover, ok := h.cfg.Docker.(aibroker.DockerVolumeRemover); ok {
			return remover.RemoveVolume(ctx, volumeName)
		}
	}
	return aibroker.ErrDocker
}

func (h *Hub) currentWorker(profileID string) (*workerSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	binding := h.profiles[profileID]
	if binding == nil {
		return nil, ErrWorkerOffline
	}
	worker := h.workers[binding.workerID]
	if worker == nil || h.workers[binding.workerID] != worker || worker.profileID != profileID || worker.epoch != binding.epoch || !h.onlineLocked(worker) {
		return nil, ErrWorkerOffline
	}
	return worker, nil
}

func (h *Hub) workerForRoute(route RouteRecord) (*workerSession, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	worker := h.workers[route.WorkerID]
	if worker == nil || h.workers[route.WorkerID] != worker || worker.profileID != route.ProfileID || worker.epoch != route.WorkerEpoch || !h.onlineLocked(worker) {
		return nil, aibroker.ErrDocker
	}
	return worker, nil
}

func (h *Hub) onlineLocked(worker *workerSession) bool {
	return worker != nil && worker.leaseUntil.After(h.now())
}

func (h *Hub) routeForContainer(containerName string) (RouteRecord, bool, error) {
	if !validRemoteName(containerName) {
		return RouteRecord{}, false, fmt.Errorf("%w: container name", aibroker.ErrInvalidRequest)
	}
	h.mu.Lock()
	route, ok := h.routes[containerName]
	h.mu.Unlock()
	if ok {
		return route, true, nil
	}
	return RouteRecord{ContainerName: containerName}, false, nil
}

func (h *Hub) enqueue(worker *workerSession, command *Command) (*commandWait, error) {
	if worker == nil || command == nil {
		return nil, aibroker.ErrDocker
	}
	wait := &commandWait{cmd: command, done: make(chan commandOutcome, 1)}
	h.mu.Lock()
	if h.closed || h.workers[worker.id] != worker || !h.onlineLocked(worker) {
		h.mu.Unlock()
		return nil, ErrWorkerOffline
	}
	if _, exists := h.commands[command.CommandID]; exists {
		h.mu.Unlock()
		return nil, ErrCommandConflict
	}
	h.commands[command.CommandID] = wait
	worker.inFlight = command.CommandID
	h.mu.Unlock()
	select {
	case worker.queue <- command:
		return wait, nil
	case <-worker.closed:
		h.finishCommand(command.CommandID, commandOutcome{err: ErrWorkerOffline})
		return nil, ErrWorkerOffline
	}
}

func (h *Hub) wait(ctx context.Context, wait *commandWait) commandOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	// Management commands must respond quickly, but a model turn can exceed
	// that timeout while the worker remains healthy. Its caller owns the turn
	// deadline (including time to persist the outcome). Do not cut that short.
	timeout := h.cfg.CommandTimeout
	if wait.cmd != nil && wait.cmd.Kind == KindRun {
		if _, bounded := ctx.Deadline(); bounded {
			timeout = 0
		} else {
			// Keep callers without a turn deadline bounded as well.
			timeout = 30 * time.Minute
		}
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	select {
	case outcome := <-wait.done:
		return outcome
	case <-ctx.Done():
		return commandOutcome{err: errors.Join(aibroker.ErrDocker, ctx.Err())}
	}
}

func (h *Hub) finishCommand(commandID string, outcome commandOutcome) {
	h.mu.Lock()
	wait := h.commands[commandID]
	if wait != nil {
		delete(h.commands, commandID)
		if worker := h.workers[wait.cmd.WorkerID]; worker != nil && worker.inFlight == commandID {
			worker.inFlight = ""
		}
	}
	h.mu.Unlock()
	if wait != nil {
		wait.done <- outcome
	}
}

func (h *Hub) saveRoute(route RouteRecord) error {
	h.mu.Lock()
	old, existed := h.routes[route.ContainerName]
	h.routes[route.ContainerName] = route
	snapshot := h.snapshotLocked()
	if err := h.store.Save(context.Background(), snapshot); err != nil {
		if existed {
			h.routes[route.ContainerName] = old
		} else {
			delete(h.routes, route.ContainerName)
		}
		h.mu.Unlock()
		return fmt.Errorf("%w: persist worker route", aibroker.ErrDocker)
	}
	h.mu.Unlock()
	return nil
}

func (h *Hub) updateRouteState(containerName, state string) error {
	h.mu.Lock()
	route, ok := h.routes[containerName]
	if !ok {
		h.mu.Unlock()
		return nil
	}
	old := route
	route.State, route.UpdatedAt = state, h.now()
	h.routes[containerName] = route
	if err := h.store.Save(context.Background(), h.snapshotLocked()); err != nil {
		h.routes[containerName] = old
		h.mu.Unlock()
		return fmt.Errorf("%w: persist worker route state", aibroker.ErrDocker)
	}
	h.mu.Unlock()
	return nil
}

func (h *Hub) snapshotLocked() Snapshot {
	routes := make([]RouteRecord, 0, len(h.routes))
	for _, route := range h.routes {
		routes = append(routes, route)
	}
	profiles := make([]ProfileRecord, 0, len(h.profiles))
	for _, binding := range h.profiles {
		profiles = append(profiles, ProfileRecord{ProfileID: binding.id, TokenHash: binding.tokenHash, Consumed: binding.consumed, WorkerID: binding.workerID, SessionHash: binding.sessionHash, Epoch: binding.epoch, ExpiresAt: binding.expiresAt, PublicBaseURL: binding.publicBaseURL})
	}
	return Snapshot{Routes: routes, Profiles: profiles}
}

func (h *Hub) Status(ctx context.Context, profileID string) (Status, error) {
	if !validRemoteID(profileID) {
		return Status{}, aibroker.ErrInvalidRequest
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	status := Status{ProfileID: profileID}
	binding := h.profiles[profileID]
	if binding == nil {
		return status, nil
	}
	status.WorkerID, status.Epoch = binding.workerID, binding.epoch
	worker := h.workers[binding.workerID]
	if worker == nil {
		return status, nil
	}
	status.WorkerID, status.Epoch, status.ConnectedAt, status.LastSeenAt, status.LeaseUntil = worker.id, worker.epoch, worker.connectedAt, worker.lastSeenAt, worker.leaseUntil
	status.Online = h.onlineLocked(worker)
	status.StartRequested, status.StartError = worker.startRequested || worker.startRunning, worker.startError
	for _, route := range h.routes {
		if route.ProfileID == profileID && route.State == string(aibroker.RunRunning) {
			status.Container, status.RequestID = route.ContainerName, route.RequestID
			break
		}
	}
	return status, nil
}

func (h *Hub) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	for _, worker := range h.workers {
		close(worker.closed)
	}
	h.mu.Unlock()
	return nil
}

func (h *Hub) connect(ctx context.Context, request ConnectRequest) (ConnectResponse, error) {
	if request.ProtocolVersion != ProtocolVersion || !validRemoteID(request.ProfileID) || !validRemoteID(request.WorkerID) {
		return ConnectResponse{}, ErrProtocol
	}
	now := h.now()
	h.mu.Lock()
	binding := h.profiles[request.ProfileID]
	if binding == nil {
		h.mu.Unlock()
		return ConnectResponse{}, ErrUnauthorized
	}
	firstEnrollment := request.EnrollmentToken != ""
	// Workers persist a client-generated session token before their first
	// request. If the enrollment response is lost after the Hub commits, the
	// same request can be retried idempotently. The one-time enrollment token
	// alone can never reclaim a consumed profile: the pending session token,
	// worker ID and epoch zero must all match the committed binding.
	resumeEnrollment := firstEnrollment && binding.consumed && request.Epoch == 0 && request.SessionToken != "" && binding.workerID == request.WorkerID && secureTokenHashMatch(binding.sessionHash, request.SessionToken) && secureTokenMatch(binding.tokenHash, request.EnrollmentToken)
	if firstEnrollment {
		if !resumeEnrollment && (binding.consumed || binding.connecting || (!binding.expiresAt.IsZero() && !binding.expiresAt.After(now)) || !secureTokenMatch(binding.tokenHash, request.EnrollmentToken)) {
			h.mu.Unlock()
			return ConnectResponse{}, ErrUnauthorized
		}
		if !resumeEnrollment {
			binding.connecting = true
		}
	} else if request.SessionToken == "" || !secureTokenHashMatch(binding.sessionHash, request.SessionToken) || binding.workerID != request.WorkerID {
		h.mu.Unlock()
		return ConnectResponse{}, ErrUnauthorized
	}
	oldWorker := h.workers[binding.workerID]
	oldBinding := *binding
	oldBinding.connecting = false
	newEpoch := binding.epoch
	if (!resumeEnrollment && firstEnrollment) || newEpoch == 0 {
		newEpoch++
	}
	if resumeEnrollment && oldWorker != nil && oldWorker.epoch == newEpoch && secureTokenHashMatch(oldWorker.sessionHash, request.SessionToken) && h.onlineLocked(oldWorker) {
		oldWorker.lastSeenAt = now
		oldWorker.leaseUntil = now.Add(h.cfg.LeaseTimeout)
		startRequested := oldWorker.startRequested || oldWorker.startRunning
		connectedAt := oldWorker.connectedAt
		h.mu.Unlock()
		return ConnectResponse{ProtocolVersion: ProtocolVersion, WorkerID: request.WorkerID, ProfileID: request.ProfileID, SessionToken: request.SessionToken, Epoch: newEpoch, LeaseSeconds: int(h.cfg.LeaseTimeout / time.Second), ConnectedAt: connectedAt, StartRequested: startRequested}, nil
	}
	guard, start := binding.guard, binding.start
	h.mu.Unlock()

	// Do not run the guard while enrolling. A worker started with --start must
	// first establish its session and enter poll; otherwise recovery may try to
	// inspect that same worker before it can receive the inspect command. The
	// deferred start path performs the guard/recovery after the session is live.
	if firstEnrollment && !resumeEnrollment && !request.Start && guard != nil {
		if err := guard(ctx, request.ProfileID); err != nil {
			h.clearConnecting(request.ProfileID)
			return ConnectResponse{}, err
		}
	}
	var err error
	sessionToken := request.SessionToken
	if firstEnrollment && !resumeEnrollment && sessionToken == "" {
		sessionToken, err = randomToken(32)
		if err != nil {
			h.clearConnecting(request.ProfileID)
			return ConnectResponse{}, err
		}
	}
	session := &workerSession{id: request.WorkerID, profileID: request.ProfileID, epoch: newEpoch, sessionHash: tokenHash(sessionToken), connectedAt: now, lastSeenAt: now, leaseUntil: now.Add(h.cfg.LeaseTimeout), queue: make(chan *Command, 16), wake: make(chan struct{}, 1), closed: make(chan struct{}), startRequested: request.Start}

	h.mu.Lock()
	current := h.profiles[request.ProfileID]
	currentMatchesResume := resumeEnrollment && current != nil && current.consumed && current.workerID == request.WorkerID && current.epoch == newEpoch && secureTokenHashMatch(current.sessionHash, request.SessionToken)
	if current == nil || (firstEnrollment && !resumeEnrollment && (!current.connecting || current.consumed)) || (!firstEnrollment && !secureTokenHashMatch(current.sessionHash, request.SessionToken)) || (resumeEnrollment && !currentMatchesResume) {
		if firstEnrollment && current != nil {
			current.connecting = false
		}
		h.mu.Unlock()
		return ConnectResponse{}, ErrUnauthorized
	}
	if oldWorker = h.workers[current.workerID]; oldWorker != nil && firstEnrollment && !resumeEnrollment { /* replacement is only possible after explicit profile reset */
		current.connecting = false
		h.mu.Unlock()
		return ConnectResponse{}, ErrUnauthorized
	}
	if oldWorker != nil && !firstEnrollment && oldWorker != h.workers[request.WorkerID] {
		select {
		case <-oldWorker.closed:
		default:
			close(oldWorker.closed)
		}
	}
	current.consumed, current.workerID, current.sessionHash, current.epoch, current.connecting = true, request.WorkerID, session.sessionHash, newEpoch, false
	h.workers[request.WorkerID] = session
	for _, wait := range h.commands {
		if wait.cmd.ProfileID == request.ProfileID && wait.cmd.WorkerID == request.WorkerID && wait.cmd.WorkerEpoch == newEpoch {
			select {
			case session.queue <- wait.cmd:
			default:
			}
		}
	}
	if err := h.store.Save(ctx, h.snapshotLocked()); err != nil {
		if oldWorker != nil {
			h.workers[oldWorker.id] = oldWorker
		} else {
			delete(h.workers, request.WorkerID)
		}
		*current = oldBinding
		h.mu.Unlock()
		return ConnectResponse{}, fmt.Errorf("%w: persist worker session", aibroker.ErrDocker)
	}
	if oldWorker != nil && oldWorker != session {
		select {
		case <-oldWorker.closed:
		default:
			close(oldWorker.closed)
		}
	}
	h.mu.Unlock()

	// Start is deliberately deferred until the worker has received this
	// response and entered poll. Supervisor.Start may inspect Hub state; doing
	// it inline would deadlock waiting for the worker's first poll.
	_ = start
	return ConnectResponse{ProtocolVersion: ProtocolVersion, WorkerID: request.WorkerID, ProfileID: request.ProfileID, SessionToken: sessionToken, Epoch: newEpoch, LeaseSeconds: int(h.cfg.LeaseTimeout / time.Second), ConnectedAt: now, StartRequested: request.Start}, nil
}

func (h *Hub) clearConnecting(profileID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if binding := h.profiles[profileID]; binding != nil {
		binding.connecting = false
	}
}
func (h *Hub) clearConnectingLocked(binding *profileBinding) {
	if binding != nil {
		binding.connecting = false
	}
}

func (h *Hub) authSession(r *http.Request) (*workerSession, error) {
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		return nil, ErrUnauthorized
	}
	workerID := strings.TrimSpace(r.Header.Get("X-StoneAge-Worker-ID"))
	profileID := strings.TrimSpace(r.Header.Get("X-StoneAge-Profile-ID"))
	epoch, _ := parseUint(r.Header.Get("X-StoneAge-Worker-Epoch"))
	h.mu.Lock()
	defer h.mu.Unlock()
	worker := h.workers[workerID]
	if worker == nil || worker.profileID != profileID || worker.epoch != epoch || !secureTokenHashMatch(worker.sessionHash, token) || !h.onlineLocked(worker) {
		return nil, ErrUnauthorized
	}
	return worker, nil
}

func (h *Hub) runStart(worker *workerSession) {
	h.mu.Lock()
	binding := h.profiles[worker.profileID]
	start := (func(context.Context, string) error)(nil)
	guard := (func(context.Context, string) error)(nil)
	if binding != nil && binding.start != nil {
		start = binding.start
	}
	if binding != nil {
		guard = binding.guard
	}
	h.mu.Unlock()
	var err error
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.CommandTimeout)
	defer cancel()
	if guard != nil {
		err = guard(ctx, worker.profileID)
	}
	if err == nil && start != nil {
		err = start(ctx, worker.profileID)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if current := h.workers[worker.id]; current == worker && current.epoch == worker.epoch {
		current.startRunning = false
		current.startRequested = false
		if err != nil {
			current.startError = "start_failed"
		} else {
			current.startError = ""
		}
	}
}

func (h *Hub) handlePoll(w http.ResponseWriter, r *http.Request) {
	worker, err := h.authSession(r)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	var request PollRequest
	if err := decodeJSON(r, &request, 4096); err != nil || request.ProtocolVersion != ProtocolVersion || request.WorkerID != worker.id || request.ProfileID != worker.profileID || request.Epoch != worker.epoch {
		writeHTTPError(w, ErrProtocol)
		return
	}
	h.mu.Lock()
	worker.lastSeenAt = h.now()
	worker.leaseUntil = h.now().Add(h.cfg.LeaseTimeout)
	startNow := worker.startRequested && !worker.startRunning
	if startNow {
		worker.startRunning = true
	}
	h.mu.Unlock()
	if startNow {
		go h.runStart(worker)
	}
	wait := time.Duration(request.WaitSeconds) * time.Second
	if wait <= 0 || wait > h.cfg.PollTimeout {
		wait = h.cfg.PollTimeout
	}
	var command *Command
	select {
	case command = <-worker.queue:
	case <-worker.wake:
	case <-worker.closed:
		writeHTTPError(w, ErrUnauthorized)
		return
	case <-r.Context().Done():
		return
	case <-time.After(wait):
	}
	writeJSON(w, http.StatusOK, PollResponse{ProtocolVersion: ProtocolVersion, Command: command, LeaseUntil: h.now().Add(h.cfg.LeaseTimeout)})
}

func validCommandState(kind, state string) bool {
	switch kind {
	case KindRun:
		return state == string(aibroker.DockerContainerExited) || state == string(aibroker.DockerContainerDead)
	case KindStop:
		return state == "stopped"
	case KindRemove:
		return state == "removed"
	case KindInspect:
		return dockerState(state) != ""
	case KindReadResult:
		return state == string(aibroker.DockerContainerExited) || state == string(aibroker.DockerContainerDead)
	}
	return false
}

func (h *Hub) handleResult(w http.ResponseWriter, r *http.Request) {
	worker, err := h.authSession(r)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	var result ResultRequest
	if err := decodeJSON(r, &result, int64(h.cfg.MaxOutputBytes)*3); err != nil {
		writeHTTPError(w, ErrProtocol)
		return
	}
	if result.ProtocolVersion != ProtocolVersion || result.WorkerID != worker.id || result.WorkerEpoch != worker.epoch || result.ProfileID != worker.profileID || result.CommandID == "" {
		writeHTTPError(w, ErrProtocol)
		return
	}
	h.mu.Lock()
	wait := h.commands[result.CommandID]
	h.mu.Unlock()
	if wait == nil || wait.cmd.WorkerID != worker.id || wait.cmd.WorkerEpoch != worker.epoch || wait.cmd.ProfileID != worker.profileID {
		writeHTTPError(w, ErrCommandUnknown)
		return
	}
	if result.ContainerName != wait.cmd.ContainerName || (result.PayloadHash != wait.cmd.PayloadHash && wait.cmd.PayloadHash != "") || (result.RequestID != "" && result.RequestID != wait.cmd.RequestID) {
		writeHTTPError(w, ErrProtocol)
		return
	}
	if len(result.Stdout) > h.cfg.MaxOutputBytes || len(result.Stderr) > h.cfg.MaxOutputBytes {
		writeHTTPError(w, aibroker.ErrOutputLimit)
		return
	}
	if result.RequestID == "" {
		result.RequestID = wait.cmd.RequestID
	}
	if result.Error == "" && !result.NotFound {
		if !validCommandState(wait.cmd.Kind, result.State) {
			writeHTTPError(w, ErrProtocol)
			return
		}
		if err := h.updateRouteState(wait.cmd.ContainerName, result.State); err != nil {
			writeHTTPError(w, err)
			return
		}
	}
	h.finishCommand(result.CommandID, commandOutcome{result: result})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Hub) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	worker, err := h.authSession(r)
	if err != nil {
		writeHTTPError(w, err)
		return
	}
	var request HeartbeatRequest
	if err := decodeJSON(r, &request, 4096); err != nil || request.ProtocolVersion != ProtocolVersion || request.WorkerID != worker.id || request.ProfileID != worker.profileID || request.Epoch != worker.epoch {
		writeHTTPError(w, ErrProtocol)
		return
	}
	h.mu.Lock()
	worker.lastSeenAt = h.now()
	worker.leaseUntil = h.now().Add(h.cfg.LeaseTimeout)
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, HeartbeatResponse{ProtocolVersion: ProtocolVersion, LeaseUntil: h.now().Add(h.cfg.LeaseTimeout)})
}

func (h *Hub) Handler() http.Handler                            { return http.HandlerFunc(h.serveHTTP) }
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.serveHTTP(w, r) }

func (h *Hub) serveHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case path == "/api/ai/worker/connect" && r.Method == http.MethodPost:
		var request ConnectRequest
		if err := decodeJSON(r, &request, 8192); err != nil {
			writeHTTPError(w, ErrProtocol)
			return
		}
		response, err := h.connect(r.Context(), request)
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, response)
	case path == "/api/ai/worker/poll" && r.Method == http.MethodPost:
		h.handlePoll(w, r)
	case path == "/api/ai/worker/result" && r.Method == http.MethodPost:
		h.handleResult(w, r)
	case path == "/api/ai/worker/heartbeat" && r.Method == http.MethodPost:
		h.handleHeartbeat(w, r)
	case path == "/api/ai/worker/v1/game" && r.Method == http.MethodPost:
		h.handleGame(w, r, "")
	case strings.HasPrefix(path, "/api/ai/worker/game/") && r.Method == http.MethodPost:
		h.handleGame(w, r, strings.TrimPrefix(path, "/api/ai/worker/game/"))
	default:
		writeHTTPError(w, aibroker.ErrInvalidRequest)
	}
}

func (h *Hub) handleGame(w http.ResponseWriter, r *http.Request, profileID string) {
	token := bearerToken(r.Header.Get("Authorization"))
	if h.cfg.GameProxyURL == "" || !validGameToken(token) {
		writeHTTPError(w, ErrUnauthorized)
		return
	}
	if profileID != "" && (!validRemoteID(profileID) || !h.isRemoteProfile(profileID)) {
		writeHTTPError(w, ErrUnauthorized)
		return
	}
	if profileID != "" && h.cfg.GameAuthorize != nil && !h.cfg.GameAuthorize(r.Context(), profileID, token) {
		writeHTTPError(w, ErrUnauthorized)
		return
	}
	target, err := url.Parse(h.cfg.GameProxyURL)
	if err != nil {
		writeHTTPError(w, ErrUnauthorized)
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String(), io.LimitReader(r.Body, int64(h.cfg.MaxPayloadBytes)))
	if err != nil {
		writeHTTPError(w, aibroker.ErrDocker)
		return
	}
	request.Header.Set("Authorization", r.Header.Get("Authorization"))
	request.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	if profileID != "" {
		request.Header.Set("X-StoneAge-Profile-ID", profileID)
	}
	response, err := h.gameClient.Do(request)
	if err != nil {
		writeHTTPError(w, aibroker.ErrDocker)
		return
	}
	defer response.Body.Close()
	w.WriteHeader(response.StatusCode)
	_, _ = io.CopyN(w, response.Body, int64(h.cfg.MaxOutputBytes))
}

func (h *Hub) gameEndpoint(profileID string) string {
	base := strings.TrimRight(h.cfg.PublicBaseURL, "/")
	h.mu.Lock()
	if binding := h.profiles[profileID]; binding != nil && binding.publicBaseURL != "" {
		base = strings.TrimRight(binding.publicBaseURL, "/")
	}
	h.mu.Unlock()
	if strings.HasSuffix(base, "/api/ai/worker") {
		return base + "/v1/game"
	}
	if base == "" {
		base = strings.TrimRight(h.cfg.GameProxyURL, "/")
	}
	return base + "/api/ai/worker/v1/game"
}

func requestIdentity(payload []byte) (string, string) {
	var request airunner.ExecuteRequest
	if json.Unmarshal(payload, &request) != nil {
		return "", ""
	}
	sum := sha256.Sum256(payload)
	return request.RequestID, hex.EncodeToString(sum[:])
}
func (h *Hub) fallbackInspector(container string) (aibroker.DockerContainerState, error) {
	if i, ok := h.cfg.Docker.(aibroker.DockerInspector); ok {
		return i.Inspect(context.Background(), container)
	}
	return "", aibroker.ErrDocker
}
func dockerState(value string) aibroker.DockerContainerState {
	switch aibroker.DockerContainerState(value) {
	case aibroker.DockerContainerCreated, aibroker.DockerContainerRunning, aibroker.DockerContainerPaused, aibroker.DockerContainerRestarting, aibroker.DockerContainerRemoving, aibroker.DockerContainerExited, aibroker.DockerContainerDead:
		return aibroker.DockerContainerState(value)
	}
	return ""
}
func clamp(value []byte, max int) []byte {
	if len(value) <= max {
		return value
	}
	return append([]byte(nil), value[:max]...)
}
func validRemoteID(value string) bool   { return remoteIDPattern.MatchString(value) }
func validRemoteName(value string) bool { return remoteNamePattern.MatchString(value) }
func validHTTPURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}
func tokenHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func secureTokenMatch(hash, token string) bool { return secureTokenHashMatch(hash, token) }
func secureTokenHashMatch(hash, token string) bool {
	expected := tokenHash(token)
	return len(hash) == len(expected) && subtle.ConstantTimeCompare([]byte(strings.ToLower(hash)), []byte(expected)) == 1
}
func bearerToken(value string) string {
	fields := strings.Fields(value)
	if len(fields) == 2 && strings.EqualFold(fields[0], "bearer") {
		return fields[1]
	}
	return ""
}
func validGameToken(value string) bool {
	if len(value) != 43 {
		return false
	}
	for _, character := range value {
		if (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func randomToken(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}
func newID(prefix string) string {
	token, err := randomToken(18)
	if err != nil {
		return prefix + "-" + fmt.Sprint(time.Now().UnixNano())
	}
	return prefix + "-" + token
}
func parseUint(value string) (uint64, error) {
	var result uint64
	_, err := fmt.Sscanf(value, "%d", &result)
	return result, err
}
func decodeJSON(r *http.Request, target any, max int64) error {
	if r.Body == nil {
		return io.EOF
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, max+1))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return ErrProtocol
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeHTTPError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, ErrUnauthorized) {
		status = http.StatusUnauthorized
	} else if errors.Is(err, ErrWorkerOffline) {
		status = http.StatusServiceUnavailable
	} else if errors.Is(err, aibroker.ErrDocker) {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]string{"error": errorCode(err)})
}
func errorCode(err error) string {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, ErrWorkerOffline):
		return "worker_offline"
	case errors.Is(err, ErrProtocol):
		return "invalid_protocol"
	case errors.Is(err, ErrCommandUnknown):
		return "unknown_command"
	case errors.Is(err, aibroker.ErrContainerNotFound):
		return "container_not_found"
	case errors.Is(err, aibroker.ErrDocker):
		return "docker_error"
	default:
		return "remote_error"
	}
}
