package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
)

type resourcePackCatalog struct {
	Revision string `json:"revision"`
	Packages []struct {
		Name          string `json:"name"`
		Path          string `json:"path"`
		Bytes         int64  `json:"bytes"`
		UnpackedBytes int64  `json:"unpacked_bytes"`
		Files         int    `json:"files"`
		SHA256        string `json:"sha256"`
	} `json:"packages"`
}

// Publish the download catalog only after every ZIP has been verified and
// uploaded. This supplements the current revision without republishing assets.
func publishResourcePacks(store objectStore, prefix, directory string) error {
	manifest, err := readManifest(store, path.Join(prefix, clientManifestName))
	if err != nil {
		return err
	}
	revision := manifestRevision(manifest)
	data, err := os.ReadFile(filepath.Join(directory, "resource-packs.json"))
	if err != nil {
		return err
	}
	var catalog resourcePackCatalog
	if err = json.Unmarshal(data, &catalog); err != nil {
		return err
	}
	if len(manifest.Objects) == 0 || catalog.Revision != revision || len(catalog.Packages) != 1 {
		return fmt.Errorf("resource pack must cover the current publication")
	}
	for _, pack := range catalog.Packages {
		if !regexp.MustCompile(`^[A-Za-z0-9_-]+\.zip$`).MatchString(path.Base(pack.Path)) || pack.Path != path.Join("packs", revision, path.Base(pack.Path)) {
			return fmt.Errorf("invalid resource pack path")
		}
		filename := filepath.Join(directory, path.Base(pack.Path))
		file, err := os.Open(filename)
		if err != nil {
			return err
		}
		hash := sha256.New()
		size, err := io.Copy(hash, file)
		file.Close()
		if err != nil {
			return err
		}
		if size != pack.Bytes || fmt.Sprintf("%x", hash.Sum(nil)) != pack.SHA256 {
			return fmt.Errorf("resource pack checksum mismatch")
		}
		if err = verifyResourceZIP(filename, prefix, revision, manifest, pack.Files, pack.UnpackedBytes); err != nil {
			return err
		}
		if err = store.PutFile(path.Join(prefix, pack.Path), filename, objectMetadata{ContentType: "application/zip", CacheControl: "public, max-age=31536000, immutable", SHA256: pack.SHA256}); err != nil {
			return err
		}
		fmt.Printf("published %s (%d bytes)\n", pack.Path, pack.Bytes)
	}
	return store.Put(path.Join(prefix, "packs", revision, "resource-packs.json"), data, objectMetadata{ContentType: "application/json", CacheControl: "no-cache", SHA256: fmt.Sprintf("%x", sha256.Sum256(data))})
}

func verifyResourceZIP(filename, prefix, revision string, manifest clientManifest, count int, bytes int64) error {
	archive, err := zip.OpenReader(filename)
	if err != nil {
		return err
	}
	defer archive.Close()
	if len(archive.File) != len(manifest.Objects)+1 || count != len(manifest.Objects) || archive.File[0].Name != "stoneage-resources.json" || archive.File[0].UncompressedSize64 > 128<<20 || archive.File[0].Method != zip.Store || archive.File[0].Flags & ^uint16(0x800) != 0 {
		return fmt.Errorf("resource ZIP does not contain the full publication")
	}
	reader, err := archive.File[0].Open()
	if err != nil {
		return err
	}
	defer reader.Close()
	var header struct {
		Format   int    `json:"format"`
		Revision string `json:"revision"`
		Bytes    int64  `json:"bytes"`
		Entries  []struct {
			Path   string `json:"path"`
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
			Offset int64  `json:"offset"`
		} `json:"entries"`
	}
	if err = json.NewDecoder(reader).Decode(&header); err != nil {
		return err
	}
	if header.Format != 2 || header.Revision != revision || len(header.Entries) != count || header.Bytes != bytes {
		return fmt.Errorf("resource ZIP manifest mismatch")
	}
	seen := map[string]bool{}
	var total int64
	var offset int64
	for i, entry := range header.Entries {
		expected, ok := manifest.Objects[path.Join(prefix, entry.Path)]
		z := archive.File[i+1]
		if !ok || seen[entry.Path] || entry.Size < 0 || entry.Size > 256<<20 || entry.Size != expected.Size || entry.SHA256 != expected.SHA256 || z.Name != entry.Path || z.Method != zip.Store || z.Flags & ^uint16(0x800) != 0 || entry.Offset != offset || z.UncompressedSize64 != uint64(entry.Size) {
			return fmt.Errorf("resource ZIP entry differs from publication: %s", entry.Path)
		}
		input, err := z.Open()
		if err != nil {
			return err
		}
		digest := sha256.New()
		_, err = io.Copy(digest, input)
		input.Close()
		if err != nil || fmt.Sprintf("%x", digest.Sum(nil)) != expected.SHA256 {
			return fmt.Errorf("resource ZIP data checksum mismatch: %s", entry.Path)
		}
		offset += 30 + int64(len(entry.Path)) + entry.Size
		seen[entry.Path] = true
		total += entry.Size
	}
	if total != bytes {
		return fmt.Errorf("resource ZIP size mismatch")
	}
	return nil
}
