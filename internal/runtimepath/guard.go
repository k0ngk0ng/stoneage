// Package runtimepath contains the small path boundary shared by the
// project-managed Codex runtime and its configuration materializer.  It only
// inspects path metadata; it never reads configuration file contents.
package runtimepath

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// ErrForbiddenPath identifies an operator-owned Codex/configuration path.
// Callers can expose this error without revealing the protected path.
var ErrForbiddenPath = errors.New("runtime path is not isolated from operator configuration")

// Guard contains the canonical roots which a project-managed runtime must
// never read or write.  A Guard is immutable after construction and is safe
// to copy into a long-lived runner.
type Guard struct {
	forbidden []string
}

// NewGuard discovers the operator's normal Codex home, the CODEX_HOME
// override, and .codex directories in the current project ancestry.  It uses
// only environment values and filesystem metadata (Lstat/EvalSymlinks); no
// config file is opened.
func NewGuard() (Guard, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		if current, currentErr := user.Current(); currentErr == nil {
			home = current.HomeDir
			err = nil
		}
	}
	if err != nil || strings.TrimSpace(home) == "" {
		return Guard{}, fmt.Errorf("%w: cannot determine operator home", ErrForbiddenPath)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return Guard{}, fmt.Errorf("%w: cannot determine current project", ErrForbiddenPath)
	}

	roots := make([]string, 0, 8)
	seen := make(map[string]struct{})
	add := func(path string) error {
		if strings.TrimSpace(path) == "" {
			return nil
		}
		canonical, err := Canonical(path)
		if err != nil {
			return fmt.Errorf("%w: cannot inspect protected root", ErrForbiddenPath)
		}
		key := canonical
		if insensitivePlatform() {
			key = strings.ToLower(key)
		}
		if _, ok := seen[key]; ok {
			return nil
		}
		seen[key] = struct{}{}
		roots = append(roots, canonical)
		return nil
	}

	// ~/.codex remains forbidden even when the operator has selected a
	// different CODEX_HOME for their interactive Codex installation.
	if err := add(filepath.Join(home, ".codex")); err != nil {
		return Guard{}, err
	}
	// HOME can be intentionally overridden for a service process. Also guard
	// the account home reported by the OS so that override cannot hide the
	// operator's normal ~/.codex directory.
	if current, currentErr := user.Current(); currentErr == nil && strings.TrimSpace(current.HomeDir) != "" && current.HomeDir != home {
		if err := add(filepath.Join(current.HomeDir, ".codex")); err != nil {
			return Guard{}, err
		}
	}
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		if err := add(codexHome); err != nil {
			return Guard{}, err
		}
	}

	// Codex project configuration may be discovered from the current
	// directory or one of its parents. Record every ancestor .codex path so a
	// workspace entered through a symlink cannot alias project configuration.
	absoluteCWD, err := filepath.Abs(cwd)
	if err != nil {
		return Guard{}, fmt.Errorf("%w: cannot resolve current project", ErrForbiddenPath)
	}
	for directory := filepath.Clean(absoluteCWD); ; directory = filepath.Dir(directory) {
		if err := add(filepath.Join(directory, ".codex")); err != nil {
			return Guard{}, err
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			break
		}
	}
	return Guard{forbidden: roots}, nil
}

// Check rejects path itself and every descendant of a protected root.  The
// path may not exist yet; its existing ancestors are resolved so a symlink
// alias is still caught before a caller creates anything there.
func (g Guard) Check(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: empty path", ErrForbiddenPath)
	}
	candidate, err := Canonical(path)
	if err != nil {
		return fmt.Errorf("%w: cannot inspect candidate path", ErrForbiddenPath)
	}
	for _, root := range g.forbidden {
		if within(root, candidate) {
			return ErrForbiddenPath
		}
	}
	return nil
}

// CheckAll applies Check to each non-empty path and is convenient for a
// runtime configuration containing several independent roots.
func (g Guard) CheckAll(paths ...string) error {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		if err := g.Check(path); err != nil {
			return err
		}
	}
	return nil
}

// Canonical returns an absolute path with all existing symlink components
// resolved.  It supports a not-yet-created leaf by resolving its nearest
// existing ancestor and appending the lexical suffix.  Lstat/EvalSymlinks
// inspect metadata only; no file contents are read.
func Canonical(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("empty path")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(absolute)
	for current := clean; ; current = filepath.Dir(current) {
		_, statErr := os.Lstat(current)
		if statErr == nil {
			resolved, evalErr := filepath.EvalSymlinks(current)
			if evalErr != nil {
				return "", evalErr
			}
			relative, relErr := filepath.Rel(current, clean)
			if relErr != nil {
				return "", relErr
			}
			return filepath.Clean(filepath.Join(resolved, relative)), nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return clean, nil
		}
	}
}

func within(root, candidate string) bool {
	if insensitivePlatform() {
		root = strings.ToLower(root)
		candidate = strings.ToLower(candidate)
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func insensitivePlatform() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "windows"
}
