//go:build linux || darwin

package aiprovision

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"sync"
)

// The private shared secret directory belongs to one provisioning authority.
// Keep this inode: unlinking it would split concurrent owners across locks.
func (p *Provisioner) lockInitialPublication() (func(), error) {
	path := filepath.Join(p.config.Secrets.root, ".provision.lock")
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		file.Close()
		return nil, fmt.Errorf("%w: invalid provisioning lock", ErrInvalidConfig)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("%w: provisioning is busy", ErrBindingConflict)
	}
	var once sync.Once
	return func() { once.Do(func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = file.Close() }) }, nil
}
