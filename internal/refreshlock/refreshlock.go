// Package refreshlock is the single place that names and takes
// <StateDir>/refresh.lock, shared by `refresh` and the SessionEnd hook probe.
package refreshlock

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const fileName = "refresh.lock"

// TryAcquire takes a non-blocking exclusive flock. ok is false when another
// process holds it (or the lock file cannot be opened). Call release when done.
func TryAcquire(stateDir string) (release func(), ok bool) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return func() {}, false
	}
	f, err := os.OpenFile(filepath.Join(stateDir, fileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, false
	}
	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		f.Close()
		return func() {}, false
	}
	return func() { unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close() }, true
}

// Held reports whether the lock is currently held by someone else. If the
// lock file cannot be opened it reports false (do not block the caller).
func Held(stateDir string) bool {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return false
	}
	f, err := os.OpenFile(filepath.Join(stateDir, fileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	if unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return true
	}
	unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return false
}
