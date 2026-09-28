package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/sacli"
)

func TestInitConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("STONEAGE_SACTL_CONFIG", "")
	path := filepath.Join(dir, "sactl", "sactl.toml")
	values := []string{"", "角色"}
	var out bytes.Buffer
	i := 0
	ask := func(_ string, secret bool) (string, error) {
		if secret {
			t.Fatal("init must not prompt for credentials")
		}
		v := values[i]
		i++
		return v, nil
	}
	if err := writeInitialConfig(path, ask, &out); err != nil {
		t.Fatal(err)
	}
	config, err := sacli.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Transport != "http" || config.WebBaseURL != "https://sa.ichenj.com" || config.Password != "" || config.Account != "" || config.Character != values[1] {
		t.Fatal("config did not round trip")
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "password") || strings.Contains(string(raw), "account") {
		t.Fatal("credential fields written")
	}
	if !strings.Contains(out.String(), "sactl login\n") {
		t.Fatal(out.String())
	}
	stat, _ := os.Stat(path)
	if runtime.GOOS != "windows" && stat.Mode().Perm() != 0600 {
		t.Fatal("unsafe permissions")
	}
	before, _ := os.ReadFile(path)
	if err = writeInitialConfig(path, func(string, bool) (string, error) {
		t.Fatal("prompted before checking existing config")
		return "", nil
	}, &out); err == nil {
		t.Fatal("overwrote config")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("changed existing config")
	}
}

func TestInitRejectsInvalidInput(t *testing.T) {
	for _, values := range [][]string{{"ftp://example.com"}, {"https://user:secret@example.com"}, {"https://example.com?token=secret"}} {
		path := filepath.Join(t.TempDir(), "sactl.toml")
		i := 0
		err := writeInitialConfig(path, func(string, bool) (string, error) { v := values[i]; i++; return v, nil }, &bytes.Buffer{})
		if err == nil {
			t.Fatal("accepted invalid input")
		}
		if _, err = os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("wrote config on failure")
		}
	}
}

func TestSessionEndpoint(t *testing.T) {
	config := sacli.DefaultConfig()
	config.Transport = "http"
	config.WebBaseURL = "https://example.com"
	if config.Endpoint() != config.WebBaseURL {
		t.Fatal("printed unused TCP address")
	}
	config.Transport = "tcp"
	if config.Endpoint() != config.Address {
		t.Fatal("wrong TCP address")
	}
}
