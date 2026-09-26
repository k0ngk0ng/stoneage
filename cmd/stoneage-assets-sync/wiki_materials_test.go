package main

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestWikiMaterialPublicationValidatesBeforeWritingAndReusesZIPs(t *testing.T) {
	root := t.TempDir()
	var buffer bytes.Buffer
	z := zip.NewWriter(&buffer)
	for _, name := range []string{"preview.html", "README.md", "reference.json", "new-asset.template.json", "SHA256SUMS"} {
		w, _ := z.Create(name)
		w.Write([]byte("{}"))
	}
	z.Close()
	raw := buffer.Bytes()
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	name := digest + ".zip"
	os.WriteFile(filepath.Join(root, name), raw, 0600)
	revision := "materials-1234567890abcdef"
	var metadata bytes.Buffer
	gz := gzip.NewWriter(&metadata)
	gz.Write([]byte("{}"))
	gz.Close()
	mf := filepath.Join("metadata", revision, "00.json.gz")
	os.MkdirAll(filepath.Dir(filepath.Join(root, mf)), 0700)
	os.WriteFile(filepath.Join(root, mf), metadata.Bytes(), 0600)
	catalog := map[string]any{"format": 1, "revision": revision, "packages": []any{map[string]any{"path": "wiki/materials/" + name, "bytes": len(raw), "sha256": digest}}, "metadata": []any{map[string]any{"path": "wiki/materials/" + revision + "/00.json", "file": filepath.ToSlash(mf), "bytes": metadata.Len(), "sha256": fmt.Sprintf("%x", sha256.Sum256(metadata.Bytes()))}}}
	encoded, _ := json.Marshal(catalog)
	os.WriteFile(filepath.Join(root, "materials-publication.json"), encoded, 0600)
	pointer, _ := json.Marshal(map[string]string{"revision": revision})
	os.WriteFile(filepath.Join(root, "catalog.json"), pointer, 0600)
	store := &memoryObjectStore{}
	// A damaged second-stage metadata file must prevent even the first ZIP upload.
	os.WriteFile(filepath.Join(root, mf), []byte("bad"), 0600)
	if err := publishWikiMaterials(store, "game", root, 1); err == nil {
		t.Fatal("corrupt metadata accepted")
	}
	if len(store.objects) != 0 {
		t.Fatal("wrote objects before validation finished")
	}
	os.WriteFile(filepath.Join(root, mf), metadata.Bytes(), 0600)
	if err := publishWikiMaterials(store, "game", root, 1); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(store.objects["game/wiki/materials/catalog.json"], pointer) {
		t.Fatal("catalog not published")
	}
	if metadata := store.metadata["game/wiki/materials/"+revision+"/00.json"]; metadata.ContentEncoding != "gzip" || metadata.ContentType != "application/json" {
		t.Fatal("compressed index headers missing", metadata)
	}
	if !bytes.Equal(store.objects["game/wiki/materials/"+name], raw) {
		t.Fatal("ZIP not published")
	}
	if err := publishWikiMaterials(store, "game", root, 1); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, name), []byte("bad zip"), 0600)
	if err := publishWikiMaterials(store, "game", root, 1); err == nil {
		t.Fatal("corrupt zip accepted")
	}
}
func TestWikiMaterialZIPRejectsTraversal(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bad.zip")
	f, _ := os.Create(file)
	z := zip.NewWriter(f)
	w, _ := z.Create("../private")
	w.Write([]byte("x"))
	z.Close()
	f.Close()
	if verifyWikiMaterialZIP(file) == nil {
		t.Fatal("traversal accepted")
	}
}
