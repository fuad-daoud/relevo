package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
	"github.com/fuad-daoud/relevo/internal/upgrade"
)

type daemonFlagValues struct {
	interval      *time.Duration
	check         *bool
	preflight     *bool
	pprof         *string
	autoExitAfter *time.Duration
}

func daemonFlagSet(fs *flag.FlagSet) *daemonFlagValues {
	v := &daemonFlagValues{}
	v.interval = fs.Duration("interval", 2*time.Second, "poll interval")
	v.check = fs.Bool("check", false, "exit 0 if a daemon is running, 1 if not; print nothing")
	v.pprof = fs.String("pprof", "", "serve net/http/pprof on this unix socket path (profiling only; off by default)")
	// --preflight is internal: the daemon runs a candidate binary's own
	// --preflight before re-exec'ing into it, so it is defined but hidden.
	v.preflight = fs.Bool("preflight", false, "validate the runtime configuration and exit (internal)")
	// --auto-exit-after is internal: only a CLI-spawned daemon gets it, so
	// only a daemon with nothing to do exits on its own; defined but hidden.
	v.autoExitAfter = fs.Duration("auto-exit-after", 0, "exit after this long idle (internal)")
	return v
}

func cmdDaemon(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	v := daemonFlagSet(fs)
	interval, check, preflight, pprofSocket := v.interval, v.check, v.preflight, v.pprof
	autoExitAfter := v.autoExitAfter
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: relevo daemon [--interval D] [--check] [--pprof <path>]")
		fmt.Fprintln(fs.Output(), "       relevo daemon stop")
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// `daemon stop` is checked before the --check/--preflight peek, so
	// `daemon stop --check` is a usage error rather than a silent --check.
	if pos := fs.Args(); len(pos) > 0 {
		if len(pos) == 1 && pos[0] == "stop" && !*check && !*preflight {
			return daemonStop()
		}
		return fail(codeUsage, "usage: relevo daemon [--interval D] [--check] [--pprof <path>]; relevo daemon stop")
	}

	// --preflight and --check are the read-only peek: neither writes and
	// neither opens a database, which would migrate it, so they return here,
	// before the lock or the database.
	if *preflight || *check {
		rt, err := newRuntimePeek()
		if err != nil {
			if *preflight {
				// Return rather than os.Exit so a test can call this path.
				fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
				return exitCodeErr{code: 1}
			}
			return err
		}
		if *preflight {
			// newRuntimePeek took no lock and opened no DB: a candidate
			// binary's --preflight runs against the live database, so this
			// returns before the DB open below.
			fmt.Printf("ok %s %s\n", buildVersion(), upgrade.SplitCapability)
			return nil
		}

		// --check is the plugin startup hook's probe: the exit status is the
		// whole answer, so it prints nothing and returns its code for main.
		running, err := rt.Store.DaemonRunning()
		if err != nil {
			return err
		}
		if !running {
			return exitCodeErr{code: 1}
		}
		return nil
	}

	// The read-only config load runs first, so a malformed config fails
	// before the lock. The loaded value is discarded: newRuntime reloads it.
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return err
	}
	configDir, err := userConfigRoot()
	if err != nil {
		return err
	}
	configDirPath := filepath.Join(configDir, "relevo")
	if _, err := loadConfigReadOnly(root, configDirPath, lockedSkip); err != nil {
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

	// The socket path is checked before newRuntime mints anything, so a bad
	// path refuses with one line and leaves no half-created root. The listener
	// is bound here, under the lock, so early connections wait in the backlog.
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

	// The profiling socket is opt-in and never fatal, and is bound before the
	// one-time passes so a stuck migration can still be profiled. A re-exec
	// skips the deferred cleanup; the next image removes the socket it finds.
	if *pprofSocket != "" {
		pln, perr := servePprof(*pprofSocket)
		if perr != nil {
			slog.Warn("relevo daemon: pprof socket not served", "err", perr)
		} else {
			defer func() {
				_ = pln.Close()
				_ = os.Remove(*pprofSocket)
			}()
			slog.Info("relevo daemon: pprof socket", "path", *pprofSocket)
		}
	}

	// The daemon's one handle: opened here under the lock and shared with
	// everything in the daemon; nothing in the daemon dials the owner. Closing
	// is explicit, not a bare defer, because *db.DB.Close is not idempotent
	// and the re-exec path closes it before syscall.Exec.
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

	// newRuntimeOn is the one constructor, called under the lock: it opens and
	// migrates relevo.db and builds the runtime over the shared handle.
	// Nothing before this point touches the database.
	rt, err := newRuntimeOn(root, d)
	if err != nil {
		return err
	}

	// Nowhere else: a CLI one-shot (any other command) must not relaunch a
	// builder it merely happens to observe as "exited, code unknown".
	rt.StartedAt = time.Now()
	// The daemon's in-memory "seen alive" set: it tells a process this daemon
	// actually watched from one that merely predates it, so a restart
	// relaunches only what it really took down.
	rt.Watched = relevo.NewWatched()
	// The daemon's per-binding auth grace: a transient 401 halts a binding
	// only after 15 minutes. Every CLI one-shot leaves AuthGrace nil.
	rt.AuthGrace = relevo.NewAuthGrace()
	// The scope template newRuntime filled is logged once here, in the same
	// shape `relevo serve` uses ("off" is scope.enabled: false; the local
	// daemon does not probe at startup, so there is no "unavailable" here).
	slog.Info(fmt.Sprintf("scopes=%s", scopeStatusText(rt.Scope)))

	// The reaper is built before anything can spawn a child, so it sees only
	// processes inherited from the previous image across a re-exec. A child
	// started after this point is never in its set.
	reaper := proc.NewInheritedReaper(os.Getpid(), "/proc")

	// Capture the identity of the running image, not of the path: a
	// replacement landing after exec would become the baseline and the watcher
	// would never fire. A failure disables re-exec with a warning.
	exe, exeErr := upgrade.ResolveExe()
	exeID, idErr := upgrade.SelfImageIdentity()
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

	// An abandoned statement the owner cannot interrupt is ended by re-execing:
	// the hook asks the upgrade hook below to return true on its next tick.
	// Without it the owner keeps refusing ad-hoc reads with no way to end the
	// statement, which is a safe degradation.
	var reapRequested atomic.Bool
	var onAbandoned func()
	if reexecOK {
		var reapLogged sync.Once
		onAbandoned = func() {
			reapLogged.Do(func() { slog.Warn("relevo daemon: reaping an abandoned statement") })
			reapRequested.Store(true)
		}
	}

	// The owner is served before the one-time passes, which run under the
	// owner's write path; the sync verb executor is installed after them, once
	// they recorded themselves (see daemonStartup for why).
	srv = daemonStartup(&rt, d, ln, onAbandoned)

	// daemon.json: what this image runs. Written under the lock, so its
	// presence with the lock held means a daemon of this build; removed on a
	// clean shutdown, kept across a re-exec.
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
	// daemon.json: an upgrade leaves the files relevo wrote for each harness
	// on disk, and one nobody edited is stale. Never fatal.
	refreshRoles(rt.Config)
	// The unit the supervisor starts this binary from is refreshed on the same
	// beat, never restarting the daemon itself.
	refreshServiceUnitAtStart()

	loaded, err := rt.Config.Load()
	if err != nil {
		return err
	}
	log, lerr := rt.Store.DB()
	if lerr != nil {
		log = nil
	}
	hooksCfg, err := resolveHooksConfig(loaded.Hooks, hooksRunLog(log))
	if err != nil {
		return err
	}
	rt.Hooks = newHooksDispatcher(hooksCfg, rt.Policy)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The watcher and its hook are the daemon's whole upgrade decision:
	// debounce the new identity over two ticks, preflight it, then re-exec or
	// refuse. The split guard refuses a candidate that cannot read the local
	// split, which would flap the visible config across re-execs. The split
	// marker cannot appear later in this process's life, so reading it once
	// here is current for every later tick.
	up := &upgrade.Watcher{
		Path:      exe,
		Started:   exeID,
		Stat:      upgrade.ExeIdentity,
		Preflight: upgrade.SplitGuardPreflight(rt.DB != nil && rt.DB.SplitDone()),
	}

	hook := func(ctx context.Context) bool {
		// Children inherited from the previous image are reaped here, once
		// per tick: nothing else will ever wait for them.
		reaper.Reap()
		if !reexecOK {
			return false
		}
		// Only the new image ends an abandoned statement: execing gives the
		// process a fresh thread for it while the drain lets other work finish.
		if reapRequested.Load() {
			return true
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

	// The idle watcher is the CLI-spawned daemon's own exit: the flag is
	// internal, so a service-managed or hand-started daemon never gets it.
	var running atomic.Pointer[relevo.Daemon]
	if *autoExitAfter > 0 {
		go watchDaemonIdle(ctx, stop, *autoExitAfter, idlePoll, rt.Now, func() daemonActivity {
			a := daemonActivityNow(root, lock, srv, rt.Store)
			a.Joining = running.Load().Joining()
			return a
		})
	}

	slog.Info("relevo daemon starting", "interval", *interval)
	daemon := relevo.NewDaemon(rt, *interval).WithRefresh(watcher.Refresh).WithUpgrade(hook)
	// A gate this daemon records has to reach the other machines now, so the
	// runtime carries the daemon's own trigger and the daemon is bound
	// afterwards: the trigger is the daemon asking itself for an attempt.
	daemon.BindSyncNow(ctx)
	// Rebind the verb hook now with the guard: from here a verb and a seal or
	// idle tick are serialized, keeping one machine's markers from being
	// written twice at once.
	installSyncVerbSerializing(ctx, daemon, srv, d)
	running.Store(daemon)
	err = daemon.Run(ctx)
	if errors.Is(err, relevo.ErrReexec) {
		// Drain the owner first: an open transaction must commit before closeDB
		// takes the handle away. Then close what must not survive the exec and
		// pass the listener's descriptor with close-on-exec cleared, so the
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

// daemonRuntimeHandle decides whether the daemon's own ingest may write d: a
// schema a newer relevo wrote is never migrated or written here, so rt.DB
// stays nil and every ingest call site treats it as a machine with no
// database. The owner still serves the handle, so a dialled client gets its
// schema answer. rt is a pointer because Runtime is copied by value elsewhere.
func daemonRuntimeHandle(rt *relevo.Runtime, d *db.DB) {
	if d.Newer() {
		have, know := d.SchemaVersions()
		slog.Warn(fmt.Sprintf("relevo.db schema v%d is newer than this relevo (v%d); ingest paused until relevo is upgraded", have, know))
		return
	}
	rt.DB = d
}

// refreshRoles lands relevo's shipped agent definitions and the
// config-rendered custom agents once per image start, with the same selection
// and env the config agents verb uses, so both share the one manifest at
// <state root>/agents-manifest.json. It is never fatal.
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

// Both nil is "no refusal"; one nil is a change.
func sameFileID(a, b *store.FileID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
