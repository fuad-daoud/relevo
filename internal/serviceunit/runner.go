package serviceunit

import (
	"fmt"
	"os/exec"
)

// Runner drives the init system. Tests fake it; nothing else may exec
// systemctl or launchctl.
type Runner interface {
	DaemonReload() error
	Enable() error
	Restart() error
	// Load starts the launchd agent at plistPath and keeps it enabled.
	Load(plistPath string) error
	// Unload stops the unit and disables it: systemd disable --now, or
	// launchctl unload of plistPath.
	Unload(plistPath string) error
}

// OSRunner runs the real systemctl --user or launchctl.
type OSRunner struct{}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w: %s", name, args, err, out)
	}
	return nil
}

func (OSRunner) DaemonReload() error { return run("systemctl", "--user", "daemon-reload") }
func (OSRunner) Enable() error       { return run("systemctl", "--user", "enable", "relevo.service") }
func (OSRunner) Restart() error      { return run("systemctl", "--user", "restart", "relevo.service") }
func (OSRunner) Load(plistPath string) error {
	return run("launchctl", "load", "-w", plistPath)
}

func (OSRunner) Unload(plistPath string) error {
	if plistPath != "" {
		return run("launchctl", "unload", plistPath)
	}
	return run("systemctl", "--user", "disable", "--now", "relevo.service")
}
