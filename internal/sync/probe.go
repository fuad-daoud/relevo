package sync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/db"
)

// ErrNotSynced reports an open that named a file the sync driver has never
// joined. The driver's own surface offers no way to ask whether a file is
// already a member, so an open that named a bare file had no way to notice it
// was about to turn that file into a sync member -- and against a remote
// holding nothing, the first pull then applied remote emptiness over the local
// file. The marker tables are what say a file is already a member, so this
// refusal is made before the driver is reached rather than after.
var ErrNotSynced = errors.New("sync: this file is not a member of a sync yet")

// syncMarkerTables are the tables the driver creates in a file it has joined to
// a remote. They are read rather than assumed because the driver's own surface
// has no membership query: a table that exists is the only evidence a previous
// open made this file a sync member, and its absence is the evidence it did
// not.
//
// They are listed as a set rather than a single name because which of them a
// driver version writes is not this package's to decide, and a file carrying
// any one of them has been opened by a sync driver.
var syncMarkerTables = []string{"turso_cdc", "turso_sync_last_change_id", "turso_named_syncs"}

// Throwaway is one empty file a probe may name instead of a live database.
//
// It exists because a question asked of a remote cannot be asked through a
// live file. The driver's only surface for "does the remote hold anything" is a
// pull, and a pull rewrites the file it was given: pointed at a live database
// holding this machine's history and a remote holding nothing, it applied the
// remote's emptiness over the history. So a probe names a file of its own,
// takes the answer there, and leaves the live file unopened.
type Throwaway struct {
	// Path is the file the probe may name. It is inside a directory only this
	// user reaches, and it is empty: nothing a caller wants to keep is in it.
	Path string

	dir      string
	released bool
}

// NewThrowaway creates the empty file a probe names, in a directory only this
// user reaches beside the live file. Beside rather than in a system temp
// directory so the driver's own scratch lands on the same filesystem the live
// file is on, which is what keeps a probe from behaving differently from a real
// open because of where it ran.
//
// The returned Throwaway removes the file and its directory on Release, and
// Release is safe to call more than once and on a nil Throwaway: a probe that
// returns an error part way through must still be able to clean up.
func NewThrowaway(beside string) (*Throwaway, error) {
	if beside == "" {
		return nil, errors.New("sync: throwaway: no path to sit beside")
	}
	dir, err := os.MkdirTemp(filepath.Dir(beside), ".relevo-probe-")
	if err != nil {
		return nil, fmt.Errorf("sync: throwaway: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("sync: throwaway: %w", err)
	}
	path := filepath.Join(dir, "probe.db")
	// The file is created rather than left to the driver so an absent file is
	// never the thing a probe discovers, and closed again immediately so
	// nothing of this process's is held against the file it hands over.
	if err := writeEmptyFile(path); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("sync: throwaway: %w", err)
	}
	return &Throwaway{Path: path, dir: dir}, nil
}

// writeEmptyFile creates path empty and owner-only, then closes it.
func writeEmptyFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// Release removes the throwaway file and the directory holding it. It is safe
// to call more than once, so a probe's cleanup can sit on every path out of the
// probe rather than only the one that succeeded.
func (t *Throwaway) Release() {
	if t == nil || t.released {
		return
	}
	t.released = true
	_ = os.RemoveAll(t.dir)
}

// HasSyncMarker reports whether path already carries the sync driver's marker
// tables, which is what makes it a member rather than a bare file.
//
// The read is a read-only open of the file's own schema and nothing else. It
// takes no lock the caller must release and joins no session, because the
// question is about the file on disk rather than about a handle: an open that
// was refused here has changed nothing, so the file it declined to touch is
// left exactly as it was found.
func HasSyncMarker(path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("sync: sync marker: %w", ErrNotSynced)
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("sync: sync marker: %s: %w", path, err)
	}
	raw, err := db.OpenRawReadOnly(path)
	if err != nil {
		return false, fmt.Errorf("sync: sync marker: %s: %w", path, err)
	}
	defer func() { _ = raw.Close() }()

	query := `SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name IN (` +
		sqlPlaceholders(len(syncMarkerTables)) + `)`
	var found int
	if err := raw.QueryRowContext(context.Background(), query, markerArgs()...).Scan(&found); err != nil {
		// A file whose schema this build cannot read is not a file this
		// package will hand to a driver either, so the answer is the refusal
		// rather than a guess that it is safe.
		return false, fmt.Errorf("sync: sync marker: %s: %w", path, err)
	}
	return found > 0, nil
}

// RequireSyncMember refuses an open that named a file the driver has not joined.
//
// It is the check a live-file open goes through, and it exists because the
// driver cannot make the same promise: opening a bare file is what turns it
// into a member, and nothing on the driver's surface says that happened. So the
// marker is read first, here, and an open that would have created membership
// where none existed refuses instead.
func RequireSyncMember(path string) error {
	ok, err := HasSyncMarker(path)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotSynced, path)
	}
	return nil
}

// sqlPlaceholders builds the "?, ?, ?" a fixed-length IN list needs.
func sqlPlaceholders(n int) string {
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

// markerArgs is the marker table names in the order sqlPlaceholders counts
// them, so the two cannot disagree about which placeholder is which name.
func markerArgs() []any {
	args := make([]any, len(syncMarkerTables))
	for i, name := range syncMarkerTables {
		args[i] = name
	}
	return args
}
