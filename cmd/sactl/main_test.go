package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/sacli"
)

// Exercise the real entry point, including its process exit and daemon RPC.
// Server-level tests alone cannot catch global parsing swallowing flags.
func TestCLIProcess(t *testing.T) {
	if os.Getenv("STONEAGE_TEST_CLI_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			if err := run(os.Args[i+1:]); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(exitFailed)
			}
			return
		}
	}
	t.Fatal("missing command arguments")
}

func TestCommandFlagsReachDaemon(t *testing.T) {
	// Keep the Unix socket below the platform's path-length limit.
	dir, err := os.MkdirTemp("", "sactl-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for _, tc := range []struct {
		name, command string
		args          []string
	}{
		{"ladder events", "ladder", []string{"wait", "42", "1s", "--stream", "session-1"}},
		{"ladder retry", "ladder", []string{"queue", "--request-id", "original-id", "--revision", "42"}},
		{"character options", "create-character", []string{"Fixture", "--vital", "5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "daemon.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			requests := make(chan sacli.Request, 1)
			errors := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					errors <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				var request sacli.Request
				if err := json.NewDecoder(conn).Decode(&request); err != nil {
					errors <- err
					return
				}
				requests <- request
				errors <- json.NewEncoder(conn).Encode(sacli.Response{OK: true})
			}()
			args := []string{"-test.run=^TestCLIProcess$", "--", "--socket", path, tc.command}
			args = append(args, tc.args...)
			args = append(args, "--json", "--timeout", "3s")
			cmd := exec.Command(os.Args[0], args...)
			cmd.Env = append(os.Environ(), "STONEAGE_TEST_CLI_PROCESS=1")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("CLI failed: %v\n%s", err, output)
			}
			if err := <-errors; err != nil {
				t.Fatal(err)
			}
			request := <-requests
			if request.Command != tc.command || !reflect.DeepEqual(request.Args, tc.args) || !request.JSON || request.Timeout != 3*time.Second {
				t.Fatalf("command flags changed before daemon: %+v", request)
			}
		})
	}
}

func TestUnknownGlobalFlagFails(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestCLIProcess$", "--", "--stream", "session-1", "ladder", "status")
	cmd.Env = append(os.Environ(), "STONEAGE_TEST_CLI_PROCESS=1")
	if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != exitFailed {
		t.Fatalf("unknown global flag exit = %v (%v)", cmd.ProcessState, err)
	}
}
