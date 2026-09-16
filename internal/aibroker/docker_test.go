package aibroker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testDockerCLI(t *testing.T, script string) *DockerCLI {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker-fixture")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	docker, err := NewDocker(path)
	if err != nil {
		t.Fatal(err)
	}
	docker.StopTimeout = 50 * time.Millisecond
	return docker
}

func cliTestSpec(t *testing.T) RunSpec {
	t.Helper()
	broker := newFakeBroker(t, &fakeDocker{}, NewMemoryJournal())
	return broker.runSpec("cli-profile", broker.VolumeName("cli-profile"), broker.ContainerName("cli-profile", "cli-request"))
}

func TestDockerCLIForwardsPrivateRequestOnStdin(t *testing.T) {
	inputPath := filepath.Join(t.TempDir(), "received-input")
	t.Setenv("STONEAGE_TEST_DOCKER_INPUT", inputPath)
	docker := testDockerCLI(t, `
interactive=false
pull_never=false
previous=''
for argument in "$@"; do
  case "$argument" in
    --interactive) interactive=true ;;
    never) if [ "$previous" = '--pull' ]; then pull_never=true; fi ;;
    *private-test-key*) exit 31 ;;
  esac
  previous="$argument"
done
[ "$interactive" = true ] || exit 32
[ "$pull_never" = true ] || exit 33
cat > "$STONEAGE_TEST_DOCKER_INPUT"
printf '%s' '{"ok":true}'
`)
	payload := []byte("{\"model\":{\"api_key\":\"private-test-key\"}}\n")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := docker.Run(ctx, cliTestSpec(t), payload)
	if err != nil || result.ExitCode != 0 || string(result.Stdout) != `{"ok":true}` {
		t.Fatalf("stdin execution failed: exit=%d err=%v", result.ExitCode, err)
	}
	received, err := os.ReadFile(inputPath)
	if err != nil || !bytes.Equal(received, payload) {
		t.Fatal("Docker stdin did not preserve the private request")
	}
}

func TestDockerCLIOutputLimitStopsProcessWithoutWaitingForEOF(t *testing.T) {
	docker := testDockerCLI(t, `
trap '' TERM
printf '%8192s' x
sleep 30
`)
	docker.MaxStdoutBytes = 1024
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := docker.Run(ctx, cliTestSpec(t), nil)
	if !errors.Is(err, ErrOutputLimit) || ctx.Err() != nil {
		t.Fatalf("output limit waited for EOF or caller deadline: err=%v context=%v", err, ctx.Err())
	}
	if len(result.Stdout) != 1024 {
		t.Fatalf("stdout capture grew past its bound: %d", len(result.Stdout))
	}
}

func TestDockerCLICollectsOutputBeforeReturningProcessExit(t *testing.T) {
	docker := testDockerCLI(t, "printf '%65536s' x\n")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := docker.Run(ctx, cliTestSpec(t), nil)
	if err != nil || len(result.Stdout) != 65536 || result.Stdout[len(result.Stdout)-1] != 'x' {
		t.Fatalf("process exit truncated buffered stdout: bytes=%d err=%v", len(result.Stdout), err)
	}
}

func TestDockerCLIInspectClassifiesMissingAndReadsState(t *testing.T) {
	t.Setenv("STONEAGE_TEST_INSPECT_MODE", "missing")
	docker := testDockerCLI(t, `
case "$1" in
  ps) exit 0 ;;
  *) exit 91 ;;
esac
`)
	state, err := docker.Inspect(context.Background(), "stoneage-ai-run-test")
	if !errors.Is(err, ErrContainerNotFound) || state != "" {
		t.Fatalf("missing container state=%q err=%v", state, err)
	}

	t.Setenv("STONEAGE_TEST_INSPECT_MODE", "terminal")
	docker = testDockerCLI(t, `
case "$1" in
  ps) printf '%s\n' fixture-id ;;
  inspect) printf '%s\n' exited ;;
  *) exit 91 ;;
esac
`)
	state, err = docker.Inspect(context.Background(), "stoneage-ai.run-test")
	if err != nil || state != DockerContainerExited {
		t.Fatalf("terminal container state=%q err=%v", state, err)
	}
}

func TestDockerCLIInspectPreservesQueryFailureAsDockerError(t *testing.T) {
	docker := testDockerCLI(t, `exit 42`)
	state, err := docker.Inspect(context.Background(), "stoneage-ai-run-test")
	if !errors.Is(err, ErrDocker) || state != "" {
		t.Fatalf("query failure state=%q err=%v", state, err)
	}
}
