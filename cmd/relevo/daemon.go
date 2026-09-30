package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/installation"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/upgrade"
)

// daemonFlagValues holds the pointers daemon parses into.
type daemonFlagValues struct {
	interval  *time.Duration
	check     *bool
	preflight *bool
}

// daemonFlagSet defines those flags on fs and returns what they parse into.
func daemonFlagSet(fs *flag.FlagSet) *daemonFlagValues {
	v := &daemonFlagValues{}
	v.interval = fs.Duration("interval", 2*time.Second, "poll interval")
	v.check = fs.Bool("check", false, "exit 0 if a daemon is running, 1 if not; print nothing")
	// --preflight is internal: the daemon runs a candidate binary's own
	// --preflight before re-exec'ing into it (#371 §4.4). It stays out of
	// the usage text and the README, so it is defined but not printed.
	v.preflight = fs.Bool("preflight", false, "validate the runtime configuration and exit (internal)")
	return v
}

func cmdDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	v := daemonFlagSet(fs)
	interval, check, preflight := v.interval, v.check, v.preflight
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: relevo daemon [--interval D] [--check]")
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// --preflight and --check are the read-only peek. §4.6: neither writes;
	// each reads config files and a database read-only and opens nothing that
	// migrates. They return here, before the daemon path resolves a root,
	// takes the lock or opens the database.
	if *preflight || *check {
		rt, err := newRuntimePeek()
		if err != nil {
			if *preflight {
				// §4.4: the error on stderr, exit 1. Returning rather than
				// os.Exit keeps the path a plain function call a test can make.
				fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
				return exitCodeErr{code: 1}
			}
			return err
		}
		if *preflight {
			// newRuntimePeek read and validated config only: it took no lock,
			// opened no DB (which would migrate), started no process and made
			// no network call, and this returns before the DB open below.
			fmt.Printf("ok %s\n", buildVersion())
			return nil
		}

		// --check is the plugin startup hook's probe. It prints nothing on
		// either path: the exit status is the whole answer, and a hook that
		// printed would only fill a log with noise on every server start.
		// Returning exitCodeErr rather than calling os.Exit keeps the
		// no-db-yet ordering a test can call (#372 §4.5): main maps the code
		// to the same exit status.
		running, err := rt.Store.DaemonRunning()
		if err != nil {
			return err
		}
		if !running {
			return exitCodeErr{code: 1}
		}
		return nil
	}

	// The daemon path: the read-only config load runs first, exactly as
	// --preflight's does, so a malformed config fails before the lock and the
	// root stays read-only until then. The loaded value is discarded --
	// newRuntime reloads config from the database it imports into.
	root, err := store.DefaultRoot()
	if err != nil {
		return err
	}
	configDir, err := userConfigRoot()
	if err != nil {
		return err
	}
	configDirPath := filepath.Join(configDir, "relevo")
	if _, err := loadConfigReadOnly(root, configDirPath); err != nil {
		return err
	}

	// The lock is the first effect of a daemon start: a daemon that loses it
	// exits "already running" before it opens, migrates or mints anything, so
	// a losing start leaves no relevo.db, -wal, -shm or installation.json.
	// `relevo serve`'s state root is its own and stays out of scope.
	lock, err := store.New(root).AcquireDaemonLock()
	if err != nil {
		return err
	}
	// DaemonLock.Close is already idempotent (it nils its file), so the
	// explicit close on the re-exec path and this defer cannot double-close
	// into an error that matters.
	defer lock.Close()

	// The socket path is checked before newRuntime mints anything, so a start
	// whose path could never bind refuses with one clear line and leaves no
	// half-created root. The listener is bound here, under the lock, so a
	// connection made while the startup passes run waits in the backlog instead
	// of finding no socket; Serve starts once the handle is ready.
	if err := checkSocketPath(root); err != nil {
		return err
	}
	ln, err := openOwnerListener(root)
	if err != nil {
		return err
	}
	var srv *owner.Server
	defer func() {
		if srv != nil {
			_ = srv.Close()
			return
		}
		_ = ln.Close()
	}()

	// The daemon's one handle: opened directly, here under the lock, and shared
	// by store.NewShared with everything in the daemon -- the config store, the
	// runtime's store (gates, claims, run log, daemon.json, the release cache)
	// and the agy deliverer's creds store. Nothing in the daemon dials the
	// owner, and the owner never opens a second handle of its own. Clients
	// still hold their own dialled connections.
	//
	// Closing is explicit rather than a bare defer: the re-exec path closes the
	// DB itself before syscall.Exec, and the deferred pass must then do
	// nothing. *db.DB.Close is not idempotent.
	d, err := openDBDirect(filepath.Join(root, "relevo.db"))
	if err != nil {
		return err
	}
	var dbClosed bool
	closeDB := func() {
		if dbClosed {
			return
		}
		dbClosed = true
		if cerr := d.Close(); cerr != nil {
			slog.Warn("relevo daemon: close db", "err", cerr)
		}
	}
	defer closeDB()

	// newRuntimeOn is the one constructor, called under the lock: it mints a
	// whole installation.json (above, in openDBDirect), opens and migrates
	// relevo.db, imports config and builds the runtime with gates over the
	// shared handle. Nothing before this point touches the database.
	rt, err := newRuntimeOn(root, d)
	if err != nil {
		return err
	}

	// Nowhere else: a CLI one-shot (any other command) must not relaunch a
	// builder it merely happens to observe as "exited, code unknown" (#244).
	rt.StartedAt = time.Now()
	// The daemon's in-memory "seen alive" set (#370, spec §4.2): it tells a
	// process this daemon actually watched from one that merely predates it,
	// so a restart relaunches only what it really took down. Nothing else
	// creates one; every CLI one-shot leaves Watched nil and keeps #244's
	// rule exactly.
	rt.Watched = relevo.NewWatched()
	// The daemon's per-binding auth grace (#373 §3): a transient 401 (clock
	// skew after a reboot) is shown and warned about, and only halts a binding
	// after 15 minutes. Every CLI one-shot leaves AuthGrace nil, so it never
	// halts on one.
	rt.AuthGrace = relevo.NewAuthGrace()
	// The scope template newRuntime filled is logged once here, in the same
	// shape `relevo serve` uses (#295). "off" is scope.enabled: false; the
	// local daemon does not probe at startup, so there is no "unavailable"
	// here -- a later probe failure is the runner's one-line warning (§3.1).
	slog.Info(fmt.Sprintf("scopes=%s", scopeStatusText(rt.Scope)))

	// The reaper is built before anything can spawn a child, so it sees only
	// processes inherited from the previous image across a re-exec. A child
	// started after this point is never in its set (#371 §4.6).
	reaper := proc.NewInheritedReaper(os.Getpid(), "/proc")

	// Capture the executable and its identity once, before any replacement
	// can land. A failure disables re-exec with a warning; the daemon runs on
	// regardless (#371 §4.7, §6).
	exe, exeErr := upgrade.ResolveExe()
	exeID, idErr := upgrade.ExeIdentity(exe)
	reexecOK := exeErr == nil && idErr == nil
	if !reexecOK {
		reason := exeErr
		if reason == nil {
			reason = idErr
		}
		slog.Warn("re-exec disabled", "err", reason)
	}

	// The watcher gets a labelled copy, so the import it runs is recorded as
	// source "import".
	var configSource relevo.ConfigSource
	if rt.Config != nil {
		configSource = rt.Config.As("import", "imported "+configDirPath)
	}
	watcher := relevo.NewConfigWatcher(configSource, configDirPath, os.Getenv)

	// The one handle is already open and shared; this decides only whether the
	// daemon's own ingest may write it.
	daemonRuntimeHandle(&rt, d)

	// The ingest mirror's proven duplicates are removed once, before daemon.json
	// is written (D3c; v2 #476). The run backs the database up first and records
	// itself in the kv table, so every later start is a no-op, and a failure
	// never stops the daemon: a machine with no database, or one where the
	// backup could not be written, still runs and retries on the next start.
	if rt.DB != nil {
		stats, ran, derr := ingest.DedupeMirrorOnce(rt.DB, filepath.Dir(rt.Store.DBPath()), time.Now())
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
	}

	// The pass converts the bulk history columns to per-row zstd once, right
	// after the dedupe removed the rows it could, so the conversion and its
	// backup run on the smaller database. It backs the database up first and
	// records itself in the kv table, so every later start is a no-op, and a
	// failure never stops the daemon.
	if rt.DB != nil {
		cstats, cran, cerr := db.CompressHistoryOnce(rt.DB, filepath.Dir(rt.Store.DBPath()), time.Now())
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

	// The installation's own row, then the origin backfill, run next to the
	// other run-once passes. The row is the projection other machines read to
	// label this installation; the backfill stamps this installation's id on
	// rows written before the origin column existed, and records itself in kv,
	// so every later start is a no-op. A failure never stops the daemon: a
	// machine with no database still runs, and the next start retries.
	if rt.DB != nil {
		inst, ierr := installation.Load(filepath.Dir(rt.Store.DBPath()))
		if ierr != nil {
			slog.Warn("relevo daemon: installation file unavailable", "err", ierr)
		} else {
			if terr := rt.DB.Tx(func(t *db.Tx) error {
				return t.InstallationTouch(inst.ID, inst.Label, time.Now())
			}); terr != nil {
				slog.Warn("relevo daemon: installation row skipped", "err", terr)
			}

			bstats, bran, berr := db.BackfillOriginOnce(rt.DB, inst.ID, time.Now())
			if berr != nil {
				slog.Warn("relevo daemon: origin backfill skipped", "err", berr)
			} else if bran {
				slog.Info("relevo daemon: origin backfill",
					"done_at", bstats.DoneAt,
					"origin", bstats.Origin,
					"binding_records", bstats.BindingRecords,
					"bindings", bstats.Bindings)
			}
		}
	}

	// daemon.json: what this image runs. Written under the lock, so its
	// presence with the lock held means a #371 daemon; removed on a clean
	// shutdown, kept across a re-exec (#371 §4.7).
	srv, err = serveOwner(d, ln)
	if err != nil {
		slog.Warn("relevo daemon: owner socket not served", "err", err)
	}
	info := store.DaemonInfo{
		Version:    buildVersion(),
		PID:        os.Getpid(),
		StartedAt:  time.Now(),
		Exe:        exe,
		ExeID:      exeID,
		ReexecFrom: os.Getenv("RELEVO_REEXEC_FROM"),
	}
	if werr := rt.Store.WriteDaemonInfo(info); werr != nil {
		slog.Warn("relevo daemon: daemon.json not written", "err", werr)
	}
	if info.ReexecFrom != "" {
		slog.Info("re-exec'd", "from", info.ReexecFrom, "to", info.Version)
	}

	// Role definitions are refreshed once per image start, after the lock and
	// daemon.json (#371 §4.10): an upgrade leaves the files relevo wrote for
	// each harness on disk, and one nobody edited is stale. Never fatal.
	refreshRoles(rt.Config)

	loaded, err := rt.Config.Load()
	if err != nil {
		return err
	}
	hooksCfg, err := resolveHooksConfig(loaded.Hooks, hooksRunLog(rt.Store))
	if err != nil {
		return err
	}
	rt.Hooks = newHooksDispatcher(hooksCfg, rt.Policy)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The watcher and its hook are the daemon's whole upgrade decision
	// (#371 §4.3): debounce the new identity over two ticks, preflight it,
	// then either re-exec or refuse and record the refusal.
	up := &upgrade.Watcher{
		Path:    exe,
		Started: exeID,
		Stat:    upgrade.ExeIdentity,
		Preflight: func(ctx context.Context, path string) error {
			out, perr := exec.CommandContext(ctx, path, "daemon", "--preflight").CombinedOutput()
			if perr == nil {
				return nil
			}
			if line := firstLine(string(out)); line != "" {
				return errors.New(line)
			}
			return perr
		},
	}

	hook := func(ctx context.Context) bool {
		// Children inherited from the previous image are reaped here, once
		// per tick: nothing else will ever wait for them (#371 §4.6).
		reaper.Reap()
		if !reexecOK {
			return false
		}

		prev := up.Refused()
		decision := up.Check(ctx)
		if !sameFileID(up.Refused(), prev) {
			if up.Refused() == nil {
				info.ReexecFailed = nil
			} else {
				info.ReexecFailed = &store.ReexecFailure{
					ExeID:  *up.Refused(),
					At:     time.Now(),
					Reason: decision.Reason,
				}
			}
			if werr := rt.Store.WriteDaemonInfo(info); werr != nil {
				slog.Warn("relevo daemon: daemon.json not written", "err", werr)
			}
		}

		switch decision.Action {
		case upgrade.Refused:
			// Check reports Refused only when the identity changed, so this
			// is the one warning per refused build.
			slog.Warn("new relevo binary refused", "exe", exe, "reason", decision.Reason)
		case upgrade.Reexec:
			slog.Info("re-exec onto new binary", "exe", exe)
			return true
		}
		return false
	}

	slog.Info("relevo daemon starting", "interval", *interval)
	err = relevo.NewDaemon(rt, *interval).WithRefresh(watcher.Refresh).WithUpgrade(hook).Run(ctx)
	if errors.Is(err, relevo.ErrReexec) {
		// Drain the owner first: an open transaction must be able to commit
		// before closeDB takes the handle away. Then close explicitly what must
		// not survive the exec -- the DB, the signal context and the lock --
		// and pass the listener's descriptor with close-on-exec cleared so the
		// next image adopts it instead of rebinding.
		fd, herr := drainAndHandoff(srv, ln)
		if herr != nil {
			slog.Warn("relevo daemon: listener handoff failed", "err", herr)
		}
		closeDB()
		stop()
		_ = lock.Close()

		env := withEnv(os.Environ(), "RELEVO_REEXEC_FROM", buildVersion())
		if fd >= 0 {
			env = withEnv(env, listenFDEnv, strconv.Itoa(fd))
		}
		execErr := reexec(exe, append([]string{exe}, os.Args[1:]...), env)
		// Only reached when the exec itself failed: a non-zero exit lets
		// systemd's Restart=on-failure start the new binary anyway.
		closeHandoff()
		return fmt.Errorf("re-exec %s: %w", exe, execErr)
	}

	// A clean shutdown removes the record; the re-exec path above must not.
	_ = rt.Store.RemoveDaemonInfo()
	return err
}

