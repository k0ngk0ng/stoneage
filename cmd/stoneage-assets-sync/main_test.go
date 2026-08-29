package main

import (
	"encoding/json"
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
	for _, directory := range []string{assets, mapDir, filepath.Join(data, "bgm"), filepath.Join(data, "se"), filepath.Join(data, "pal")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for filename, content := range map[string]string{
		filepath.Join(assets, "manifest.json"):    "{}",
		filepath.Join(mapDir, "100.MAP"):          "map",
		filepath.Join(data, "auto.dat"):           "auto",
		filepath.Join(data, "bgm", "0.wav"):       "bgm",
		filepath.Join(data, "se", "1.wav"):        "se",
		filepath.Join(data, "pal", "Palet_1.sap"): "palette",
		filepath.Join(data, "savedata.dat"):       "private",
		filepath.Join(data, "chatreg.dat"):        "private",
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
			{Path: filepath.Join(data, "pal"), Prefix: "pal"},
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
	want := []string{"assets/manifest.json", "audio/auto.dat", "audio/bgm/0.wav", "audio/pal/Palet_1.sap", "audio/se/1.wav", "maps/100.MAP"}
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

func TestCredentialValuePrefersSecretFile(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "access-key")
	if err := os.WriteFile(filename, []byte("file-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", "environment-value")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID_FILE", filename)
	value, err := credentialValue("ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_ID_FILE")
	if err != nil || value != "file-value" {
		t.Fatalf("credentialValue=%q err=%v", value, err)
	}
}

func TestCredentialValueFallsBackToEnvironment(t *testing.T) {
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "environment-value")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET_FILE", "")
	value, err := credentialValue("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ALIBABA_CLOUD_ACCESS_KEY_SECRET_FILE")
	if err != nil || value != "environment-value" {
		t.Fatalf("credentialValue=%q err=%v", value, err)
	}
}

func TestBuildPlanContainsStableHashesAndKeys(t *testing.T) {
	root := t.TempDir()
	assets := filepath.Join(root, "assets")
	if err := os.MkdirAll(filepath.Join(assets, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "nested", "sprite.png"), []byte("sprite"), 0o600); err != nil {
		t.Fatal(err)
	}
	objects, err := buildPlan([]sourceTree{{Name: "assets", Roots: []sourceRoot{{Path: assets}}}}, "stoneage")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].Key != "stoneage/assets/nested/sprite.png" || objects[0].Size != 6 {
		t.Fatalf("planned objects = %#v", objects)
	}
	if objects[0].SHA256 != "4a046e33ecf7aced9bfd000747bb1fda7836c8ceeff662af33c2a2c288b4e78c" {
		t.Fatalf("unexpected digest %q", objects[0].SHA256)
	}
}

func TestPartitionPublicationObjectsUploadsPayloadBeforeIndexes(t *testing.T) {
	objects := []plannedObject{
		{Key: "stoneage/assets/manifest.json"},
		{Key: "stoneage/assets/bitmaps/bitmap_1.png"},
		{Key: "stoneage/audio/auto.dat"},
		{Key: "stoneage/maps/100.MAP"},
		{Key: "stoneage/assets/sprites.json"},
	}
	regular, metadata := partitionPublicationObjects(objects)
	if got := []string{regular[0].Key, regular[1].Key}; !reflect.DeepEqual(got, []string{"stoneage/assets/bitmaps/bitmap_1.png", "stoneage/maps/100.MAP"}) {
		t.Fatalf("regular publication objects = %#v", got)
	}
	if got := []string{metadata[0].Key, metadata[1].Key, metadata[2].Key}; !reflect.DeepEqual(got, []string{"stoneage/assets/manifest.json", "stoneage/audio/auto.dat", "stoneage/assets/sprites.json"}) {
		t.Fatalf("metadata publication objects = %#v", got)
	}
}

func TestPublicationMetadataKeyOnlyMatchesIndexes(t *testing.T) {
	for _, key := range []string{"stoneage/assets/manifest.json", "stoneage/audio/auto.dat", "stoneage/assets/nested/SPRITES.JSON"} {
		if !publicationMetadataKey(key) {
			t.Errorf("publication metadata key %q was not recognized", key)
		}
	}
	for _, key := range []string{"stoneage/assets/bitmap_1.png", "stoneage/maps/100.MAP", "stoneage/audio/bgm/0.wav"} {
		if publicationMetadataKey(key) {
			t.Errorf("payload key %q was recognized as metadata", key)
		}
	}
}

func TestClientManifestRoundTrips(t *testing.T) {
	manifest := clientManifest{Format: 1, Generated: "2026-08-30T00:00:00Z", Objects: map[string]manifestObject{
		"stoneage/assets/a": {Size: 3, SHA256: "abc"},
	}}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var decoded clientManifest
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest, decoded) {
		t.Fatalf("manifest round trip = %#v, want %#v", decoded, manifest)
	}
}
