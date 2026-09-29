//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

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

// daemonArgv is the command line of the daemon relevo spawns itself.
func daemonArgv(exe string) []string { return []string{exe, "daemon"} }

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
