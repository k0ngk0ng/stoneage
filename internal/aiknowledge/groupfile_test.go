package aiknowledge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEffectiveGroupFileMatchesNativeSelection(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	for _, tc := range []struct {
		config, explicit, want string
		rejected               bool
	}{
		{"", "", "group.txt", false},
		{"#groupfile=./data/unused.txt\npassword=private\ngroupfile=./data/group1.txt\r\n", "", "group1.txt", false},
		{"groupfile=data/custom.txt\n", "", "custom.txt", false},
		{"groupfile=./data/group1.txt\n", "group.txt", "group.txt", false},
		{"groupfile=./elsewhere/group1.txt\n", "", "", true},
		{"groupfile=/tmp/group1.txt\n", "", "", true},
		{"groupfile=../../group1.txt\n", "", "", true},
		{"groupfile=\n", "", "", true},
		{"", "../group1.txt", "", true},
	} {
		if err := os.WriteFile(filepath.Join(root, "setup.cf"), []byte(tc.config), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := effectiveGroupFile(data, tc.explicit)
		if (err != nil) != tc.rejected || got != tc.want {
			t.Fatalf("selection=%q error=%v, want=%q rejected=%v", got, err, tc.want, tc.rejected)
		}
	}
	if err := os.Remove(filepath.Join(root, "setup.cf")); err != nil {
		t.Fatal(err)
	}
	if got, err := effectiveGroupFile(data, ""); err != nil || got != "group.txt" {
		t.Fatal(got, err)
	}
}

func TestLoadUsesOnlyConfiguredGroupTable(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(data, "map"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path, contents string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	row := func(fields int) string { return strings.Repeat("0,", fields-1) + "0\n" }
	write(filepath.Join(data, "exp.txt"), "0\n100\n")
	write(filepath.Join(data, "enemybase.txt"), row(56))
	write(filepath.Join(data, "enemy.txt"), row(34))
	encounter := strings.Split(strings.TrimSpace(row(33)), ",")
	encounter[8] = "1" // native maximum enemy count must be 1..10
	write(filepath.Join(data, "encount.txt"), strings.Join(encounter, ",")+"\n")
	write(filepath.Join(data, "map/mapwarp.txt"), "NONE:NULL:100,1,2:101,3,4:NULL\n")
	write(filepath.Join(data, "group1.txt"), row(24))
	write(filepath.Join(root, "setup.cf"), "groupfile=./data/group1.txt\npassword=first\n")
	k, err := LoadDataDir(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if k.GroupFile != "group1.txt" || !k.CoverageReport.CoreTables["group1.txt"] {
		t.Fatalf("effective table not recorded: %q %+v", k.GroupFile, k.CoverageReport.CoreTables)
	}
	if _, exists := k.CoverageReport.CoreTables["group.txt"]; exists {
		t.Fatal("coverage requires an unused table")
	}
	if len(k.Groups) != 1 || k.Groups[0].Source.Path != "group1.txt" || k.Groups[0].Source.SHA256 != SHA256Hex([]byte(row(24))) {
		t.Fatalf("group provenance does not match selected table: %+v", k.Groups)
	}
	write(filepath.Join(root, "setup.cf"), "groupfile=./data/group1.txt\npassword=second\n")
	other, err := LoadDataDir(context.Background(), root)
	if err != nil || other.Fingerprint() != k.Fingerprint() {
		t.Fatalf("unrelated setup values changed knowledge fingerprint: %v", err)
	}
	write(filepath.Join(root, "setup.cf"), "groupfile=./data/missing.txt\n")
	if _, err := LoadDataDir(context.Background(), root); err == nil {
		t.Fatal("missing configured table must fail, not fall back")
	}
	if _, err := Load(context.Background(), Options{DataDir: root, GroupFile: "group1.txt"}); err != nil {
		t.Fatalf("explicit override: %v", err)
	}
}
