package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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
	// --preflight before re-exec'ing into it. It stays out of
	// the usage text and the README, so it is defined but not printed.
	v.preflight = fs.Bool("preflight", false, "validate the runtime configuration and exit (internal)")
	// --auto-exit-after is internal: only the daemon this CLI spawns gets it,
	// so only a daemon with nothing to do exits on its own. It stays out of
	// the usage text and the README, so it is defined but not printed.
	v.autoExitAfter = fs.Duration("auto-exit-after", 0, "exit after this long idle (internal)")
	return v
}

// daemonStopRequest handles the `daemon stop` positional form: it stops the
// daemon and reports handled, or refuses any other positional. An empty argv
// is not handled: the daemon starts.
func daemonStopRequest(args []string, check, preflight bool) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	if len(args) == 1 && args[0] == "stop" && !check && !preflight {
		return true, daemonStop()
	}
	return true, fail(codeUsage, "usage: relevo daemon [--interval D] [--check] [--pprof <path>]; relevo daemon stop")
}

// daemonPeek runs the read-only peek: neither --preflight nor --check writes;
// each reads config files and a database read-only and opens nothing that
// migrates.
func daemonPeek(preflight, check bool) error {
	rt, err := newRuntimePeek()
	if err != nil {
		if preflight {
			// The error goes to stderr, exit 1. Returning rather than
			// os.Exit keeps the path a plain function call a test can make.
			fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
			return exitCodeErr{code: 1}
		}
		return err
	}
	if preflight {
		// newRuntimePeek read and validated config only: it took no lock,
		// opened no DB (which would migrate), started no process and made
		// no network call, and this returns before the DB open below.
		fmt.Printf("ok %s %s\n", buildVersion(), upgrade.SplitCapability)
		return nil
	}

	// --check is the plugin startup hook's probe. It prints nothing on
	// either path: the exit status is the whole answer, and a hook that
	// printed would only fill a log with noise on every server start.
	// Returning exitCodeErr rather than calling os.Exit keeps the
	// no-db-yet ordering a test can call: main maps the code
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

// daemonStartupInput parses the startup and runs the read-only paths: the
// `daemon stop` positional form, the --check/--preflight peek, and the
// read-only config load the daemon path runs first. A stop positional is
// checked before the peek, so `daemon stop --check` is a usage error rather
// than a silent --check.
func daemonStartupInput(fs *flag.FlagSet, check, preflight bool) (root, configDirPath string, done bool, err error) {
	// A handled stop request answers the invocation, successfully or not:
	// the daemon never starts.
	if handled, herr := daemonStopRequest(fs.Args(), check, preflight); handled {
		return "", "", true, herr
	}

	// --preflight and --check are the read-only peek: neither writes. They
	// answer here, before the daemon path resolves a root, takes the lock or
	// opens the database.
	if preflight || check {
		return "", "", true, daemonPeek(preflight, check)
	}

	// The daemon path: the read-only config load runs first, exactly as
	// --preflight's does, so a malformed config fails before the lock and the
	// root stays read-only until then. The loaded value is discarded --
	// newRuntime reloads config from the database it imports into.
	root, configDirPath, err = daemonReadOnlyConfig()
	if err != nil {
		return "", "", false, err
	}
	return root, configDirPath, false, nil
}

// daemonReadOnlyConfig resolves the state root and loads the config
// read-only, exactly as --preflight does: a malformed config fails before
// the lock, and the root stays read-only until then.
func daemonReadOnlyConfig() (root, configDirPath string, err error) {
	root, err = tmpdir.DefaultRoot()
	if err != nil {
		return "", "", err
	}
	configDir, err := userConfigRoot()
	if err != nil {
		return "", "", err
	}
	configDirPath = filepath.Join(configDir, "relevo")
	if _, err := loadConfigReadOnly(root, configDirPath, lockedSkip); err != nil {
		return "", "", err
	}
	return root, configDirPath, nil
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

	// The stop request, the read-only peek and the read-only config load all
	// run before the lock: nothing here opens or migrates anything. A
	// handled invocation answers here and never reaches the lock.
	root, configDirPath, done, err := daemonStartupInput(fs, *check, *preflight)
	if err != nil || done {
		return err
	}

	// The lock is the first effect of a daemon start: a daemon that loses it
	// exits "already running" before it opens, migrates or mints anything, so
	// a losing start leaves no relevo.db, -wal, -shm or installation.json.
	// `relevo serve`'s state root is its own and stays out of scope.
	lock, ln, err := daemonTakeLock(root)
	if err != nil {
		return err
	}
	// DaemonLock.Close is already idempotent (it nils its file), so the
	// explicit close on the re-exec path and this defer cannot double-close
	// into an error that matters.
	defer func() { _ = lock.Close() }()

	var srv *owner.Server
	defer func() {
		if srv != nil {
			_ = srv.Close()
			return
		}
		_ = ln.Close()
	}()

	// The profiling socket is opt-in and never fatal: a path it cannot bind
	// must not take the daemon -- and the database it owns -- down. It is
	// bound before the one-time passes so a start stuck in a migration can
	// still be profiled. A re-exec skips the deferred cleanup (the image is
	// replaced), and the next image removes the socket file it finds.
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

	// The daemon's one handle: opened directly, here under the lock, and shared
	// by store.NewShared with everything in the daemon. Closing is explicit
	// rather than a bare defer: the re-exec path closes the DB itself before
	// syscall.Exec, and the deferred pass must then do nothing.
	d, rt, err := daemonOpenRuntime(root)
	if err != nil {
		return err
	}
	closeDB := daemonCloser(d)
	defer closeDB()

	st := daemonBootServing(&rt, d, ln, configDirPath)
	srv = st.srv

	// daemon.json: what this image runs. Written under the lock, so its
	// presence with the lock held means a daemon of this build; removed on a
	// clean shutdown, kept across a re-exec.
	info, err := daemonLoadHooks(&rt, st.exe, st.exeID)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return st.runLoop(ctx, stop, lock, closeDB, &info, root, *interval, *autoExitAfter)
}

// daemonResolveExe resolves this image's executable and identity for the
// re-exec watch. A failure disables re-exec with a warning; the daemon runs
// on with whatever the resolvers returned.
func daemonResolveExe() (exe string, exeID store.FileID, reexecOK bool) {
	exe, exeErr := upgrade.ResolveExe()
	exeID, idErr := upgrade.SelfImageIdentity()
	reexecOK = exeErr == nil && idErr == nil
	if !reexecOK {
		reason := exeErr
		if reason == nil {
			reason = idErr
		}
		slog.Warn("re-exec disabled", "err", reason)
	}
	return exe, exeID, reexecOK
}

// daemonLoadHooks writes this image's daemon.json, refreshes the role files
// and the service unit once per image start, and wires the hooks dispatcher.
// Only the config load itself is fatal.
func daemonLoadHooks(rt *relevo.Runtime, exe string, exeID store.FileID) (store.DaemonInfo, error) {
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
		return store.DaemonInfo{}, err
	}
	log, lerr := rt.Store.DB()
	if lerr != nil {
		log = nil
	}
	hooksCfg, err := resolveHooksConfig(loaded.Hooks, hooksRunLog(log))
	if err != nil {
		return store.DaemonInfo{}, err
	}
	rt.Hooks = newHooksDispatcher(hooksCfg, rt.Policy)
	return info, nil
}

