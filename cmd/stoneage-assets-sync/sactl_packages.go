package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

type sactlDownload struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type sactlDownloads struct {
	Version    string            `json:"version"`
	Packages   []sactlDownload   `json:"packages"`
	Installers map[string]string `json:"installers"`
}

// Verify every release file before uploading any bytes. Publish discovery last;
// installers and archives use immutable version paths, independent of game assets.
func publishSactlPackages(store objectStore, prefix, directory, version string) error {
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(version) {
		return fmt.Errorf("sactl CDN publication requires a stable release tag")
	}
	sums, err := os.ReadFile(filepath.Join(directory, "SHA256SUMS"))
	if err != nil {
		return err
	}
	hashes := map[string]string{}
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "./")
		if _, ok := hashes[name]; ok {
			return fmt.Errorf("duplicate checksum for %s", name)
		}
		hashes[name] = fields[0]
	}
	catalog := sactlDownloads{Version: version, Installers: map[string]string{}}
	root := path.Join("downloads/sactl", version)
	var files []sactlDownload
	for _, target := range [][2]string{{"darwin", "arm64"}, {"darwin", "amd64"}, {"windows", "amd64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		ext := ".tar.gz"
		if target[0] == "windows" {
			ext = ".zip"
		}
		name := fmt.Sprintf("stoneage-sactl-%s-%s-%s%s", version, target[0], target[1], ext)
		files = append(files, sactlDownload{OS: target[0], Arch: target[1], Path: path.Join(root, name)})
	}
	for _, name := range []string{"install-sactl.sh", "install-sactl.ps1"} {
		files = append(files, sactlDownload{Path: path.Join(root, name)})
	}
	for i := range files {
		item := &files[i]
		name := path.Base(item.Path)
		f, err := os.Open(filepath.Join(directory, name))
		if err != nil {
			return err
		}
		h := sha256.New()
		size, err := io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		digest := fmt.Sprintf("%x", h.Sum(nil))
		if digest != hashes[name] {
			return fmt.Errorf("checksum mismatch: %s", name)
		}
		item.Bytes = size
		item.SHA256 = digest
		if item.OS != "" {
			catalog.Packages = append(catalog.Packages, *item)
		} else {
			catalog.Installers[path.Ext(name)] = item.Path
		}
	}
	for _, item := range files {
		kind := "application/octet-stream"
		if strings.HasSuffix(item.Path, ".zip") {
			kind = "application/zip"
		}
		if strings.HasSuffix(item.Path, ".gz") {
			kind = "application/gzip"
		}
		if strings.HasSuffix(item.Path, ".sh") || strings.HasSuffix(item.Path, ".ps1") {
			kind = "text/plain; charset=utf-8"
		}
		if err := store.PutFile(path.Join(prefix, item.Path), filepath.Join(directory, path.Base(item.Path)), objectMetadata{ContentType: kind, CacheControl: "public, max-age=31536000, immutable", SHA256: item.SHA256}); err != nil {
			return err
		}
	}
	if err := store.Put(path.Join(prefix, root, "SHA256SUMS"), sums, objectMetadata{ContentType: "text/plain", CacheControl: "public, max-age=31536000, immutable", SHA256: fmt.Sprintf("%x", sha256.Sum256(sums))}); err != nil {
		return err
	}
	data, _ := json.Marshal(catalog)
	// Avoid an older workflow retry downgrading the public latest pointer.
	previous, err := store.GetObject(path.Join(prefix, "downloads/sactl/latest.json"))
	if err == nil {
		var old sactlDownloads
		decodeErr := json.NewDecoder(previous).Decode(&old)
		previous.Close()
		if decodeErr != nil {
			return decodeErr
		}
		if releaseNewer(old.Version, version) {
			return nil
		}
	} else if !errors.Is(err, errObjectNotFound) {
		return err
	}
	return store.Put(path.Join(prefix, "downloads/sactl/latest.json"), data, objectMetadata{ContentType: "application/json", CacheControl: "no-cache", SHA256: fmt.Sprintf("%x", sha256.Sum256(data))})
}
func releaseNewer(a, b string) bool {
	var av, bv [3]int
	if _, err := fmt.Sscanf(a, "v%d.%d.%d", &av[0], &av[1], &av[2]); err != nil {
		return false
	}
	fmt.Sscanf(b, "v%d.%d.%d", &bv[0], &bv[1], &bv[2])
	for i := range av {
		if av[i] != bv[i] {
			return av[i] > bv[i]
		}
	}
	return false
}
