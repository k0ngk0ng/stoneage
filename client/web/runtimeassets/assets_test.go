package runtimeassets

import (
	"bytes"
	"strings"
	"testing"
)

func TestExternalProgramAndStylesUseSamePublishedContent(t *testing.T) {
	base := "https://cdn.example/game"
	shell := Externalize(RewriteRoots(SourcePage, base), base)
	if len(shell) > 50000 {
		t.Fatalf("HTML still carries program payload: %d bytes", len(shell))
	}
	for _, name := range []string{"app.js", "app.css"} {
		if !bytes.Contains(shell, []byte(base+"/"+Root(base)+name)) {
			t.Fatal("missing CDN link", name)
		}
	}
	if bytes.Count(shell, []byte("const AUTO_MAP_FILES={};")) != 1 {
		t.Fatal("map deployment configuration must remain in HTML")
	}
	app := Extra(base)["app.js"]
	if bytes.Contains(app, []byte("const AUTO_MAP_FILES=")) {
		t.Fatal("app shadows dynamic map inventory")
	}
	if !bytes.Contains(app, []byte(base+"/assets/")) || !bytes.Contains(app, []byte("/api/sessions")) {
		t.Fatal("static CDN or API route lost")
	}
	if Root(base) == Root("https://other.example/game") {
		t.Fatal("different CDN roots must have distinct published content hashes")
	}
	if strings.Contains(string(Extra(base)["app.css"]), `url("/assets/`) {
		t.Fatal("CSS retained origin images")
	}
}
