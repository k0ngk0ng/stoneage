package main

import (
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

type wikiMaterialCatalog struct {
	Revision string `json:"revision"`
	Metadata []struct {
		Path   string `json:"path"`
		File   string `json:"file"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	} `json:"metadata"`
	Format   int `json:"format"`
	Packages []struct {
		Path   string `json:"path"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	} `json:"packages"`
}

// ZIPs are built offline and published under immutable content hashes. This
// separate ledger never changes the game resource revision or its caches.
func publishWikiMaterials(store objectStore, prefix, directory string, workers int) error {
	raw, err := os.ReadFile(filepath.Join(directory, "materials-publication.json"))
	if err != nil {
		return err
	}
	var catalog wikiMaterialCatalog
	if err = json.Unmarshal(raw, &catalog); err != nil {
		return err
	}
	if catalog.Format != 1 || len(catalog.Packages) == 0 || len(catalog.Metadata) == 0 || !regexp.MustCompile(`^materials-[a-f0-9]{16}$`).MatchString(catalog.Revision) {
		return fmt.Errorf("empty or invalid wiki material publication")
	}
	ledgerKey := path.Join(prefix, "wiki/materials/_publication.json")
	previous, err := readManifest(store, ledgerKey)
	if err != nil {
		return err
	}
	next := clientManifest{Format: 1, Objects: map[string]manifestObject{}}
	objects := make([]plannedObject, 0, len(catalog.Packages))
	pattern := regexp.MustCompile(`^wiki/materials/([a-f0-9]{64})\.zip$`)
	for _, pack := range catalog.Packages {
		match := pattern.FindStringSubmatch(pack.Path)
		if len(match) != 2 || match[1] != pack.SHA256 || pack.Bytes <= 0 {
			return fmt.Errorf("invalid material ZIP path")
		}
		key := path.Join(prefix, pack.Path)
		if _, ok := next.Objects[key]; ok {
			return fmt.Errorf("duplicate material ZIP")
		}
		filename := filepath.Join(directory, path.Base(pack.Path))
		stat, err := os.Lstat(filename)
		if err != nil {
			return err
		}
		if !stat.Mode().IsRegular() {
			return fmt.Errorf("material ZIP must be a regular file")
		}
		digest, size, err := fileDigest(filename)
		if err != nil {
			return err
		}
		if size != pack.Bytes || digest != pack.SHA256 {
			return fmt.Errorf("material ZIP checksum mismatch: %s", path.Base(pack.Path))
		}
		if err = verifyWikiMaterialZIP(filename); err != nil {
			return err
		}
		next.Objects[key] = manifestObject{Size: size, SHA256: digest}
		objects = append(objects, plannedObject{Filename: filename, Key: key, Size: size, SHA256: digest})
	}
	// Validate every metadata shard before making any remote changes.
	for _, item := range catalog.Metadata {
		name := path.Base(item.Path)
		if !regexp.MustCompile(`^(?:[a-f0-9]{2}|sprite-[0-9]+)\.json$`).MatchString(name) || item.Path != path.Join("wiki/materials", catalog.Revision, name) || item.File != path.Join("metadata", catalog.Revision, name+".gz") {
			return fmt.Errorf("invalid material metadata path")
		}
		filename := filepath.Join(directory, filepath.FromSlash(item.File))
		stat, err := os.Lstat(filename)
		if err != nil {
			return err
		}
		if !stat.Mode().IsRegular() {
			return fmt.Errorf("metadata must be regular files")
		}
		digest, size, err := fileDigest(filename)
		if err != nil {
			return err
		}
		if digest != item.SHA256 || size != item.Bytes {
			return fmt.Errorf("metadata checksum mismatch")
		}
		f, err := os.Open(filename)
		if err != nil {
			return err
		}
		z, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return err
		}
		data, err := io.ReadAll(io.LimitReader(z, 16<<20))
		z.Close()
		f.Close()
		if err != nil || !json.Valid(data) {
			return fmt.Errorf("invalid compressed JSON metadata")
		}
	}
	pointer, err := os.ReadFile(filepath.Join(directory, "catalog.json"))
	if err != nil {
		return err
	}
	var target struct {
		Revision string `json:"revision"`
	}
	if json.Unmarshal(pointer, &target) != nil || target.Revision != catalog.Revision {
		return fmt.Errorf("material catalog revision mismatch")
	}
	uploaded, skipped, err := syncObjectBatch(store, objects, previous.Objects, workers)
	if err != nil {
		return err
	}
	for _, item := range catalog.Metadata {
		if err = putFileWithRetry(store, path.Join(prefix, item.Path), filepath.Join(directory, filepath.FromSlash(item.File)), objectMetadata{ContentType: "application/json", ContentEncoding: "gzip", CacheControl: "public, max-age=31536000, immutable", SHA256: item.SHA256}); err != nil {
			return err
		}
	}
	if err = store.Put(path.Join(prefix, "wiki/materials/catalog.json"), pointer, objectMetadata{ContentType: "application/json", CacheControl: "no-cache", SHA256: fmt.Sprintf("%x", sha256.Sum256(pointer))}); err != nil {
		return err
	}
	if err = writeManifest(store, ledgerKey, next); err != nil {
		return err
	}
	fmt.Printf("published wiki materials: %d uploaded, %d reused\n", uploaded, skipped)
	return nil
}

func verifyWikiMaterialZIP(filename string) error {
	archive, err := zip.OpenReader(filename)
	if err != nil {
		return err
	}
	defer archive.Close()
	required := map[string]bool{"preview.html": false, "README.md": false, "reference.json": false, "new-asset.template.json": false, "SHA256SUMS": false}
	seen := map[string]bool{}
	for _, file := range archive.File {
		if path.Clean(file.Name) != file.Name || strings.HasPrefix(file.Name, "/") || strings.HasPrefix(file.Name, "../") || strings.ContainsAny(file.Name, "\\:") || file.Mode()&os.ModeSymlink != 0 || seen[file.Name] {
			return fmt.Errorf("unsafe ZIP entry: %s", file.Name)
		}
		seen[file.Name] = true
		if _, ok := required[file.Name]; ok {
			required[file.Name] = true
		}
		input, err := file.Open()
		if err != nil {
			return err
		}
		_, err = io.Copy(io.Discard, input)
		input.Close()
		if err != nil {
			return fmt.Errorf("invalid ZIP payload: %w", err)
		}
	}
	for file, found := range required {
		if !found {
			return fmt.Errorf("material ZIP lacks %s", file)
		}
	}
	return nil
}
