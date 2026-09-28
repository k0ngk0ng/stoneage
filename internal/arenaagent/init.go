package arenaagent

import (
	"fmt"
	"github.com/pelletier/go-toml/v2"
	"os"
	"path/filepath"
)

// Init creates only a new directory. Existing accounts, models and checkpoints
// are never overwritten. Secrets are filled by the user, not command arguments.
func Init(directory string, mode int) (Object, error) {
	if directory == "" || mode < 1 || mode > 5 {
		return nil, fmt.Errorf("init requires --directory and mode 1–5")
	}
	dir, e := filepath.Abs(directory)
	if e != nil {
		return nil, e
	}
	if len([]byte(filepath.Join(dir, "member-1.sock"))) > 100 {
		return nil, fmt.Errorf("directory too long for local sockets; choose a shorter path")
	}
	if e = os.MkdirAll(filepath.Dir(dir), 0700); e != nil {
		return nil, e
	}
	if e = os.Mkdir(dir, 0700); e != nil {
		return nil, fmt.Errorf("init needs a new directory: %w", e)
	}
	exe, e := os.Executable()
	if e != nil {
		return nil, e
	}
	binary := exe
	c := Config{Schema: 1, Sactl: binary, StateDir: filepath.Join(dir, "data"), Mode: mode, Strategy: "basic", Fallback: "basic", Members: []MemberConfig{}, LLM: Object{"endpoint": "http://127.0.0.1:8000/v1/chat/completions", "model": "your-model-name", "api_key_env": "STONEAGE_ARENA_MODEL_KEY", "timeout_seconds": 12, "context_bytes": 180000, "response_format": "json_object"}}
	for i := 0; i < mode; i++ {
		id := fmt.Sprintf("member-%d", i+1)
		socket := filepath.Join(dir, id+".sock")
		if len([]byte(socket)) > 100 {
			return nil, fmt.Errorf("directory too long for local sockets; choose a shorter path")
		}
		m := MemberConfig{ID: id, Config: filepath.Join(dir, id+".toml"), Socket: socket}
		login := Object{"socket_path": socket, "transport": "http", "web_base_url": "https://sa.ichenj.com", "account": "YOUR_ACCOUNT", "character": "YOUR_CHARACTER", "password_file": filepath.Join(dir, id+".password")}
		raw, e := toml.Marshal(login)
		if e != nil {
			return nil, e
		}
		if e = writePrivate(m.Config, raw); e != nil {
			return nil, e
		}
		if e = writePrivate(str(login["password_file"]), nil); e != nil {
			return nil, e
		}
		c.Members = append(c.Members, m)
	}
	path := filepath.Join(dir, "team.json")
	if e = writePrivate(path, append(enc(c), '\n')); e != nil {
		return nil, e
	}
	return Object{"config": path, "mode": mode, "next": "Edit each member TOML account/character and password file, then run sactl arena check --config <team.json> and sactl arena run --config <team.json> --forever."}, nil
}
