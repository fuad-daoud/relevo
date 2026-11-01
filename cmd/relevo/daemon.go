package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
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
		_, _ = fmt.Fprintln(fs.Output(), "usage: relevo daemon [--interval D] [--check] [--pprof <path>]")
		_, _ = fmt.Fprintln(fs.Output(), "       relevo daemon stop")
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if stop, err := daemonStopOrUsage(fs.Args(), *check, *preflight); err != nil {
		return err
	} else if stop {
		return daemonStop()
	}

	if done, err := runDaemonPeek(*preflight, *check); err != nil {
		return err
	} else if done {
		return nil
	}

	return runDaemon(*interval, *pprofSocket, *autoExitAfter)
}

// daemonStopOrUsage routes `daemon stop`: true runs the stop arm, an error
// refuses any other positional, and false continues into the daemon itself.
// `daemon stop` is checked before the --check/--preflight peek, so
// `daemon stop --check` is a usage error rather than a silent --check.
func daemonStopOrUsage(positional []string, check, preflight bool) (bool, error) {
	if len(positional) == 0 {
		return false, nil
	}
	if len(positional) == 1 && positional[0] == "stop" && !check && !preflight {
		return true, nil
	}
	return false, fail(codeUsage, "usage: relevo daemon [--interval D] [--check] [--pprof <path>]; relevo daemon stop")
}

// runDaemonPeek runs the read-only peek: --preflight validates the runtime
// and prints, --check probes whether a daemon runs. Neither writes and
// neither opens a database, which would migrate it. True means the peek
// answered and the daemon must not start.
func runDaemonPeek(preflight, check bool) (bool, error) {
	if !preflight && !check {
		return false, nil
	}
	rt, err := newRuntimePeek()
	if err != nil {
		if preflight {
			// Return rather than os.Exit so a test can call this path.
			_, _ = fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
			return true, exitCodeErr{code: 1}
		}
		return true, err
	}
	if preflight {
		// newRuntimePeek took no lock and opened no DB: a candidate
		// binary's --preflight runs against the live database, so this
		// returns before the DB open below.
		fmt.Printf("ok %s %s\n", buildVersion(), upgrade.SplitCapability)
		return true, nil
	}

	// --check is the plugin startup hook's probe: the exit status is the
	// whole answer, so it prints nothing and returns its code for main.
	running, err := rt.Store.DaemonRunning()
	if err != nil {
		return true, err
	}
	if !running {
		return true, exitCodeErr{code: 1}
	}
	return true, nil
}

// daemonHandles carries runDaemon's open state across its phases: the lock
// held since start, the owner listener and server, the shared database
// handle, and the image identity the upgrade watcher compares against.
type daemonHandles struct {
	root          string
	configDirPath string
	lock          *store.DaemonLock
	ln            net.Listener
	srv           *owner.Server
	pprofCleanup  func()
	d             *db.DB
	closeDB       func()
	rt            relevo.Runtime
	watcher       *relevo.ConfigWatcher
	daemonInfo    store.DaemonInfo
	exe           string
	exeID         store.FileID
	reexecOK      bool
	reapRequested *atomic.Bool
	onAbandoned   func()
}

// runDaemon starts the daemon over its open handles: the runtime, its hooks
// and the serve loop. The lock, the listener and the database close here, in
// the reverse of the order openDaemonHandles took them.
func runDaemon(interval time.Duration, pprofSocket string, autoExitAfter time.Duration) error {
	h, err := openDaemonHandles(pprofSocket)
	if err != nil {
		return err
	}
	// DaemonLock.Close is already idempotent (it nils its file), so the
	// explicit close on the re-exec path and this defer cannot double-close
	// into an error that matters.
	defer func() { _ = h.lock.Close() }()
	defer func() {
		if h.srv != nil {
			_ = h.srv.Close()
			return
		}
		_ = h.ln.Close()
	}()
	if h.pprofCleanup != nil {
		defer h.pprofCleanup()
	}
	// Closing is explicit, not a bare defer, because *db.DB.Close is not
	// idempotent and the re-exec path closes it before syscall.Exec.
	defer h.closeDB()

	if err := initDaemonRuntime(h); err != nil {
		return err
	}
	if err := loadDaemonHooks(h); err != nil {
		return err
	}
	return serveDaemon(h, interval, autoExitAfter)
}

