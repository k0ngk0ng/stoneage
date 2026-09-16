//go:build linux || darwin

package aibroker

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryAcquireJournalLock creates or opens a private persistent inode and holds
// an advisory exclusive lock on it until release is called. The non-blocking
// operation makes a second broker fail during construction instead of opening
// a journal it cannot safely own.
func tryAcquireJournalLock(path string) (func(), error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("cannot open journal lock")
	}
	closeWithoutLock := func() { _ = file.Close() }
	if err := file.Chmod(0600); err != nil {
		closeWithoutLock()
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		closeWithoutLock()
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		closeWithoutLock()
		return nil, errors.New("journal lock is not a private regular file")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		closeWithoutLock()
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EACCES) {
			return nil, ErrJournalBusy
		}
		return nil, err
	}
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = file.Close()
	}, nil
}
