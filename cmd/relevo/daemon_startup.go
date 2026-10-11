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
//
// Serving first is what the pass order used to cost. The listener was bound
// early but nothing accepted on it until every pass had finished, so a client
// that connected in the meantime met a socket with a backlog and no server: it
// spent its whole start wait on an owner that was never given the chance to
// answer, and then failed. On a large database the passes are open-ended, so the
// window was open-ended too, and a plain read -- which needs no write lock and
// would succeed against a serving owner -- failed anyway. Served first, that same
// client is answered the moment it connects and its statement queues behind the
// pass's own transaction on the one shared pool, under the same busy mapping and
// begin-retry every other write already gets.
//
// The passes run unchanged underneath that: the same rows move, in the same
// order, out of the same place. What changes is what they can no longer do --
// the in-place compaction each one wanted would close the pool the owner's
// clients are reading through, so each one skips it and says so, and the freed
// pages stay on the file's free list for the engine to reuse.
//
// The verb executor is installed after the passes and not with the serve, and
// it is the last thing here for a reason: a verb is the one request that pushes
// rows off this machine, and the split and the origin backfill are the passes
// that must finish before any row may leave it. A verb that arrives while they
// run is refused with the owner's own no-executor code, which a client of any
// version already knows how to read, rather than answered from a database whose
// secrets are still in the shared file. It is installed only once both passes
// recorded themselves, so a database whose split failed keeps refusing it and
// says why, instead of exporting the very rows the split exists to move.
//
// Nothing else here starts a push. The daemon's own tick, the daemon it builds
// later, and the verb hook that daemon installs are all further down the start,
// and the sync worker is a child no verb and no tick has driven yet.
func daemonStartup(rt *relevo.Runtime, d *db.DB, ln net.Listener, onAbandoned func()) *owner.Server {
	srv := daemonServeOwner(d, ln, onAbandoned)

	if daemonStartupPasses(rt) {
		installSyncVerbHook(srv, d)
	}
	return srv
}

// daemonServeOwner starts the owner over d on ln. A listener or a handle that
// cannot be served is one warning and no server: the daemon keeps running its
// passes over the handle it holds, and a client that finds no socket is the same
// dead end it found before the passes ran.
func daemonServeOwner(d *db.DB, ln net.Listener, onAbandoned func()) *owner.Server {
	srv, err := serveOwner(d, ln, onAbandoned)
	if err != nil {
		slog.Warn("relevo daemon: owner socket not served", "err", err)
	}
	return srv
}

// daemonStartupPasses is the one-time work a start owes the database, and it is
// a var so a test can stand in a pass that outlives the start wait and pin that
// the owner is already answering while it runs. The passes themselves are pinned
// by their own tests; what only the wiring can get wrong is the order.
var daemonStartupPasses = runStartupPasses

// runStartupPasses runs the passes in the order a start has always run them.
//
// The ingest mirror's proven duplicates go first, then the conversion of the
// bulk history columns, so the conversion and its backup run on the smaller
// database the dedupe left. Then the dead change-capture table. Then the
// installation's own row, and behind it the local split and the origin
// backfill -- the two passes the enable preflight names when it refuses, so
// running them here is what gives a refusal its fix.
//
// Not one failure stops the daemon: a machine with no database still runs, and a
// pass that failed is retried on the next start.
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

	// A client key stored under the pre-rename PEM label is rewritten once here,
	// so every later reader sees the current label. The reader accepts either
	// label, so no remote path refuses the key in the meantime; the trigger is
	// the stored label itself, so the pass is self-limiting and needs no kv
	// once-row. A failure never stops the daemon, and the key stays readable, so
	// the next start retries.
	if rt.DB != nil {
		if wrote, err := rt.Config.NormalizeClientKey(); err != nil {
			slog.Warn("relevo daemon: client key label not updated", "err", err)
		} else if wrote {
			slog.Info("relevo daemon: client key PEM label updated")
		}
	}

	return rowsMayLeave
}
