// Package runtimeassets is the single source for local development and CDN
// publication. The publisher and Web binary therefore agree on every byte.
package runtimeassets

import (
	"crypto/sha256"
	"embed"
	"fmt"
)

//go:embed *.js *.json *.webmanifest
var Files embed.FS

// Root is content addressed independently of the large game asset revision.
// Publishing program resources never invalidates existing image/map caches.
var root = computeRoot()

func Root() string { return root }

func computeRoot() string {
	hash := sha256.New()
	entries, _ := Files.ReadDir(".")
	for _, entry := range entries {
		data, _ := Files.ReadFile(entry.Name())
		fmt.Fprintf(hash, "%s\x00%x\x00", entry.Name(), sha256.Sum256(data))
	}
	return fmt.Sprintf("web/%x/", hash.Sum(nil))
}
