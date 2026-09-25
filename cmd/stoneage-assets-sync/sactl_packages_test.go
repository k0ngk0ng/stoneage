package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sactlFixture(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	var sums strings.Builder
	names := []string{"install-sactl.sh", "install-sactl.ps1"}
	for _, target := range []string{"darwin-arm64.tar.gz", "darwin-amd64.tar.gz", "windows-amd64.zip", "linux-amd64.tar.gz", "linux-arm64.tar.gz"} {
		names = append(names, "stoneage-sactl-"+version+"-"+target)
	}
	for _, name := range names {
		data := []byte("fixture " + name)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sums, "%x  ./%s\n", sha256.Sum256(data), name)
	}
	os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0600)
	return dir
}
func TestSactlCDNChecksumsAndLatest(t *testing.T) {
	dir := sactlFixture(t, "v0.1.92")
	store := &memoryObjectStore{}
	if err := publishSactlPackages(store, "game", dir, "v0.1.92"); err != nil {
		t.Fatal(err)
	}
	var catalog sactlDownloads
	if err := json.Unmarshal(store.objects["game/downloads/sactl/latest.json"], &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Packages) != 5 || len(catalog.Installers) != 2 {
		t.Fatal(catalog)
	}
	for _, pack := range catalog.Packages {
		if len(store.objects["game/"+pack.Path]) == 0 {
			t.Fatal("missing published archive")
		}
	}
	older := sactlFixture(t, "v0.1.91")
	if err := publishSactlPackages(store, "game", older, "v0.1.91"); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(store.objects["game/downloads/sactl/latest.json"], &catalog)
	if catalog.Version != "v0.1.92" {
		t.Fatal("latest downgraded")
	}
	os.WriteFile(filepath.Join(dir, "install-sactl.ps1"), []byte("tampered"), 0600)
	empty := &memoryObjectStore{}
	if err := publishSactlPackages(empty, "game", dir, "v0.1.92"); err == nil || len(empty.objects) != 0 {
		t.Fatal("uploaded before validating all checksums")
	}
}
