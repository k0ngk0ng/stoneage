//go:build linux || darwin

package aiservice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestContainerRunnerLockAcrossProcesses(t *testing.T) {
	const pathEnv = "STONEAGE_TEST_CONTAINER_LOCK_PATH"
	const heldEnv = "STONEAGE_TEST_CONTAINER_LOCK_HELD"
	if path := os.Getenv(pathEnv); path != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		release, err := acquireContainerRunnerLock(ctx, path)
		if os.Getenv(heldEnv) == "1" {
			if release != nil {
				release()
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("child acquired a parent-owned lock: %v", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("child could not acquire released lock: %v", err)
		}
		release()
		return
	}

	path := filepath.Join(t.TempDir(), "profile.lock")
	if err := ensureContainerRunnerLock(path); err != nil {
		t.Fatal(err)
	}
	release, err := acquireContainerRunnerLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if release != nil {
			release()
		}
	}()
	runChild := func(held string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestContainerRunnerLockAcrossProcesses$", "-test.count=1")
		command.Env = append(os.Environ(), pathEnv+"="+path, heldEnv+"="+held)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("cross-process lock check failed: %v\n%s", err, output)
		}
	}
	runChild("1")
	release()
	release = nil
	runChild("0")
}