// openDaemonHandles reads the config, takes the lock, binds the owner
// listener and opens the shared database. A failure releases what an earlier
// phase took, as runDaemon's deferred cleanup would.
func openDaemonHandles(pprofSocket string) (*daemonHandles, error) {
	h := &daemonHandles{}
	// The read-only config load runs first, so a malformed config fails
	// before the lock. The loaded value is discarded: newRuntime reloads it.
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return nil, err
	}
	h.root = root
	configDir, err := userConfigRoot()
	if err != nil {
		return nil, err
	}
	h.configDirPath = filepath.Join(configDir, "relevo")
	if _, err := loadConfigReadOnly(root, h.configDirPath, lockedSkip); err != nil {
		return nil, err
	}

	// The lock is the first effect of a daemon start: a daemon that loses it
	// exits "already running" before it opens, migrates or mints anything, so
	// a losing start leaves no relevo.db, -wal, -shm or installation.json.
	// `relevo serve`'s state root is its own and stays out of scope.
	lock, err := store.New(root).AcquireDaemonLock()
	if err != nil {
		return nil, err
	}
	h.lock = lock
	// The socket path is checked before newRuntime mints anything, so a bad
	// path refuses with one line and leaves no half-created root. The listener
	// is bound here, under the lock, so early connections wait in the backlog.
	if err := checkSocketPath(root); err != nil {
		_ = lock.Close()
		return nil, err
	}
	ln, err := openOwnerListener(root)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	h.ln = ln

	// The profiling socket is opt-in and never fatal, and is bound before the
	// one-time passes so a stuck migration can still be profiled. A re-exec
	// skips the deferred cleanup; the next image removes the socket it finds.
	h.pprofCleanup = serveDaemonPprof(pprofSocket)

	// The daemon's one handle: opened here under the lock and shared with
	// everything in the daemon; nothing in the daemon dials the owner.
	d, err := openDBDirect(filepath.Join(root, "relevo.db"))
	if err != nil {
		if h.pprofCleanup != nil {
			h.pprofCleanup()
		}
		_ = ln.Close()
		_ = lock.Close()
		return nil, err
	}
	h.d = d
	var dbClosed bool
	h.closeDB = func() {
		if dbClosed {
			return
		}
		dbClosed = true
		if cerr := d.Close(); cerr != nil {
			slog.Warn("relevo daemon: close db", "err", cerr)
		}
	}
	return h, nil
}

// serveDaemonPprof binds the profiling socket when asked. A failure warns and
// the daemon runs without it; the cleanup is nil unless a listener was bound.
func serveDaemonPprof(socket string) func() {
	if socket == "" {
		return nil
	}
	pln, perr := servePprof(socket)
	if perr != nil {
		slog.Warn("relevo daemon: pprof socket not served", "err", perr)
		return nil
	}
	slog.Info("relevo daemon: pprof socket", "path", socket)
	return func() {
		_ = pln.Close()
		_ = os.Remove(socket)
	}
}

