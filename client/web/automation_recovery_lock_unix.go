//go:build linux || darwin

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

const automationRecoveryLockSuffix = ".web-recovery.lock"

// acquireAutomationRecoveryLock holds an exclusive, nonblocking lock on a
// persistent sidecar next to the automation database. SQLite uses its own
// locks on the database and WAL files, so locking a separate inode avoids
// interfering with an already-open store.
func acquireAutomationRecoveryLock(path string) (func(), error) {
	if path == ":memory:" {
		return func() {}, nil
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve automation database for recovery lock: %w", err)
	}
	info, err := os.Lstat(absPath)
	if err != nil {
		return nil, fmt.Errorf("stat automation database for recovery lock: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("automation database for recovery lock must not be a symlink")
	}
	if err := validateAutomationRecoveryFile(info, "automation database"); err != nil {
		return nil, err
	}

	canonicalDir, err := filepath.EvalSymlinks(filepath.Dir(absPath))
	if err != nil {
		return nil, fmt.Errorf("resolve automation database directory for recovery lock: %w", err)
	}
	canonicalPath := filepath.Join(canonicalDir, filepath.Base(absPath))
	// Re-check the canonical path after resolving the directory. This keeps
	// validation tied to the path whose sidecar will actually be locked.
	info, err = os.Lstat(canonicalPath)
	if err != nil {
		return nil, fmt.Errorf("stat canonical automation database for recovery lock: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("automation database for recovery lock must not be a symlink")
	}
	if err := validateAutomationRecoveryFile(info, "automation database"); err != nil {
		return nil, err
	}

	lockPath := canonicalPath + automationRecoveryLockSuffix
	if info, err := os.Lstat(lockPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("automation recovery lock must not be a symlink")
		}
		if err := validateAutomationRecoveryFile(info, "automation recovery lock"); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat automation recovery lock: %w", err)
	}

	fd, err := unix.Open(lockPath, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("open automation recovery lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), lockPath)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open automation recovery lock: invalid file descriptor")
	}
	info, err = file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("stat automation recovery lock: %w", err)
	}
	if err := validateAutomationRecoveryFile(info, "automation recovery lock"); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire automation recovery lock: %w", err)
	}

	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			_ = unix.Flock(fd, unix.LOCK_UN)
			_ = file.Close()
		})
	}
	return release, nil
}

func validateAutomationRecoveryFile(info os.FileInfo, name string) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s for recovery lock must be a regular file", name)
	}
	if info.Mode().Perm() != 0600 {
		return fmt.Errorf("%s for recovery lock must have mode 0600", name)
	}
	return nil
}
