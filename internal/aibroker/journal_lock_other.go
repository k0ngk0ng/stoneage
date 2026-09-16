//go:build !linux && !darwin

package aibroker

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Unsupported platforms retain the private lock inode and process-local
// exclusion used by the other project lock adapters. Production containers
// run on Linux; the fallback keeps package users buildable elsewhere.
var journalFallbackLocks sync.Map // canonical path -> *sync.Mutex

func tryAcquireJournalLock(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		_ = file.Close()
		return nil, errors.New("journal lock is not a private regular file")
	}
	canonical, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	value, _ := journalFallbackLocks.LoadOrStore(canonical, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	if !lock.TryLock() {
		_ = file.Close()
		return nil, ErrJournalBusy
	}
	return func() {
		lock.Unlock()
		_ = file.Close()
	}, nil
}
