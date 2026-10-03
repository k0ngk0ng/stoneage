package aiknowledge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Resolve the same table as GMSV, instead of letting the wiki, CLI and task
// planner silently describe different encounters. Only this setting is used;
// setup.cf may contain credentials and is never exported or fingerprinted.
func effectiveGroupFile(dataDir, explicit string) (string, error) {
	if explicit != "" {
		return validateGroupFilename(explicit)
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, "..", "setup.cf"))
	if errors.Is(err, os.ErrNotExist) {
		return "group.txt", nil
	}
	if err != nil {
		return "", fmt.Errorf("aiknowledge: cannot read native groupfile setting: %w", err)
	}
	selected := "group.txt"
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "groupfile" {
			continue
		}
		value = filepath.Clean(strings.TrimSpace(value))
		name := filepath.Base(value)
		// A sibling setup file resolves data/... relative to the GMSV root.
		// Do not silently take the basename of an unrelated/absolute path.
		if value != name && value != filepath.Join(filepath.Base(dataDir), name) {
			return "", fmt.Errorf("aiknowledge: native groupfile must reference a table in the configured data directory")
		}
		selected, err = validateGroupFilename(name)
		if err != nil {
			return "", err
		}
	}
	return selected, nil
}

func validateGroupFilename(name string) (string, error) {
	if filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) || !strings.HasSuffix(name, ".txt") || name == ".txt" {
		return "", fmt.Errorf("aiknowledge: invalid group table filename")
	}
	return name, nil
}
