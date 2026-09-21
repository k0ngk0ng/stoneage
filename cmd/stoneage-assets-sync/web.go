package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/k0ngk0ng/stoneage/client/web/runtimeassets"
)

// publishWeb adds supplementary objects without changing the asset revision or
// invalidating existing image/map caches. Catalogs are published last.
func publishWeb(store objectStore, prefix, assetDirectory, packDirectory string) error {
	manifest, err := readManifest(store, path.Join(prefix, clientManifestName))
	if err != nil {
		return err
	}
	if len(manifest.Objects) == 0 {
		return fmt.Errorf("publish the core assets first")
	}
	revision := manifestRevision(manifest)
	put := func(name string, data []byte, kind string) error {
		return store.Put(path.Join(prefix, name), data, objectMetadata{ContentType: kind, CacheControl: "public, max-age=31536000, immutable", SHA256: fmt.Sprintf("%x", sha256.Sum256(data))})
	}
	catalog := map[string]any{}
	// Only public top-level JSON indexes from the existing manifest are eligible.
	for key, expected := range manifest.Objects {
		relative := strings.TrimPrefix(key, path.Join(prefix, "assets")+"/")
		if relative == key || strings.Contains(relative, "/") || !strings.HasSuffix(relative, ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(assetDirectory, relative))
		if err != nil {
			return err
		}
		if int64(len(data)) != expected.Size || fmt.Sprintf("%x", sha256.Sum256(data)) != expected.SHA256 {
			return fmt.Errorf("index differs from published assets: %s", relative)
		}
		var compressed bytes.Buffer
		writer, _ := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
		if _, err = writer.Write(data); err != nil {
			return err
		}
		if err = writer.Close(); err != nil {
			return err
		}
		target := fmt.Sprintf("indexes/%x.json.gz", sha256.Sum256(compressed.Bytes()))
		if err = put(target, compressed.Bytes(), "application/gzip"); err != nil {
			return err
		}
		catalog["assets/"+relative] = map[string]any{"path": target, "bytes": len(data), "sha256": expected.SHA256}
		fmt.Printf("index %s: %d -> %d bytes\n", relative, len(data), compressed.Len())
	}
	data, _ := json.Marshal(catalog)
	if err = put("indexes/"+revision+"/catalog.json", data, "application/json"); err != nil {
		return err
	}
	if packDirectory != "" {
		data, _ := runtimeassets.Files.ReadFile("map-packs.json")
		var packs struct {
			Revision string `json:"revision"`
			Packages []struct {
				Path   string `json:"path"`
				Bytes  int64  `json:"bytes"`
				SHA256 string `json:"sha256"`
			} `json:"packages"`
		}
		if err = json.Unmarshal(data, &packs); err != nil {
			return err
		}
		if packs.Revision != revision {
			return fmt.Errorf("map pack catalog revision does not match published assets")
		}
		for _, pack := range packs.Packages {
			filename := filepath.Join(packDirectory, path.Base(pack.Path))
			data, err := os.ReadFile(filename)
			if err != nil {
				return err
			}
			if int64(len(data)) != pack.Bytes || fmt.Sprintf("%x", sha256.Sum256(data)) != pack.SHA256 {
				return fmt.Errorf("map pack checksum mismatch: %s", filename)
			}
			// Pack headers include their publication revision; the importer verifies
			// each file hash before placing it in the browser cache.
			if len(data) < 16 || !bytes.Contains(data[:min(len(data), 1048576)], []byte(revision)) {
				return fmt.Errorf("map pack revision missing: %s", filename)
			}
			if err = put(pack.Path, data, "application/octet-stream"); err != nil {
				return err
			}
			fmt.Printf("published %s (%d bytes)\n", pack.Path, len(data))
		}
	}
	entries, _ := runtimeassets.Files.ReadDir(".")
	for _, entry := range entries {
		data, err := runtimeassets.Files.ReadFile(entry.Name())
		if err != nil {
			return err
		}
		kind := mime.TypeByExtension(path.Ext(entry.Name()))
		if kind == "" {
			kind = "application/octet-stream"
		}
		if err = put(runtimeassets.Root()+entry.Name(), data, kind); err != nil {
			return err
		}
	}
	fmt.Printf("published runtime %s; resource revision unchanged: %s\n", runtimeassets.Root(), revision)
	return nil
}
