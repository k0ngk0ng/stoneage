// Package runtimeassets is the single source for local development and CDN
// publication. The publisher and Web binary therefore agree on every byte.
package runtimeassets

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"regexp"
	"sort"
	"sync"
)

//go:embed *.js *.json *.webmanifest
var Files embed.FS

// SourcePage remains directly readable by the legacy protocol regression
// tests. Production extracts its code and CSS into immutable CDN objects.
//
//go:embed index.html
var SourcePage []byte

var style = regexp.MustCompile(`(?s)<style>(.*?)</style>`)
var script = regexp.MustCompile(`(?s)<script>(.*?)</script>`)
var bundles sync.Map

type bundle struct {
	root  string
	extra map[string][]byte
}

func RewriteRoots(source []byte, base string) []byte {
	result := append([]byte(nil), source...)
	roots := []string{"/assets/", "/maps/", "/audio/"}
	for i, root := range roots {
		result = bytes.ReplaceAll(result, []byte(root), []byte(fmt.Sprintf("__STONEAGE_STATIC_ROOT_%d__", i)))
	}
	for i, root := range roots {
		result = bytes.ReplaceAll(result, []byte(fmt.Sprintf("__STONEAGE_STATIC_ROOT_%d__", i)), []byte(base+root))
	}
	return result
}

func get(base string) *bundle {
	if value, ok := bundles.Load(base); ok {
		return value.(*bundle)
	}
	css := style.FindSubmatch(SourcePage)[1]
	app := script.FindSubmatch(SourcePage)[1]
	// The map filename inventory is deployment configuration supplied by the
	// HTML entry. It is shared as a global lexical binding with the CDN script.
	app = bytes.Replace(app, []byte("const AUTO_MAP_FILES={};"), nil, 1)
	extra := map[string][]byte{"app.js": RewriteRoots(app, base), "app.css": RewriteRoots(css, base)}
	all := map[string][]byte{}
	entries, _ := Files.ReadDir(".")
	for _, entry := range entries {
		all[entry.Name()], _ = Files.ReadFile(entry.Name())
	}
	for name, data := range extra {
		all[name] = data
	}
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		fmt.Fprintf(hash, "%s\x00%x\x00", name, sha256.Sum256(all[name]))
	}
	value, _ := bundles.LoadOrStore(base, &bundle{root: fmt.Sprintf("web/%x/", hash.Sum(nil)), extra: extra})
	return value.(*bundle)
}

// Root is content addressed independently of the large game asset revision.
func Root(base ...string) string {
	value := ""
	if len(base) > 0 {
		value = base[0]
	}
	return get(value).root
}
func Extra(base string) map[string][]byte { return get(base).extra }

func Externalize(source []byte, base string) []byte {
	root := base + "/" + Root(base)
	source = style.ReplaceAllLiteral(source, []byte(`<link rel="stylesheet" href="`+root+`app.css">`))
	return script.ReplaceAllLiteral(source, []byte(`<script>const AUTO_MAP_FILES={};</script><script src="`+root+`app.js"></script>`))
}
