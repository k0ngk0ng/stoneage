//go:build windows

package aibroker

import "os/exec"

// Windows' os/exec package has no portable process-group signal equivalent.
// The Docker CLI itself is the only process owned here; the broker separately
// stops the exact container, so terminating this process preserves the
// request cancellation boundary without relying on Unix-only APIs.
func configureDockerProcess(*exec.Cmd) {}

func terminateDockerProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

func forceKillDockerProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
