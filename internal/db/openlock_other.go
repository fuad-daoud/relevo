//go:build !unix

package db

// openLock is a no-op off unix: there is no flock, and a platform without one
// is a build-only target with no owner socket to reach the database through.
type openLock struct{}

// acquireOpenLock does nothing off unix.
func acquireOpenLock(string, bool) (*openLock, error) { return nil, nil }

// release does nothing off unix.
func (l *openLock) release() {}
