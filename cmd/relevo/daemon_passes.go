package main

import (
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// daemonEnablePath runs the two run-once passes a database must finish before
// its rows may leave this machine, and which the enable preflight names by name
// when it refuses. Both are idempotent, so a start that finds their markers
// already written logs nothing and takes no backup.
//
// The split runs first. It is the only pass here that moves rows and the only
// one that backs the file up, so a backup taken before anything has been stamped
// is a backup of the database a long-lived installation actually left behind. It
// moves every machine-local row out of the shared file into the machine-local
// file beside it -- the secret table above all, along with the pids, paths and
// offsets nothing else on another machine could read. That move is the whole of
// the fix the shared-secrets refusal names, so until this pass runs the refusal
// has nothing the user can do about it.
//
// The backfill runs second: it stamps this installation's id on every row
// written before the origin column existed, one table at a time, and records
// itself in kv last, only once nothing anywhere is left unstamped. So a start
// that halted partway leaves no marker and the next one resumes: it stamps only
// what is left and neither re-stamps nor skips what already moved. A stale row
// whose stamp would collide with a row this installation already stamped is
// settled before the table is stamped -- dropped for the live row on a mirror
// table, left for a person on the record table -- so one colliding pair no longer
// rolls back the rest. It is additive and needs no backup of its own.
//
// Neither failure stops the daemon. A machine with no database still runs, and a
// pass that failed is retried on the next start.
func daemonEnablePath(d *db.DB, backupDir, origin string, now time.Time) {
	sstats, sran, serr := db.SplitOnce(d, backupDir, now)
	if serr != nil {
		slog.Warn("relevo daemon: local split skipped", "err", serr)
	} else if sran {
		slog.Info("relevo daemon: local split",
			"done_at", sstats.DoneAt,
			"backup_path", sstats.BackupPath,
			"rows_moved", sstats.RowsMoved,
			"kv_keys_moved", sstats.KVKeysMoved)
	}

	// Both lines name the table and the counts, never a row's contents: a stale
	// twin is reported as how many rows in which table were dropped for the
	// stamped live row, and a halt as which table still holds how many. What a
	// row held is a working tree path or a record body, and neither belongs in a
	// journal that may be uploaded.
	bstats, bran, berr := db.BackfillOriginOnce(d, origin, now)
	if berr != nil {
		// A pass that stopped on rows it will not decide alone is not the same
		// event as a pass that had nothing to do, and one line for both is what
		// let a halt over referenced rows read as a pass that settled nothing:
		// stamped=0 and dropped=0 look alike whichever it was. So a pass that
		// halted gets its own line, naming the tables and how many rows in each
		// are waiting on a person, and the stall it really was keeps the skip
		// line it always had.
		if bstats.Halted() > 0 {
			slog.Warn("relevo daemon: origin backfill halted on rows a person must decide",
				"err", berr,
				"stamped", bstats.Stamped(),
				"stale_rows_dropped", bstats.Dropped(),
				"rows_halted", bstats.Halted(),
				"rows_halted_by_table", bstats.HaltedByTable(),
				"rows_left_unstamped", bstats.LeftUnstamped)
			return
		}
		slog.Warn("relevo daemon: origin backfill skipped", "err", berr,
			"stamped", bstats.Stamped(),
			"stale_rows_dropped", bstats.Dropped(),
			"rows_left_unstamped", bstats.LeftUnstamped)
	} else if bran {
		slog.Info("relevo daemon: origin backfill",
			"done_at", bstats.DoneAt,
			"origin", bstats.Origin,
			"binding_records", bstats.BindingRecords,
			"bindings", bstats.Bindings,
			"repos", bstats.Repos,
			"masterminds", bstats.Masterminds,
			"chains", bstats.Chains,
			"stale_rows_dropped", bstats.Dropped(),
			"twins", bstats.Twins)
	}
}