// initDaemonRuntime builds the daemon's runtime over the shared handle and
// runs its one-time passes: the re-exec identity, the config watcher, the
// owner server and the daemon record.
func initDaemonRuntime(h *daemonHandles) error {
	// newRuntimeOn is the one constructor, called under the lock: it opens and
	// migrates relevo.db and builds the runtime over the shared handle.
	// Nothing before this point touches the database.
	rt, err := newRuntimeOn(h.root, h.d)
	if err != nil {
		return err
	}
	h.rt = rt

	// Nowhere else: a CLI one-shot (any other command) must not relaunch a
	// builder it merely happens to observe as "exited, code unknown".
	h.rt.StartedAt = time.Now()
	// The daemon's in-memory "seen alive" set: it tells a process this daemon
	// actually watched from one that merely predates it, so a restart
	// relaunches only what it really took down.
	h.rt.Watched = relevo.NewWatched()
	// The daemon's per-binding auth grace: a transient 401 halts a binding
	// only after 15 minutes. Every CLI one-shot leaves AuthGrace nil.
	h.rt.AuthGrace = relevo.NewAuthGrace()
	// The scope template newRuntime filled is logged once here, in the same
	// shape `relevo serve` uses ("off" is scope.enabled: false; the local
	// daemon does not probe at startup, so there is no "unavailable" here).
	slog.Info(fmt.Sprintf("scopes=%s", scopeStatusText(h.rt.Scope)))

	// Capture the identity of the running image, not of the path: a
	// replacement landing after exec would become the baseline and the watcher
	// would never fire. A failure disables re-exec with a warning.
	exe, exeErr := upgrade.ResolveExe()
	exeID, idErr := upgrade.SelfImageIdentity()
	h.exe, h.exeID = exe, exeID
	h.reexecOK = exeErr == nil && idErr == nil
	if !h.reexecOK {
		reason := exeErr
		if reason == nil {
			reason = idErr
		}
		slog.Warn("re-exec disabled", "err", reason)
	}

	// The watcher gets a labelled copy, so the import it runs is recorded as
	// source "import".
	var configSource relevo.ConfigSource
	if h.rt.Config != nil {
		configSource = h.rt.Config.As("import", "imported "+h.configDirPath)
	}
	h.watcher = relevo.NewConfigWatcher(configSource, h.configDirPath, os.Getenv)

	// The one handle is already open and shared; this decides only whether the
	// daemon's own ingest may write it.
	daemonRuntimeHandle(&h.rt, h.d)

	// An abandoned statement the owner cannot interrupt is ended by re-execing:
	// the hook asks the upgrade hook below to return true on its next tick.
	// Without it the owner keeps refusing ad-hoc reads with no way to end the
	// statement, which is a safe degradation.
	var reapRequested atomic.Bool
	h.reapRequested = &reapRequested
	var onAbandoned func()
	if h.reexecOK {
		var reapLogged sync.Once
		onAbandoned = func() {
			reapLogged.Do(func() { slog.Warn("relevo daemon: reaping an abandoned statement") })
			reapRequested.Store(true)
		}
	}
	h.onAbandoned = onAbandoned

	// The owner is served before the one-time passes, which run under the
	// owner's write path; the sync verb executor is installed after them, once
	// they recorded themselves (see daemonStartup for why).
	h.srv = daemonStartup(&h.rt, h.d, h.ln, onAbandoned)

	// daemon.json: what this image runs. Written under the lock, so its
	// presence with the lock held means a daemon of this build; removed on a
	// clean shutdown, kept across a re-exec.
	h.daemonInfo = store.DaemonInfo{
		Version:    buildVersion(),
		PID:        os.Getpid(),
		StartedAt:  time.Now(),
		Exe:        exe,
		ExeID:      exeID,
		ReexecFrom: os.Getenv("RELEVO_REEXEC_FROM"),
	}
	if werr := h.rt.Store.WriteDaemonInfo(h.daemonInfo); werr != nil {
		slog.Warn("relevo daemon: daemon.json not written", "err", werr)
	}
	if h.daemonInfo.ReexecFrom != "" {
		slog.Info("re-exec'd", "from", h.daemonInfo.ReexecFrom, "to", h.daemonInfo.Version)
	}

	// Role definitions are refreshed once per image start, after the lock and
	// daemon.json: an upgrade leaves the files relevo wrote for each harness
	// on disk, and one nobody edited is stale. Never fatal.
	refreshRoles(h.rt.Config)
	// The unit the supervisor starts this binary from is refreshed on the same
	// beat, never restarting the daemon itself.
	refreshServiceUnitAtStart()
	return nil
}

// loadDaemonHooks loads the config and wires the hooks dispatcher over the
// stored run log.
func loadDaemonHooks(h *daemonHandles) error {
	loaded, err := h.rt.Config.Load()
	if err != nil {
		return err
	}
	log, lerr := h.rt.Store.DB()
	if lerr != nil {
		log = nil
	}
	hooksCfg, err := resolveHooksConfig(loaded.Hooks, hooksRunLog(log))
	if err != nil {
		return err
	}
	h.rt.Hooks = newHooksDispatcher(hooksCfg, h.rt.Policy)
	return nil
}

