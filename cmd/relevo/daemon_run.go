package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/upgrade"
)

// daemonTakeLock takes the daemon lock and binds the owner listener under it:
// a losing start leaves no database or installation behind, and a connection
// made while the startup passes run waits in the backlog instead of finding
// no socket. A bind failure closes the lock it just took.
func daemonTakeLock(root string) (*store.DaemonLock, net.Listener, error) {
	lock, err := store.New(root).AcquireDaemonLock()
	if err != nil {
		return nil, nil, err
	}
	if err := checkSocketPath(root); err != nil {
		_ = lock.Close()
		return nil, nil, err
	}
	ln, err := openOwnerListener(root)
	if err != nil {
		_ = lock.Close()
		return nil, nil, err
	}
	return lock, ln, nil
}

// daemonOpenRuntime opens the daemon's one handle and builds the runtime over
// it, stamping the daemon-only fields a CLI one-shot never carries. A runtime
// failure closes the handle it just opened, warning the way the deferred
// close would have.
func daemonOpenRuntime(root string) (d *db.DB, rt relevo.Runtime, err error) {
	d, err = openDBDirect(filepath.Join(root, "relevo.db"))
	if err != nil {
		return nil, relevo.Runtime{}, err
	}
	rt, err = newRuntimeOn(root, d)
	if err != nil {
		if cerr := d.Close(); cerr != nil {
			slog.Warn("relevo daemon: close db", "err", cerr)
		}
		return nil, relevo.Runtime{}, err
	}
	// Nowhere else: a CLI one-shot (any other command) must not relaunch a
	// builder it merely happens to observe as "exited, code unknown".
	rt.StartedAt = time.Now()
	// The daemon's in-memory "seen alive" set: it tells a process this
	// daemon actually watched from one that merely predates it, so a
	// restart relaunches only what it really took down. Nothing else
	// creates one; every CLI one-shot leaves Watched nil and keeps the
	// rule above exactly.
	rt.Watched = relevo.NewWatched()
	// The daemon's per-binding auth grace: a transient 401 (clock skew after
	// a reboot) is shown and warned about, and only halts a binding after 15
	// minutes. Every CLI one-shot leaves AuthGrace nil, so it never halts on
	// one.
	rt.AuthGrace = relevo.NewAuthGrace()
	// The scope template newRuntime filled is logged once here, in the same
	// shape `relevo serve` uses. "off" is scope.enabled: false; the local
	// daemon does not probe at startup, so there is no "unavailable" here --
	// a later probe failure is the runner's one-line warning.
	slog.Info(fmt.Sprintf("scopes=%s", scopeStatusText(rt.Scope)))
	// The one handle is already open and shared; this decides only whether the
	// daemon's own ingest may write it.
	daemonRuntimeHandle(&rt, d)
	return d, rt, nil
}

// daemonCloser closes the daemon's handle exactly once, warning on failure:
// the re-exec path closes the DB itself before syscall.Exec, and the deferred
// pass must then do nothing. *db.DB.Close is not idempotent.
func daemonCloser(d *db.DB) func() {
	var dbClosed bool
	return func() {
		if dbClosed {
			return
		}
		dbClosed = true
		if cerr := d.Close(); cerr != nil {
			slog.Warn("relevo daemon: close db", "err", cerr)
		}
	}
}

// daemonServing bundles one daemon start's serving handles: the runtime
// and handle they were built over, the inherited reaper, the image identity
// the upgrade watch compares, and the owner server the verbs run through.
type daemonServing struct {
	rt            *relevo.Runtime
	d             *db.DB
	ln            net.Listener
	reaper        *proc.InheritedReaper
	exe           string
	exeID         store.FileID
	reexecOK      bool
	reapRequested *atomic.Bool
	watcher       *relevo.ConfigWatcher
	onAbandoned   func()
	srv           *owner.Server
}

