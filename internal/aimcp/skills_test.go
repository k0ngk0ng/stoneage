package aimcp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixedSkillCatalogVerifiesAndInstallsIdempotently(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "ai", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	installer, err := NewSkillInstaller(root)
	if err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	for _, spec := range SkillCatalog() {
		verified, err := installer.Verify(spec.Name)
		if err != nil {
			t.Fatalf("verify %s: %v", spec.Name, err)
		}
		if verified.Version == "" || verified.Version != spec.Version || verified.SHA256 == "" {
			t.Fatalf("catalog entry incomplete: %+v", verified)
		}
		installed, err := installer.Install(spec.Name, workdir)
		if err != nil {
			t.Fatalf("install %s: %v", spec.Name, err)
		}
		if installed != spec {
			t.Fatalf("installed spec differs: %+v vs %+v", installed, spec)
		}
		if _, err := os.Stat(filepath.Join(workdir, ".agents", "skills", spec.Name, "SKILL.md")); err != nil {
			t.Fatalf("installed skill missing: %v", err)
		}
		if _, err := installer.Install(spec.Name, workdir); err != nil {
			t.Fatalf("idempotent install %s: %v", spec.Name, err)
		}
	}
}

func TestSkillInstallerRejectsSymlinkAndConflictingDestination(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "ai", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	installer, err := NewSkillInstaller(root)
	if err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	agents := filepath.Join(workdir, ".agents")
	if err := os.Symlink(t.TempDir(), agents); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := installer.Install("stoneage-play", workdir); !errors.Is(err, ErrSkillPath) {
		t.Fatalf("symlink destination error = %v, want ErrSkillPath", err)
	}

	workdir = t.TempDir()
	target := filepath.Join(workdir, ".agents", "skills", "stoneage-play")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Install("stoneage-play", workdir); !errors.Is(err, ErrSkillConflict) {
		t.Fatalf("conflicting destination error = %v, want ErrSkillConflict", err)
	}
}

