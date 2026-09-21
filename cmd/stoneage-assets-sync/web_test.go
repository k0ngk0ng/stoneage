package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/client/web/runtimeassets"
)

func TestPublishWebPreservesRevisionAndCompressesIndexes(t *testing.T) {
	root := t.TempDir()
	source := []byte(`{"frames":[1,2,3]}`)
	if err := os.WriteFile(filepath.Join(root, "sprites.json"), source, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := clientManifest{Format: 1, Objects: map[string]manifestObject{"game/assets/sprites.json": {Size: int64(len(source)), SHA256: fmt.Sprintf("%x", sha256.Sum256(source))}}}
	store := &memoryObjectStore{}
	if err := writeManifest(store, "game/"+clientManifestName, manifest); err != nil {
		t.Fatal(err)
	}
	before := append([]byte(nil), store.objects["game/"+clientManifestName]...)
	if err := publishWeb(store, "game", root, "", "https://cdn.example/game"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, store.objects["game/"+clientManifestName]) {
		t.Fatal("core manifest changed")
	}
	if _, ok := store.objects["game/"+clientVersionName]; ok {
		t.Fatal("core version changed")
	}
	var catalog map[string]struct {
		Path  string
		Bytes int
	}
	if err := json.Unmarshal(store.objects["game/indexes/"+manifestRevision(manifest)+"/catalog.json"], &catalog); err != nil {
		t.Fatal(err)
	}
	key := "game/" + catalog["assets/sprites.json"].Path
	reader, err := gzip.NewReader(bytes.NewReader(store.objects[key]))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(data, source) {
		t.Fatalf("gzip mismatch: %v", err)
	}
	if store.metadata[key].CacheControl != "public, max-age=31536000, immutable" {
		t.Fatal("gzip is not immutable")
	}
	if len(store.objects["game/"+runtimeassets.Root("https://cdn.example/game")+"resource-worker.js"]) == 0 {
		t.Fatal("runtime missing")
	}
	if err := os.WriteFile(filepath.Join(root, "sprites.json"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishWeb(store, "game", root, "", "https://cdn.example/game"); err == nil {
		t.Fatal("unpublished index accepted")
	}
}