// daemonBootServing builds the serving handles: the inherited reaper, the
// image identity, the labelled config watcher and the owner server. The
// reaper is built before anything can spawn a child, so it sees only
// processes inherited from the previous image across a re-exec.
func daemonBootServing(rt *relevo.Runtime, d *db.DB, ln net.Listener, configDirPath string) daemonServing {
	var st daemonServing
	st.rt = rt
	st.d = d
	st.ln = ln
	st.reaper = proc.NewInheritedReaper(os.Getpid(), "/proc")
	st.exe, st.exeID, st.reexecOK = daemonResolveExe()

	// The watcher gets a labelled copy, so the import it runs is recorded as
	// source "import".
	var configSource relevo.ConfigSource
	if rt.Config != nil {
		configSource = rt.Config.As("import", "imported "+configDirPath)
	}
	st.watcher = relevo.NewConfigWatcher(configSource, configDirPath, os.Getenv)

	// An abandoned statement the owner cannot interrupt is ended by re-execing
	// this binary: the hook the owner calls once logs the reap and asks the
	// upgrade hook below to return true on its next tick. With re-exec
	// unavailable the hook is not wired, so the owner keeps refusing ad-hoc
	// reads without any way to end the statement -- a safe degradation.
	reapRequested := &atomic.Bool{}
	if st.reexecOK {
		var reapLogged sync.Once
		st.onAbandoned = func() {
			reapLogged.Do(func() { slog.Warn("relevo daemon: reaping an abandoned statement") })
			reapRequested.Store(true)
		}
	}
	st.reapRequested = reapRequested

	// The owner is served before the one-time passes, which then run under the
	// owner's normal write path; the sync verb executor is installed after them,
	// and only once they recorded themselves. See daemonStartup for why the
	// order is this one.
	st.srv = daemonStartup(rt, d, ln, st.onAbandoned)
	return st
}

// runLoop runs the daemon's upgrade watch and main loop: debounce the new
// identity over two ticks, preflight it, then either re-exec or refuse and
// record the refusal.
func (st *daemonServing) runLoop(ctx context.Context, stop context.CancelFunc, lock *store.DaemonLock, closeDB func(), info *store.DaemonInfo, root string, interval, autoExitAfter time.Duration) error {
	// The watcher and its hook are the daemon's whole upgrade decision:
	// debounce the new identity over two ticks, preflight it, then either
	// re-exec or refuse and record the refusal.
	// The split guard refuses a candidate that cannot read the local split:
	// following one would flap the visible config across re-execs. The
	// marker cannot appear later in this process's life, so reading it once
	// here is current for every later tick.
	up := &upgrade.Watcher{
		Path:      st.exe,
		Started:   st.exeID,
		Stat:      upgrade.ExeIdentity,
		Preflight: upgrade.SplitGuardPreflight(st.rt.DB != nil && st.rt.DB.SplitDone()),
	}

	upgrader := &daemonUpgrader{
		reaper:        st.reaper,
		reexecOK:      st.reexecOK,
		reapRequested: st.reapRequested,
		up:            up,
		info:          info,
		exe:           st.exe,
		st:            st.rt.Store,
	}
	hook := upgrader.tick

	// The idle watcher is the CLI-spawned daemon's own exit: the flag is
	// internal, so a service-managed or hand-started daemon never gets it. It
	// cancels the context once the daemon has been idle past the period, which
	// Run reports as a clean shutdown.
	var running atomic.Pointer[relevo.Daemon]
	watchDaemonIdleOn(ctx, stop, autoExitAfter, root, lock, st.srv, st.rt.Store, st.rt.Now, &running)

	slog.Info("relevo daemon starting", "interval", interval)
	daemon := relevo.NewDaemon(*st.rt, interval).WithRefresh(st.watcher.Refresh).WithUpgrade(hook)
	// A gate this daemon records has to reach the other machines now, not at
	// the end of the export debounce, so the runtime it was built from carries
	// the daemon's own trigger. The daemon is bound afterwards, because the
	// trigger is the daemon asking itself for an attempt.
	daemon.BindSyncNow(ctx)
	// The verb hook was installed before this Daemon existed, because the owner
	// is served before it is built. Rebind it now with the guard: from here a
	// verb and a seal or idle tick are serialized against each other, which is
	// what keeps one machine's markers from being written twice at once.
	installSyncVerbSerializing(ctx, daemon, st.srv, st.d)
	running.Store(daemon)
	err := daemon.Run(ctx)
	if errors.Is(err, relevo.ErrReexec) {
		// Drain the owner first: an open transaction must be able to commit
		// before closeDB takes the handle away. Then close explicitly what must
		// not survive the exec -- the DB, the signal context and the lock --
		// and pass the listener's descriptor with close-on-exec cleared so the
		// next image adopts it instead of rebinding.
		return daemonReexec(st.srv, st.ln, closeDB, stop, lock, st.exe)
	}

	// A clean shutdown removes the record; the re-exec path above must not.
	_ = st.rt.Store.RemoveDaemonInfo()
	return err
}
