package admin

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/k0ngk0ng/stoneage/internal/runtimepath"
)

func TestConnectionWorkRootRejectsOperatorHomeBeforeCreatingFiles(t *testing.T) {
	root := t.TempDir()
	operatorHome := filepath.Join(root, "operator-codex")
	if err := os.Mkdir(operatorHome, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", operatorHome)
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(operatorHome, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{operatorHome, filepath.Join(operatorHome, "probe"), filepath.Join(alias, "probe")} {
		if err := ensureAIConnectionWorkRoot(path); !errors.Is(err, runtimepath.ErrForbiddenPath) {
			t.Fatalf("operator root accepted: %v", err)
		}
	}
	entries, err := os.ReadDir(operatorHome)
	if err != nil || len(entries) != 0 {
		t.Fatalf("operator root modified: entries=%d err=%v", len(entries), err)
	}
	if err := ensureAIConnectionWorkRoot(filepath.Join(root, "isolated-game-runtime")); err != nil {
		t.Fatalf("independent root rejected: %v", err)
	}
}
