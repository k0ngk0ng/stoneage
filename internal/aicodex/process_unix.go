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
// returns immediately. The caller keeps draining stdout/stderr while the
// grace timer runs, preventing a blocked JSONL writer from deadlocking Wait.
//
// The KILL must not be skipped when the direct child exits after TERM. Codex
// can leave a runner (or another descendant) alive in the same process group;
// cmd.Wait then closes finished even though that descendant can still make
// provider requests. Reap the group as soon as the leader exits; if it does
// not exit, reap it when the grace period expires. Killing immediately after
// finished also keeps the original PGID reuse window as small as possible.
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
		case <-timer.C:
		}
		// A process-group kill is sufficient for the direct child and its
		// descendants, including the case where the direct child has already
		// exited. Do not call cmd.Process.Kill here: once the child has exited,
		// its numeric PID could have been reused by an unrelated process.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
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
