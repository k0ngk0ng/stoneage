package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOperatorOnlyRunsFixedRestartScript(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "marker")
	command := filepath.Join(directory, "restart-server.sh")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf ok >\""+marker+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	value := operator{packageRoot: directory}
	if err := value.restart(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(content)) != "ok" {
		t.Fatalf("marker = %q, err=%v", content, err)
	}
	if err := os.Remove(command); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restart-server.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, command); err == nil {
		if err := value.restart(); err == nil {
			t.Fatal("operator followed a symlink restart command")
		}
	}
}

func TestProbeDatabase(t *testing.T) {
	if got := probeDatabase(""); got != "not configured" {
		t.Fatalf("empty database = %q", got)
	}
	path := filepath.Join(t.TempDir(), "auth.db")
	if err := os.WriteFile(path, []byte("sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := probeDatabase(path); got != "ready" {
		t.Fatalf("database = %q", got)
	}
}
