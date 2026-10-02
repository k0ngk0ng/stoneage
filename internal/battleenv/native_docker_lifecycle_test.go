package battleenv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Explicit integration gate: never pulls an image or touches unrelated
// containers. Each fixture has a cryptographically unique ownership label.
func TestNativeDockerShutdown(t *testing.T) {
	image := os.Getenv("STONEAGE_TEST_DOCKER_LIFECYCLE_IMAGE")
	if image == "" {
		t.Skip("explicit cached Docker image required")
	}
	control, err := exec.Command("docker", "run", "-d", "--rm", "--pull", "never", "--network", "none", "--read-only", image, "sleep", "300").Output()
	if err != nil {
		t.Fatal("start unrelated control fixture", err)
	}
	controlID := strings.TrimSpace(string(control))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", controlID).Run() })
	for _, mode := range []string{"graceful", "timeout", "cancel", "startup-cancel"} {
		t.Run(mode, func(t *testing.T) {
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				t.Fatal(err)
			}
			label := "stoneage.test.native-shutdown=" + hex.EncodeToString(random[:])
			list := func() []string {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				out, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label="+label).Output()
				if err != nil {
					t.Fatal("cannot inspect fixture", err)
				}
				return strings.Fields(string(out))
			}
			t.Cleanup(func() {
				for _, id := range list() {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					out, err := exec.CommandContext(ctx, "docker", "rm", "-f", id).CombinedOutput()
					cancel()
					if err != nil {
						t.Errorf("fixture cleanup: %v %s", err, out)
					}
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			greeting := `{"schema_version":1,"ok":true,"ready":true,"scenario":"controlled-battle-v8"}`
			script := `printf '%s\n' "$1"; exec sleep 300`
			if mode == "graceful" {
				script = `printf '%s\n' "$1"; exec cat >/dev/null`
			} else if mode == "startup-cancel" {
				script = `exec sleep 300`
			}
			command := []string{"docker", "run", "--rm", "-i", "--pull", "never", "--network", "none", "--read-only", "--label", label, image, "sh", "-c", script, "fixture", greeting}
			if mode == "startup-cancel" {
				started := make(chan error, 1)
				go func() {
					n, err := Start(ctx, command, io.Discard)
					if n != nil {
						_ = n.Close()
					}
					started <- err
				}()
				deadline := time.Now().Add(10 * time.Second)
				for len(list()) == 0 && time.Now().Before(deadline) {
					time.Sleep(20 * time.Millisecond)
				}
				if len(list()) != 1 {
					t.Fatal("startup fixture did not appear")
				}
				cancel()
				select {
				case err := <-started:
					if err == nil {
						t.Fatal("cancelled startup succeeded")
					}
				case <-time.After(8 * time.Second):
					t.Fatal("cancelled startup did not finish cleanup")
				}
			} else {
				n, err := Start(ctx, command, io.Discard)
				if err != nil {
					t.Fatal(err)
				}
				if len(list()) != 1 {
					t.Fatal("fixture not running before shutdown")
				}
				if mode == "cancel" {
					cancel()
				}
				if mode == "graceful" {
					err = n.Close()
				} else {
					err = n.closeWithin(150 * time.Millisecond)
				}
				if (err == nil) != (mode == "graceful") {
					t.Fatalf("unexpected shutdown result: %v", err)
				}
			}
			if remaining := list(); len(remaining) != 0 {
				t.Fatal(fmt.Sprintf("worker ended but its Docker container remains: %v", remaining))
			}
			out, err := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", controlID).Output()
			if err != nil || strings.TrimSpace(string(out)) != "true" {
				t.Fatal("shutdown affected unrelated control container", err)
			}
		})
	}
}
