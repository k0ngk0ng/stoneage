package aibroker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// The local driver keeps output available after the broker process exits.
	// Keep this fixed and larger than the broker's default 8 MiB stdout bound;
	// callers cannot redirect a run to an unbounded or host-backed log sink.
	dockerLogMaxSize = "20m"
	dockerLogMaxFile = "1"
)

var (
	containerNamePattern = mustNamePattern(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
	volumeNamePattern    = mustNamePattern(`^[a-z0-9][a-z0-9_.-]{0,62}$`)
)

// DockerCLI invokes the server-owned Docker binary without a shell. It is
// intentionally small so production can use the normal Docker CLI while
// tests inject the Docker interface above and never start a real container.
type DockerCLI struct {
	Binary         string
	MaxStdoutBytes int
	MaxStderrBytes int
	StopTimeout    time.Duration
}

// NewDocker constructs a process-backed Docker adapter. Binary must be an
// absolute executable path; PATH lookup is intentionally not used for the
// host control boundary.
func NewDocker(binary string) (*DockerCLI, error) {
	binary = strings.TrimSpace(binary)
	if binary == "" || !isAbsolutePath(binary) {
		return nil, fmt.Errorf("%w: DockerBinary must be an absolute path", ErrInvalidConfig)
	}
	info, err := os.Lstat(binary)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.IsDir() || info.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("%w: DockerBinary is not executable", ErrInvalidConfig)
	}
	return &DockerCLI{Binary: binary, MaxStdoutBytes: DefaultMaxStdoutBytes, MaxStderrBytes: DefaultMaxStderrBytes, StopTimeout: DefaultStopTimeout}, nil
}

// NewDockerCLI is an alias for integrations that use the concrete name.
func NewDockerCLI(binary string) (*DockerCLI, error) { return NewDocker(binary) }

