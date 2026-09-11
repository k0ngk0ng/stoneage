package playerbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k0ngk0ng/stoneage/internal/playerdata"
)

type saacRequestHandler func(id string, request []byte) ([]byte, error)

func newTestQueue(t *testing.T) Queue {
	t.Helper()
	root := t.TempDir()
	requests := filepath.Join(root, "requests")
	responses := filepath.Join(root, "responses")
	if err := os.MkdirAll(requests, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(responses, 0o700); err != nil {
		t.Fatal(err)
	}
	return Queue{Requests: requests, Responses: responses, Timeout: time.Second}
}

// startFakeSAAC models the main loop's one-request, atomic response exchange.
// It deliberately only observes *.req files, so temporary request files cannot
// be consumed before Queue.Exchange renames them into place.
func startFakeSAAC(t *testing.T, q Queue, handler saacRequestHandler) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	go func() {
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			entries, err := os.ReadDir(q.Requests)
			if err != nil {
				done <- fmt.Errorf("read request directory: %w", err)
				return
			}
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".req") {
					continue
				}
				id := strings.TrimSuffix(entry.Name(), ".req")
				requestPath := filepath.Join(q.Requests, entry.Name())
				request, err := os.ReadFile(requestPath)
				if err != nil {
					if errors.Is(err, os.ErrNotExist) {
						continue
					}
					done <- fmt.Errorf("read request: %w", err)
					return
				}
				response, err := handler(id, request)
				if err != nil {
					done <- err
					return
				}
				if err := publishTestResponse(q.Responses, id, response); err != nil {
					done <- err
					return
				}
				if err := os.Remove(requestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
					done <- fmt.Errorf("remove request: %w", err)
					return
				}
				done <- nil
				return
			}

			select {
			case <-ctx.Done():
				done <- ctx.Err()
				return
			case <-deadline.C:
				done <- fmt.Errorf("timed out waiting for request")
				return
			case <-ticker.C:
			}
		}
	}()
	return done
}

func publishTestResponse(directory, id string, response []byte) error {
	tmp, err := os.CreateTemp(directory, ".response-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(response); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(directory, id+".resp"))
}

func waitForFakeSAAC(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fake SAAC did not consume the request")
	}
}

func parseTestRequest(id string, request []byte) (map[string]string, []byte, error) {
	header, payload, ok := bytes.Cut(request, []byte("---\n"))
	if !ok {
		return nil, nil, fmt.Errorf("request has no delimiter")
	}
	fields, err := parseLines(header, false)
	if err != nil {
		return nil, nil, err
	}
	if fields["version"] != "1" || fields["id"] != id || !validTestID(id) {
		return nil, nil, fmt.Errorf("invalid request identity")
	}
	return fields, payload, nil
}

func validTestID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func buildTestResponse(id, status, op, account, code string, slot, length int, includeSlot, includeLength bool, payload []byte) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "version=1\nid=%s\nstatus=%s\nop=%s\naccount=%s\n", id, status, op, account)
	if code != "" {
		fmt.Fprintf(&out, "code=%s\n", code)
	}
	if includeSlot {
		fmt.Fprintf(&out, "slot=%d\n", slot)
	}
	if includeLength {
		fmt.Fprintf(&out, "length=%d\n", length)
	}
	out.WriteString("---\n")
	out.Write(payload)
	return out.Bytes()
}

func TestQueueExchangeDoesNotCreateMissingRequestDirectory(t *testing.T) {
	root := t.TempDir()
	q := Queue{
		Requests:  filepath.Join(root, "missing-requests"),
		Responses: filepath.Join(root, "responses"),
		Timeout:   100 * time.Millisecond,
	}
	if err := os.Mkdir(q.Responses, 0o700); err != nil {
		t.Fatal(err)
	}

	_, _, err := q.Exchange(context.Background(), func(string) ([]byte, error) {
		return []byte("request"), nil
	})
	if !errors.Is(err, playerdata.ErrUnavailable) {
		t.Fatalf("Exchange error = %v, want ErrUnavailable", err)
	}
	if _, err := os.Stat(q.Requests); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing request directory was created or had unexpected error: %v", err)
	}
}

func TestQueueExchangeTimesOutAndRemovesRequest(t *testing.T) {
	q := newTestQueue(t)
	q.Timeout = 60 * time.Millisecond

	_, _, err := q.Exchange(context.Background(), func(string) ([]byte, error) {
		return []byte("request"), nil
	})
	if !errors.Is(err, playerdata.ErrUnavailable) {
		t.Fatalf("Exchange error = %v, want ErrUnavailable", err)
	}
	entries, err := os.ReadDir(q.Requests)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".req") {
			t.Fatalf("timed out request was left behind: %s", entry.Name())
		}
	}
}
