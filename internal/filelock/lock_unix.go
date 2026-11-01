//go:build !windows

package filelock

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

// TryLock takes an exclusive lock on f without waiting. It reports false when
// another process holds it.
func TryLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) {
		return false, nil
	}
	return err == nil, err
}

// Unlock releases the lock.
func Unlock(f *os.File) { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