func (docker *DockerCLI) Run(ctx context.Context, spec RunSpec, payload []byte) (DockerResult, error) {
	if docker == nil || strings.TrimSpace(docker.Binary) == "" {
		return DockerResult{}, fmt.Errorf("%w: Docker client is nil", ErrInvalidConfig)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRunSpec(spec); err != nil {
		return DockerResult{}, err
	}
	if len(payload) > DefaultMaxRequestBytes {
		return DockerResult{}, ErrRequestTooLarge
	}
	if err := ctx.Err(); err != nil {
		return DockerResult{}, err
	}
	stdoutLimit := docker.MaxStdoutBytes
	if stdoutLimit <= 0 {
		stdoutLimit = DefaultMaxStdoutBytes
	}
	stderrLimit := docker.MaxStderrBytes
	if stderrLimit <= 0 {
		stderrLimit = DefaultMaxStderrBytes
	}
	args := dockerArgs(spec)
	command := exec.Command(docker.Binary, args...)
	command.Stdin = bytes.NewReader(payload)
	configureDockerProcess(command)
	grace := docker.StopTimeout
	if grace <= 0 || grace > 30*time.Second {
		grace = DefaultStopTimeout
	}
	command.WaitDelay = grace
	limitReached := make(chan struct{}, 1)
	stdout := &boundedCapture{limit: stdoutLimit, exceeded: limitReached}
	stderr := &boundedCapture{limit: stderrLimit, exceeded: limitReached}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Start(); err != nil {
		return DockerResult{}, fmt.Errorf("%w: start Docker", ErrDocker)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	var waitErr error
	var interrupted error
	select {
	case waitErr = <-waitDone:
	case <-ctx.Done():
		interrupted = ctx.Err()
	case <-limitReached:
		interrupted = ErrOutputLimit
	}
	if interrupted != nil {
		// Terminate the CLI group and keep draining its output. The broker also
		// stops the exact container; killing a Docker client alone is insufficient.
		terminateDockerProcess(command)
		timer := time.NewTimer(grace)
		select {
		case waitErr = <-waitDone:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			forceKillDockerProcess(command)
			waitErr = <-waitDone
		}
	}
	result := DockerResult{ExitCode: exitCode(waitErr), Stdout: stdout.bytes, Stderr: stderr.bytes}
	if stdout.overflow || stderr.overflow {
		return result, ErrOutputLimit
	}
	if interrupted != nil {
		return result, interrupted
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return result, fmt.Errorf("%w: wait Docker", ErrDocker)
		}
	}
	return result, nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func (docker *DockerCLI) Stop(ctx context.Context, containerName string) error {
	if docker == nil || strings.TrimSpace(docker.Binary) == "" {
		return fmt.Errorf("%w: Docker client is nil", ErrInvalidConfig)
	}
	if !containerNamePattern.MatchString(containerName) {
		return fmt.Errorf("%w: container name is invalid", ErrInvalidRequest)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	command := exec.CommandContext(ctx, docker.Binary, "stop", "--time", stopSeconds(docker.StopTimeout), containerName)
	if err := command.Run(); err != nil {
		return fmt.Errorf("%w: stop container", ErrDocker)
	}
	return nil
}

// ReadResult reads the exit status and attached output of one terminated
// container. Docker's CLI exposes the two attached log channels through its
// stdout/stderr pipes; each pipe is bounded independently so a broken runtime
// cannot make recovery consume unbounded memory. The broker deliberately does
// not persist stderr, which may contain daemon diagnostics.
func (docker *DockerCLI) ReadResult(ctx context.Context, containerName string) (DockerResult, error) {
	if docker == nil || strings.TrimSpace(docker.Binary) == "" {
		return DockerResult{}, fmt.Errorf("%w: Docker client is nil", ErrInvalidConfig)
	}
	if !containerNamePattern.MatchString(containerName) {
		return DockerResult{}, fmt.Errorf("%w: container name is invalid", ErrInvalidRequest)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := docker.Inspect(ctx, containerName)
	if err != nil {
		return DockerResult{}, err
	}
	if !terminalContainerState(state) {
		return DockerResult{}, fmt.Errorf("%w: container is not terminated", ErrDocker)
	}

	// Inspect the daemon's authoritative exit code instead of deriving it from
	// the `docker logs` command status. A successful log query says nothing
	// about whether the runtime process completed successfully.
	exitRaw, err := docker.readOnly(ctx, "inspect", "--type", "container", "--format", "{{.State.ExitCode}}", containerName)
	if err != nil {
		return DockerResult{}, err
	}
	exitCode, err := strconv.Atoi(strings.TrimSpace(string(exitRaw)))
	if err != nil {
		return DockerResult{}, fmt.Errorf("%w: invalid container exit code", ErrDocker)
	}

	result, err := docker.readLogs(ctx, containerName)
	if err != nil {
		return result, err
	}
	result.ExitCode = exitCode
	return result, nil
}

// Remove removes one container only after a read-only lifecycle check proves
// that it has terminated. In particular this never passes --force, so a
// mistaken cleanup call cannot kill a live provider turn.
func (docker *DockerCLI) Remove(ctx context.Context, containerName string) error {
	if docker == nil || strings.TrimSpace(docker.Binary) == "" {
		return fmt.Errorf("%w: Docker client is nil", ErrInvalidConfig)
	}
	if !containerNamePattern.MatchString(containerName) {
		return fmt.Errorf("%w: container name is invalid", ErrInvalidRequest)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	state, err := docker.Inspect(ctx, containerName)
	if errors.Is(err, ErrContainerNotFound) {
		// Cleanup is idempotent. Another owner may have removed the container
		// after the journal result was published.
		return nil
	}
	if err != nil {
		return err
	}
	if !terminalContainerState(state) {
		return fmt.Errorf("%w: refusing to remove a live container", ErrDocker)
	}
	command := exec.CommandContext(ctx, docker.Binary, "rm", containerName)
	if err := command.Run(); err != nil {
		return fmt.Errorf("%w: remove container", ErrDocker)
	}
	return nil
}

// Inspect reads the daemon's state for exactly one deterministic container.
// The preliminary `ps -a` query distinguishes a successful, empty lookup
// (the container is absent) from a Docker/daemon error. A second read obtains
// the machine-readable lifecycle state without modifying the container.
func (docker *DockerCLI) Inspect(ctx context.Context, containerName string) (DockerContainerState, error) {
	if docker == nil || strings.TrimSpace(docker.Binary) == "" {
		return "", fmt.Errorf("%w: Docker client is nil", ErrInvalidConfig)
	}
	if !containerNamePattern.MatchString(containerName) {
		return "", fmt.Errorf("%w: container name is invalid", ErrInvalidRequest)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	filter := "^/" + regexp.QuoteMeta(containerName) + "$"
	ids, err := docker.readOnly(ctx, "ps", "--all", "--filter", "name="+filter, "--format", "{{.ID}}")
	if err != nil {
		return "", err
	}
	lines := strings.Fields(string(ids))
	if len(lines) == 0 {
		return "", ErrContainerNotFound
	}
	if len(lines) != 1 || lines[0] == "" {
		return "", fmt.Errorf("%w: container lookup returned multiple matches", ErrDocker)
	}
	status, err := docker.readOnly(ctx, "inspect", "--type", "container", "--format", "{{.State.Status}}", lines[0])
	if err != nil {
		return "", err
	}
	state := DockerContainerState(strings.TrimSpace(string(status)))
	switch state {
	case DockerContainerCreated, DockerContainerRunning, DockerContainerPaused,
		DockerContainerRestarting, DockerContainerRemoving, DockerContainerExited,
		DockerContainerDead:
		return state, nil
	default:
		return "", fmt.Errorf("%w: Docker returned an unsupported container state", ErrDocker)
	}
}

func (docker *DockerCLI) readLogs(ctx context.Context, containerName string) (DockerResult, error) {
	stdoutLimit := docker.MaxStdoutBytes
	if stdoutLimit <= 0 {
		stdoutLimit = DefaultMaxStdoutBytes
	}
	stderrLimit := docker.MaxStderrBytes
	if stderrLimit <= 0 {
		stderrLimit = DefaultMaxStderrBytes
	}
	command := exec.CommandContext(ctx, docker.Binary, "logs", containerName)
	stdout := &boundedCapture{limit: stdoutLimit}
	stderr := &boundedCapture{limit: stderrLimit}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		return DockerResult{Stdout: stdout.bytes, Stderr: stderr.bytes}, fmt.Errorf("%w: read Docker logs", ErrDocker)
	}
	result := DockerResult{Stdout: stdout.bytes, Stderr: stderr.bytes}
	if stdout.overflow || stderr.overflow {
		return result, ErrOutputLimit
	}
	return result, nil
}

func (docker *DockerCLI) readOnly(ctx context.Context, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, docker.Binary, args...)
	stdout := &boundedCapture{limit: 4096}
	stderr := &boundedCapture{limit: 4096}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%w: inspect Docker", ErrDocker)
	}
	if stdout.overflow || stderr.overflow {
		return nil, fmt.Errorf("%w: inspect Docker output exceeded limit", ErrDocker)
	}
	return stdout.bytes, nil
}

func stopSeconds(timeout time.Duration) string {
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = DefaultStopTimeout
	}
	seconds := int(timeout / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprintf("%d", seconds)
}

// boundedCapture signals immediately when output exceeds its limit. It keeps
// accepting writes until the process is stopped so exec.Cmd can drain pipes;
// Wait synchronizes all copying before the caller inspects the captured bytes.
type boundedCapture struct {
	limit    int
	bytes    []byte
	overflow bool
	exceeded chan<- struct{}
}

func (capture *boundedCapture) Write(data []byte) (int, error) {
	remaining := capture.limit - len(capture.bytes)
	if remaining > 0 {
		capture.bytes = append(capture.bytes, data[:min(len(data), remaining)]...)
	}
	if len(data) > remaining && !capture.overflow {
		capture.overflow = true
		select {
		case capture.exceeded <- struct{}{}:
		default:
		}
	}
	return len(data), nil
}

func dockerArgs(spec RunSpec) []string {
	args := []string{"run", "--interactive", "--pull", "never", "--log-driver", "local", "--log-opt", "max-size=" + dockerLogMaxSize, "--log-opt", "max-file=" + dockerLogMaxFile, "--log-opt", "compress=false", "--name", spec.ContainerName, "--network", spec.Network,
		"--user", spec.User, "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true"}
	paths := make([]string, 0, len(spec.Tmpfs))
	for path := range spec.Tmpfs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		args = append(args, "--tmpfs", path+":"+spec.Tmpfs[path])
	}
	mounts := spec.Mounts
	if len(mounts) == 0 {
		mounts = spec.Volumes
	}
	for _, mount := range mounts {
		value := "type=volume,source=" + mount.Name + ",target=" + mount.Target
		if mount.ReadOnly {
			value += ",readonly"
		}
		args = append(args, "--mount", value)
	}
	keys := make([]string, 0, len(spec.Environment))
	for key := range spec.Environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--env", key+"="+spec.Environment[key])
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)
	return args
}

func validateRunSpec(spec RunSpec) error {
	if !containerNamePattern.MatchString(spec.ContainerName) || !volumeNamePattern.MatchString(spec.VolumeName) {
		return fmt.Errorf("%w: Docker names are invalid", ErrInvalidRequest)
	}
	if strings.TrimSpace(spec.Image) == "" || hasControlOrSpace(spec.Image) || strings.HasPrefix(spec.Image, "-") {
		return fmt.Errorf("%w: image is invalid", ErrInvalidRequest)
	}
	if strings.TrimSpace(spec.Network) == "" || hasControlOrSpace(spec.Network) || strings.HasPrefix(spec.Network, "-") {
		return fmt.Errorf("%w: network is invalid", ErrInvalidRequest)
	}
	if spec.User != "10001" || !spec.ReadOnlyRootfs || !spec.NoNewPrivileges {
		return fmt.Errorf("%w: runtime security options are incomplete", ErrInvalidRequest)
	}
	if len(spec.CapDrop) != 1 || strings.ToUpper(spec.CapDrop[0]) != "ALL" {
		return fmt.Errorf("%w: all capabilities must be dropped", ErrInvalidRequest)
	}
	if len(spec.SecurityOpt) > 0 && !containsNoNewPrivileges(spec.SecurityOpt) {
		return fmt.Errorf("%w: no-new-privileges is required", ErrInvalidRequest)
	}
	if len(spec.Mounts) != 1 || len(spec.Volumes) != 1 || spec.Mounts[0] != spec.Volumes[0] {
		return fmt.Errorf("%w: exactly one profile volume is required", ErrInvalidRequest)
	}
	mount := spec.Mounts[0]
	if mount.Name != spec.VolumeName || mount.Target != "/var/lib/stoneage-ai" || mount.ReadOnly {
		return fmt.Errorf("%w: profile volume mount is invalid", ErrInvalidRequest)
	}
	if len(spec.Command) == 0 || len(spec.Command) > 16 {
		return fmt.Errorf("%w: runner command is invalid", ErrInvalidRequest)
	}
	for _, arg := range spec.Command {
		if arg == "" || strings.ContainsAny(arg, "\x00\r\n") {
			return fmt.Errorf("%w: runner command contains control characters", ErrInvalidRequest)
		}
	}
	for path, options := range spec.Tmpfs {
		if path != "/tmp" && path != "/run" {
			return fmt.Errorf("%w: tmpfs path is not allowlisted", ErrInvalidRequest)
		}
		if options == "" || hasControlOrSpace(options) || !strings.Contains(options, "size=") {
			return fmt.Errorf("%w: tmpfs is not bounded", ErrInvalidRequest)
		}
	}
	for key, value := range spec.Environment {
		if !validEnvKey(key) || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%w: environment is invalid", ErrInvalidRequest)
		}
	}
	return nil
}

func containsNoNewPrivileges(values []string) bool {
	for _, value := range values {
		if value == "no-new-privileges:true" || value == "no-new-privileges" {
			return true
		}
	}
	return false
}

func validEnvKey(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || index > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
