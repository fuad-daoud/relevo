//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// daemonLabel is the launchd label the Makefile's plist installs.
const daemonLabel = "com.github.fuad-daoud.relevo"

// daemonStarter is the auto-start seam a test replaces, so no test starts a
// real process.
var daemonStarter = startDaemon

// startDaemon brings the owner up: the installed systemd user unit, the
// installed launchd plist, or a detached relevo daemon of our own.
func startDaemon() error {
	if relevoUnitInstalled() {
		return exec.Command("systemctl", "--user", "start", "--no-block", "relevo").Run()
	}
	if daemonPlistInstalled() {
		return exec.Command("launchctl", "kickstart",
			"gui/"+strconv.Itoa(os.Getuid())+"/"+daemonLabel).Run()
	}
	return spawnDetachedDaemon()
}

// relevoUnitInstalled reports whether the systemd user unit is installed. The
// path resolves through userConfigRoot, so a temporary HOME isolates it.
func relevoUnitInstalled() bool {
	dir, err := userConfigRoot()
	if err != nil {
		return false
	}
	return fileExists(filepath.Join(dir, "systemd", "user", "relevo.service"))
}

// daemonPlistInstalled reports whether the launchd plist is installed, under
// the temporary HOME too.
func daemonPlistInstalled() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return fileExists(filepath.Join(home, "Library", "LaunchAgents", daemonLabel+".plist"))
}

// daemonAutoExitAfter is how long a CLI-spawned daemon waits, with nothing to
// do, before it exits. It is internal: only the daemon the CLI spawns gets it,
// so a service-managed or hand-started daemon never auto-exits.
const daemonAutoExitAfter = 10 * time.Minute

// daemonArgv is the command line of the daemon relevo spawns itself.
func daemonArgv(exe string) []string {
	return []string{exe, "daemon", "--auto-exit-after", daemonAutoExitAfter.String()}
}

// daemonLogPath is where a spawned daemon appends its output.
func daemonLogPath(root string) string { return filepath.Join(root, "daemon.log") }

// detachAttrs puts the spawned daemon in its own session, so it survives the
// CLI that started it and never lives in a harness's process scope.
func detachAttrs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// spawnDetachedDaemon starts relevo daemon in its own session, with stdin from
// /dev/null and output appended to the state root's daemon.log.
func spawnDetachedDaemon() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("relevo daemon: resolve executable: %w", err)
	}
	root, err := store.DefaultRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, store.StateRootMode); err != nil {
		return fmt.Errorf("relevo daemon: create %s: %w", root, err)
	}
	log, err := os.OpenFile(daemonLogPath(root), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("relevo daemon: open log: %w", err)
	}
	defer func() { _ = log.Close() }()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("relevo daemon: open %s: %w", os.DevNull, err)
	}
	defer func() { _ = devnull.Close() }()

	argv := daemonArgv(exe)
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = devnull
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = detachAttrs()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("relevo daemon: start: %w", err)
	}
	return nil
}

// The stop seams: a test replaces them, so no test ever signals a process or
// opens a real daemon lock. daemonStopRunning defaults to the daemon lock over
// the default root, which is what tells a started daemon from a dead one.
var (
	daemonStopSignal  = func(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
	daemonStopRunning = func(root string) (bool, error) { return store.New(root).DaemonRunning() }
	daemonStopSleep   = time.Sleep
)

const (
	// daemonStopBound is how long daemonStop waits for the lock to clear after
	// SIGTERM before it gives up and reports.
	daemonStopBound = 5 * time.Second
	// daemonStopPoll is how often daemonStop re-checks the lock.
	daemonStopPoll = 50 * time.Millisecond
)

// daemonStop stops the daemon this CLI started: the one recorded in this root's
// daemon.json and holding the daemon lock. It sends SIGTERM to the recorded pid
// and waits, bounded, for the lock to clear. A service-managed daemon is
// refused, so the stop never fights the service manager's restart policy, and
// there is no SIGKILL and no escalation.
func daemonStop() error {
	if relevoUnitInstalled() {
		return errors.New("relevo daemon: the systemd user unit owns the daemon; stop it with systemctl --user stop relevo")
	}
	if daemonPlistInstalled() {
		return fmt.Errorf("relevo daemon: the launchd agent owns the daemon; stop it with launchctl kill SIGTERM gui/%d/%s", os.Getuid(), daemonLabel)
	}

	root, err := store.DefaultRoot()
	if err != nil {
		return err
	}
	info, ok, err := store.New(root).ReadDaemonInfo()
	if err != nil {
		return err
	}
	if !ok {
		fmt.Println("no daemon is running")
		return nil
	}
	running, err := daemonStopRunning(root)
	if err != nil {
		return err
	}
	if !running {
		fmt.Println("no daemon is running")
		return nil
	}

	if err := daemonStopSignal(info.PID); err != nil {
		// A pid already gone is the friendly outcome: there is nothing left to
		// stop, and no signal was delivered.
		if errors.Is(err, syscall.ESRCH) {
			fmt.Println("no daemon is running")
			return nil
		}
		return fmt.Errorf("relevo daemon: signal %d: %w", info.PID, err)
	}

	// The loop's nominal wait, not the wall clock, is the bound: each pass
	// sleeps one poll, so a real run waits about daemonStopBound and a test
	// that no-ops the sleep still terminates at the same poll count.
	for waited := time.Duration(0); waited < daemonStopBound; waited += daemonStopPoll {
		running, err := daemonStopRunning(root)
		if err != nil {
			return err
		}
		if !running {
			fmt.Printf("stopped daemon %d\n", info.PID)
			return nil
		}
		daemonStopSleep(daemonStopPoll)
	}
	return fmt.Errorf("relevo daemon: pid %d did not stop within %s", info.PID, daemonStopBound)
}
