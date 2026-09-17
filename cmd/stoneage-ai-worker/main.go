// stoneage-ai-worker is a small outbound worker. It does not expose a port or
// require Docker access: the published runtime image already contains the
// runner, Codex, MCP executable and native skills. It polls the production
// Hub over HTTPS and executes one fixed runner child for each command.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/airemote"
	"github.com/k0ngk0ng/stoneage/internal/airunner"
)

const (
	defaultStateRoot = "/var/lib/stoneage-ai"
	defaultRunner    = "/usr/local/bin/stoneage-ai-runner"
	defaultCodex     = "/usr/local/bin/codex"
	defaultMCP       = "/usr/local/bin/stoneage-game-mcp"
	defaultSkills    = "/opt/stoneage/ai/skills"
	maxOutputBytes   = 8 << 20
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

var ErrRunningJob = errors.New("worker: unresolved previous runner process")
var ErrJobConflict = errors.New("worker: job already exists")
var ErrWorkerLocked = errors.New("worker already running")
var ErrWorkerLockUnavailable = errors.New("worker lock unavailable on this platform")

func workerLogID(value string) string {
	if value == "" || len(value) > 128 || !idPattern.MatchString(value) {
		return "redacted"
	}
	return value
}

// workerErrorClass deliberately returns a bounded classification rather than
// the original error text. Runner and HTTP errors can contain prompts,
// credentials or provider responses and must never reach stdout logs.
func workerErrorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, airemote.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, airemote.ErrProtocol):
		return "protocol_error"
	case errors.Is(err, ErrWorkerLocked):
		return "worker_locked"
	case errors.Is(err, ErrWorkerLockUnavailable):
		return "worker_lock_unavailable"
	default:
		return "operation_failed"
	}
}

func workerInitErrorClass(stage string, err error) string {
	switch stage {
	case "options":
		return "invalid_options"
	case "state_root":
		return "state_root_failed"
	case "lock":
		switch {
		case errors.Is(err, ErrWorkerLocked):
			return "worker_locked"
		case errors.Is(err, ErrWorkerLockUnavailable):
			return "worker_lock_unavailable"
		}
		return "lock_failed"
	case "journal":
		return "journal_failed"
	case "worker_state", "worker_id", "session_token":
		return "state_init_failed"
	default:
		return workerErrorClass(err)
	}
}

func logWorkerInitFailure(logger *log.Logger, profile, stage string, err error) error {
	if logger != nil {
		logger.Printf("event=worker_exit profile=%s status=error stage=%s error=%s", workerLogID(profile), workerLogID(stage), workerInitErrorClass(stage, err))
	}
	return err
}

type options struct {
	server      string
	profile     string
	enrollment  string
	workerID    string
	stateRoot   string
	runner      string
	codex       string
	mcp         string
	skills      string
	start       bool
	pollSeconds int
}

type workerState struct {
	WorkerID     string `json:"worker_id"`
	SessionToken string `json:"session_token"`
	Epoch        uint64 `json:"epoch"`
	ProfileID    string `json:"profile_id"`
}

