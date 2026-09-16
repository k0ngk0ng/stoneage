//go:build linux || darwin

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/automation"
)

func makeRecoveryLockDatabase(t *testing.T, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "automation.db")
	if err := os.WriteFile(path, []byte("sqlite"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAutomationRecoveryLockSerializesSameDatabase(t *testing.T) {
	path := makeRecoveryLockDatabase(t, 0600)
	release, err := acquireAutomationRecoveryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := acquireAutomationRecoveryLock(path)
	if err == nil || second != nil {
		if second != nil {
			second()
		}
		t.Fatalf("second lock unexpectedly acquired: release=%v err=%v", second != nil, err)
	}
	release()
	release()

	second, err = acquireAutomationRecoveryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := canonicalRecoveryLockPath(t, path)
	if info, err := os.Stat(lockPath); err != nil {
		t.Fatalf("sidecar was not retained: %v", err)
	} else if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("unexpected sidecar metadata: mode=%#o regular=%v", info.Mode().Perm(), info.Mode().IsRegular())
	}
	second()
}

func TestAutomationRecoveryLockRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.db")
	link := filepath.Join(dir, "automation.db")
	if err := os.WriteFile(target, []byte("sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if release, err := acquireAutomationRecoveryLock(link); err == nil {
		release()
		t.Fatal("symlink database unexpectedly accepted")
	}
}

func TestAutomationRecoveryLockRequiresRegular0600File(t *testing.T) {
	t.Run("database mode", func(t *testing.T) {
		path := makeRecoveryLockDatabase(t, 0640)
		if release, err := acquireAutomationRecoveryLock(path); err == nil {
			release()
			t.Fatal("insecure database mode unexpectedly accepted")
		}
	})

	t.Run("database nonregular", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "automation.db")
		if err := os.Mkdir(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if release, err := acquireAutomationRecoveryLock(directory); err == nil {
			release()
			t.Fatal("directory unexpectedly accepted as database")
		}
	})

	t.Run("sidecar mode", func(t *testing.T) {
		path := makeRecoveryLockDatabase(t, 0600)
		if err := os.WriteFile(path+automationRecoveryLockSuffix, []byte("lock"), 0640); err != nil {
			t.Fatal(err)
		}
		if release, err := acquireAutomationRecoveryLock(path); err == nil {
			release()
			t.Fatal("insecure sidecar mode unexpectedly accepted")
		}
	})

	t.Run("sidecar nonregular", func(t *testing.T) {
		path := makeRecoveryLockDatabase(t, 0600)
		if err := os.Mkdir(path+automationRecoveryLockSuffix, 0700); err != nil {
			t.Fatal(err)
		}
		if release, err := acquireAutomationRecoveryLock(path); err == nil {
			release()
			t.Fatal("sidecar directory unexpectedly accepted")
		}
	})
}

func TestAutomationRecoveryLockMemoryIsNoop(t *testing.T) {
	release, err := acquireAutomationRecoveryLock(":memory:")
	if err != nil || release == nil {
		t.Fatalf("memory lock=%v release=%v", err, release != nil)
	}
	release()
	release()
}

func TestAutomationRecoveryLockRejectsSidecarSymlink(t *testing.T) {
	path := makeRecoveryLockDatabase(t, 0600)
	target := filepath.Join(t.TempDir(), "lock-target")
	if err := os.WriteFile(target, []byte("lock"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+automationRecoveryLockSuffix); err != nil {
		t.Fatal(err)
	}
	if release, err := acquireAutomationRecoveryLock(path); err == nil {
		release()
		t.Fatal("sidecar symlink unexpectedly accepted")
	}
}

func TestAutomationRecoveryLockCanonicalizesDirectoryAliases(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "automation.db")
	if err := os.WriteFile(path, []byte("sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, aliasRoot); err != nil {
		t.Fatal(err)
	}

	aliasPath := filepath.Join(aliasRoot, filepath.Base(path))
	release, err := acquireAutomationRecoveryLock(aliasPath)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := acquireAutomationRecoveryLock(path); err == nil {
		t.Fatal("canonical and directory-alias paths did not share a lock")
	}
}

func TestAutomationRecoveryLockWithOpenStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "automation.db")
	store, err := automation.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	release, err := acquireAutomationRecoveryLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	now := time.Now().UTC()
	checkpoint := automation.Checkpoint{
		Plan: automation.Plan{
			ID:                "lock-integration",
			CharacterID:       "character:0",
			Mode:              "quest",
			KnowledgeRevision: "test-v1",
			Title:             "lock integration",
			Completion:        []automation.Condition{{Kind: "flag_set", ID: "complete"}},
			Steps: []automation.Step{{
				ID:             "step-1",
				Action:         automation.Action{Skill: "test.action"},
				Success:        []automation.Condition{{Kind: "flag_set", ID: "step-complete"}},
				TimeoutSeconds: 10,
			}},
			MaximumSeconds: 60,
		},
		Revision:  1,
		Status:    automation.Running,
		StartedAt: now,
		UpdatedAt: now,
	}
	if err := store.Create(context.Background(), checkpoint); err != nil {
		t.Fatalf("create checkpoint while lock is held: %v", err)
	}
	loaded, err := store.Load(context.Background(), checkpoint.Plan.ID)
	if err != nil {
		t.Fatalf("load checkpoint while lock is held: %v", err)
	}
	if loaded.Plan.ID != checkpoint.Plan.ID || loaded.Plan.CharacterID != checkpoint.Plan.CharacterID || loaded.Status != automation.Running {
		t.Fatalf("loaded checkpoint mismatch: got=%+v want=%+v", loaded, checkpoint)
	}
}

func canonicalRecoveryLockPath(t *testing.T, path string) string {
	t.Helper()
	absPath, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(absPath))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, filepath.Base(absPath)) + automationRecoveryLockSuffix
}
