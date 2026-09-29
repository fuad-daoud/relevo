package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

var ErrNotFound = errors.New("binding not found")

// ErrCWDTaken reports that another active binding already drives that working
// tree: two builders in one tree is the one failure that destroys work.
var ErrCWDTaken = errors.New("working tree already bound")

// ErrAmbiguousCWD reports several reader bindings on one working directory and
// no writer to prefer; the caller must name the binding it means.
var ErrAmbiguousCWD = errors.New("ambiguous working directory")

const (
	// MaxAgentNameLen is the cap ValidName enforces; no name relevo already
	// wrote becomes invalid.
	MaxAgentNameLen = 32
	bindingFileMode = 0o644
	bindingDirMode  = 0o755
	defaultRoundCap = 20
	// defaultRoundMSecs is the round budget: 24 hours. A builder working a real
	// stage runs for hours, so a short budget flags healthy work as needing a
	// human. A runaway guard, not a progress estimate; `relevo bind --timeout`
	// overrides it per binding.
	defaultRoundMSecs = 86400000
	lockFileName      = ".lock"
	// daemonLockFileName is separate from lockFileName because the daemon
	// holds it for its entire lifetime: sharing one would block every other
	// command forever.
	daemonLockFileName = ".daemon.lock"
	lockRetryDelay     = 50 * time.Millisecond

	// lockAcquireLimit must exceed the longest possible hold, or a slow
	// external call turns every other caller's wait into a failure. The longest
	// hold is Reconcile's critical section on the report-close path: two git
	// calls for round diff capture (10s + 10s) and one deliverer call (30s).
	// 90s is that worst case plus headroom; if client timeouts change, this
	// must change with them.
	lockAcquireLimit = 90 * time.Second
)

// Store is the state directory. All writes are atomic within it.
type Store struct {
	root string

	// owner is the scope every record call is filtered to: "" for the local
	// store, the enrolled client id for a shared one. A Store never writes a
	// row outside it.
	owner string

	// logCap is the binding log's entry cap; 0 means maxLogEntries. Only tests
	// set it.
	logCap int

	// shared is the machine-database handle a NewShared store borrows: the
	// caller owns its lifetime, and the store never closes it. Nil for a New
	// store, which opens <root>/relevo.db lazily.
	shared *db.DB

	mu sync.Mutex

	// The database at <root>/relevo.db holds the bindings, their logs and the
	// small kv records. It opens lazily on the first data-method call: path
	// helpers and WithLock alone never open it, and a shared store borrows
	// shared above.
	dbOnce sync.Once
	dbh    *db.DB
	dbErr  error
}

// Tx is a locked view of the store. Its methods assume the state lock is
// already held, and Tx never re-locks.
type Tx struct {
	s *Store
}

// New returns a Store rooted at root, scoped to the local owner "".
func New(root string) *Store {
	return &Store{root: root}
}

// NewShared returns a Store over root's bindings that reads and writes through
// d, the machine database the caller opened and owns; owner scopes every record
// call.
func NewShared(root, owner string, d *db.DB) *Store {
	return &Store{root: root, owner: owner, shared: d}
}

// maxLog is the binding log's entry cap: logCap when a test set one, else the
// package default.
func (s *Store) maxLog() int {
	if s.logCap > 0 {
		return s.logCap
	}
	return maxLogEntries
}

// DefaultRoot resolves $XDG_STATE_HOME/relevo, falling back to
// ~/.local/state/relevo.
func DefaultRoot() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "relevo"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	return filepath.Join(home, ".local", "state", "relevo"), nil
}

// ValidName enforces relevo's binding-name rule: a lowercase letter first,
// then up to 31 more of [a-z0-9_-].
func ValidName(name string) error {
	if name == "" {
		return errors.New("binding name is empty")
	}
	if len(name) > MaxAgentNameLen {
		return fmt.Errorf("binding name %q exceeds %d characters", name, MaxAgentNameLen)
	}
	if name[0] < 'a' || name[0] > 'z' {
		return fmt.Errorf("binding name %q must start with a lowercase letter", name)
	}

	for i := 1; i < len(name); i++ {
		c := name[i]
		lower := c >= 'a' && c <= 'z'
		digit := c >= '0' && c <= '9'
		if !lower && !digit && c != '-' && c != '_' {
			return fmt.Errorf("binding name %q has an invalid character %q", name, string(c))
		}
	}

	return nil
}

// WithLock runs fn while holding an exclusive advisory lock on the state root.
// Every load-modify-save sequence must run inside it.
//
// The lock is an flock, so the kernel drops it if a holder is killed and there
// is no stale lock file to reap. Acquisition is bounded, so a wedged holder
// surfaces as an error instead of hanging the caller forever. The in-process
// mutex serialises callers so they block on a cheap mutex rather than
// busy-polling the flock.
func (s *Store) WithLock(fn func(tx *Tx) error) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.root, bindingDirMode); err != nil {
		return fmt.Errorf("create state root: %w", err)
	}

	f, err := os.OpenFile(filepath.Join(s.root, lockFileName), os.O_CREATE|os.O_RDWR, bindingFileMode)
	if err != nil {
		return fmt.Errorf("open state lock: %w", err)
	}

	// Closing the descriptor releases the flock, so this defer is both the
	// unlock and the cleanup.
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close state lock: %w", cerr)
		}
	}()

	if err := acquireFlock(f, lockAcquireLimit); err != nil {
		return err
	}

	return fn(&Tx{s: s})
}

// acquireFlock polls for the exclusive lock until limit elapses; the
// platform-specific half is tryLockExclusive, in lock_unix.go.
func acquireFlock(f *os.File, limit time.Duration) error {
	deadline := time.Now().Add(limit)

	for {
		locked, err := tryLockExclusive(f)
		if err != nil {
			return fmt.Errorf("lock state root: %w", err)
		}
		if locked {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("state lock still held after %s", limit)
		}

		time.Sleep(lockRetryDelay)
	}
}
