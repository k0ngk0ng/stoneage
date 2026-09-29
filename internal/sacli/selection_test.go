package sacli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigFreeProfilesAndSelection(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	first, path, err := LoadClientProfileConfig("first", "")
	if err != nil || path != "" || first.Transport != "http" || first.WebBaseURL != "https://sa.ichenj.com" {
		t.Fatal("config-free profile failed", err)
	}
	second, _, err := LoadClientProfileConfig("second", "")
	if err != nil || first.SocketPath == second.SocketPath {
		t.Fatal("profiles not isolated", err)
	}
	for _, name := range []string{"first", "second", "default"} {
		if err := SelectProfile(name); err != nil {
			t.Fatal(err)
		}
		if selected, err := SelectedProfile(); err != nil || selected != name {
			t.Fatal("selection lost", err)
		}
	}
	if err := SelectProfile("../bad"); err == nil {
		t.Fatal("accepted traversal")
	}
}

func TestClientConfigDoesNotReadLegacyCredentialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("password_file = '/nonexistent/secret'\naccount = 'legacy'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config, _, err := LoadClientProfileConfig("", path)
	if err != nil || config.Account != "" || config.Password != "" || config.PasswordFile != "" {
		t.Fatal("client queried legacy credentials", err)
	}
	if _, err = LoadConfig(path); err == nil {
		t.Fatal("explicit automation config ignored missing credential file")
	}
}
