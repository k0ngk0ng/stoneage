package battletrain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCollectionCloseHelper(t *testing.T) {
	directory := ""
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "--collection-close=") {
			directory = strings.TrimPrefix(arg, "--collection-close=")
		}
	}
	if directory == "" {
		return
	}
	fmt.Printf("{\"schema_version\":1,\"ok\":true,\"ready\":true,\"scenario\":\"controlled-battle-v8\",\"platform\":\"linux-arm64\",\"rules_digest\":%q}\n", strings.Repeat("a", 64))
	_, _ = io.Copy(io.Discard, os.Stdin)
	if os.WriteFile(filepath.Join(directory, strconv.Itoa(os.Getpid())), []byte("EOF"), 0600) != nil {
		os.Exit(8)
	}
	os.Exit(7)
}

func TestRunPreservesOriginalAndAllWorkerCloseFailures(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	markers := filepath.Join(root, "markers")
	if err := os.Mkdir(markers, 0700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultRunConfig()
	want := errors.New("stop after initial checkpoint")
	err = Run(context.Background(), RunOptions{
		Directory: filepath.Join(root, "run"), Config: &cfg, Batches: 1, Workers: 3,
		Command:  []string{exe, "-test.run=^TestCollectionCloseHelper$", "--", "--collection-close=" + markers},
		Progress: func(p Progress) error { return want },
	})
	var exit *exec.ExitError
	if !errors.Is(err, want) || !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("lost original or shutdown error: %v", err)
	}
	for _, worker := range []string{"collection worker 1 shutdown", "collection worker 2 shutdown"} {
		if !strings.Contains(err.Error(), worker) {
			t.Fatalf("missing %s: %v", worker, err)
		}
	}
	files, err := os.ReadDir(markers)
	if err != nil || len(files) != 3 {
		t.Fatalf("workers did not all receive EOF: %d, %v", len(files), err)
	}
	if _, err := os.Stat(filepath.Join(root, "run", "latest.json")); err != nil {
		t.Fatal("failed close discarded checkpoint", err)
	}
}