type jobRecord struct {
	CommandID     string    `json:"command_id"`
	ProfileID     string    `json:"profile_id"`
	RequestID     string    `json:"request_id"`
	ContainerName string    `json:"container_name"`
	PayloadHash   string    `json:"payload_hash"`
	State         string    `json:"state"`
	ExitCode      int       `json:"exit_code,omitempty"`
	Stdout        []byte    `json:"stdout,omitempty"`
	Stderr        []byte    `json:"stderr,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
	PID           int       `json:"pid,omitempty"`
	PIDNamespace  string    `json:"pid_namespace,omitempty"`
}

type jobStore struct {
	mu   sync.Mutex
	path string
	jobs map[string]jobRecord
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, stableError(err))
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	logger := log.New(os.Stdout, "stoneage-ai-worker ", log.LstdFlags|log.LUTC)
	opts, err := parseOptions(args, os.Getenv)
	if err != nil {
		logger.Printf("event=worker_start profile=redacted status=error stage=options error=invalid_options")
		return logWorkerInitFailure(logger, "", "options", err)
	}
	logger.Printf("event=worker_start profile=%s poll_seconds=%d", workerLogID(opts.profile), opts.pollSeconds)
	if err := ensurePrivateStateRoot(opts.stateRoot); err != nil {
		return logWorkerInitFailure(logger, opts.profile, "state_root", err)
	}
	lock, err := acquireWorkerLock(opts.stateRoot)
	if err != nil {
		return logWorkerInitFailure(logger, opts.profile, "lock", err)
	}
	defer lock()
	namespace := processNamespaceID()
	store, err := openJobStore(opts.stateRoot, opts.runner, namespace)
	if err != nil {
		return logWorkerInitFailure(logger, opts.profile, "journal", err)
	}
	statePath := filepath.Join(opts.stateRoot, "worker", "worker.json")
	state, err := readWorkerState(statePath)
	if err != nil {
		return logWorkerInitFailure(logger, opts.profile, "worker_state", err)
	}
	stateChanged := false
	if state.WorkerID == "" {
		state.WorkerID = opts.workerID
		if state.WorkerID == "" {
			state.WorkerID, err = randomWorkerID()
			if err != nil {
				return logWorkerInitFailure(logger, opts.profile, "worker_id", err)
			}
		}
		stateChanged = true
	}
	if state.ProfileID != "" && state.ProfileID != opts.profile {
		return logWorkerInitFailure(logger, opts.profile, "worker_state", errors.New("worker profile does not match its persisted state"))
	}
	if state.ProfileID == "" {
		state.ProfileID = opts.profile
		stateChanged = true
	}
	// Persist both the generated worker ID and a pending session credential
	// before the first enrollment request. A lost connect response can then be
	// retried with the same identity without reusing the one-time token alone.
	if state.Epoch == 0 && state.SessionToken == "" && opts.enrollment != "" {
		state.SessionToken, err = randomSessionToken()
		if err != nil {
			return logWorkerInitFailure(logger, opts.profile, "session_token", err)
		}
		stateChanged = true
	}
	if stateChanged {
		if err := writeWorkerState(statePath, state); err != nil {
			return logWorkerInitFailure(logger, opts.profile, "worker_state", err)
		}
	}
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	worker := &worker{opts: opts, client: client, logger: logger, statePath: statePath, state: state, jobs: store, processes: make(map[string]*processState), namespace: namespace, executeTurn: executeTurn}
	worker.logf("event=worker_ready profile=%s worker_id=%s epoch=%d", workerLogID(opts.profile), workerLogID(state.WorkerID), state.Epoch)
	err = worker.loop(ctx)
	if err != nil {
		worker.logf("event=worker_exit profile=%s status=error error=%s", workerLogID(opts.profile), workerErrorClass(err))
	} else {
		worker.logf("event=worker_exit profile=%s status=stopped", workerLogID(opts.profile))
	}
	return err
}

func parseOptions(args []string, lookup func(string) string) (options, error) {
	if lookup == nil {
		lookup = os.Getenv
	}
	opts := options{
		server:      firstEnv(lookup, "STONEAGE_AI_WORKER_SERVER"),
		profile:     firstEnv(lookup, "STONEAGE_AI_PROFILE_ID", "STONEAGE_AI_PROFILE"),
		enrollment:  firstEnv(lookup, "STONEAGE_AI_ENROLLMENT_TOKEN"),
		workerID:    firstEnv(lookup, "STONEAGE_AI_WORKER_ID"),
		stateRoot:   firstEnv(lookup, "STONEAGE_AI_STATE_ROOT", "STONEAGE_AI_STATE"),
		runner:      firstEnv(lookup, "STONEAGE_AI_RUNNER_BINARY"),
		codex:       firstEnv(lookup, "STONEAGE_AI_CODEX_BINARY", "STONEAGE_AI_CODEX"),
		mcp:         firstEnv(lookup, "STONEAGE_AI_MCP_BINARY", "STONEAGE_AI_MCP"),
		skills:      firstEnv(lookup, "STONEAGE_AI_SKILL_ROOT", "STONEAGE_AI_SKILLS"),
		pollSeconds: 30,
	}
	if opts.stateRoot == "" {
		opts.stateRoot = defaultStateRoot
	}
	if opts.runner == "" {
		opts.runner = defaultRunner
	}
	if opts.codex == "" {
		opts.codex = defaultCodex
	}
	if opts.mcp == "" {
		opts.mcp = defaultMCP
	}
	if opts.skills == "" {
		opts.skills = defaultSkills
	}
	flags := flag.NewFlagSet("stoneage-ai-worker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&opts.server, "server", opts.server, "production worker control URL")
	flags.StringVar(&opts.server, "endpoint", opts.server, "production worker control URL")
	flags.StringVar(&opts.profile, "profile", opts.profile, "AI profile identifier")
	flags.StringVar(&opts.profile, "profile-id", opts.profile, "AI profile identifier")
	flags.StringVar(&opts.enrollment, "enrollment-token", opts.enrollment, "one-time enrollment token")
	flags.StringVar(&opts.workerID, "worker-id", opts.workerID, "stable worker identifier")
	flags.StringVar(&opts.stateRoot, "state", opts.stateRoot, "persistent profile state root")
	flags.StringVar(&opts.stateRoot, "state-root", opts.stateRoot, "persistent profile state root")
	flags.StringVar(&opts.runner, "runner", opts.runner, "fixed stoneage-ai-runner executable")
	flags.StringVar(&opts.codex, "codex", opts.codex, "fixed Codex executable")
	flags.StringVar(&opts.mcp, "mcp", opts.mcp, "fixed game MCP executable")
	flags.StringVar(&opts.skills, "skills", opts.skills, "fixed native skill root")
	flags.BoolVar(&opts.start, "start", false, "explicitly start the paused/stopped profile")
	flags.IntVar(&opts.pollSeconds, "poll-seconds", opts.pollSeconds, "long-poll wait in seconds")
	if err := flags.Parse(args); err != nil {
		return options{}, errors.New("invalid worker options")
	}
	if flags.NArg() != 0 || strings.TrimSpace(opts.server) == "" || !idPattern.MatchString(opts.profile) || opts.pollSeconds < 1 || opts.pollSeconds > 90 || strings.ContainsAny(opts.server, "\x00\r\n") || !validControlURL(opts.server) {
		return options{}, errors.New("server, profile and valid polling options are required")
	}
	return opts, nil
}

func validControlURL(value string) bool {
	parsed, err := url.Parse(strings.TrimRight(value, "/"))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	host := parsed.Hostname()
	if parsed.Scheme != "http" {
		return false
	}
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsPrivate()
}

func firstEnv(lookup func(string) string, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(lookup(name)); value != "" {
			return value
		}
	}
	return ""
}

func (w *worker) loop(ctx context.Context) error {
	request := airemote.ConnectRequest{ProtocolVersion: airemote.ProtocolVersion, WorkerID: w.state.WorkerID, ProfileID: w.opts.profile, Start: w.opts.start}
	if w.state.Epoch == 0 {
		request.EnrollmentToken = w.opts.enrollment
		request.SessionToken = w.state.SessionToken
	} else {
		request.SessionToken, request.Epoch = w.state.SessionToken, w.state.Epoch
	}
	response, err := w.connectWithRetry(ctx, request)
	if errors.Is(err, airemote.ErrUnauthorized) && request.EnrollmentToken == "" && w.opts.enrollment != "" {
		// A command generated after an offline Hub rotation carries a fresh
		// enrollment token, while this volume still has the old session. Keep
		// normal session reconnect as the first choice so rerunning the same
		// command remains safe; only an unauthorized session may fall back to
		// a newly persisted pending enrollment session.
		request, err = w.prepareEnrollmentRequest()
		if err == nil {
			response, err = w.connectWithRetry(ctx, request)
		}
	}
	if err != nil {
		w.logf("event=connection_failed profile=%s error=%s", workerLogID(w.opts.profile), workerErrorClass(err))
		return err
	}
	if err := w.acceptSession(response); err != nil {
		w.logf("event=connection_failed profile=%s error=%s", workerLogID(w.opts.profile), workerErrorClass(err))
		return err
	}
	w.logf("event=connected profile=%s worker_id=%s epoch=%d", workerLogID(w.opts.profile), workerLogID(w.state.WorkerID), w.state.Epoch)
	// Enrollment is one-use. Keep it in the process only and never write it to
	// worker state or logs. Automatic reconnects never send --start.
	w.opts.enrollment, w.opts.start = "", false
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		poll, err := w.poll(ctx)
		if err != nil {
			if errors.Is(err, airemote.ErrUnauthorized) {
				w.logf("event=connection_lost profile=%s reason=unauthorized", workerLogID(w.opts.profile))
				// The server may have restarted and rebuilt its in-memory worker
				// session from the durable profile binding. Re-handshake using the
				// stable session credential; never reuse enrollment here.
				reconnect := airemote.ConnectRequest{ProtocolVersion: airemote.ProtocolVersion, WorkerID: w.state.WorkerID, ProfileID: w.opts.profile, SessionToken: w.state.SessionToken, Epoch: w.state.Epoch}
				response, reconnectErr := w.connect(ctx, reconnect)
				if reconnectErr != nil {
					w.logf("event=reconnect_failed profile=%s error=%s", workerLogID(w.opts.profile), workerErrorClass(reconnectErr))
				} else if acceptErr := w.acceptSession(response); acceptErr != nil {
					w.logf("event=reconnect_failed profile=%s error=%s", workerLogID(w.opts.profile), workerErrorClass(acceptErr))
				} else {
					w.logf("event=reconnected profile=%s worker_id=%s epoch=%d", workerLogID(w.opts.profile), workerLogID(w.state.WorkerID), w.state.Epoch)
					continue
				}
				return err
			}
			w.logf("event=connection_error profile=%s error=%s", workerLogID(w.opts.profile), workerErrorClass(err))
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
				continue
			}
		}
		if poll.Command != nil {
			w.dispatch(ctx, *poll.Command)
		}
	}
}

func (w *worker) prepareEnrollmentRequest() (airemote.ConnectRequest, error) {
	sessionToken, err := randomSessionToken()
	if err != nil {
		return airemote.ConnectRequest{}, err
	}
	w.state.SessionToken, w.state.Epoch = sessionToken, 0
	if err := writeWorkerState(w.statePath, w.state); err != nil {
		return airemote.ConnectRequest{}, err
	}
	return airemote.ConnectRequest{
		ProtocolVersion: airemote.ProtocolVersion,
		WorkerID:        w.state.WorkerID,
		ProfileID:       w.opts.profile,
		EnrollmentToken: w.opts.enrollment,
		SessionToken:    sessionToken,
		Start:           w.opts.start,
	}, nil
}

type processState struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type worker struct {
	opts        options
	client      *http.Client
	logger      *log.Logger
	statePath   string
	state       workerState
	jobs        *jobStore
	mu          sync.Mutex
	processes   map[string]*processState
	namespace   string
	executeTurn executeTurnFunc
}

func (w *worker) logf(format string, args ...any) {
	if w != nil && w.logger != nil {
		w.logger.Printf(format, args...)
	}
}

func (w *worker) logCommand(command airemote.Command) {
	w.logf("event=command_received kind=%s profile=%s request_id=%s", workerLogID(command.Kind), workerLogID(command.ProfileID), workerLogID(command.RequestID))
}

func (w *worker) logTaskStarted(kind string, command airemote.Command) time.Time {
	started := time.Now()
	w.logf("event=task_started kind=%s profile=%s request_id=%s", workerLogID(kind), workerLogID(command.ProfileID), workerLogID(command.RequestID))
	return started
}

func (w *worker) logTaskCompleted(kind string, command airemote.Command, started time.Time, status, errorCode string) {
	if status == "" {
		status = "unknown"
	}
	if errorCode == "" {
		w.logf("event=task_completed kind=%s profile=%s request_id=%s duration_ms=%d status=%s", workerLogID(kind), workerLogID(command.ProfileID), workerLogID(command.RequestID), time.Since(started).Milliseconds(), workerLogID(status))
		return
	}
	w.logf("event=task_completed kind=%s profile=%s request_id=%s duration_ms=%d status=%s error=%s", workerLogID(kind), workerLogID(command.ProfileID), workerLogID(command.RequestID), time.Since(started).Milliseconds(), workerLogID(status), workerLogID(errorCode))
}

func (w *worker) acceptSession(response airemote.ConnectResponse) error {
	if err := w.validateSessionResponse(response); err != nil {
		return err
	}
	w.state.SessionToken, w.state.Epoch, w.state.ProfileID = response.SessionToken, response.Epoch, response.ProfileID
	return writeWorkerState(w.statePath, w.state)
}

func (w *worker) validateSessionResponse(response airemote.ConnectResponse) error {
	if response.ProtocolVersion != airemote.ProtocolVersion || response.ProfileID != w.opts.profile || response.WorkerID != w.state.WorkerID || response.Epoch == 0 || response.SessionToken == "" {
		return airemote.ErrProtocol
	}
	return nil
}

func (w *worker) connect(ctx context.Context, request airemote.ConnectRequest) (airemote.ConnectResponse, error) {
	var response airemote.ConnectResponse
	if err := w.post(ctx, "/api/ai/worker/connect", request, "", &response); err != nil {
		return response, err
	}
	if err := w.validateSessionResponse(response); err != nil {
		return response, err
	}
	return response, nil
}

func (w *worker) connectWithRetry(ctx context.Context, request airemote.ConnectRequest) (airemote.ConnectResponse, error) {
	response, err := w.connect(ctx, request)
	if err == nil || request.EnrollmentToken == "" || request.SessionToken == "" || request.Epoch != 0 || errors.Is(err, airemote.ErrUnauthorized) {
		return response, err
	}
	// Only a pending enrollment with a pre-persisted session may be retried.
	// A normal reconnect never retries with an enrollment credential, and an
	// unauthorized enrollment is not retried because the token may be invalid.
	for attempt := 0; attempt < 3; attempt++ {
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return response, ctx.Err()
		case <-timer.C:
		}
		response, err = w.connect(ctx, request)
		if err == nil || errors.Is(err, airemote.ErrUnauthorized) {
			return response, err
		}
	}
	return response, err
}

func (w *worker) poll(ctx context.Context) (airemote.PollResponse, error) {
	request := airemote.PollRequest{ProtocolVersion: airemote.ProtocolVersion, WorkerID: w.state.WorkerID, ProfileID: w.opts.profile, Epoch: w.state.Epoch, WaitSeconds: w.opts.pollSeconds}
	var response airemote.PollResponse
	err := w.post(ctx, "/api/ai/worker/poll", request, w.state.SessionToken, &response)
	return response, err
}

func (w *worker) dispatch(parent context.Context, command airemote.Command) {
	if command.ProtocolVersion != airemote.ProtocolVersion || command.WorkerID != w.state.WorkerID || command.WorkerEpoch != w.state.Epoch || command.ProfileID != w.opts.profile || !namePattern.MatchString(command.ContainerName) {
		return
	}
	w.logCommand(command)
	switch command.Kind {
	case airemote.KindRun:
		go w.executeRun(parent, command)
	case airemote.KindStop:
		go w.executeStop(parent, command)
	case airemote.KindInspect, airemote.KindReadResult, airemote.KindRemove:
		go w.executeLifecycle(parent, command)
	case airemote.KindRemoveVol:
		go w.executeRemoveVolume(parent, command)
	default:
		w.logf("event=command_completed kind=%s profile=%s request_id=%s status=ignored error=unsupported_kind", workerLogID(command.Kind), workerLogID(command.ProfileID), workerLogID(command.RequestID))
	}
}

type executeTurnFunc func(context.Context, options, airunner.ExecuteRequest) (airunner.Response, error)

func executeTurn(ctx context.Context, opts options, request airunner.ExecuteRequest) (airunner.Response, error) {
	executor, err := airunner.New(airunner.Config{ProfileID: request.ProfileID, StateRoot: opts.stateRoot, CodexBinary: opts.codex, MCPBinary: opts.mcp, SkillRoot: opts.skills, TerminationGrace: 2 * time.Second})
	if err != nil {
		return airunner.Response{OK: false, ProfileID: request.ProfileID, RequestID: request.RequestID}, err
	}
	return executor.Execute(ctx, request)
}

func (w *worker) executeRun(parent context.Context, command airemote.Command) {
	started := w.logTaskStarted("run", command)
	status, errorCode := "unknown", ""
	defer func() { w.logTaskCompleted("run", command, started, status, errorCode) }()
	if len(command.Payload) == 0 || len(command.Payload) > 1<<20 || command.PayloadHash == "" {
		errorCode = "invalid_run"
		_ = w.postResult(context.Background(), command, airemote.ResultRequest{State: "unknown", Error: "invalid_run"})
		return
	}
	payload := append([]byte(nil), command.Payload...)
	// Authenticate the exact server payload before rewriting only the game
	// endpoint for the local MCP process.
	if hashBytes(payload) != command.PayloadHash {
		errorCode = "payload_conflict"
		_ = w.postResult(context.Background(), command, airemote.ResultRequest{State: "unknown", Error: "payload_conflict", PayloadHash: command.PayloadHash})
		return
	}
	var request airunner.ExecuteRequest
	if json.Unmarshal(payload, &request) != nil || request.ProfileID != command.ProfileID || request.RequestID != command.RequestID {
		errorCode = "invalid_payload"
		_ = w.postResult(context.Background(), command, airemote.ResultRequest{State: "unknown", Error: "invalid_payload", PayloadHash: command.PayloadHash})
		return
	}
	if command.GameEndpoint != "" {
		if !validEndpoint(command.GameEndpoint) {
			errorCode = "invalid_game_endpoint"
			_ = w.postResult(context.Background(), command, airemote.ResultRequest{State: "unknown", Error: "invalid_game_endpoint", PayloadHash: command.PayloadHash})
			return
		}
		request.MCP.Endpoint = command.GameEndpoint
	}
	if existing, ok := w.jobs.get(command.ContainerName); ok {
		if existing.PayloadHash != command.PayloadHash {
			errorCode = "payload_conflict"
			_ = w.postResult(context.Background(), command, airemote.ResultRequest{State: "unknown", Error: "payload_conflict", PayloadHash: command.PayloadHash})
			return
		}
		switch existing.State {
		case "running":
			status, errorCode = "running", "already_running"
			return
		case "exited", "dead":
			status = existing.State
			if err := w.postResult(context.Background(), command, airemote.ResultRequest{RequestID: command.RequestID, PayloadHash: command.PayloadHash, State: existing.State, ExitCode: existing.ExitCode, Stdout: existing.Stdout, Stderr: existing.Stderr}); err != nil {
				errorCode = "result_post_failed"
			}
			return
		default:
			errorCode = "request_tombstoned"
			_ = w.postResult(context.Background(), command, airemote.ResultRequest{State: "unknown", Error: "request_tombstoned", PayloadHash: command.PayloadHash})
			return
		}
	}
	w.mu.Lock()
	for _, process := range w.processes {
		if process != nil {
			w.mu.Unlock()
			errorCode = "profile_busy"
			_ = w.postResult(context.Background(), command, airemote.ResultRequest{State: "unknown", Error: "profile_busy", PayloadHash: command.PayloadHash})
			return
		}
	}
	runCtx, cancel := context.WithCancel(parent)
	process := &processState{cancel: cancel, done: make(chan struct{})}
	w.processes[command.ContainerName] = process
	w.mu.Unlock()
	var releaseProcess sync.Once
	finishProcess := func() {
		releaseProcess.Do(func() {
			cancel()
			close(process.done)
			w.mu.Lock()
			delete(w.processes, command.ContainerName)
			w.mu.Unlock()
		})
	}
	defer finishProcess()
	if err := w.jobs.start(jobRecord{CommandID: command.CommandID, ProfileID: command.ProfileID, RequestID: command.RequestID, ContainerName: command.ContainerName, PayloadHash: command.PayloadHash, PIDNamespace: w.namespace, State: "running", UpdatedAt: time.Now().UTC()}); err != nil {
		finishProcess()
		errorCode = "journal_failed"
		_ = w.postResult(context.Background(), command, airemote.ResultRequest{State: "unknown", Error: "journal_failed", PayloadHash: command.PayloadHash})
		return
	}
	response, execErr := w.executeTurn(runCtx, w.opts, request)
	raw, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		finishProcess()
		errorCode = "response_encode_failed"
		_ = w.postResult(context.Background(), command, airemote.ResultRequest{State: "unknown", Error: "response_encode_failed", PayloadHash: command.PayloadHash})
		return
	}
	state, exitCode := "exited", 0
	if response.Result != nil {
		exitCode = response.Result.Process.ExitCode
		if response.Result.Process.Status != "exited" {
			state = "dead"
		}
	}
	if execErr != nil && response.Result == nil {
		state, exitCode = "dead", -1
		errorCode = "runner_failed"
	} else if execErr != nil {
		errorCode = "runner_reported_error"
	}
	status = state
	record := jobRecord{CommandID: command.CommandID, ProfileID: command.ProfileID, RequestID: command.RequestID, ContainerName: command.ContainerName, PayloadHash: command.PayloadHash, PIDNamespace: w.namespace, State: state, ExitCode: exitCode, Stdout: raw, UpdatedAt: time.Now().UTC()}
	if err := w.jobs.finish(record); err != nil {
		finishProcess()
		errorCode = "journal_failed"
		_ = w.postResult(context.Background(), command, airemote.ResultRequest{State: "unknown", Error: "journal_failed", PayloadHash: command.PayloadHash})
		return
	}
	// The runtime process is finished and the durable journal is terminal
	// before any network result is posted. Stop can therefore observe a
	// consistent state and never wait on the result HTTP request.
	finishProcess()
	if err := w.postResult(context.Background(), command, airemote.ResultRequest{RequestID: command.RequestID, PayloadHash: command.PayloadHash, State: state, ExitCode: exitCode, Stdout: raw}); err != nil && errorCode == "" {
		errorCode = "result_post_failed"
	}
}
func (w *worker) executeStop(parent context.Context, command airemote.Command) {
	started := w.logTaskStarted(airemote.KindStop, command)
	status, errorCode := "unknown", ""
	defer func() { w.logTaskCompleted(airemote.KindStop, command, started, status, errorCode) }()
	w.logf("event=stop_requested kind=%s profile=%s request_id=%s", workerLogID(command.Kind), workerLogID(command.ProfileID), workerLogID(command.RequestID))
	w.mu.Lock()
	process := w.processes[command.ContainerName]
	w.mu.Unlock()
	if process == nil {
		if record, ok := w.jobs.get(command.ContainerName); ok && record.State == "running" {
			status, errorCode = "unknown", "process_unavailable"
			if err := w.postResult(context.Background(), command, airemote.ResultRequest{RequestID: command.RequestID, PayloadHash: command.PayloadHash, State: status, Error: errorCode}); err != nil && errorCode == "" {
				errorCode = "result_post_failed"
			}
			return
		}
		status = "stopped"
		if err := w.postResult(parent, command, airemote.ResultRequest{RequestID: command.RequestID, PayloadHash: command.PayloadHash, State: status}); err != nil && errorCode == "" {
			errorCode = "result_post_failed"
		}
		return
	}
	process.cancel()
	waitCtx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	select {
	case <-process.done:
		status = "stopped"
		if err := w.postResult(context.Background(), command, airemote.ResultRequest{RequestID: command.RequestID, PayloadHash: command.PayloadHash, State: status}); err != nil && errorCode == "" {
			errorCode = "result_post_failed"
		}
	case <-waitCtx.Done():
		status, errorCode = "unknown", "stop_timeout"
		if err := w.postResult(context.Background(), command, airemote.ResultRequest{RequestID: command.RequestID, PayloadHash: command.PayloadHash, State: status, Error: errorCode}); err != nil && errorCode == "" {
			errorCode = "result_post_failed"
		}
	}
}
func (w *worker) executeLifecycle(parent context.Context, command airemote.Command) {
	started := w.logTaskStarted(command.Kind, command)
	status, errorCode := "unknown", ""
	defer func() { w.logTaskCompleted(command.Kind, command, started, status, errorCode) }()
	record, ok := w.jobs.get(command.ContainerName)
	result := airemote.ResultRequest{RequestID: command.RequestID, PayloadHash: command.PayloadHash}
	if !ok {
		result.NotFound, result.State = true, "absent"
	} else if record.PayloadHash != command.PayloadHash {
		result.Error, result.State = "payload_conflict", "unknown"
	} else {
		switch command.Kind {
		case airemote.KindInspect:
			if record.State == "removed" {
				result.NotFound, result.State = true, "absent"
			} else {
				result.State = record.State
			}
		case airemote.KindReadResult:
			if record.State != "exited" && record.State != "dead" {
				result.Error, result.State = "not_terminal", record.State
			} else {
				result.State, result.ExitCode, result.Stdout, result.Stderr = record.State, record.ExitCode, record.Stdout, record.Stderr
			}
		case airemote.KindRemove:
			if record.State == "running" {
				result.Error, result.State = "live", record.State
			} else if err := w.jobs.remove(command.ContainerName); err != nil {
				result.Error, result.State = "journal_failed", "unknown"
			} else {
				result.State = "removed"
			}
		}
	}
	status, errorCode = result.State, result.Error
	if err := w.postResult(parent, command, result); err != nil && errorCode == "" {
		errorCode = "result_post_failed"
	}
}

func (w *worker) executeRemoveVolume(parent context.Context, command airemote.Command) {
	started := w.logTaskStarted(airemote.KindRemoveVol, command)
	status, errorCode := "removed", ""
	defer func() { w.logTaskCompleted(airemote.KindRemoveVol, command, started, status, errorCode) }()
	// The named volume belongs to the local operator and is intentionally
	// retained across reconnects; acknowledge the server-side cleanup command
	// without attempting to access Docker from inside this worker container.
	if err := w.postResult(parent, command, airemote.ResultRequest{State: status}); err != nil {
		errorCode = "result_post_failed"
	}
}

func (w *worker) postResult(ctx context.Context, command airemote.Command, result airemote.ResultRequest) error {
	result.ProtocolVersion, result.CommandID, result.WorkerID, result.WorkerEpoch, result.ProfileID, result.ContainerName = airemote.ProtocolVersion, command.CommandID, w.state.WorkerID, w.state.Epoch, w.opts.profile, command.ContainerName
	var response map[string]any
	return w.post(ctx, "/api/ai/worker/result", result, w.state.SessionToken, &response)
}

func (w *worker) post(ctx context.Context, path string, body any, token string, result any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return airemote.ErrProtocol
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, controlURL(w.opts.server, path), bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-StoneAge-Worker-ID", w.state.WorkerID)
		request.Header.Set("X-StoneAge-Profile-ID", w.opts.profile)
		request.Header.Set("X-StoneAge-Worker-Epoch", fmt.Sprint(w.state.Epoch))
	}
	response, err := w.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusUnauthorized {
			return airemote.ErrUnauthorized
		}
		return errors.New("worker control request failed")
	}
	if result == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<20))
	if err := decoder.Decode(result); err != nil {
		return err
	}
	return nil
}

func controlURL(server, path string) string {
	base := strings.TrimRight(server, "/")
	const prefix = "/api/ai/worker"
	if strings.HasSuffix(base, prefix) && strings.HasPrefix(path, prefix) {
		return base + strings.TrimPrefix(path, prefix)
	}
	return base + path
}

func openJobStore(root, runner, namespace string) (*jobStore, error) {
	path := filepath.Join(root, "worker", "jobs.json")
	store := &jobStore{path: path, jobs: make(map[string]jobRecord)}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > 64<<20 {
		return nil, errors.New("worker journal is too large")
	}
	if err := json.Unmarshal(raw, &store.jobs); err != nil {
		return nil, err
	}
	changed := false
	for container, record := range store.jobs {
		if !namePattern.MatchString(container) || record.ContainerName != container || record.ProfileID == "" || record.PayloadHash == "" {
			return nil, errors.New("worker journal is corrupt")
		}
		if record.State != "running" {
			continue
		}
		// A running row is not silently declared absent. PID zero means the
		// worker died before recording the child; refuse a second execution.
		if record.PIDNamespace != "" && namespace != "" && record.PIDNamespace != namespace {
			record.State, record.Stdout, record.Stderr, record.UpdatedAt = "dead", nil, nil, time.Now().UTC()
			store.jobs[container] = record
			changed = true
			continue
		}
		if record.PID <= 0 {
			return nil, ErrRunningJob
		}
		if runnerProcessAlive(record.PID, runner) {
			return nil, ErrRunningJob
		}
		record.State, record.Stdout, record.Stderr, record.UpdatedAt = "dead", nil, nil, time.Now().UTC()
		store.jobs[container] = record
		changed = true
	}
	if changed {
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func ensurePrivateStateRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) {
		return errors.New("invalid state root")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("invalid state root")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return err
	}
	return nil
}

func processNamespaceID() string {
	link, err := os.Readlink("/proc/1/ns/pid")
	if err != nil {
		return ""
	}
	stat, err := os.ReadFile("/proc/1/stat")
	if err != nil {
		return link
	}
	return link + ":" + string(stat)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
func runnerProcessAlive(pid int, runner string) bool {
	if !processAlive(pid) {
		return false
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return true
	}
	return bytes.Contains(raw, []byte(runner))
}

func (s *jobStore) start(record jobRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.jobs[record.ContainerName]; ok {
		if existing.PayloadHash != record.PayloadHash {
			return ErrJobConflict
		}
		return ErrJobConflict
	}
	s.jobs[record.ContainerName] = record
	return s.saveLocked()
}
func (s *jobStore) setPID(container string, pid int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.jobs[container]
	if !ok || record.State != "running" {
		return ErrJobConflict
	}
	record.PID = pid
	record.UpdatedAt = time.Now().UTC()
	s.jobs[container] = record
	return s.saveLocked()
}
func (s *jobStore) finish(record jobRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.jobs[record.ContainerName]
	if !ok || existing.State != "running" || existing.PayloadHash != record.PayloadHash {
		return ErrJobConflict
	}
	if record.PID == 0 {
		record.PID = existing.PID
	}
	s.jobs[record.ContainerName] = record
	return s.saveLocked()
}
func (s *jobStore) get(container string) (jobRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.jobs[container]
	return record, ok
}
func (s *jobStore) remove(container string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, ok := s.jobs[container]; ok {
		record.State = "removed"
		record.Stdout, record.Stderr = nil, nil
		record.UpdatedAt = time.Now().UTC()
		s.jobs[container] = record
	}
	return s.saveLocked()
}
func (s *jobStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(s.jobs)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".jobs-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}
func readWorkerState(path string) (workerState, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return workerState{}, nil
	}
	if err != nil {
		return workerState{}, err
	}
	var state workerState
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, err
	}
	if state.WorkerID != "" && !idPattern.MatchString(state.WorkerID) {
		return workerState{}, errors.New("invalid persisted worker id")
	}
	return state, nil
}
func writeWorkerState(path string, state workerState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".worker-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
func randomWorkerID() (string, error) {
	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	sum := sha256.Sum256(randomBytes)
	return "worker-" + hex.EncodeToString(sum[:])[:24], nil
}
func randomSessionToken() (string, error) {
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(randomBytes), nil
}
func hashBytes(raw []byte) string     { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func validEndpoint(value string) bool { parsed, err := urlParse(value); return err == nil && parsed }
func urlParse(value string) (bool, error) {
	if strings.ContainsAny(value, "\x00\r\n") || strings.Contains(value, "?") || strings.Contains(value, "#") {
		return false, errors.New("invalid endpoint")
	}
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://"), nil
}
func stableError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, airemote.ErrUnauthorized) {
		return "unauthorized"
	}
	if errors.Is(err, airemote.ErrProtocol) {
		return "invalid_protocol"
	}
	if errors.Is(err, ErrWorkerLocked) {
		return "worker_locked"
	}
	if errors.Is(err, ErrWorkerLockUnavailable) {
		return "worker_lock_unavailable"
	}
	return "worker_failed"
}

type boundedOutput struct {
	limit    int
	data     []byte
	overflow bool
}

func (o *boundedOutput) Write(value []byte) (int, error) {
	remaining := o.limit - len(o.data)
	if remaining > 0 {
		if len(value) > remaining {
			o.data = append(o.data, value[:remaining]...)
			o.overflow = true
		} else {
			o.data = append(o.data, value...)
		}
	} else {
		o.overflow = true
	}
	return len(value), nil
}
func (o *boundedOutput) bytes() []byte { return append([]byte(nil), o.data...) }
