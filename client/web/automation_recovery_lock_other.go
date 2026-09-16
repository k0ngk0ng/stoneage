//go:build !linux && !darwin

package main

import "errors"

// Other platforms fail closed because this process has no reviewed database
// locking implementation there. In-memory stores have no shared inode.
func acquireAutomationRecoveryLock(path string) (func(), error) {
	if path == ":memory:" {
		return func() {}, nil
	}
	return nil, errors.New("automation recovery locking is unavailable on this platform")
}
