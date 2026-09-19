// Package sacli implements the sactl command-line game client.
//
// sactl has two parts. `sactl serve` is a long-running process that holds one
// named-protocol game session and listens on a private Unix socket; every
// other invocation is a one-shot client that sends one request to that
// socket and prints the answer. The split exists because entering the world
// is a stateful operation while a terminal agent needs short, stateless
// commands.
package sacli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// Config is the daemon configuration. Credentials live here or in
// PasswordFile; they are never command-line arguments.
type Config struct {
	SocketPath string `toml:"socket_path"`
	// Transport selects how the game session reaches the server:
	//
	//   "tcp"  (default) dials the named-protocol gateway directly
	//   "http" speaks the same protocol through the Web front end, which is
	//          what lets a client reach a deployment over its normal HTTPS
	//          address without a tunnel or an exposed game port
	Transport string `toml:"transport"`
	// Address is the named-protocol gateway listener, not a raw GMSV address.
	Address string `toml:"address"`
	// WebBaseURL is the deployment's Web address for the http transport, for
	// example https://sa.ichenj.com. The browser client uses the same entry.
	WebBaseURL string `toml:"web_base_url"`
	// ServerID selects the game line for the http transport, as listed by the
	// Web server directory. Empty picks the first enabled line.
	ServerID string `toml:"server_id"`
	Account  string `toml:"account"`
	// Password is the account password. Prefer PasswordFile so the secret is
	// not stored in a file that may be edited or shared casually.
	Password     string `toml:"password"`
	PasswordFile string `toml:"password_file"`
	// Character is entered after login. Empty keeps the session at the
	// character list.
	Character string `toml:"character"`
	// MapDirectory is the server data directory used for routing and warps.
	// It may be the gmsv data directory (containing map/), a gmsv directory,
	// or the map directory itself; both the navigator and the knowledge layer
	// resolve the layout themselves.
	MapDirectory string `toml:"map_directory"`
}

// DefaultConfig returns the defaults an installed sactl uses. Paths live
// under the operator's state directory so the binary works from any working
// directory, not only from a repository checkout.
func DefaultConfig() Config {
	return Config{
		SocketPath:   StatePath("sactl.sock"),
		Address:      "127.0.0.1:9065",
		MapDirectory: "",
	}
}

// StatePath returns a path inside the operator's state directory
// ($XDG_STATE_HOME/sactl or ~/.local/state/sactl), creating nothing.
func StatePath(name string) string {
	base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			base = filepath.Join(home, ".local", "state")
		}
	}
	if base == "" {
		// No home directory: stay relative rather than refusing to run.
		return filepath.Join("runtime", "sactl", name)
	}
	return filepath.Join(base, "sactl", name)
}

// ConfigSearchPaths lists the config files LoadConfigPath tries when the
// caller did not name one, in priority order.
func ConfigSearchPaths() []string {
	paths := make([]string, 0, 4)
	if env := strings.TrimSpace(os.Getenv("STONEAGE_SACTL_CONFIG")); env != "" {
		paths = append(paths, env)
	}
	paths = append(paths, "sactl.toml", filepath.Join("runtime", "sactl.toml"))
	if base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); base != "" {
		paths = append(paths, filepath.Join(base, "sactl", "sactl.toml"))
	} else if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		paths = append(paths, filepath.Join(home, ".config", "sactl", "sactl.toml"))
	}
	return paths
}

// LoadConfigPath finds and loads the first config file that exists. With no
// file it returns the defaults, so a fresh install can still run.
func LoadConfigPath(explicit string) (Config, string, error) {
	if strings.TrimSpace(explicit) != "" {
		config, err := LoadConfig(explicit)
		return config, explicit, err
	}
	for _, path := range ConfigSearchPaths() {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		config, err := LoadConfig(path)
		return config, path, err
	}
	return DefaultConfig(), "", nil
}

// LoadConfig reads a TOML file over the defaults. An empty path returns the
// defaults unchanged.
func LoadConfig(path string) (Config, error) {
	config := DefaultConfig()
	if strings.TrimSpace(path) != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		if err := toml.Unmarshal(data, &config); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if config.Password == "" && config.PasswordFile != "" {
		data, err := os.ReadFile(config.PasswordFile)
		if err != nil {
			return Config{}, fmt.Errorf("read password file: %w", err)
		}
		config.Password = strings.TrimRight(string(data), "\r\n")
	}
	if config.SocketPath == "" {
		return Config{}, fmt.Errorf("config: socket_path is required")
	}
	switch strings.ToLower(strings.TrimSpace(config.Transport)) {
	case "", "tcp":
		config.Transport = "tcp"
		if config.Address == "" {
			return Config{}, fmt.Errorf("config: address is required for the tcp transport")
		}
	case "http", "web":
		config.Transport = "http"
		if config.WebBaseURL == "" {
			return Config{}, fmt.Errorf("config: web_base_url is required for the http transport")
		}
	default:
		return Config{}, fmt.Errorf("config: unknown transport %q (use tcp or http)", config.Transport)
	}
	return config, nil
}
