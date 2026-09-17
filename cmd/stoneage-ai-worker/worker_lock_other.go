//go:build !linux && !darwin

package main

// Worker containers target Linux and local development supports Darwin. Do
// not silently fall back to a PID or process-local lock on other platforms.
func acquireWorkerLock(string) (func(), error) {
	return nil, ErrWorkerLockUnavailable
}
