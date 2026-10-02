package arenaagent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrainCollectionWorkerFlags(t *testing.T) {
	for _, n := range []string{"0", "-1", "9"} {
		path := filepath.Join(t.TempDir(), "must-not-exist")
		var out bytes.Buffer
		err := Main(context.Background(), []string{"train", "--workers", n, "--data-dir", path}, "test", &out)
		if err == nil || !strings.Contains(err.Error(), "--workers must be") {
			t.Fatal(n, err)
		}
		if _, e := os.Stat(path); !os.IsNotExist(e) {
			t.Fatal("invalid workers wrote training files")
		}
	}
	for _, args := range [][]string{{"train", "--database", "unused", "--workers", "2"}, {"train", "--demonstrations", "unused", "--workers", "2"}} {
		var out bytes.Buffer
		err := Main(context.Background(), args, "test", &out)
		if err == nil || !strings.Contains(err.Error(), "workers") {
			t.Fatal("non-native trainer ignored workers", args, err)
		}
	}
	var out bytes.Buffer
	err := Main(context.Background(), []string{"train", "--resume", "--workers", "2", "--data-dir", filepath.Join(t.TempDir(), "missing"), "--environment", "missing-worker-config"}, "test", &out)
	if err == nil || !strings.Contains(err.Error(), "missing-worker-config") {
		t.Fatal("resume did not accept runtime-only worker override", err)
	}
}
