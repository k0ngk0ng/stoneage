//go:build !windows

package battletrain

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockTraining(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
