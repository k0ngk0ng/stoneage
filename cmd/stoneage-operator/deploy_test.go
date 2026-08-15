package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentStatusReadsStateFile(t *testing.T) {
	directory := t.TempDir()
	state := filepath.Join(directory, "deploy.status")
	if err := os.WriteFile(state, []byte("version=v1.2.3\nphase=verifying\nmessage=等待健康检查\nupdated_at=2026-08-16T01:02:03Z\nbackup=/state/backups/v1.2.3-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value := operator{statePath: state}
	got := value.deploymentStatus()
	if got.Version != "v1.2.3" || got.Phase != "verifying" || got.Message != "等待健康检查" || got.UpdatedAt != "2026-08-16T01:02:03Z" || got.Backup != "/state/backups/v1.2.3-123" {
		t.Fatalf("deployment status = %#v", got)
	}
}

func TestDeploymentStatusMissingFileIsIdle(t *testing.T) {
	value := operator{statePath: filepath.Join(t.TempDir(), "missing")}
	got := value.deploymentStatus()
	if got.Phase != "idle" || got.Version != "" || got.Message != "" {
		t.Fatalf("missing deployment status = %#v", got)
	}
}

func TestDeployVersionRejectsInvalidConfigurationBeforeDocker(t *testing.T) {
	value := operator{projectHostRoot: "relative/project"}
	for _, version := range []string{"latest", "v1.2.3+build"} {
		if err := value.deployVersion(version); err == nil || !strings.Contains(err.Error(), "版本号") {
			t.Fatalf("invalid version %q error = %v", version, err)
		}
	}
	if err := value.deployVersion("v1.2.3"); err == nil || !strings.Contains(err.Error(), "绝对路径") {
		t.Fatalf("invalid project root error = %v", err)
	}
}

func TestDeployVersionPassesHostAndContainerProjectRootsSeparately(t *testing.T) {
	directory := t.TempDir()
	hostRoot := filepath.Join(directory, "host-project")
	containerRoot := filepath.Join(directory, "container-project")
	if err := os.MkdirAll(filepath.Join(hostRoot, "compose"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(containerRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	composeFile := filepath.Join(hostRoot, "docker-compose.yml")
	if err := os.WriteFile(composeFile, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gmsvRoot := filepath.Join(directory, "gmsv")
	saacRoot := filepath.Join(directory, "saac")
	if err := os.MkdirAll(gmsvRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(saacRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	argsFile := filepath.Join(directory, "docker.args")
	docker := filepath.Join(directory, "docker")
	contents := "#!/bin/sh\nprintf '%s\\n' \"$@\" >" + shellQuote(argsFile) + "\n"
	if err := os.WriteFile(docker, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	value := operator{
		projectRoot:     containerRoot,
		projectHostRoot: hostRoot,
		composeFile:     composeFile,
		controlImage:    "ghcr.io/example/control-plane",
		legacyImage:     "ghcr.io/example/legacy-runtime",
		dockerBin:       docker,
		statePath:       filepath.Join(directory, "state", "deploy.status"),
		stateVolume:     "stoneage-operator-state",
		authVolume:      "stoneage-auth",
		gmsvDataRoot:    gmsvRoot,
		saacDataRoot:    saacRoot,
	}
	if err := value.deployVersion("v1.2.3"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "STONEAGE_PROJECT_CONTAINER_ROOT="+containerRoot+"\n") || !strings.Contains(text, "STONEAGE_PROJECT_ROOT="+hostRoot+"\n") || !strings.Contains(text, "STONEAGE_PROJECT_HOST_ROOT="+hostRoot+"\n") {
		t.Fatalf("docker arguments did not separate project paths:\n%s", text)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
