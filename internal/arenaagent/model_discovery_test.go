package arenaagent

import (
	"github.com/k0ngk0ng/stoneage/internal/battlepolicy"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalModelDiscoveryPrecedenceAmbiguityAndMode(t *testing.T) {
	root := t.TempDir()
	dirs := []string{filepath.Join(root, "cwd"), filepath.Join(root, "models")}
	for _, d := range dirs {
		if e := os.MkdirAll(d, 0700); e != nil {
			t.Fatal(e)
		}
	}
	l, _ := neuralFixtureModel(t, 1)
	save := func(name string, a battlepolicy.Artifact) string {
		t.Helper()
		if e := battlepolicy.SaveArtifact(name, a); e != nil {
			t.Fatal(e)
		}
		return name
	}
	local := save(filepath.Join(dirs[1], "one.safetensors"), *l.neural)
	if got, e := resolveLocalModel("", 1, dirs); e != nil || got != local {
		t.Fatal(got, e)
	}
	if got, e := resolveLocalModel("one.safetensors", 1, dirs); e != nil || got != local {
		t.Fatal(got, e)
	}
	if _, e := resolveLocalModel("./missing/one.safetensors", 1, dirs); e == nil {
		t.Fatal("explicit path fell back")
	}
	if _, e := resolveLocalModel("", 2, dirs); e == nil || !strings.Contains(e.Error(), "Rejected") {
		t.Fatal(e)
	}
	cwd := save(filepath.Join(dirs[0], "first.safetensors"), *l.neural)
	if got, e := resolveLocalModel("", 1, dirs); e != nil || got != cwd {
		t.Fatal(got, e)
	}
	save(filepath.Join(dirs[0], "identical.safetensors"), *l.neural)
	if got, e := resolveLocalModel("", 1, dirs); e != nil || got != cwd {
		t.Fatal(got, e)
	}
	other := *l.neural
	other.TrainingReport = strings.Repeat("1", 64)
	save(filepath.Join(dirs[0], "other.safetensors"), other)
	if _, e := resolveLocalModel("", 1, dirs); e == nil || !strings.Contains(e.Error(), "multiple local models") || !strings.Contains(e.Error(), cwd) {
		t.Fatal(e)
	}
	if got, e := resolveLocalModel("first.safetensors", 1, dirs); e != nil || got != cwd {
		t.Fatal(got, e)
	}
	if e := os.WriteFile(filepath.Join(dirs[0], "one.safetensors"), []byte("broken"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := resolveLocalModel("one.safetensors", 1, dirs); e == nil {
		t.Fatal("invalid cwd model silently fell back")
	}
}
