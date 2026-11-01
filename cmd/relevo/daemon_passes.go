package main

import (
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// daemonEnablePath runs the two run-once passes a database must finish before
// its rows may leave, which the enable preflight names when it refuses. The
// split moves every machine-local row into the machine-local file, backing
// the file up first; the backfill stamps every unstamped row and records
// itself last, so a halted start resumes where it left off. A colliding pair
// is settled per table rather than rolling back the rest. Neither failure
// stops the daemon, and both record themselves only once finished, so an
// error keeps the sync verb uninstalled.
func daemonEnablePath(d *db.DB, backupDir, origin string, now time.Time) (rowsMayLeave bool) {
	sstats, sran, serr := db.SplitOnce(d, backupDir, now)
	if serr != nil {
		slog.Warn("relevo daemon: local split skipped", "err", serr)
	} else if sran {
		slog.Info("relevo daemon: local split",
			"done_at", sstats.DoneAt,
			"backup_path", sstats.BackupPath,
			"rows_moved", sstats.RowsMoved,
			"kv_keys_moved", sstats.KVKeysMoved,
			"conflicts", sstats.Conflicts)
		// A conflict names only which row kept its value and which backup holds
		// the stray: a conflicting row may be a secret, so never its contents.
		for _, name := range sstats.ConflictKeys {
			slog.Warn("relevo daemon: local split kept a local row over a shared stray",
				"row", name, "backup_path", sstats.BackupPath)
		}
		if perr := db.PruneSplitBackups(d, backupDir, sstats.BackupPath); perr != nil {
			slog.Warn("relevo daemon: pre-split backup prune", "err", perr)
		}
	}

	// Both lines name tables and counts, never a row's contents: a row holds a
	// working tree path or a record body, and neither belongs in a journal
	// that may be uploaded.
	bstats, bran, berr := db.BackfillOriginOnce(d, origin, now)
	if berr != nil {
		// A halted pass gets its own line: stamped=0 and dropped=0 read alike,
		// so one line for both would let a halt over referenced rows read as
		// a pass that settled nothing.
		if bstats.Halted() > 0 {
			slog.Warn("relevo daemon: origin backfill halted on rows a person must decide",
				"err", berr,
				"stamped", bstats.Stamped(),
				"stale_rows_dropped", bstats.Dropped(),
				"rows_halted", bstats.Halted(),
				"rows_halted_by_table", bstats.HaltedByTable(),
				"rows_left_unstamped", bstats.LeftUnstamped)
			return false
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
	// No error from either pass is the marker: both record themselves only after
	// they finished, so err nil here means the shared file holds nothing this
	// machine has not already classified, and the rows here may leave.
	return serr == nil && berr == nil
}
