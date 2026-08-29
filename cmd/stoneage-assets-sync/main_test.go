package main

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestWalkTreeKeepsPublicClientAssetAllowlist(t *testing.T) {
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	mapDir := filepath.Join(root, "map")
	data := filepath.Join(root, "data")
	for _, directory := range []string{assets, mapDir, filepath.Join(data, "bgm"), filepath.Join(data, "se")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for filename, content := range map[string]string{
		filepath.Join(assets, "manifest.json"): "{}",
		filepath.Join(mapDir, "100.MAP"):       "map",
		filepath.Join(data, "auto.dat"):        "auto",
		filepath.Join(data, "bgm", "0.wav"):   "bgm",
		filepath.Join(data, "se", "1.wav"):    "se",
		filepath.Join(data, "savedata.dat"):    "private",
		filepath.Join(data, "chatreg.dat"):     "private",
	} {
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	trees := []sourceTree{
		{Name: "assets", Roots: []sourceRoot{{Path: assets}}},
		{Name: "maps", Roots: []sourceRoot{{Path: mapDir}}},
		{Name: "audio", Roots: []sourceRoot{
			{Path: filepath.Join(data, "auto.dat")},
			{Path: filepath.Join(data, "bgm"), Prefix: "bgm"},
			{Path: filepath.Join(data, "se"), Prefix: "se"},
		}},
	}
	var got []string
	for _, tree := range trees {
		if err := walkTree(tree, func(_, relative string) error {
			got = append(got, tree.Name+"/"+filepath.ToSlash(relative))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(got)
	want := []string{"assets/manifest.json", "audio/auto.dat", "audio/bgm/0.wav", "audio/se/1.wav", "maps/100.MAP"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public upload plan = %#v, want %#v", got, want)
	}
}

func TestValidateObjectPrefix(t *testing.T) {
	for _, prefix := range []string{"", "stoneage", "client/static"} {
		if err := validateObjectPrefix(prefix); err != nil {
			t.Errorf("prefix %q rejected: %v", prefix, err)
		}
	}
	for _, prefix := range []string{"../private", "stoneage/../private", `stoneage\\private`, "stoneage\x00private"} {
		if err := validateObjectPrefix(prefix); err == nil {
			t.Errorf("unsafe prefix %q accepted", prefix)
		}
	}
}
