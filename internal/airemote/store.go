package airemote

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// MemoryStore is useful for tests and for callers that already own durable
// lifecycle state. Hub never stores credentials or payloads in it.
type MemoryStore struct {
	mu       sync.Mutex
	snapshot Snapshot
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }
func (s *MemoryStore) Load(context.Context) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSnapshot(s.snapshot), nil
}
func (s *MemoryStore) Save(_ context.Context, snapshot Snapshot) error {
	s.mu.Lock()
	s.snapshot = cloneSnapshot(snapshot)
	s.mu.Unlock()
	return nil
}

// FileStore provides a small atomic JSON journal for worker routing. It is
// intentionally separate from aibroker's request journal: it never receives a
// model request, API key, game token or runtime output.
type FileStore struct {
	path string
	mu   sync.Mutex
}

func NewFileStore(path string) *FileStore { return &FileStore{path: path} }
func (s *FileStore) Load(_ context.Context) (Snapshot, error) {
	if s == nil || s.path == "" {
		return Snapshot{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if len(raw) > 4<<20 {
		return Snapshot{}, errors.New("airemote: worker state is too large")
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return Snapshot{}, err
	}
	return cloneSnapshot(snapshot), nil
}
func (s *FileStore) Save(_ context.Context, snapshot Snapshot) error {
	if s == nil || s.path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".worker-state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Routes = append([]RouteRecord(nil), snapshot.Routes...)
	snapshot.Profiles = append([]ProfileRecord(nil), snapshot.Profiles...)
	return snapshot
}
