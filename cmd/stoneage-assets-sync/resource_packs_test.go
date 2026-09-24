package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

func TestResourcePackPublicationChecksBytesAndPublishesCatalogLast(t *testing.T) {
	root := t.TempDir()
	source := []byte("public pet image")
	object := manifestObject{Size: int64(len(source)), SHA256: fmt.Sprintf("%x", sha256.Sum256(source))}
	manifest := clientManifest{Format: 1, Objects: map[string]manifestObject{"game/assets/pet.png": object}}
	revision := manifestRevision(manifest)
	header, _ := json.Marshal(map[string]any{"format": 2, "revision": revision, "bytes": len(source), "entries": []any{map[string]any{"path": "assets/pet.png", "size": len(source), "sha256": object.SHA256, "offset": 0}}})
	filename := filepath.Join(root, "stoneage-resources.zip")
	file, err := os.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(file)
	for _, entry := range []struct {
		name string
		data []byte
	}{{"stoneage-resources.json", header}, {"assets/pet.png", source}} {
		writer, err := archive.CreateRaw(&zip.FileHeader{Name: entry.name, Method: zip.Store, CRC32: crc32.ChecksumIEEE(entry.data), CompressedSize64: uint64(len(entry.data)), UncompressedSize64: uint64(len(entry.data))})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = writer.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err = archive.Close(); err != nil {
		t.Fatal(err)
	}
	file.Close()
	payload, _ := os.ReadFile(filename)
	catalog := resourcePackCatalog{Revision: revision}
	data, _ := json.Marshal(map[string]any{"revision": revision, "packages": []any{map[string]any{"name": "完整游戏资源", "files": 1, "bytes": len(payload), "unpacked_bytes": len(source), "path": "packs/" + revision + "/stoneage-resources.zip", "sha256": fmt.Sprintf("%x", sha256.Sum256(payload))}}})
	if err = json.Unmarshal(data, &catalog); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "resource-packs.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	store := &memoryObjectStore{}
	if err = writeManifest(store, "game/"+clientManifestName, manifest); err != nil {
		t.Fatal(err)
	}
	if err = publishResourcePacks(store, "game", root); err != nil {
		t.Fatal(err)
	}
	if len(store.objects["game/packs/"+revision+"/resource-packs.json"]) == 0 {
		t.Fatal("catalog missing")
	}
	if store.metadata["game/packs/"+revision+"/stoneage-resources.zip"].ContentType != "application/zip" {
		t.Fatal("wrong ZIP content type")
	}
	delete(store.objects, "game/packs/"+revision+"/resource-packs.json")
	payload[len(payload)/2] ^= 1
	if err = os.WriteFile(filename, payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err = publishResourcePacks(store, "game", root); err == nil {
		t.Fatal("corrupt ZIP accepted")
	}
	if len(store.objects["game/packs/"+revision+"/resource-packs.json"]) != 0 {
		t.Fatal("failed upload exposed catalog")
	}
	catalog.Revision = "stale"
	data, _ = json.Marshal(catalog)
	os.WriteFile(filepath.Join(root, "resource-packs.json"), data, 0600)
	if err = publishResourcePacks(store, "game", root); err == nil {
		t.Fatal("stale pack accepted")
	}
}
