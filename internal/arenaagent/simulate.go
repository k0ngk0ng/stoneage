package arenaagent

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Simulation struct {
	Root, Work, Image, Strategy, Model string
	Mode, Matches                      int
	Seed                               int64
	Reconnect                          bool
	Output                             io.Writer
}

func (s Simulation) validate() (Simulation, error) {
	var e error
	s.Root, e = filepath.Abs(s.Root)
	if e != nil {
		return s, e
	}
	s.Work = absolute(s.Root, s.Work)
	if s.Mode < 1 || s.Mode > 5 || s.Matches < 1 {
		return s, fmt.Errorf("mode must be 1–5 and matches positive")
	}
	if s.Strategy != "basic" && s.Strategy != "learned" && s.Strategy != "explore" {
		return s, fmt.Errorf("simulation strategy must be basic, learned or explore")
	}
	if s.Strategy == "learned" && s.Model == "" {
		return s, fmt.Errorf("learned simulation requires model")
	}
	for _, path := range []string{s.Work, s.Model} {
		if path == "" {
			continue
		}
		p := absolute(s.Root, path)
		relative, e := filepath.Rel(filepath.Join(s.Root, "build"), p)
		if e != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return s, fmt.Errorf("work and model must be inside repository build/")
		}
		parent := filepath.Dir(p)
		for {
			if info, e := os.Lstat(parent); e == nil && info.Mode()&os.ModeSymlink != 0 {
				return s, fmt.Errorf("simulation paths cannot contain symlinks")
			}
			if parent == s.Root {
				break
			}
			next := filepath.Dir(parent)
			if parent == next {
				break
			}
			parent = next
		}
	}
	if _, e = os.Lstat(s.Work); !os.IsNotExist(e) {
		return s, fmt.Errorf("work must be a new directory")
	}
	if s.Model != "" {
		s.Model = absolute(s.Root, s.Model)
	}
	if s.Output == nil {
		s.Output = io.Discard
	}
	return s, nil
}
func Simulate(ctx context.Context, s Simulation) error {
	s, e := s.validate()
	if e != nil {
		return e
	}
	if s.Image == "" {
		s.Image = "gcc:13-bookworm"
	}
	for _, name := range []string{"bin/sactl", "bin/stoneage-gateway", "bin/seed-network", "native/gmsv/gmsvjt.exe", "native/saac/saacjt.exe"} {
		if info, e := os.Stat(filepath.Join(s.Root, "build/local-arena", name)); e != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("missing prepared Linux binary: build/local-arena/%s", name)
		}
	}
	if e = os.MkdirAll(filepath.Dir(s.Work), 0700); e != nil {
		return e
	}
	containerPath := func(path string) string {
		rel, _ := filepath.Rel(s.Root, path)
		return "/repo/" + filepath.ToSlash(rel)
	}
	args := []string{"run", "--rm", "--pull", "never", "--network", "none", "--read-only", "--cpus", "2", "--memory", "2g", "--tmpfs", "/tmp:rw,size=128m", "--mount", "type=bind,src=" + s.Root + ",dst=/repo,readonly", "--mount", "type=bind,src=" + filepath.Join(s.Root, "build") + ",dst=/repo/build", "-e", "STONEAGE_ARENA_ISOLATED=1", "--entrypoint", "/repo/build/local-arena/bin/sactl", s.Image, "ai", "native-simulate", "--root", "/repo", "--work", containerPath(s.Work), "--mode", strconv.Itoa(s.Mode), "--matches", strconv.Itoa(s.Matches), "--strategy", s.Strategy, "--seed", strconv.FormatInt(s.Seed, 10)}
	if s.Model != "" {
		args = append(args, "--model", containerPath(s.Model))
	}
	if s.Reconnect {
		args = append(args, "--reconnect")
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = s.Output
	cmd.Stderr = s.Output
	cmd.WaitDelay = time.Second
	return cmd.Run()
}

type fixtureProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func (p *fixtureProcess) close() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}
func nativeProcess(ctx context.Context, work, name, dir string, env []string, args ...string) (*fixtureProcess, error) {
	log, e := os.OpenFile(filepath.Join(work, "logs", name+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return nil, e
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = log
	cmd.Stderr = log
	if e = cmd.Start(); e != nil {
		log.Close()
		return nil, e
	}
	p := &fixtureProcess{cmd, make(chan struct{})}
	go func() { _ = cmd.Wait(); log.Close(); close(p.done) }()
	return p, nil
}
func waitUntil(ctx context.Context, label string, condition func() bool) error {
	deadline := time.Now().Add(40 * time.Second)
	for {
		if condition() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s timed out", label)
		}
		if e := sleep(ctx, 100*time.Millisecond); e != nil {
			return e
		}
	}
}
func writePrivate(path string, data []byte) error { return os.WriteFile(path, data, 0600) }

// NativeSimulate runs only inside the isolated collector container. All native
// data and synthetic credentials go under a new repository build directory.
func NativeSimulate(ctx context.Context, s Simulation) error {
	if os.Getenv("STONEAGE_ARENA_ISOLATED") != "1" {
		return fmt.Errorf("native-simulate is internal; use simulate")
	}
	s, e := s.validate()
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(max(180, s.Matches*120))*time.Second)
	defer cancel()
	if e = os.Mkdir(s.Work, 0700); e != nil {
		return e
	}
	for _, dir := range []string{"tmp", "state", "admin", "logs", "saac", "gmsv", "gmsv/log", "gmsv/Dengon", "gmsv/Schedule", "saac/char", "saac/char_sleep", "saac/log", "saac/lock", "saac/db", "saac/mail", "saac/family", "saac/fmpoint", "saac/fmsmemo"} {
		if e = os.MkdirAll(filepath.Join(s.Work, dir), 0700); e != nil {
			return e
		}
	}
	source := filepath.Join(s.Root, "server/legacy/source/2.5/gmsv")
	if e = os.CopyFS(filepath.Join(s.Work, "gmsv/data"), os.DirFS(filepath.Join(source, "data"))); e != nil {
		return e
	}
	for dest, src := range map[string]string{"gmsv/log/log.cf": "log.cf", "gmsv/badpetstring.txt": "badpetstring.txt"} {
		b, e := os.ReadFile(filepath.Join(source, src))
		if e != nil {
			return e
		}
		if e = writePrivate(filepath.Join(s.Work, dest), b); e != nil {
			return e
		}
	}
	listeners := []net.Listener{}
	ports := []int{}
	for i := 0; i < 3; i++ {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			for _, l := range listeners {
				l.Close()
			}
			return e
		}
		listeners = append(listeners, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	for _, l := range listeners {
		l.Close()
	}
	saac := fmt.Sprintf("port %d\npass ladder-fixture\nlogdir log\nlockdir lock\nchardir char\ndbdir db\nmaildir mail\nfamilydir family\nfmpointdir fmpoint\nfmsmemodir fmsmemo\nrotate_interval 604800\nTotal_Charlist 3600\nExpired_mail 600\nDel_Family_or_Member 3600\nWrite_Family 600\nSameIpMun 32\n", ports[0])
	if e = writePrivate(filepath.Join(s.Work, "saac/acserv.cf"), []byte(saac)); e != nil {
		return e
	}
	raw, e := os.ReadFile(filepath.Join(s.Root, "config/gmsv/setup.cf.example"))
	if e != nil {
		return e
	}
	overrides := map[string]string{"acserv": "127.0.0.1", "acservport": strconv.Itoa(ports[0]), "acpasswd": "ladder-fixture", "port": strconv.Itoa(ports[1]), "gameservname": "ladder-fixture", "gameservid": "ladder-fixture", "usememoryunitnum": "8000000"}
	lines := strings.Split(string(raw), "\n")
	for i, line := range lines {
		key := strings.SplitN(line, "=", 2)[0]
		if value, ok := overrides[key]; ok {
			lines[i] = key + "=" + value
		}
	}
	if e = writePrivate(filepath.Join(s.Work, "gmsv/setup.cf"), []byte(strings.Join(lines, "\n"))); e != nil {
		return e
	}
	env := append(os.Environ(), "TMPDIR="+filepath.Join(s.Work, "tmp"), "XDG_STATE_HOME="+filepath.Join(s.Work, "state"), "XDG_CONFIG_HOME="+filepath.Join(s.Work, "state"), "STONEAGE_PLAYER_ADMIN_DIR="+filepath.Join(s.Work, "admin"), "STONEAGE_LADDER_DB="+filepath.Join(s.Work, "ladder.db"), "STONEAGE_GMSV_TRUSTED_GATEWAY_HOST=127.0.0.1", "STONEAGE_GATEWAY_ROUTES=", "STONEAGE_GATEWAY_TRUSTED_PROXY_HOSTS=")
	binaries := filepath.Join(s.Root, "build/local-arena")
	seed := exec.CommandContext(ctx, filepath.Join(binaries, "bin/seed-network"), s.Work)
	seed.Env = env
	if e = seed.Run(); e != nil {
		return fmt.Errorf("seed synthetic accounts: %w", e)
	}
	processes := []*fixtureProcess{}
	defer func() {
		for i := len(processes) - 1; i >= 0; i-- {
			processes[i].close()
		}
	}()
	launch := func(name, dir string, port int, args ...string) error {
		p, e := nativeProcess(ctx, s.Work, name, dir, env, args...)
		if e != nil {
			return e
		}
		processes = append(processes, p)
		return waitUntil(ctx, name, func() bool {
			select {
			case <-p.done:
				return false
			default:
			}
			c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
			if e == nil {
				c.Close()
			}
			return e == nil
		})
	}
	if e = launch("saac", filepath.Join(s.Work, "saac"), ports[0], filepath.Join(binaries, "native/saac/saacjt.exe")); e != nil {
		return e
	}
	if e = launch("gmsv", filepath.Join(s.Work, "gmsv"), ports[1], filepath.Join(binaries, "native/gmsv/gmsvjt.exe"), "-f", "setup.cf"); e != nil {
		return e
	}
	if e = launch("gateway", s.Work, ports[2], filepath.Join(binaries, "bin/stoneage-gateway"), "-listen", fmt.Sprintf("127.0.0.1:%d", ports[2]), "-upstream", fmt.Sprintf("127.0.0.1:%d", ports[1]), "-auth-db", filepath.Join(s.Work, "auth.db"), "-auth-required"); e != nil {
		return e
	}
	before := map[int]Object{}
	configs := []MemberConfig{}
	ownership := filepath.Join(s.Work, "ownership")
	setupStore, e := OpenStore(filepath.Join(s.Work, "setup"))
	if e != nil {
		return e
	}
	defer setupStore.DB.Close()
	for i := 0; i < s.Mode*2; i++ {
		user := fmt.Sprintf("ladderqa%02d", i)
		mc := MemberConfig{ID: fmt.Sprintf("member-%d", i), Config: filepath.Join(s.Work, user+".toml"), Socket: filepath.Join(s.Work, user+".sock")}
		login := Object{"socket_path": mc.Socket, "transport": "tcp", "address": fmt.Sprintf("127.0.0.1:%d", ports[2]), "account": user, "password_file": filepath.Join(s.Work, user+".password"), "map_directory": filepath.Join(s.Work, "gmsv/data")}
		b, e := toml.Marshal(login)
		if e != nil {
			return e
		}
		if e = writePrivate(mc.Config, b); e != nil {
			return e
		}
		m := &member{cfg: mc, binary: filepath.Join(binaries, "bin/sactl"), ownership: ownership, store: setupStore}
		if e = m.start(ctx); e != nil {
			m.close()
			return e
		}
		e = func() error {
			defer m.close()
			if e := waitUntil(ctx, "character list", func() bool { _, e := m.request(ctx, "chars"); return e == nil }); e != nil {
				return e
			}
			name := fmt.Sprintf("LadderQA%02d", i)
			if _, e := m.request(ctx, "create-character", name); e != nil {
				return e
			}
			if _, e := m.request(ctx, "enter", name); e != nil {
				return e
			}
			if e := waitUntil(ctx, "world status", func() bool {
				v, e := m.data(ctx, "observe")
				if e == nil && yes(obj(v["Player"])["HasStatus"]) {
					before[i] = v
					return true
				}
				return false
			}); e != nil {
				return e
			}
			login["character"] = name
			b, e := toml.Marshal(login)
			if e != nil {
				return e
			}
			return writePrivate(mc.Config, b)
		}()
		if e != nil {
			return e
		}
		configs = append(configs, mc)
	}
	runners := []*Runner{}
	for side := 0; side < 2; side++ {
		c := Config{Schema: 1, Sactl: filepath.Join(binaries, "bin/sactl"), StateDir: filepath.Join(s.Work, fmt.Sprintf("commander-%d", side)), OwnershipDir: ownership, Mode: s.Mode, Strategy: "basic", Fallback: "basic", Members: []MemberConfig{}}
		if side == 0 && s.Strategy == "learned" {
			c.Strategy = "learned"
			c.Model = s.Model
		}
		for i, m := range configs {
			if i%2 == side {
				c.Members = append(c.Members, m)
			}
		}
		r, e := NewRunner(c)
		if e != nil {
			for _, r := range runners {
				r.Close()
			}
			return e
		}
		r.Output = s.Output
		if side == 0 && s.Strategy == "explore" {
			r.strategy = &Explore{Seed: s.Seed}
		}
		r.strategy = &clockChecked{runner: r, inner: r.strategy}
		runners = append(runners, r)
	}
	results := make(chan error, 3)
	for _, r := range runners {
		go func(r *Runner) { results <- r.Run(ctx, s.Matches) }(r)
	}
	jobs := 2
	if s.Reconnect {
		jobs++
		go func() {
			for {
				var n int
				e := runners[0].store.DB.QueryRow("SELECT count(*) FROM battle_intents WHERE status='written'").Scan(&n)
				if e != nil {
					results <- e
					return
				}
				if n > 0 {
					m := runners[0].members[0]
					results <- m.killForFixture()
					return
				}
				if e = sleep(ctx, 10*time.Millisecond); e != nil {
					results <- e
					return
				}
			}
		}()
	}
	var first error
	for i := 0; i < jobs; i++ {
		if e = <-results; e != nil && first == nil {
			first = e
			cancel()
		}
	}
	if first != nil {
		return first
	}
	evidence := []Object{}
	for side, r := range runners {
		if !r.strategy.(*clockChecked).checked {
			return fmt.Errorf("clock was never checked")
		}
		store, e := OpenStore(r.config.StateDir)
		if e != nil {
			return e
		}
		values, e := readObjects(store.DB, "SELECT body FROM results")
		if e != nil {
			store.DB.Close()
			return e
		}
		if len(values) != s.Matches {
			store.DB.Close()
			return fmt.Errorf("unexpected settled match count")
		}
		wins := 0
		for _, v := range values {
			if integer(v["mode"]) != s.Mode || !yes(v["rated"]) || str(v["reason"]) != "defeat" {
				store.DB.Close()
				return fmt.Errorf("invalid native result")
			}
			delta := 0
			won := false
			for _, p := range objects(v["members"]) {
				delta += integer(p["rating_delta"])
				if r.expected()[str(p["id"])] && integer(p["side"]) == integer(v["winner_side"]) {
					won = true
				}
			}
			if delta != 0 {
				store.DB.Close()
				return fmt.Errorf("rating sum changed")
			}
			if won {
				wins++
			}
		}
		var turns, written, restarted int
		queries := []struct {
			q string
			p *int
		}{{"SELECT count(*) FROM records WHERE kind='turn'", &turns}, {"SELECT count(*) FROM battle_intents WHERE status='written'", &written}, {"SELECT count(*) FROM records WHERE kind='daemon_restarted'", &restarted}}
		for _, q := range queries {
			if e = store.DB.QueryRow(q.q).Scan(q.p); e != nil {
				store.DB.Close()
				return e
			}
		}
		store.DB.Close()
		if turns == 0 || written == 0 || s.Reconnect && side == 0 && restarted == 0 {
			return fmt.Errorf("missing decision/recovery evidence")
		}
		evidence = append(evidence, Object{"commander": side, "strategy": r.strategy.ID(), "version": r.strategy.Version(), "results": len(values), "wins": wins, "decisions": turns, "written": written})
	}
	for i, mc := range configs {
		m := &member{cfg: mc, binary: filepath.Join(binaries, "bin/sactl"), ownership: ownership, store: setupStore}
		e = func() error {
			defer m.close()
			if e := m.start(ctx); e != nil {
				return e
			}
			var after Object
			if e := waitUntil(ctx, "relogin", func() bool {
				v, e := m.data(ctx, "observe")
				if e == nil && yes(obj(v["Player"])["HasStatus"]) {
					after = v
					return true
				}
				return false
			}); e != nil {
				return e
			}
			for _, key := range []string{"HP", "MP", "Gold"} {
				if num(obj(after["Player"])[key]) != num(obj(before[i]["Player"])[key]) {
					return fmt.Errorf("resource %s was not restored", key)
				}
			}
			if string(enc(after["Inventory"])) != string(enc(before[i]["Inventory"])) {
				return fmt.Errorf("inventory was not restored")
			}
			return nil
		}()
		if e != nil {
			return e
		}
	}
	return writePrivate(filepath.Join(s.Work, "commander-passed.json"), append(enc(Object{"mode": s.Mode, "evidence": evidence}), '\n'))
}

type clockChecked struct {
	runner  *Runner
	inner   Strategy
	checked bool
}

func (c *clockChecked) ID() string      { return c.inner.ID() }
func (c *clockChecked) Version() string { return c.inner.Version() }
func (c *clockChecked) Decide(ctx context.Context, t Object, h []Object) (Decision, error) {
	if !c.checked {
		m := c.runner.members[0]
		original := obj(obj(obj(obj(t["members"])[m.cfg.ID])["battle"])["Clock"])
		for i := 0; i < 3; i++ {
			if _, e := m.request(ctx, "query", "BTIME"); e != nil {
				return Decision{}, e
			}
			v, e := m.data(ctx, "battle-state")
			if e != nil {
				return Decision{}, e
			}
			clock := obj(obj(v["battle"])["Clock"])
			if !yes(clock["Known"]) || integer(clock["ServerTurn"]) != integer(original["ServerTurn"]) || num(clock["DeadlineMS"]) != num(original["DeadlineMS"]) || str(clock["RulesVersion"]) != RulesVersion {
				return Decision{}, fmt.Errorf("observer query changed native deadline")
			}
		}
		c.checked = true
	}
	return c.inner.Decide(ctx, t, h)
}
