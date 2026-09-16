package runtimepath

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func testRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	base := filepath.Join(filepath.Dir(filename), "..", "..", "build", "ai")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "runtimepath-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func changeDir(t *testing.T, path string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func TestGuardRejectsOperatorCodexHomesAndProjectAliases(t *testing.T) {
	root := testRoot(t)
	operatorHome := filepath.Join(root, "operator-home")
	operatorCodex := filepath.Join(operatorHome, ".codex")
	if err := os.MkdirAll(operatorCodex, 0700); err != nil {
		t.Fatal(err)
	}
	operatorAlias := filepath.Join(root, "operator-alias")
	if err := os.Symlink(operatorCodex, operatorAlias); err != nil {
		t.Fatal(err)
	}

	project := filepath.Join(root, "project")
	projectCodexTarget := filepath.Join(root, "project-codex")
	if err := os.MkdirAll(projectCodexTarget, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(projectCodexTarget, filepath.Join(project, ".codex")); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", operatorHome)
	t.Setenv("CODEX_HOME", operatorCodex)
	changeDir(t, project)
	guard, err := NewGuard()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		operatorCodex,
		filepath.Join(operatorCodex, "agent", "config.toml"),
		filepath.Join(operatorAlias, "agent", "config.toml"),
		filepath.Join(project, ".codex"),
		filepath.Join(project, ".codex", "config.toml"),
		filepath.Join(projectCodexTarget, "config.toml"),
	} {
		if err := guard.Check(path); err == nil {
			t.Errorf("protected path accepted: %s", path)
		}
	}
	for _, path := range []string{
		filepath.Join(root, "runtime"),
		filepath.Join(root, "runtime", "agent", "config.toml"),
		filepath.Join(root, "operator-home", ".codex.backup"),
	} {
		if err := guard.Check(path); err != nil {
			t.Errorf("independent path rejected: %s: %v", path, err)
		}
	}
}

func TestCanonicalResolvesSymlinkedExistingAncestorForMissingLeaf(t *testing.T) {
	root := testRoot(t)
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	got, err := Canonical(filepath.Join(alias, "not-yet-created", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(target, "not-yet-created", "config.toml")
	if got != want {
		t.Fatalf("canonical path = %q, want %q", got, want)
	}
}
