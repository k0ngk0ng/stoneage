//go:build linux || darwin

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

const (
	workerLockHelperEnv   = "STONEAGE_WORKER_LOCK_HELPER"
	workerLockHelperRoot  = "STONEAGE_WORKER_LOCK_ROOT"
	workerLockHelperReady = "STONEAGE_WORKER_LOCK_READY"
)

func TestWorkerLockHelperProcess(t *testing.T) {
	if os.Getenv(workerLockHelperEnv) != "1" {
		return
	}
	release, err := acquireWorkerLock(os.Getenv(workerLockHelperRoot))
	if err != nil {
		t.Fatalf("helper could not acquire worker lock: %v", err)
	}
	defer release()
	if err := os.WriteFile(os.Getenv(workerLockHelperReady), []byte("ready\n"), 0o600); err != nil {
		t.Fatalf("helper could not report readiness: %v", err)
	}
	select {}
}

func TestAcquireWorkerLockUsesOSOwnershipInsteadOfPersistedPID(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "worker", "worker.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// This is the stale file left by a prior worker/container. The PID is
	// deliberately the current test process's PID; no live file descriptor owns
	// the inode, so flock must still allow acquisition.
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release, err := acquireWorkerLock(root)
	if err != nil {
		t.Fatalf("stale PID lock blocked acquisition: %v", err)
	}
	release()
}

func TestAcquireWorkerLockExcludesLiveOwnerAndReleases(t *testing.T) {
	root := t.TempDir()
	release, err := acquireWorkerLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireWorkerLock(root); !errors.Is(err, ErrWorkerLocked) {
		t.Fatalf("second worker lock error=%v, want ErrWorkerLocked", err)
	}
	release()
	second, err := acquireWorkerLock(root)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	second()
}

func TestAcquireWorkerLockReleasesAfterOwnerIsKilled(t *testing.T) {
	root := t.TempDir()
	ready := filepath.Join(root, "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerLockHelperProcess$")
	cmd.Env = append(os.Environ(),
		workerLockHelperEnv+"=1",
		workerLockHelperRoot+"="+root,
		workerLockHelperReady+"="+ready,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("worker lock helper did not acquire lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("worker lock helper unexpectedly exited successfully")
	}
	release, err := acquireWorkerLock(root)
	if err != nil {
		t.Fatalf("lock remained held after owner exit: %v", err)
	}
	release()
}
