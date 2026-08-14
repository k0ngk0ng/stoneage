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
	if err := manager.Update(map[string]string{
		"debuglevel":             "3",
		"enable_nu_flow_control": "1",
		"runlevel":               "2",
		"MAXLEVEL":               "200",
		"SAMEIPLOGIN":            "1",
	}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "debuglevel=3") || !strings.Contains(text, "enable_nu_flow_control=1") || !strings.Contains(text, "runlevel=2") || !strings.Contains(text, "MAXLEVEL=200") || !strings.Contains(text, "SAMEIPLOGIN=1") || !strings.Contains(text, "other=value") {
		t.Fatalf("updated config = %q", text)
	}
	if err := manager.Update(map[string]string{"debuglevel": "9"}); err == nil {
		t.Fatal("invalid debug level accepted")
	}
	if err := manager.Update(map[string]string{"MAXLEVEL": "0"}); err == nil {
		t.Fatal("invalid max level accepted")
	}
	groups := BuildConfigGroups(map[string]string{"debuglevel": "3", "enable_nu_flow_control": "1"})
	if len(groups) < 4 || len(groups[0].Fields) == 0 {
		t.Fatalf("config groups = %#v", groups)
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

func TestSAACConfigManagerUsesSpaceDelimitedSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acserv.cf")
	original := "# comment\nport 9300\npass test\nrotate_interval 604800\nSameIpMun 10\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	manager := ConfigManager{Path: path, Service: "saac"}
	values, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	if values["SameIpMun"] != "10" {
		t.Fatalf("loaded SAAC values = %#v", values)
	}
	if _, ok := values["pass"]; ok {
		t.Fatal("SAAC password should not be exposed")
	}
	if err := manager.Update(map[string]string{"SameIpMun": "12"}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "port 9300") || !strings.Contains(text, "SameIpMun 12") || !strings.Contains(text, "pass test") {
		t.Fatalf("updated SAAC config = %q", text)
	}
}