// daemonUpgrader carries the upgrade watcher's per-tick state: the inherited
// reaper, the re-exec switch, the daemon record a changed refusal rewrites,
// and the store that record lives in.
type daemonUpgrader struct {
	reaper        *proc.InheritedReaper
	reexecOK      bool
	reapRequested *atomic.Bool
	up            *upgrade.Watcher
	info          *store.DaemonInfo
	exe           string
	st            *store.Store
}

// tick is the daemon's upgrade hook: reap inherited children, then re-exec
// onto a new binary or refuse and record it, debounced over two ticks.
func (u *daemonUpgrader) tick(ctx context.Context) bool {
	// Children inherited from the previous image are reaped here, once
	// per tick: nothing else will ever wait for them.
	u.reaper.Reap()
	if !u.reexecOK {
		return false
	}
	// The owner's grace elapsed on a statement the engine will not
	// interrupt. Only the new image ends it: execing gives the process a
	// fresh thread for the abandoned statement while the drain lets
	// ordinary work finish.
	if u.reapRequested.Load() {
		return true
	}

	prev := u.up.Refused()
	decision := u.up.Check(ctx)
	u.noteRefused(prev, decision)

	switch decision.Action {
	case upgrade.Refused:
		// Check reports Refused only when the identity changed, so this
		// is the one warning per refused build.
		slog.Warn("new relevo binary refused", "exe", u.exe, "reason", decision.Reason)
	case upgrade.Reexec:
		slog.Info("re-exec onto new binary", "exe", u.exe)
		return true
	}
	return false
}

// noteRefused records a changed refusal on the daemon record: clearing it or
// stamping the refused build, its time and the reason.
func (u *daemonUpgrader) noteRefused(prev *store.FileID, decision upgrade.Decision) {
	if sameFileID(u.up.Refused(), prev) {
		return
	}
	if u.up.Refused() == nil {
		u.info.ReexecFailed = nil
	} else {
		u.info.ReexecFailed = &store.ReexecFailure{
			ExeID:  *u.up.Refused(),
			At:     time.Now(),
			Reason: decision.Reason,
		}
	}
	if werr := u.st.WriteDaemonInfo(*u.info); werr != nil {
		slog.Warn("relevo daemon: daemon.json not written", "err", werr)
	}
}

// watchDaemonIdleOn starts the CLI-spawned daemon's own exit: the flag is
// internal, so a service-managed or hand-started daemon never gets it. It
// cancels the context once the daemon has been idle past the period, which
// Run reports as a clean shutdown.
func watchDaemonIdleOn(ctx context.Context, stop context.CancelFunc, after time.Duration, root string, lock *store.DaemonLock, srv *owner.Server, st *store.Store, now func() time.Time, running *atomic.Pointer[relevo.Daemon]) {
	if after <= 0 {
		return
	}
	go watchDaemonIdle(ctx, stop, after, idlePoll, now, func() daemonActivity {
		a := daemonActivityNow(root, lock, srv, st)
		a.Joining = running.Load().Joining()
		return a
	})
}

// daemonReexec drains the owner and re-execs onto the new image, carrying the
// listener's descriptor across. It only returns when the exec itself failed.
func daemonReexec(srv *owner.Server, ln net.Listener, closeDB func(), stop context.CancelFunc, lock *store.DaemonLock, exe string) error {
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

// daemonRuntimeHandle decides whether the daemon's own ingest may write d, the
// one handle the daemon opened and the owner serves. A schema a newer relevo
// wrote is never migrated or written by this binary: rt.DB stays nil so every
// ingest call site treats it as a machine with no database, and the reason is
// said once. The owner still serves the handle, so a dialled client gets its
// schema answer.
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
// the config renders, once per image start. The kinds are the ones `relevo
// config agents` picks by default
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

// Both nil is "no refusal"; one nil is a change.
func sameFileID(a, b *store.FileID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
