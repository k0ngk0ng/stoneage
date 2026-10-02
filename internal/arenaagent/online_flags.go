package arenaagent

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/k0ngk0ng/stoneage/internal/sacli"
)

// Online sessions remain owned by normal sactl login. The commander never
// persists credentials, starts replacement daemons, or logs these sessions out.
type onlineFlags struct {
	profiles                                                   pathsFlag
	strategy, model, state, endpoint, llmModel, keyEnv, format string
	mode, petMask, contextBytes                                int
	timeout                                                    float64
}

func (o *onlineFlags) bind(f *flag.FlagSet) {
	f.Var(&o.profiles, "profile", "logged-in session; repeat for a team; default current session")
	f.StringVar(&o.strategy, "strategy", "", "basic, learned, llm or hybrid")
	f.StringVar(&o.model, "model", "", "external local model file")
	f.IntVar(&o.mode, "mode", 0, "team size 1–5; default number of profiles")
	f.StringVar(&o.state, "state-dir", "", "persistent commander data directory")
	f.IntVar(&o.petMask, "pet-mask", -1, "registered pet-slot bitmask 0–31 for each member")
	f.StringVar(&o.endpoint, "llm-endpoint", "", "Chat Completions URL")
	f.StringVar(&o.llmModel, "llm-model", "", "provider model name")
	f.StringVar(&o.keyEnv, "llm-api-key-env", "", "environment variable containing provider key")
	f.StringVar(&o.format, "llm-response-format", "", "json_object, json_schema or none")
	f.Float64Var(&o.timeout, "llm-timeout", 0, "provider timeout in seconds")
	f.IntVar(&o.contextBytes, "llm-context-bytes", 0, "complete request byte limit")
}
func (o *onlineFlags) config(path string, f *flag.FlagSet) (Config, error) {
	set := map[string]bool{}
	f.Visit(func(v *flag.Flag) { set[v.Name] = true })
	var c Config
	var err error
	if path != "" {
		if len(o.profiles) > 0 {
			return c, fmt.Errorf("--profile cannot be combined with --config")
		}
		c, err = LoadConfig(path)
		if err != nil {
			return c, err
		}
	} else {
		profiles := append(pathsFlag{}, o.profiles...)
		if len(profiles) == 0 {
			p, e := sacli.SelectedProfile()
			if e != nil {
				return c, e
			}
			if p == "" {
				p = "default"
			}
			profiles = append(profiles, p)
		}
		exe, e := os.Executable()
		if e != nil {
			return c, e
		}
		cache, e := os.UserCacheDir()
		if e != nil {
			return c, e
		}
		c = Config{Schema: 1, Sactl: exe, Strategy: "basic", Fallback: "basic", Mode: len(profiles), OwnershipDir: filepath.Join(cache, "stoneage-arena", "ownership")}
		seen := map[string]bool{}
		for i, p := range profiles {
			cfg, _, e := sacli.LoadClientProfileConfig(p, "")
			if e != nil {
				return c, e
			}
			if seen[cfg.SocketPath] {
				return c, fmt.Errorf("duplicate profile socket")
			}
			seen[cfg.SocketPath] = true
			c.Members = append(c.Members, MemberConfig{ID: fmt.Sprintf("member-%d", i+1), Profile: p, Socket: cfg.SocketPath})
		}
		// Stable across strategy changes, so existing match/submission fences apply.
		c.StateDir = sacli.StatePath(filepath.Join("ai", hash(profiles)[:20]))
	}
	if set["strategy"] {
		c.Strategy = o.strategy
	}
	if set["model"] {
		c.Model = o.model
		c.ChampionDirectory = ""
		if c.Model != "" {
			c.Model, err = filepath.Abs(c.Model)
			if err != nil {
				return c, err
			}
		}
	}
	if set["mode"] {
		c.Mode = o.mode
	}
	if c.Mode < 1 || c.Mode > 5 || len(c.Members) != c.Mode {
		return c, fmt.Errorf("--mode must be 1–5 and match the number of profiles/members")
	}
	if set["state-dir"] {
		if o.state == "" {
			return c, fmt.Errorf("--state-dir cannot be empty")
		}
		c.StateDir, err = filepath.Abs(o.state)
		if err != nil {
			return c, err
		}
	}
	if set["pet-mask"] {
		if o.petMask < 0 || o.petMask > 31 {
			return c, fmt.Errorf("--pet-mask must be 0–31")
		}
		for i := range c.Members {
			mask := o.petMask
			c.Members[i].PetMask = &mask
		}
	}
	if c.LLM == nil {
		c.LLM = Object{}
	}
	for flagName, v := range map[string]struct {
		key   string
		value any
	}{
		"llm-endpoint": {"endpoint", o.endpoint}, "llm-model": {"model", o.llmModel}, "llm-api-key-env": {"api_key_env", o.keyEnv}, "llm-response-format": {"response_format", o.format}, "llm-timeout": {"timeout_seconds", o.timeout}, "llm-context-bytes": {"context_bytes", o.contextBytes},
	} {
		if set[flagName] {
			c.LLM[v.key] = v.value
		}
	}
	if err = c.validateModelSource(); err != nil {
		return c, err
	}
	return c, nil
}
func (m *member) attachProfile(ctx context.Context) error {
	if m.socketLock == nil {
		var e error
		m.socketLock, e = claim(m.cfg.Socket + ".commander.lock")
		if e != nil {
			return e
		}
	}
	ping, e := m.data(ctx, "ping")
	if e != nil {
		return fmt.Errorf("profile %s is unavailable; run sactl login --profile %s", m.cfg.Profile, m.cfg.Profile)
	}
	if !yes(ping["interactive_login"]) {
		return fmt.Errorf("profile %s uses an unsupported daemon; no game action performed", m.cfg.Profile)
	}
	status, e := m.data(ctx, "status")
	if e != nil {
		return e
	}
	if str(status["Account"]) == "" || str(status["Character"]) == "" || !yes(obj(status["Player"])["HasStatus"]) {
		return fmt.Errorf("profile %s must be logged in and entered: sactl login --profile %s; sactl --profile %s enter <character>", m.cfg.Profile, m.cfg.Profile, m.cfg.Profile)
	}
	login := Object{"account": status["Account"], "character": status["Character"], "transport": ping["transport"], "server_id": ping["server_id"], "socket_path": m.cfg.Socket}
	endpoint := str(ping["endpoint"])
	if endpoint == "" {
		return fmt.Errorf("daemon did not identify its endpoint; no game action performed")
	}
	service := []string{str(ping["transport"]), endpoint}
	if str(ping["transport"]) == "http" {
		login["web_base_url"] = endpoint
		service = []string{"http", strings.TrimRight(endpoint, "/"), str(ping["server_id"])}
	} else {
		login["address"] = endpoint
	}
	identity := append(append([]string{}, service...), str(login["account"]), str(login["character"]))
	if m.login != nil && (str(m.login["account"]) != str(login["account"]) || str(m.login["character"]) != str(login["character"]) || m.service != hash(service)) {
		return fmt.Errorf("profile identity changed; refusing to control another character")
	}
	if m.identityLock == nil {
		m.identityLock, e = claim(filepath.Join(m.ownership, hash(identity)+".lock"))
		if e != nil {
			return e
		}
	}
	if m.store != nil {
		binding := hash(identity)
		m.store.tx(func(tx *sql.Tx) error {
			var old string
			e := tx.QueryRow("SELECT body FROM records WHERE kind='profile_binding' AND member=? ORDER BY id LIMIT 1", m.cfg.ID).Scan(&old)
			if e == sql.ErrNoRows {
				return record(tx, "profile_binding", binding, "", m.cfg.ID)
			}
			if e != nil {
				return e
			}
			if old != string(enc(binding)) {
				return fmt.Errorf("profile now belongs to another character/server; use a different --state-dir")
			}
			return nil
		})
		if e := m.store.Err(); e != nil {
			return e
		}
	}
	m.login = login
	m.service = hash(service)
	return nil
}
