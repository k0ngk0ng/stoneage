package main

import (
	"bytes"
	"testing"
)

func TestPageWithReleaseVersion(t *testing.T) {
	for _, version := range []string{"v0.1.23", "dev", `<script>"&`} {
		rendered := pageWithReleaseVersion(page, version)
		if bytes.Contains(rendered, []byte("<!--STONEAGE_RELEASE_VERSION-->")) {
			t.Fatal("release placeholder leaked")
		}
		expected := version
		if version == `<script>"&` {
			expected = "&lt;script&gt;&#34;&amp;"
		}
		if !bytes.Contains(rendered, []byte(`aria-label="当前版本">`+expected+`</span>`)) {
			t.Fatalf("missing safe release label for %q", version)
		}
	}
}
