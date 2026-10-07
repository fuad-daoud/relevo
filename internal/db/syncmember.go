//go:build !modernc

package db

// Membership: whether a file is one the sync engine has joined to a remote, and
// how a handle learns that without asking the file on every write.
//
// Membership is decided by the driver, not by this package: opening a file is
// what turns it into a member, and nothing on the driver's surface says so
// afterwards. The marker tables are the only evidence, and they are what the
// sync package already reads for its own role gate. The cache below is only
// that read's result: a handle that does not know asks once and remembers, and a
// file nothing in this tree has un-joins, so a record is never stale.

import (
	"context"
	"os"
	"sync"
)

// The sync driver's marker tables. A file carrying any one of them has been
// opened by the sync engine, which is what makes it a member rather than a bare
// database this package owns outright. They are a set rather than one name
// because which of them a driver version writes is the driver's decision.
var syncMarkerTables = []string{"turso_cdc", "turso_sync_last_change_id", "turso_named_syncs"}

// syncMembers records the paths this process knows to be members.
var syncMembers sync.Map

// MarkSyncMember records that path is a sync member.
//
// The sync package calls it once its member open has succeeded, which is the
// moment the driver has joined the file. Without it the handle that was opened
// before the enable would keep writing rows the push cannot see for as long as
// the process lives, and every push would still report success.
func MarkSyncMember(path string) {
	if path == "" {
		return
	}
	syncMembers.Store(path, struct{}{})
}

// IsSyncMember reports whether path carries the sync driver's marker tables.
//
// The read is a read-only open of the file's own schema and nothing else: it
// takes no handle a caller must release and joins no session, so a pool built
// from the answer has changed nothing about the file it read. A file that does
// not exist yet, and one whose schema cannot be read, both answer false rather
// than refusing -- a member is a statement about a file that is there, and every
// caller of this is about to open the file anyway.
func IsSyncMember(path string) (bool, error) {
	if path == "" {
		return false, nil
	}
	if _, ok := syncMembers.Load(path); ok {
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
