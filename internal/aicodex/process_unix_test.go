//go:build !windows

package aicodex

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTerminateProcessKillsDescendantAfterLeaderExits(t *testing.T) {
	root := t.TempDir()
	pidPath := filepath.Join(root, "orphan.pid")
	scriptPath := filepath.Join(root, "orphan.sh")
	script := `#!/bin/sh
set -eu
(
  trap '' TERM
  exec </dev/null >/dev/null 2>/dev/null
  while :; do sleep 1; done
) &
printf '%s\n' "$!" > "$1"
trap 'exit 0' TERM INT
while :; do sleep 1; done
`
	if err := os.WriteFile(scriptPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(scriptPath, pidPath)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	configureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	finished := make(chan struct{})
	waitDone := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		waitDone <- err
		close(finished)
	}()

	var childPID int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidPath)
		if err == nil {
			childPID, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && childPID > 0 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if childPID <= 0 {
		_ = cmd.Process.Kill()
		<-finished
		t.Fatal("descendant pid was not recorded")
	}
	// Ensure a failed test does not leave the deliberately orphaned helper
	// behind while still allowing the implementation to prove it cleaned up.
	defer syscall.Kill(childPID, syscall.SIGKILL)

	terminateProcess(cmd, finished, 50*time.Millisecond)
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("leader did not exit after termination")
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant pid %d survived process-group termination", childPID)
}
