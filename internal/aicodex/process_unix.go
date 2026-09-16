//go:build !windows

package aicodex

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateProcess starts a TERM-then-KILL sequence for the process group and
// returns immediately.  The caller keeps draining stdout/stderr while the
// grace timer runs, preventing a blocked JSONL writer from deadlocking Wait.
func terminateProcess(cmd *exec.Cmd, finished <-chan struct{}, grace time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		return
	}
	select {
	case <-finished:
		return
	default:
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	if grace <= 0 {
		grace = time.Second
	}
	go func() {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-finished:
			return
		case <-timer.C:
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = cmd.Process.Kill()
		}
	}()
}

func processSignal(err error) string {
	if err == nil {
		return ""
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return ""
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return ""
	}
	return status.Signal().String()
}
