//go:build !linux && !darwin

package aiservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Other platforms retain the same persistent inode and cancellable in-process
// behavior. Production container runners target Linux/Darwin, where the Unix
// implementation above adds the required cross-process flock.
var containerRunnerFallbackLocks sync.Map // canonical path -> chan struct{}

func ensureContainerRunnerLock(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !privateContainerRunnerLockInfo(info) {
		if err != nil {
			return err
		}
		return errors.New("container runner lock is not a private regular file")
	}
	return nil
}

func acquireContainerRunnerLock(ctx context.Context, path string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	canonical, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	value, _ := containerRunnerFallbackLocks.LoadOrStore(canonical, make(chan struct{}, 1))
	lock := value.(chan struct{})
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func privateContainerRunnerLockInfo(info os.FileInfo) bool {
	return info != nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0
}
