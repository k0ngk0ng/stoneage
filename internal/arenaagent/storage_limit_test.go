package arenaagent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrainStorageThresholdFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "must-not-exist")
	for _, args := range [][]string{
		{"train", "--stop-at-data-bytes", "-1", "--data-dir", path},
		{"train", "--database", "unused", "--stop-at-data-bytes", "1"},
		{"train", "--demonstrations", "unused", "--stop-at-data-bytes", "1"},
	} {
		var out bytes.Buffer
		err := Main(context.Background(), args, "test", &out)
		if err == nil || !strings.Contains(err.Error(), "stop-at-data-bytes") {
			t.Fatal(args, err)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid storage threshold wrote files")
	}
	var out bytes.Buffer
	err := Main(context.Background(), []string{"train", "--resume", "--stop-at-data-bytes", "0", "--data-dir", path, "--environment", "missing-resource-test-env"}, "test", &out)
	if err == nil || !strings.Contains(err.Error(), "missing-resource-test-env") {
		t.Fatal("resume rejects invocation resource setting", err)
	}
}
