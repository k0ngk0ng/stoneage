package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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
		{"arena events", "arena", []string{"wait", "42", "1s", "--stream", "session-1"}},
		{"arena retry", "arena", []string{"queue", "--request-id", "original-id", "--revision", "42"}},
		{"legacy alias", "ladder", []string{"status"}},
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
			expectedCommand := tc.command
			if expectedCommand == "arena" {
				expectedCommand = "ladder"
			}
			if request.Command != expectedCommand || !reflect.DeepEqual(request.Args, tc.args) || !request.JSON || request.Timeout != 3*time.Second {
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

// Exercise main, not just the daemon-command parser: ai runs locally and
// must never attempt daemon RPC for help, init or check.
func TestAICLIProcess(t *testing.T) {
	if os.Getenv("STONEAGE_TEST_ARENA_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			main()
			return
		}
	}
	t.Fatal("missing arguments")
}
func TestAIIntegratedCLI(t *testing.T) {
	dir, err := os.MkdirTemp("", "sa-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	invoke := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestAICLIProcess$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "STONEAGE_TEST_ARENA_PROCESS=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s (%v)", args, out, err)
		}
		return out
	}
	if !strings.Contains(string(invoke("ai", "--help")), "sactl ai") {
		t.Fatal("missing integrated help")
	}
	if !strings.Contains(string(invoke("arena", "--help")), "player arena") {
		t.Fatal("arena help must work without a daemon")
	}
	for _, shell := range []string{"bash", "zsh"} {
		if !strings.Contains(string(invoke("completion", shell)), "__complete") {
			t.Fatal("missing shell completion", shell)
		}
	}
	if output := string(invoke("__complete", "ai", "run", "--f")); !strings.Contains(output, "--forever") {
		t.Fatal("completion unexpectedly requires daemon", output)
	}
	for _, command := range []string{"run", "train", "evaluate", "simulate"} {
		invoke("ai", command, "--help")
	}
	team := filepath.Join(dir, "team")
	invoke("ai", "init", "--directory", team, "--mode", "2")
	raw, err := os.ReadFile(filepath.Join(team, "team.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	if config["sactl"] != exe {
		t.Fatal("init must use current executable", config["sactl"])
	}
	output := invoke("ai", "check", "--config", filepath.Join(team, "team.json"))
	if !strings.Contains(string(output), `"ok":true`) {
		t.Fatal(string(output))
	}
}

func TestOldArenaAICommandsFailBeforeOpeningSession(t *testing.T) {
	for _, sub := range []string{"init", "check", "run", "train", "evaluate", "simulate", "native-simulate", "version"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestAICLIProcess$", "--", "--config", "/does-not-exist", "arena", sub)
		cmd.Env = append(os.Environ(), "STONEAGE_TEST_ARENA_PROCESS=1")
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "sactl ai "+sub) || strings.Contains(string(out), "daemon running") {
			t.Fatalf("old arena %s did not safely explain migration: %v %s", sub, err, out)
		}
	}
}
