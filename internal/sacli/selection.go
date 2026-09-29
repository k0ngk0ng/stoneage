package sacli

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func SelectedProfile() (string, error) {
	raw, err := os.ReadFile(StatePath("current-profile"))
	if errors.Is(err, os.ErrNotExist) {
		return "default", nil
	}
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(raw))
	if _, _, err := ProfilePaths(name); err != nil {
		return "", err
	}
	return name, nil
}

func SelectProfile(name string) error {
	if name == "" {
		name = "default"
	}
	if _, _, err := ProfilePaths(name); err != nil {
		return err
	}
	path := StatePath("current-profile")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".profile-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.WriteString(name + "\n"); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// KnownProfiles includes running/configured profiles; credentials are never read.
func KnownProfiles() []string {
	seen := map[string]bool{"default": true}
	path, _, _ := ProfilePaths("placeholder")
	for directory, suffix := range map[string]string{filepath.Dir(path): ".toml", StatePath("profiles"): ".sock"} {
		entries, _ := os.ReadDir(directory)
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), suffix)
			if profileName.MatchString(name) {
				seen[name] = true
			}
		}
	}
	if current, err := SelectedProfile(); err == nil {
		seen[current] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
