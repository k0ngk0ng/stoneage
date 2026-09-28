package sacli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfilesIsolateSockets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	seen := map[string]bool{}
	for _, name := range []string{"main", "alt"} {
		path, socket, err := ProfilePaths(name)
		if err != nil {
			t.Fatal(err)
		}
		if seen[socket] {
			t.Fatal("profiles share socket")
		}
		seen[socket] = true
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte("transport = 'http'\nweb_base_url = 'https://example.com'\nsocket_path = 'incorrect-shared.sock'\n"), 0600); err != nil {
			t.Fatal(err)
		}
		config, _, err := LoadProfileConfig(name, "")
		if err != nil || config.SocketPath != socket {
			t.Fatal("profile must force its isolated socket", err)
		}
	}
	for _, name := range []string{"../bad", "", "with space", strings.Repeat("a", 33)} {
		if _, _, err := ProfilePaths(name); err == nil {
			t.Fatal("invalid profile accepted", name)
		}
	}
	if _, _, err := LoadProfileConfig("main", "other.toml"); err == nil {
		t.Fatal("ambiguous configuration accepted")
	}
}

func TestStatusDoesNotLoginAndLogoutClearsCredentials(t *testing.T) {
	// A nonexistent address makes any accidental connection fail. Status must
	// still succeed and report disconnected even with legacy credentials set.
	config := DefaultConfig()
	config.Address = "invalid"
	config.Account = "test-only"
	config.Password = "test-only"
	config.PasswordFile = "test-only"
	server := NewServer(config)
	response := server.Dispatch(context.Background(), Request{Command: "status", JSON: true})
	if !response.OK {
		t.Fatal("status tried to log in", response.Error)
	}
	var state struct {
		Connected bool
		Phase     string
	}
	if err := json.Unmarshal(response.Data, &state); err != nil || state.Connected || state.Phase != "logged_out" {
		t.Fatal("invalid disconnected status")
	}
	response = server.Dispatch(context.Background(), Request{Command: "logout"})
	if !response.OK {
		t.Fatal(response.Error)
	}
	if server.config.Account != "" || server.config.Password != "" || server.config.PasswordFile != "" {
		t.Fatal("logout retained credentials")
	}
	if _, err := server.session(context.Background()); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatal("logout can reconnect", err)
	}
}
