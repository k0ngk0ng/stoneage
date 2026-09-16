package aibroker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerArgsKeepTerminatedContainerAndBoundItsLogs(t *testing.T) {
	args := dockerArgs(cliTestSpec(t))
	joined := strings.Join(args, "\x00")
	if strings.Contains(joined, "--rm") {
		t.Fatal("Docker run must retain the container for post-crash recovery")
	}
	for _, option := range []string{"--log-driver", "local", "--log-opt", "max-size=20m", "max-file=1", "compress=false"} {
		if !strings.Contains(joined, option) {
			t.Fatalf("Docker run missing fixed logging option %q: %v", option, args)
		}
	}
}

func TestDockerCLIReadResultReadsExitCodeAndBoundsBothLogStreams(t *testing.T) {
	docker := testDockerCLI(t, `
case "$1" in
  ps) printf '%s\n' fixture-id ;;
  inspect)
    for arg in "$@"; do
      case "$arg" in
        *State.Status*) printf '%s\n' exited; exit 0 ;;
        *State.ExitCode*) printf '%s\n' 0; exit 0 ;;
      esac
    done
    exit 91 ;;
  logs) printf '%s' '{"ok":true}'; printf '%s' 'fixture-stderr' >&2 ;;
  *) exit 92 ;;
esac
`)
	result, err := docker.ReadResult(context.Background(), "stoneage-ai-run-test")
	if err != nil || result.ExitCode != 0 || string(result.Stdout) != `{"ok":true}` || string(result.Stderr) != "fixture-stderr" {
		t.Fatalf("read result=%+v err=%v", result, err)
	}

	docker.MaxStdoutBytes = 1024
	docker.MaxStderrBytes = 1024
	docker = testDockerCLI(t, `
case "$1" in
  ps) printf '%s\n' fixture-id ;;
  inspect)
    for arg in "$@"; do
      case "$arg" in
        *State.Status*) printf '%s\n' exited; exit 0 ;;
        *State.ExitCode*) printf '%s\n' 0; exit 0 ;;
      esac
    done
    exit 91 ;;
  logs) printf '%2048s' x; printf '%2048s' y >&2 ;;
  *) exit 92 ;;
esac
`)
	docker.MaxStdoutBytes = 1024
	docker.MaxStderrBytes = 1024
	result, err = docker.ReadResult(context.Background(), "stoneage-ai-run-test")
	if !errors.Is(err, ErrOutputLimit) || len(result.Stdout) != 1024 || len(result.Stderr) != 1024 {
		t.Fatalf("bounded logs result bytes=(%d,%d) err=%v", len(result.Stdout), len(result.Stderr), err)
	}
}

func TestDockerCLIRemoveNeverForceRemovesActiveContainer(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "rm-args")
	t.Setenv("STONEAGE_TEST_DOCKER_RM_ARGS", argsPath)
	docker := testDockerCLI(t, `
case "$1" in
  ps) printf '%s\n' fixture-id ;;
  inspect) printf '%s\n' running ;;
  rm) printf '%s\n' "$@" > "$STONEAGE_TEST_DOCKER_RM_ARGS" ;;
  *) exit 92 ;;
esac
`)
	if err := docker.Remove(context.Background(), "stoneage-ai-run-test"); !errors.Is(err, ErrDocker) {
		t.Fatalf("active removal err=%v, want Docker error", err)
	}
	if _, err := os.Stat(argsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active removal invoked rm: stat err=%v", err)
	}

	docker = testDockerCLI(t, `
case "$1" in
  ps) printf '%s\n' fixture-id ;;
  inspect) printf '%s\n' exited ;;
  rm)
    for arg in "$@"; do
      [ "$arg" != "--force" ] || exit 93
    done
    printf '%s\n' "$@" > "$STONEAGE_TEST_DOCKER_RM_ARGS" ;;
  *) exit 92 ;;
esac
`)
	if err := docker.Remove(context.Background(), "stoneage-ai-run-test"); err != nil {
		t.Fatalf("terminal removal err=%v", err)
	}
	raw, err := os.ReadFile(argsPath)
	if err != nil || !strings.Contains(string(raw), "rm") {
		t.Fatalf("terminal removal args=%q err=%v", raw, err)
	}
}
