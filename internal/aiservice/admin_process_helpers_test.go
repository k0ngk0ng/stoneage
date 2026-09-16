package aiservice

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

// adminProcess owns one test child process. Child diagnostics are discarded
// so provider errors and credentials cannot enter test output.
type adminProcess struct {
	command  *exec.Cmd
	done     chan error
	stopOnce sync.Once
}

func startAdminProcess(t *testing.T, command *exec.Cmd) *adminProcess {
	t.Helper()
	if command == nil {
		t.Fatal("start admin process: nil command")
	}
	command.Stdout = io.Discard
	process := &adminProcess{command: command, done: make(chan error, 1)}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatalf("start admin process: %v", err)
	}
	go func() {
		process.done <- command.Wait()
	}()
	t.Cleanup(func() { process.stop(t) })
	return process
}

func (process *adminProcess) stop(t *testing.T) {
	t.Helper()
	if process == nil {
		return
	}
	process.stopOnce.Do(func() {
		if process.command == nil || process.command.Process == nil {
			t.Fatal("stop admin process: process was not started")
		}
		if err := process.command.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("send SIGTERM to admin process: %v", err)
		}
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case err := <-process.done:
			if err != nil || process.command.ProcessState == nil || !process.command.ProcessState.Success() {
				t.Fatalf("admin process did not exit successfully: %v", err)
			}
		case <-timer.C:
			_ = process.command.Process.Kill()
			err := <-process.done
			t.Fatalf("admin process did not exit after SIGTERM: %v", err)
		}
	})
}
