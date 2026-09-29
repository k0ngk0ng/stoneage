package sacli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var profileName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$`)

// ProfilePaths never consults cwd or default-account environment overrides.
func ProfilePaths(name string) (config, socket string, err error) {
	if !profileName.MatchString(name) {
		return "", "", fmt.Errorf("invalid profile %q: use 1-32 letters, digits, _ or -", name)
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return "", "", e
		}
		base = filepath.Join(home, ".config")
	}
	config = filepath.Join(base, "sactl", "profiles", name+".toml")
	socket = StatePath(filepath.Join("profiles", name+".sock"))
	return
}

func LoadProfileConfig(name, path string) (Config, string, error) {
	return loadProfileConfig(name, path, true)
}

// LoadClientProfileConfig resolves public connection settings without reading
// legacy password files. Interactive login and queries never need those files.
func LoadClientProfileConfig(name, path string) (Config, string, error) {
	return loadProfileConfig(name, path, false)
}

func loadProfileConfig(name, path string, credentials bool) (Config, string, error) {
	if name == "default" {
		name = ""
	}
	if name == "" {
		return loadConfigPath(path, credentials)
	}
	if path != "" {
		return Config{}, "", fmt.Errorf("--profile and --config cannot be combined")
	}
	path, socket, err := ProfilePaths(name)
	if err != nil {
		return Config{}, "", err
	}
	config, err := loadConfig(path, credentials)
	if errors.Is(err, os.ErrNotExist) {
		config, path, err = DefaultConfig(), "", nil
	}
	if err != nil {
		return Config{}, path, err
	}
	config.SocketPath = socket
	return config, path, nil
}

func (config Config) Endpoint() string {
	if config.Transport == "http" {
		return config.WebBaseURL
	}
	return config.Address
}