// makeDaemonHook builds the per-tick upgrade decision: reap inherited
// children, end an abandoned statement by re-execing, and debounce the new
// binary's identity into a re-exec or a refusal.
func makeDaemonHook(h *daemonHandles, up *upgrade.Watcher) func(context.Context) bool {
	// The reaper is built before anything can spawn a child, so it sees only
	// processes inherited from the previous image across a re-exec. A child
	// started after this point is never in its set.
	reaper := proc.NewInheritedReaper(os.Getpid(), "/proc")
	return func(ctx context.Context) bool {
		// Children inherited from the previous image are reaped here, once
		// per tick: nothing else will ever wait for them.
		reaper.Reap()
		if !h.reexecOK {
			return false
		}
		// Only the new image ends an abandoned statement: execing gives the
		// process a fresh thread for it while the drain lets other work finish.
		if h.reapRequested.Load() {
			return true
		}

		prev := up.Refused()
		decision := up.Check(ctx)
		if !sameFileID(up.Refused(), prev) {
			if up.Refused() == nil {
				h.daemonInfo.ReexecFailed = nil
			} else {
				h.daemonInfo.ReexecFailed = &store.ReexecFailure{
					ExeID:  *up.Refused(),
					At:     time.Now(),
					Reason: decision.Reason,
				}
			}
			if werr := h.rt.Store.WriteDaemonInfo(h.daemonInfo); werr != nil {
				slog.Warn("relevo daemon: daemon.json not written", "err", werr)
			}
		}

		switch decision.Action {
		case upgrade.Refused:
			// Check reports Refused only when the identity changed, so this
			// is the one warning per refused build.
			slog.Warn("new relevo binary refused", "exe", h.exe, "reason", decision.Reason)
		case upgrade.Reexec:
			slog.Info("re-exec onto new binary", "exe", h.exe)
			return true
		}
		return false
	}
}

// serveDaemon runs the daemon's serve loop: the upgrade watcher, the idle
// watcher for CLI-spawned daemons, and the reconciler with its re-exec
// handoff.
func serveDaemon(h *daemonHandles, interval, autoExitAfter time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The watcher and its hook are the daemon's whole upgrade decision:
	// debounce the new identity over two ticks, preflight it, then re-exec or
	// refuse. The split guard refuses a candidate that cannot read the local
	// split, which would flap the visible config across re-execs. The split
	// marker cannot appear later in this process's life, so reading it once
	// here is current for every later tick.
	up := &upgrade.Watcher{
		Path:      h.exe,
		Started:   h.exeID,
		Stat:      upgrade.ExeIdentity,
		Preflight: upgrade.SplitGuardPreflight(h.rt.DB != nil && h.rt.DB.SplitDone()),
	}
	hook := makeDaemonHook(h, up)

	// The idle watcher is the CLI-spawned daemon's own exit: the flag is
	// internal, so a service-managed or hand-started daemon never gets it.
	var running atomic.Pointer[relevo.Daemon]
	if autoExitAfter > 0 {
		go watchDaemonIdle(ctx, stop, autoExitAfter, idlePoll, h.rt.Now, func() daemonActivity {
			a := daemonActivityNow(h.root, h.lock, h.srv, h.rt.Store)
			a.Joining = running.Load().Joining()
			return a
		})
	}

	slog.Info("relevo daemon starting", "interval", interval)
	daemon := relevo.NewDaemon(h.rt, interval).WithRefresh(h.watcher.Refresh).WithUpgrade(hook)
	// A gate this daemon records has to reach the other machines now, so the
	// runtime carries the daemon's own trigger and the daemon is bound
	// afterwards: the trigger is the daemon asking itself for an attempt.
	daemon.BindSyncNow(ctx)
	// Rebind the verb hook now with the guard: from here a verb and a seal or
	// idle tick are serialized, keeping one machine's markers from being
	// written twice at once.
	installSyncVerbSerializing(ctx, daemon, h.srv, h.d)
	running.Store(daemon)
	err := daemon.Run(ctx)
	if errors.Is(err, relevo.ErrReexec) {
		// Drain the owner first: an open transaction must commit before closeDB
		// takes the handle away. Then close what must not survive the exec and
		// pass the listener's descriptor with close-on-exec cleared, so the
		// next image adopts it instead of rebinding.
		fd, herr := drainAndHandoff(h.srv, h.ln)
		if herr != nil {
			slog.Warn("relevo daemon: listener handoff failed", "err", herr)
		}
		h.closeDB()
		stop()
		_ = h.lock.Close()

		env := withEnv(os.Environ(), "RELEVO_REEXEC_FROM", buildVersion())
		if fd >= 0 {
			env = withEnv(env, listenFDEnv, strconv.Itoa(fd))
		}
		execErr := reexec(h.exe, append([]string{h.exe}, os.Args[1:]...), env)
		// Only reached when the exec itself failed: a non-zero exit lets
		// systemd's Restart=on-failure start the new binary anyway.
		closeHandoff()
		return fmt.Errorf("re-exec %s: %w", h.exe, execErr)
	}

	// A clean shutdown removes the record; the re-exec path above must not.
	_ = h.rt.Store.RemoveDaemonInfo()
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
