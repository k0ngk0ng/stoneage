package battleenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNativeCloseHelper(t *testing.T) {
	mode, marker := "", ""
	for _, a := range os.Args {
		if strings.HasPrefix(a, "--close-helper=") {
			mode = strings.TrimPrefix(a, "--close-helper=")
		}
		if strings.HasPrefix(a, "--marker=") {
			marker = strings.TrimPrefix(a, "--marker=")
		}
	}
	if mode == "" {
		return
	}
	fmt.Println(`{"schema_version":1,"ok":true,"ready":true,"scenario":"controlled-battle-v8"}`)
	_, _ = io.Copy(io.Discard, os.Stdin)
	if mode == "hang" {
		time.Sleep(time.Minute)
	}
	if mode == "failure" {
		os.Exit(7)
	}
	// Simulate the writer's final durable commit, after EOF rather than before
	// the last frame. Killing the child before shutdown loses this marker.
	f, err := os.Create(marker)
	if err != nil {
		os.Exit(8)
	}
	_, err = f.WriteString("flushed\n")
	if err != nil || f.Sync() != nil || f.Close() != nil {
		os.Exit(9)
	}
	os.Exit(0)
}

func closeFixture(t *testing.T, ctx context.Context, mode string) (*Native, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "commit")
	n, err := Start(ctx, []string{exe, "-test.run=^TestNativeCloseHelper$", "--", "--close-helper=" + mode, "--marker=" + marker}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.abort(); _ = n.Close() })
	return n, marker
}

func TestNativeCloseWaitsForDurableExit(t *testing.T) {
	n, marker := closeFixture(t, context.Background(), "flush")
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := n.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if data, err := os.ReadFile(marker); err != nil || string(data) != "flushed\n" {
		t.Fatalf("Close returned before durable commit: %q, %v", data, err)
	}
	if _, err := n.exchange(context.Background(), "reset"); err == nil {
		t.Fatal("closed worker accepted another request")
	}
}

func TestNativeCloseReportsExitFailure(t *testing.T) {
	n, _ := closeFixture(t, context.Background(), "failure")
	err := n.Close()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("lost writer failure: %v", err)
	}
	if n.Close() != err {
		t.Fatal("repeated Close lost original error")
	}
}

func TestNativeCloseBoundsHungWriter(t *testing.T) {
	n, _ := closeFixture(t, context.Background(), "hang")
	start := time.Now()
	if err := n.closeWithin(100 * time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown timeout disappeared: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("shutdown did not terminate hung worker")
	}
	select {
	case <-n.done:
	default:
		t.Fatal("Close left worker alive")
	}
}

func TestNativeCloseAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n, _ := closeFixture(t, ctx, "hang")
	cancel()
	start := time.Now()
	if err := n.Close(); err == nil {
		t.Fatal("cancelled process claimed successful flush")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancelled worker waited for graceful deadline")
	}
}
