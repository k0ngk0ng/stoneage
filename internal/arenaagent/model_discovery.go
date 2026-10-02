package arenaagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Discovery is shallow, read-only, and tiered. Legacy JSON models remain
// supported when explicitly named; automatic selection uses safetensors.
func localModelDirectories() ([]string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return nil, e
		}
		data = filepath.Join(home, ".local", "share")
	}
	return []string{cwd, filepath.Join(cwd, "runtime", "ai-models"), filepath.Join(data, "sactl", "models")}, nil
}
func resolveLocalModel(name string, mode int, dirs []string) (string, error) {
	if name != "" {
		// Explicit paths must never silently select a different file.
		if filepath.IsAbs(name) || filepath.Base(name) != name {
			p, e := filepath.Abs(name)
			if e != nil {
				return "", e
			}
			if _, e = inspectLocalModel(p, mode); e != nil {
				return "", fmt.Errorf("model %s: %w", p, e)
			}
			return p, nil
		}
		for _, dir := range dirs {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); os.IsNotExist(err) {
				continue
			}
			if _, err := inspectLocalModel(p, mode); err != nil {
				return "", fmt.Errorf("model %s: %w", p, err)
			}
			return p, nil
		}
		return "", fmt.Errorf("model %q not found; searched: %s", name, strings.Join(dirs, ", "))
	}
	var rejected []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		candidates := []string{}
		versions := map[string]bool{}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".safetensors" {
				continue
			}
			p := filepath.Join(dir, entry.Name())
			m, e := inspectLocalModel(p, mode)
			if e != nil {
				rejected = append(rejected, fmt.Sprintf("%s: %v", p, e))
				continue
			}
			if !versions[m.Version()] {
				candidates = append(candidates, p)
				versions[m.Version()] = true
			}
		}
		if len(candidates) == 1 {
			return candidates[0], nil
		}
		if len(candidates) > 1 {
			return "", fmt.Errorf("multiple local models; choose with --model <path>:\n  %s", strings.Join(candidates, "\n  "))
		}
	}
	detail := ""
	if len(rejected) > 0 {
		detail = "\nRejected models:\n  " + strings.Join(rejected, "\n  ")
	}
	return "", fmt.Errorf("no usable local .safetensors model for %dv%d; use --model <path>; searched: %s%s", mode, mode, strings.Join(dirs, ", "), detail)
}

func inspectLocalModel(path string, mode int) (*Learned, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 128<<20 {
		return nil, fmt.Errorf("model must be a regular file of at most 128 MiB")
	}
	return NewLearned(path, mode)
}
