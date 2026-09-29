package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrDaemonRunning = errors.New("relevo daemon already running")

// DaemonLock is a held daemon lock. Closing it releases the lock; the kernel
// also drops it when the process exits, so there is no stale lock file to reap.
type DaemonLock struct {
	f *os.File
}

// AcquireDaemonLock takes the exclusive daemon lock, returning ErrDaemonRunning
// if another process holds it.
//
// It deliberately does not take s.mu: the daemon holds this lock for its whole
// lifetime and s.mu serialises WithLock, so holding both would deadlock every
// state operation the daemon makes.
func (s *Store) AcquireDaemonLock() (*DaemonLock, error) {
	f, err := s.openDaemonLockFile()
	if err != nil {
		return nil, err
	}

	locked, err := tryLockExclusive(f)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	if !locked {
		_ = f.Close()
		return nil, ErrDaemonRunning
	}

	return &DaemonLock{f: f}, nil
}

// DaemonRunning reports whether a daemon holds the lock, by trying to take it
// and releasing it immediately.
func (s *Store) DaemonRunning() (bool, error) {
	f, err := s.openDaemonLockFile()
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	locked, err := tryLockExclusive(f)
	if err != nil {
		return false, fmt.Errorf("lock %s: %w", f.Name(), err)
	}

	return !locked, nil
}

func (s *Store) openDaemonLockFile() (*os.File, error) {
	if err := os.MkdirAll(s.root, bindingDirMode); err != nil {
		return nil, fmt.Errorf("create state root: %w", err)
	}

	path := filepath.Join(s.root, daemonLockFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, bindingFileMode)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock: %w", err)
	}
	return f, nil
}

func (l *DaemonLock) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	if err := l.f.Close(); err != nil {
		return fmt.Errorf("close daemon lock: %w", err)
	}
	l.f = nil
	return nil
}
