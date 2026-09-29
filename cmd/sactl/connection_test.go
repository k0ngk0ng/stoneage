package main

import (
	"github.com/k0ngk0ng/stoneage/internal/sacli"
	"testing"
)

func TestConnectionOverridesAndProfilePrecedence(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := sacli.SelectProfile("second"); err != nil {
		t.Fatal(err)
	}
	implicit := clientOptions{}
	if err := implicit.resolveProfile(); err != nil || implicit.profile != "second" {
		t.Fatal("active session not used", err)
	}
	explicit := clientOptions{profile: "first"}
	if err := explicit.resolveProfile(); err != nil || explicit.profile != "first" {
		t.Fatal("explicit session overridden", err)
	}
	custom := clientOptions{config: "custom.toml"}
	if err := custom.resolveProfile(); err != nil || custom.profile != "" {
		t.Fatal("custom config overridden", err)
	}
	config := sacli.DefaultConfig()
	if err := (connectionOptions{"address": "127.0.0.1:1234"}).apply(&config); err != nil || config.Transport != "tcp" {
		t.Fatal(err)
	}
	if err := (connectionOptions{"web-base-url": "https://example.com", "server-id": "line2"}).apply(&config); err != nil || config.Transport != "http" || config.ServerID != "line2" {
		t.Fatal(err)
	}
	if err := (connectionOptions{"web-base-url": "https://user:secret@example.com"}).apply(&config); err == nil {
		t.Fatal("credential URL accepted")
	}
}
