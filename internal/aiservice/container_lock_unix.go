//go:build linux || darwin

package aiservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// ensureContainerRunnerLock creates the persistent lock inode or validates
// the one already owned by the profile state directory. The inode is kept for
// the lifetime of the state root so a second process can safely flock it.
func ensureContainerRunnerLock(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !privateContainerRunnerLockInfo(info) {
			return fmt.Errorf("container runner lock is not a private regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := openContainerRunnerLock(path, true)
	if err != nil {
		return err
	}
	return file.Close()
}

// acquireContainerRunnerLock holds the advisory OS lock until the returned
// release function is called. Non-blocking flock attempts make lock waiting
// cancellable even though flock itself has no context-aware form.
func acquireContainerRunnerLock(ctx context.Context, path string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := openContainerRunnerLock(path, false)
	if err != nil {
		return nil, err
	}
	fd := int(file.Fd())
	closeWithoutLock := func() {
		_ = file.Close()
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() {
				_ = unix.Flock(fd, unix.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EACCES) {
			closeWithoutLock()
			return nil, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			closeWithoutLock()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func openContainerRunnerLock(path string, create bool) (*os.File, error) {
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if create {
		flags |= unix.O_CREAT
	}
	fd, err := unix.Open(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("cannot open container runner lock")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !privateContainerRunnerLockInfo(info) {
		_ = file.Close()
		return nil, errors.New("container runner lock is not a private regular file")
	}
	return file, nil
}

func privateContainerRunnerLockInfo(info os.FileInfo) bool {
	return info != nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular() && info.Mode().Perm()&0077 == 0
}