// daemonRuntimeHandle decides whether the daemon's own ingest may write d, the
// one handle the daemon opened and the owner serves. A schema a newer relevo
// wrote is never migrated or written by this binary: rt.DB stays nil so every
// ingest call site treats it as a machine with no database, and the reason is
// said once (#372 §4.5). The owner still serves the handle, so a dialled client
// gets its schema answer.
//
// It is a function of its own so the tests drive the very branch the daemon
// does; rt is a pointer because a Runtime is copied by value everywhere else.
func daemonRuntimeHandle(rt *relevo.Runtime, d *db.DB) {
	if d.Newer() {
		have, know := d.SchemaVersions()
		slog.Warn(fmt.Sprintf("relevo.db schema v%d is newer than this relevo (v%d); ingest paused until relevo is upgraded", have, know))
		return
	}
	rt.DB = d
}

// refreshRoles lands relevo's shipped agent definitions and the custom agents
// the config renders, once per image start
// (#371 §4.10). The kinds are the ones `relevo config agents` picks by default
// -- harness.Install's own "every harness whose binary is on PATH" selection --
// and the env is the same one that verb uses, so both read and write the one
// manifest at <state root>/agents-manifest.json.
//
// It is never fatal: a definition that could not be written is one warning, a
// manifest relevo cannot read or save is one warning, and the daemon's own work
// does not depend on either.
func refreshRoles(cfg *config.Store) {
	env, err := agentInstallEnv()
	if err != nil {
		slog.Warn("agent definitions not refreshed", "err", err)
		return
	}

	results, err := harness.Install(env, harness.InstallOptions{})
	if err != nil {
		slog.Warn("agent definitions not refreshed", "err", err)
	}

	custom, cerr := relevo.InstallCustomAgents(cfg, env, harness.InstallOptions{})
	if cerr != nil {
		slog.Warn("agent definitions not refreshed", "err", cerr)
	}
	results = append(results, custom...)

	for _, r := range results {
		switch r.Outcome {
		case harness.OutcomeWrote, harness.OutcomeUpdated:
			slog.Info("agent definition refreshed", "kind", r.Kind, "role", r.Role, "path", r.Path)
		case harness.OutcomeKeptDiffers:
			slog.Info(fmt.Sprintf("%s was edited; relevo config agents --force replaces it", r.Path))
		case harness.OutcomeError:
			slog.Warn("agent definition not refreshed", "kind", r.Kind, "role", r.Role, "path", r.Path, "err", r.Err)
		}
	}
}

// sameFileID reports whether two identity pointers name the same file. Both
// nil is "no refusal"; one nil is a change.
func sameFileID(a, b *store.FileID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
