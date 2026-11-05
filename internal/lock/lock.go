// Package lock provides advisory file locks between elan processes (the
// daemon and manual commands).
package lock

import (
	"errors"
	"os"
	"syscall"
)

// ErrHeld means another process holds the lock.
var ErrHeld = errors.New("em uso por outro processo do elan")

// Lock is a held file lock.
type Lock struct{ f *os.File }

// Try takes the lock at path without waiting. The lock is released by
// Release or when the process exits.
func Try(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, err
	}
	return &Lock{f: f}, nil
}

// Release frees the lock.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN) //nolint:errcheck // closing releases it anyway
	err := l.f.Close()
	l.f = nil
	return err
}
