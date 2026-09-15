package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigManagerPreservesUnknownSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.cf")
	original := "# comment\ndebuglevel=1\nenable_nu_flow_control=0\nother=value\nSAMEIPLOGIN=1\nFUSIONBEIT=1\n"
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
		"MAXLEVEL":               "180",
	}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if !strings.Contains(text, "debuglevel=3") || !strings.Contains(text, "enable_nu_flow_control=1") || !strings.Contains(text, "runlevel=2") || !strings.Contains(text, "MAXLEVEL=180") || !strings.Contains(text, "SAMEIPLOGIN=1") || !strings.Contains(text, "other=value") {
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

func TestConfigValuesMatchRuntimeBounds(t *testing.T) {
	for _, tc := range []struct{ key, low, high, below, above string }{
		{"BATTLEGOLD", "0", "100", "-1", "101"},
		{"loghour", "0", "24", "-1", "25"},
		{"MAXLEVEL", "1", "199", "0", "200"},
		{"LEVEL", "1", "199", "0", "200"},
		{"CHARTRANS", "0", "6", "-1", "7"},
		{"PETTRANS", "-1", "2", "-2", "3"},
		{"GOLD", "0", "1000000", "-1", "1000001"},
		{"ENEMYACTION", "1", "100", "0", "101"},
		{"ANGELPLAYERTIME", "1", "100000000", "0", "100000001"},
		{"ANGELPLAYERMUN", "2", "100000", "1", "100001"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			for _, value := range []string{tc.low, tc.high} {
				if err := validateConfigValue(tc.key, value); err != nil {
					t.Fatal(err)
				}
			}
			for _, value := range []string{tc.below, tc.above, "1.5", ""} {
				if err := validateConfigValue(tc.key, value); err == nil {
					t.Fatalf("accepted %q", value)
				}
			}
		})
	}
	for _, key := range []string{"SAMEIPLOGIN", "FUSIONBEIT"} {
		if err := validateConfigValue(key, "1"); err == nil {
			t.Fatalf("inert setting %s editable", key)
		}
	}
	level, _ := definitionFor(configFieldDefinitions, "LEVEL")
	if level.Default != "140" {
		t.Fatalf("ordinary level default = %s", level.Default)
	}
}

func TestSAACRotateIntervalMustBePositive(t *testing.T) {
	if err := validateConfigValueFor(saacConfigFieldDefinitions, "rotate_interval", "0"); err == nil {
		t.Fatal("zero rotation divisor accepted")
	}
	if err := validateConfigValueFor(saacConfigFieldDefinitions, "rotate_interval", "1"); err != nil {
		t.Fatal(err)
	}
}
