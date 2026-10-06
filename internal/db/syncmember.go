package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
)

// The sync driver's marker tables. A file carrying any one of them has been
// opened by the sync engine, which is what makes it a member rather than a bare
// database this package owns outright.
//
// They are a set rather than one name because which of them a driver version
// writes is the driver's decision, not this package's.
var syncMarkerTables = []string{"turso_cdc", "turso_sync_last_change_id", "turso_named_syncs"}

// captureChangesPragma is the change-data-capture pragma, and it is the whole of
// why a member's rows reach the remote.
//
// The sync engine sets it on each connection it opens, because the CDC table it
// fills is written by the connection that made the change: turso captures into
// turso_cdc per connection, so a connection without the pragma writes rows that
// no later push can send. A pool this package opens is a second set of
// connections over the same file, so a member's writes are invisible to the
// engine unless this pool sets the pragma too.
//
// A file that is not a member never gets it: the pragma creates turso_cdc, and
// that table is one of the markers above, so setting it on a bare file would
// turn this package's own database into something that looks joined to a remote.
const captureChangesPragma = "PRAGMA capture_data_changes_conn('full,turso_cdc')"

// syncMembers records the paths known to be sync members, so the per-connection
// question below is answered without reopening the file each time.
//
// It is a cache and not the answer, because a file becomes a member after this
// package has already opened it: the sync engine is what turns a bare file into
// a member, and it does that through an open of its own. A record is therefore
// only ever added, never removed -- nothing in this tree un-joins a file -- and
// the open itself writes the record it learns.
var syncMembers sync.Map

// memberPools holds the live writable pools over each path, so a pool that was
// open before the membership existed can be made to open fresh connections.
//
// The pools are what make MarkSyncMember more than a note: a connection is given
// the capture pragma when it is created, and the daemon's pool was created at
// start-up, before any enable. Retiring its idle connections is what makes the
// next connection the first one that captures.
var (
	memberPoolsMu sync.Mutex
	memberPools   = map[string][]*sql.DB{}
)

// registerMemberPool records a writable pool over path so MarkSyncMember can
// reach it. It is called from the open that builds the pool, which is the only
// place that knows a pool is writable and which file it is over.
func registerMemberPool(path string, pool *sql.DB) {
	if path == "" || pool == nil {
		return
	}
	memberPoolsMu.Lock()
	defer memberPoolsMu.Unlock()
	memberPools[path] = append(memberPools[path], pool)
}

// retirePool makes every later use of pool run on a connection built after now,
// by dropping the limit on retained idle connections so each is closed as it
// comes back rather than kept for reuse.
//
// The connections already checked out are left alone: closing one under its
// caller would fail that call, and it is returned to a pool that no longer
// retains connections, so it is closed then. No query in this tree can be left
// running on a connection that will outlive the retirement.
func retirePool(pool *sql.DB) {
	pool.SetMaxIdleConns(0)
}

// MarkSyncMember records that path is a sync member, and makes the pools this
// process already holds over it capture from their next connection on.
//
// The enable calls it once its member open has succeeded. Without it the pool
// that was opened at start-up would keep writing rows the push cannot see for as
// long as the process lives, and every push would still report success.
func MarkSyncMember(path string) {
	if path == "" {
		return
	}
	syncMembers.Store(path, struct{}{})

	memberPoolsMu.Lock()
	pools := memberPools[path]
	delete(memberPools, path)
	memberPoolsMu.Unlock()
	for _, pool := range pools {
		retirePool(pool)
	}
}

// isSyncMember reports whether path is already known to be a sync member.
//
// This is the form the per-connection path asks, because it must not open the
// file: a pool asking about itself would open a second pool over the same
// database on every connection it creates. A file this has not been told about
// and has not read is answered false, which is the safe direction -- the pragma
// is not set, and nothing is captured.
func isSyncMember(path string) bool {
	if path == "" {
		return false
	}
	_, ok := syncMembers.Load(path)
	return ok
}

// IsSyncMember reports whether path carries the sync driver's marker tables.
//
// The read is a read-only open of the file's own schema and nothing else: it
// takes no handle a caller must release and joins no session, so a pool built
// from the answer has changed nothing about the file it read. A file that does
// not exist yet, and one whose schema cannot be read, both answer false rather
// than refusing, because every caller of this is about to open the file anyway
// and a member is a statement about a file that is there.
//
// A file it finds to be a member is recorded, which is what lets the pool that
// is opening it capture from its first connection.
func IsSyncMember(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	if isSyncMember(path) {
		return true, nil
	}
	if _, err := os.Stat(path); err != nil {
		return false, nil
	}
	raw, err := OpenRawReadOnly(path)
	if err != nil {
		return false, nil
	}
	defer func() { _ = raw.Close() }()

	query := `SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name IN (` +
		markerPlaceholders(len(syncMarkerTables)) + `)`
	var found int
	if err := raw.QueryRowContext(context.Background(), query, markerArgs()...).Scan(&found); err != nil {
		return false, nil
	}
	if found > 0 {
		MarkSyncMember(path)
	}
	return found > 0, nil
}

// unregisterMemberPool forgets a pool whose handle is closing, so a pool that is
// gone is not retired later and a leak cannot outlive the file it names.
func unregisterMemberPool(path string, pool *sql.DB) {
	if path == "" || pool == nil {
		return
	}
	memberPoolsMu.Lock()
	defer memberPoolsMu.Unlock()
	pools := memberPools[path]
	for i, p := range pools {
		if p == pool {
			memberPools[path] = append(pools[:i], pools[i+1:]...)
			break
		}
	}
	if len(memberPools[path]) == 0 {
		delete(memberPools, path)
	}
}

// requireWritableMember is the refusal a WAL-rewriting call makes on a member.
//
// Truncating the log behind the sync engine's back is what breaks a pull. The
// engine holds a watermark naming the last frame it transferred into the revert
// database; a truncate over a plain connection resets the log, and the next pull
// then asks the engine to checkpoint up to a frame the log no longer has, which
// the engine refuses rather than guess at. So the caller is stopped here, where
// the reason can be named, rather than at a later pull with a driver message
// that names neither the cause nor the file.
func (d *DB) requireWritableMember(op string) error {
	if d.path == "" || d.readOnly {
		return nil
	}
	if isSyncMember(d.path) {
		return fmt.Errorf("db: %s: %s is a sync member, so its write-ahead log belongs to the sync engine: %w",
			op, d.path, ErrInvalid)
	}
	return nil
}

// markerPlaceholders builds the "?, ?, ?" a fixed-length IN list needs.
func markerPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, n*3-1)
	for i := 0; i < n; i++ {
		if i > 0 {
			out = append(out, ',', ' ')
		}
		out = append(out, '?')
	}
	return string(out)
}

// markerArgs is the marker table names in the order markerPlaceholders counts
// them, so the two cannot disagree about which placeholder is which name.
func markerArgs() []any {
	args := make([]any, len(syncMarkerTables))
	for i, name := range syncMarkerTables {
		args[i] = name
	}
	return args
}
