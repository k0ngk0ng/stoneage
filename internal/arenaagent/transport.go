package arenaagent

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/pelletier/go-toml/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type commandError struct{ Response Object }

func (e *commandError) Error() string {
	code := str(obj(e.Response["data"])["code"])
	if code == "" {
		code = str(e.Response["kind"])
	}
	return "sactl: " + code
}
func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
func claim(path string) (*os.File, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = os.Chmod(path, 0600); e == nil {
		e = lockFile(f)
	}
	if e != nil {
		f.Close()
		return nil, fmt.Errorf("commander lock unavailable: %w", e)
	}
	return f, nil
}

type member struct {
	cfg                        MemberConfig
	binary, ownership, service string
	store                      *Store
	mu                         sync.Mutex
	processMu                  sync.Mutex
	process                    *exec.Cmd
	done                       chan struct{}
	socketInfo                 os.FileInfo
	socketLock, identityLock   *os.File
	login                      Object
}

func (m *member) call(ctx context.Context, timeout time.Duration, check bool, args ...string) (Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	base := []string{"--socket", m.cfg.Socket, "--json"}
	if m.cfg.Profile == "" {
		base = append([]string{"--config", m.cfg.Config}, base...)
	}
	argv := append(base, args...)
	cmd := exec.CommandContext(ctx, m.binary, argv...)
	cmd.WaitDelay = 200 * time.Millisecond
	out := &limitedBuffer{limit: 16 << 20}
	cmd.Stdout = out
	runErr := cmd.Run()
	var response Object
	if decode(out.Bytes(), &response) != nil {
		return nil, &commandError{Object{"ok": false, "kind": "unknown"}}
	}
	if _, ok := response["ok"].(bool); !ok {
		return nil, &commandError{Object{"ok": false, "kind": "unknown"}}
	}
	if runErr != nil && yes(response["ok"]) {
		return nil, &commandError{Object{"ok": false, "kind": "unknown"}}
	}
	if check && !yes(response["ok"]) {
		return response, &commandError{response}
	}
	return response, nil
}
func (m *member) request(ctx context.Context, args ...string) (Object, error) {
	return m.call(ctx, 8*time.Second, true, args...)
}
func (m *member) data(ctx context.Context, args ...string) (Object, error) {
	r, e := m.request(ctx, args...)
	return obj(r["data"]), e
}
func (m *member) exited() bool {
	if m.done == nil {
		return false
	}
	select {
	case <-m.done:
		return true
	default:
		return false
	}
}
func (m *member) start(ctx context.Context) error {
	if m.cfg.Profile != "" {
		return m.attachProfile(ctx)
	}
	raw, e := os.ReadFile(m.cfg.Config)
	if e != nil {
		return e
	}
	var login Object
	if e = toml.Unmarshal(raw, &login); e != nil {
		return fmt.Errorf("invalid member TOML")
	}
	m.login = login
	socket, e := filepath.Abs(str(login["socket_path"]))
	if e != nil || socket != m.cfg.Socket {
		return fmt.Errorf("member %s: socket must match TOML socket_path", m.cfg.ID)
	}
	if str(login["account"]) == "" {
		return fmt.Errorf("member %s requires account", m.cfg.ID)
	}
	// Include HTTP endpoint and line as well as TCP address; accounts at different
	// servers must not alias. All identities use one per-user lock directory.
	transport := strings.ToLower(str(login["transport"]))
	if transport == "" {
		transport = "tcp"
	}
	address := str(login["address"])
	if address == "" {
		address = "127.0.0.1:9065"
	}
	service := []string{transport, address}
	if transport == "http" {
		service = []string{transport, strings.TrimRight(str(login["web_base_url"]), "/"), str(login["server_id"])}
	}
	m.service = hash(service)
	if m.socketLock == nil {
		m.socketLock, e = claim(m.cfg.Socket + ".commander.lock")
		if e != nil {
			return e
		}
	}
	if m.identityLock == nil {
		identity := append(append([]string{}, service...), str(login["account"]), str(login["character"]))
		m.identityLock, e = claim(filepath.Join(m.ownership, hash(identity)+".lock"))
		if e != nil {
			return e
		}
	}
	if _, e = m.call(ctx, 2*time.Second, true, "ping"); e == nil {
		return nil
	}
	if m.exited() && m.socketInfo != nil {
		if current, e := os.Stat(m.cfg.Socket); e == nil && current.Mode()&os.ModeSocket != 0 && os.SameFile(current, m.socketInfo) {
			if e = os.Remove(m.cfg.Socket); e != nil {
				return e
			}
		}
	}
	if _, e = os.Lstat(m.cfg.Socket); e == nil {
		return fmt.Errorf("existing socket for %s is unresponsive; refusing second daemon", m.cfg.ID)
	} else if !os.IsNotExist(e) {
		return e
	}
	log, e := os.OpenFile(filepath.Join(m.store.Directory, m.cfg.ID+"-sactl.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	cmd := exec.Command(m.binary, "serve", "--foreground", "--config", m.cfg.Config)
	cmd.Stdout = log
	cmd.Stderr = log
	if e = cmd.Start(); e != nil {
		log.Close()
		return e
	}
	m.processMu.Lock()
	m.process = cmd
	m.processMu.Unlock()
	m.done = make(chan struct{})
	done := m.done
	go func() { _ = cmd.Wait(); log.Close(); close(done) }()
	for i := 0; i < 40; i++ {
		if m.exited() {
			return fmt.Errorf("sactl daemon for %s exited; inspect its local log", m.cfg.ID)
		}
		if _, e = m.call(ctx, time.Second, true, "ping"); e == nil {
			m.socketInfo, e = os.Stat(m.cfg.Socket)
			return e
		}
		if e = sleep(ctx, 100*time.Millisecond); e != nil {
			return e
		}
	}
	return fmt.Errorf("sactl daemon for %s did not become ready", m.cfg.ID)
}
func (m *member) mutate(ctx context.Context, operation string, args ...string) (Object, error) {
	if e := m.store.Err(); e != nil {
		return nil, e
	}
	if pending := m.store.Pending(m.cfg.ID); pending != nil {
		reply, e := m.retry(ctx, pending)
		if e != nil {
			return nil, e
		}
		if str(pending["operation"]) == operation && string(enc(pending["args"])) == string(enc(args)) {
			return reply, nil
		}
	}
	status, e := m.request(ctx, "arena", "status")
	if e != nil {
		return nil, e
	}
	pending := Object{"operation": operation, "args": args, "request_id": uuid.NewString(), "revision": obj(status["data"])["revision"]}
	m.store.SetPending(m.cfg.ID, pending)
	return m.retry(ctx, pending)
}
func (m *member) retry(ctx context.Context, p Object) (Object, error) {
	var response Object
	for i := 0; i < 3; i++ {
		if e := m.store.Err(); e != nil {
			return nil, e
		}
		args := []string{"arena", str(p["operation"])}
		switch a := p["args"].(type) {
		case []string:
			args = append(args, a...)
		case []any:
			for _, v := range a {
				args = append(args, str(v))
			}
		}
		args = append(args, "--request-id", str(p["request_id"]), "--revision", strconv.FormatInt(int64(num(p["revision"])), 10))
		r, e := m.call(ctx, 10*time.Second, false, args...)
		if e != nil {
			response = Object{"ok": false, "kind": "unknown"}
		} else {
			response = r
		}
		if str(response["kind"]) != "unknown" && str(obj(response["data"])["code"]) != "outcome_unknown" {
			m.store.Record("ladder_receipt", Object{"request": p, "response": response}, "", m.cfg.ID)
			m.store.SetPending(m.cfg.ID, nil)
			if e = m.store.Err(); e != nil {
				return nil, e
			}
			if !yes(response["ok"]) {
				return response, &commandError{response}
			}
			return response, nil
		}
		if e = sleep(ctx, time.Duration(i+1)*100*time.Millisecond); e != nil {
			return nil, e
		}
	}
	return response, &commandError{response}
}
func (m *member) close() {
	if m.process != nil && !m.exited() {
		_ = m.process.Process.Signal(os.Interrupt)
		select {
		case <-m.done:
		case <-time.After(3 * time.Second):
			_ = m.process.Process.Kill()
			<-m.done
		}
	}
	if m.socketLock != nil {
		m.socketLock.Close()
		m.socketLock = nil
	}
	if m.identityLock != nil {
		m.identityLock.Close()
		m.identityLock = nil
	}
}

func (m *member) killForFixture() error {
	m.processMu.Lock()
	defer m.processMu.Unlock()
	if m.process == nil {
		return fmt.Errorf("missing reconnect process")
	}
	return m.process.Process.Kill()
}
func retryable(e error) bool {
	var ce *commandError
	if errors.As(e, &ce) {
		kind := str(ce.Response["kind"])
		code := str(obj(ce.Response["data"])["code"])
		return kind == "unknown" || kind == "session" || code == "outcome_unknown" || code == "stale_revision" || code == "cooldown" || code == "member_not_ready" || code == "room_locked"
	}
	return errors.Is(e, context.DeadlineExceeded)
}
