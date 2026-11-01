package main

import (
	"log/slog"
	"net"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/installation"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// daemonStartup is a daemon start's order: the owner is served first, the
// one-time passes run after it, and the sync verb executor is installed last.
// Serving first bounds a client's wait, which the open-ended passes would
// otherwise leave unbounded. The passes skip their in-place compaction, which
// would close the pool the owner's clients read through. The verb comes last
// because the split and the origin backfill must finish before any row may
// leave this machine; a database whose split failed keeps refusing it instead
// of exporting the secrets the split exists to move.
func daemonStartup(rt *relevo.Runtime, d *db.DB, ln net.Listener, onAbandoned func()) *owner.Server {
	reclaimBeforeServing(d)
	srv := daemonServeOwner(d, ln, onAbandoned)

	if daemonStartupPasses(rt) {
		installSyncVerbHook(srv, d)
	}
	return srv
}

// reclaimBeforeServing returns the pages an earlier start's passes freed but
// could not compact on a served handle. This is the one moment the handle is
// not served yet; a failure is one warning and the start goes on.
func reclaimBeforeServing(d *db.DB) {
	files, err := reclaimFreePages(d)
	for _, f := range files {
		slog.Info("relevo daemon: reclaimed free pages", "path", f.Path,
			"bytes_before", f.BytesBefore, "bytes_after", f.BytesAfter)
	}
	if err != nil {
		slog.Warn("relevo daemon: free-page reclaim skipped", "err", err)
	}
}

// reclaimFreePages is a var so a test can pin that it runs on a handle the
// owner does not serve yet.
var reclaimFreePages = db.ReclaimFreePages

// daemonServeOwner starts the owner over d on ln; a listener or handle that
// cannot be served is one warning and no server, and the daemon still runs
// its passes over the handle it holds.
func daemonServeOwner(d *db.DB, ln net.Listener, onAbandoned func()) *owner.Server {
	srv, err := serveOwner(d, ln, onAbandoned)
	if err != nil {
		slog.Warn("relevo daemon: owner socket not served", "err", err)
	}
	return srv
}

// daemonStartupPasses is the one-time work a start owes the database, a var
// so a test can pin that the owner answers while a pass outlives the start
// wait. The passes are pinned by their own tests; only the order is wiring's.
var daemonStartupPasses = runStartupPasses

// runStartupPasses runs the passes in a start's order, dedupe first so the
// conversion runs on the smaller database it leaves. No failure stops the
// daemon: a failed pass is retried on the next start.
func runStartupPasses(rt *relevo.Runtime) bool {
	now := time.Now()
	backupDir := filepath.Dir(rt.Store.DBPath())

	if rt.DB != nil {
		stats, ran, derr := ingest.DedupeMirrorOnce(rt.DB, backupDir, now)
		if derr != nil {
			slog.Warn("relevo daemon: mirror dedupe skipped", "err", derr)
		} else if ran {
			slog.Info("relevo daemon: mirror dedupe",
				"done_at", stats.DoneAt,
				"mirror_bindings", stats.MirrorBindings,
				"unmapped", stats.Unmapped,
				"artifacts_deleted", stats.ArtifactsDeleted,
				"artifacts_kept", stats.ArtifactsKept,
				"transcript_rounds_deleted", stats.TranscriptRoundsDeleted,
				"transcript_rows_deleted", stats.TranscriptRowsDeleted,
				"transcript_rounds_kept", stats.TranscriptRoundsKept,
				"transcript_rounds_by_stream_lines", stats.TranscriptRoundsByStreamLines,
				"backup_path", stats.BackupPath,
				"vacuum_err", stats.VacuumErr)
		}

		cstats, cran, cerr := db.CompressHistoryOnce(rt.DB, backupDir, now)
		if cerr != nil {
			slog.Warn("relevo daemon: history compression skipped", "err", cerr)
		} else if cran {
			for _, ts := range cstats.Tables {
				slog.Info("relevo daemon: history compression",
					"table", ts.Table,
					"rows_compressed", ts.RowsCompressed,
					"columns_compressed", ts.ColumnsCompressed,
					"columns_kept_plain", ts.ColumnsKeptPlain,
					"bytes_in", ts.BytesIn,
					"bytes_out", ts.BytesOut)
			}
			slog.Info("relevo daemon: history compression done",
				"done_at", cstats.DoneAt,
				"backup_path", cstats.BackupPath,
				"checkpoint_err", cstats.CheckpointErr,
				"vacuum_err", cstats.VacuumErr)
		}
	}

	dropChangeCaptureTable(*rt)

	// The row other machines read to label this installation, and the two passes
	// behind it. It reports whether this machine's rows may leave, which is what
	// decides whether the sync verb is served at all.
	rowsMayLeave := false
	if rt.DB != nil {
		inst, ierr := installation.Load(backupDir)
		if ierr != nil {
			slog.Warn("relevo daemon: installation file unavailable", "err", ierr)
		} else {
			if terr := rt.DB.Tx(func(t *db.Tx) error {
				return t.InstallationTouch(inst.ID, inst.Label, time.Now())
			}); terr != nil {
				slog.Warn("relevo daemon: installation row skipped", "err", terr)
			}

			rowsMayLeave = daemonEnablePath(rt.DB, backupDir, inst.ID, time.Now())
		}
	}

	// A client key under a stale PEM label is rewritten once here; readers
	// accept either label, so the key stays readable throughout. The stored
	// label is the trigger, so the pass needs no once-row; a failure retries.
	if rt.DB != nil {
		if wrote, err := rt.Config.NormalizeClientKey(); err != nil {
			slog.Warn("relevo daemon: client key label not updated", "err", err)
		} else if wrote {
			slog.Info("relevo daemon: client key PEM label updated")
		}
	}

	return rowsMayLeave
}
