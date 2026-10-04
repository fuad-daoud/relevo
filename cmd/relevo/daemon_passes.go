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
// written before the origin column existed, and records itself in kv last
// inside its own transaction, so a failure anywhere before that marker leaves
// the next start retrying from the top. It is additive and needs no backup of
// its own.
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

	bstats, bran, berr := db.BackfillOriginOnce(d, origin, now)
	if berr != nil {
		slog.Warn("relevo daemon: origin backfill skipped", "err", berr)
	} else if bran {
		slog.Info("relevo daemon: origin backfill",
			"done_at", bstats.DoneAt,
			"origin", bstats.Origin,
			"binding_records", bstats.BindingRecords,
			"bindings", bstats.Bindings,
			"repos", bstats.Repos,
			"masterminds", bstats.Masterminds,
			"chains", bstats.Chains)
	}
}