func TestSkillInstallerRejectsUnknownSkillAndRelativeWorkdir(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "ai", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	installer, err := NewSkillInstaller(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Verify("not-catalogued"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("unknown skill error = %v", err)
	}
	if _, err := installer.Install("stoneage-play", "relative-agent"); !errors.Is(err, ErrSkillPath) {
		t.Fatalf("relative workdir error = %v, want ErrSkillPath", err)
	}
}

func TestSkillReconcileRemovesDeselectedCatalogTreeAndPreservesModifiedContent(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "ai", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	installer, err := NewSkillInstaller(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if err := installer.Reconcile([]string{"stoneage-play", "stoneage-social"}, workspace); err != nil {
		t.Fatal(err)
	}
	if err := installer.Reconcile([]string{"stoneage-play"}, workspace); err != nil {
		t.Fatal(err)
	}
	social := filepath.Join(workspace, ".agents", "skills", "stoneage-social")
	if _, err := os.Stat(social); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deselected skill remains discoverable")
	}
	if _, err := installer.Install("stoneage-social", workspace); err != nil {
		t.Fatal(err)
	}
	privateNote := filepath.Join(social, "private-note.txt")
	if err := os.WriteFile(privateNote, []byte("unreviewed content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installer.Reconcile([]string{"stoneage-play"}, workspace); !errors.Is(err, ErrSkillConflict) {
		t.Fatalf("modified tree was not rejected: %v", err)
	}
	if _, err := os.Stat(privateNote); err != nil {
		t.Fatal("reconcile deleted unreviewed content")
	}
	if err := installer.Reconcile([]string{"unknown-skill"}, workspace); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("invalid selection was not rejected: %v", err)
	}
}

func TestSkillInstallerUpgradesOnlyAnIntactLegacyTree(t *testing.T) {
	oldCatalog := builtinSkillCatalog
	oldLegacyHashes := legacySkillTreeHashes
	t.Cleanup(func() {
		builtinSkillCatalog = oldCatalog
		legacySkillTreeHashes = oldLegacyHashes
	})

	root := t.TempDir()
	source := filepath.Join(root, "stoneage-play")
	writeSkillFixture(t, source, "2.0.0", "current")
	currentHash, err := hashSkillTree(source)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(t.TempDir(), "legacy")
	writeSkillFixture(t, legacy, "1.0.0", "legacy")
	legacyHash, err := hashSkillTree(legacy)
	if err != nil {
		t.Fatal(err)
	}
	builtinSkillCatalog = map[string]SkillSpec{
		"stoneage-play": {
			Name: "stoneage-play", Version: "2.0.0", RelativePath: "stoneage-play", SHA256: currentHash,
		},
	}
	legacySkillTreeHashes = map[string]string{"stoneage-play": legacyHash}

	installer, err := NewSkillInstaller(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	target := filepath.Join(workspace, ".agents", "skills", "stoneage-play")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	copyFixture(t, legacy, target)

	if _, err := installer.Install("stoneage-play", workspace); err != nil {
		t.Fatalf("legacy install: %v", err)
	}
	got, err := hashSkillTree(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != currentHash {
		t.Fatalf("upgraded hash = %s, want %s", got, currentHash)
	}
	if data, err := os.ReadFile(filepath.Join(target, "SKILL.md")); err != nil || !strings.Contains(string(data), "version: 2.0.0") {
		t.Fatalf("upgraded document = %q, err = %v", data, err)
	}
	assertNoSkillTransactionArtifacts(t, filepath.Dir(target))
	if _, err := installer.Install("stoneage-play", workspace); err != nil {
		t.Fatalf("repeat install after upgrade: %v", err)
	}
	assertNoSkillTransactionArtifacts(t, filepath.Dir(target))

	// A byte added to the old tree changes its complete digest and must keep
	// the installation in place rather than trigger an overwrite.
	workspace = t.TempDir()
	target = filepath.Join(workspace, ".agents", "skills", "stoneage-play")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	copyFixture(t, legacy, target)
	if err := os.WriteFile(filepath.Join(target, "private-note.txt"), []byte("edited"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Install("stoneage-play", workspace); !errors.Is(err, ErrSkillConflict) {
		t.Fatalf("modified legacy install error = %v, want ErrSkillConflict", err)
	}
	if _, err := os.Stat(filepath.Join(target, "private-note.txt")); err != nil {
		t.Fatalf("modified legacy tree was not preserved: %v", err)
	}
}

func TestSkillReconcileRemovesAnIntactLegacyTree(t *testing.T) {
	oldCatalog := builtinSkillCatalog
	oldLegacyHashes := legacySkillTreeHashes
	t.Cleanup(func() {
		builtinSkillCatalog = oldCatalog
		legacySkillTreeHashes = oldLegacyHashes
	})

	root := t.TempDir()
	source := filepath.Join(root, "stoneage-social")
	writeSkillFixture(t, source, "2.0.0", "current")
	currentHash, err := hashSkillTree(source)
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(t.TempDir(), "legacy")
	writeSkillFixture(t, legacy, "1.0.0", "legacy")
	legacyHash, err := hashSkillTree(legacy)
	if err != nil {
		t.Fatal(err)
	}
	builtinSkillCatalog = map[string]SkillSpec{
		"stoneage-social": {
			Name: "stoneage-social", Version: "2.0.0", RelativePath: "stoneage-social", SHA256: currentHash,
		},
	}
	legacySkillTreeHashes = map[string]string{"stoneage-social": legacyHash}
	installer, err := NewSkillInstaller(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	target := filepath.Join(workspace, ".agents", "skills", "stoneage-social")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	copyFixture(t, legacy, target)
	if err := installer.Reconcile(nil, workspace); err != nil {
		t.Fatalf("reconcile legacy tree: %v", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy tree remains after reconcile, err = %v", err)
	}
}

func writeSkillFixture(t *testing.T, path, version, marker string) {
	t.Helper()
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	document := fmt.Sprintf("---\nname: %s\nversion: %s\ndescription: fixture\n---\n\n%s\n", filepath.Base(path), version, marker)
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(document), 0644); err != nil {
		t.Fatal(err)
	}
}

func copyFixture(t *testing.T, source, destination string) {
	t.Helper()
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(destination, entry.Name()), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func assertNoSkillTransactionArtifacts(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stoneage-skill-") {
			t.Fatalf("skill transaction artifact remains: %s", entry.Name())
		}
	}
}
