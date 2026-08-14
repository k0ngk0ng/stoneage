package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigManagerPreservesUnknownSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.cf")
	original := "# comment\ndebuglevel=1\nenable_nu_flow_control=0\nother=value\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	manager := ConfigManager{Path: path}
	values, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	if values["debuglevel"] != "1" || values["enable_nu_flow_control"] != "0" {
		t.Fatalf("loaded values = %#v", values)
	}
	if err := manager.Update(map[string]string{"debuglevel": "3", "enable_nu_flow_control": "1"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "debuglevel=3") || !strings.Contains(text, "enable_nu_flow_control=1") || !strings.Contains(text, "other=value") {
		t.Fatalf("updated config = %q", text)
	}
	if err := manager.Update(map[string]string{"debuglevel": "9"}); err == nil {
		t.Fatal("invalid debug level accepted")
	}
}

func TestConfigManagerRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.cf")
	link := filepath.Join(directory, "setup.cf")
	if err := os.WriteFile(target, []byte("debuglevel=1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := (ConfigManager{Path: link}).Load(); err == nil {
		t.Fatal("symlink config accepted")
	}
}
