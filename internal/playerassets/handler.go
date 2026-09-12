// Package playerassets serves the same native bitmap references used by the
// Web client without letting an admin request an arbitrary filesystem path.
package playerassets

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/gamecatalog"
)

type Handler struct {
	Root        string
	mu          sync.Mutex
	manifest    *gamecatalog.Manifest
	modified    time.Time
	size        int64
	petPreviews map[int]gamecatalog.Asset
	petModified time.Time
	petSize     int64
}

func (h *Handler) petAsset(id int) (gamecatalog.Asset, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	path := filepath.Join(h.Root, "sprites.json")
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		path = filepath.Join(h.Root, "manifest.json")
		info, err = os.Stat(path)
	}
	if err != nil {
		return gamecatalog.Asset{}, false, err
	}
	if h.petPreviews == nil || !info.ModTime().Equal(h.petModified) || info.Size() != h.petSize {
		file, err := os.Open(path)
		if err != nil {
			return gamecatalog.Asset{}, false, err
		}
		previews, err := gamecatalog.ParsePetPreviews(file)
		file.Close()
		if err != nil {
			return gamecatalog.Asset{}, false, err
		}
		h.petPreviews, h.petModified, h.petSize = previews, info.ModTime(), info.Size()
	}
	asset, ok := h.petPreviews[id]
	return asset, ok, nil
}

func (h *Handler) load() (*gamecatalog.Manifest, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	path := filepath.Join(h.Root, "manifest.json")
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if h.manifest != nil && info.ModTime().Equal(h.modified) && info.Size() == h.size {
		return h.manifest, nil
	}
	manifest, err := gamecatalog.LoadManifest(path)
	if err != nil {
		return nil, err
	}
	h.manifest, h.modified, h.size = manifest, info.ModTime(), info.Size()
	return manifest, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	const prefix = "/api/player-assets/graphic/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, prefix))
	if err != nil || id < 0 {
		http.NotFound(w, r)
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "pet" && kind != "item" && kind != "character" {
		http.NotFound(w, r)
		return
	}
	manifest, err := h.load()
	if err != nil {
		http.Error(w, "外观资源暂时不可用", 503)
		return
	}
	asset, ok := manifest.ResolveItem(id)
	if kind == "pet" || kind == "character" {
		asset, ok, err = h.petAsset(id)
		if err != nil {
			http.Error(w, "角色外观资源暂时不可用", 503)
			return
		}
	}
	if !ok || asset.File == "" {
		http.NotFound(w, r)
		return
	}
	path, err := assetPath(h.Root, asset.File)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Include the resolved frame identity, not only the PNG timestamp. Different
	// frames in one resource pack commonly share a timestamp, and a stale
	// If-Modified-Since must not keep an unrelated cached pet image alive.
	etag := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", asset.File, info.Size(), info.ModTime().UnixNano())))
	w.Header().Set("ETag", fmt.Sprintf("\"%x\"", etag))
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, filepath.Base(path), time.Time{}, file)
}

func assetPath(root, name string) (string, error) {
	if filepath.IsAbs(name) || filepath.Ext(name) != ".png" || strings.Contains(name, "\\") {
		return "", fmt.Errorf("invalid bitmap path")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(resolvedRoot, name))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(resolvedRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("bitmap path escapes resource directory")
	}
	return path, nil
}
