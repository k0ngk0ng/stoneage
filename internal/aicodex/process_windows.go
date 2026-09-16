//go:build windows

package aicodex

import (
	"os/exec"
	"time"
)

func configureProcessGroup(cmd *exec.Cmd) {}

func terminateProcess(cmd *exec.Cmd, finished <-chan struct{}, grace time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	select {
	case <-finished:
		return
	default:
	}
	_ = cmd.Process.Kill()
	// Windows has no portable process-group TERM equivalent in the standard
	// library. A second Kill after the grace window covers a child that has not
	// exited while preserving the same bounded cancellation contract.
	if grace <= 0 {
		grace = time.Second
	}
	go func() {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-finished:
		case <-timer.C:
			_ = cmd.Process.Kill()
		}
	}()
}

func processSignal(error) string { return "" }
