package main

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/serviceunit"
)

// launchdLabel names the macOS agent; the Makefile's LABEL is the same string.
const launchdLabel = "com.github.fuad-daoud.relevo"

// unitSpec is where the daemon's service unit lives on this OS and what the
// binary ships for it. goos, bin and home are fields so a test can pick the
// platform without running on it.
type unitSpec struct {
	goos string
	path string
	bin  string
	home string
}

func (u unitSpec) darwin() bool { return u.goos == "darwin" }

// newUnitSpec composes the unit path from the config root the rest of relevo
// resolves, so XDG_CONFIG_HOME moves the unit too.
func newUnitSpec(goos, configRoot, home, bin string) unitSpec {
	u := unitSpec{goos: goos, bin: bin, home: home}
	if u.darwin() {
		u.path = filepath.Join(home, "Library", "LaunchAgents", launchdLabel+".plist")
	} else {
		u.path = filepath.Join(configRoot, "systemd", "user", serviceunit.ServiceName)
	}
	return u
}

func osUnitSpec() (unitSpec, error) {
	configRoot, err := userConfigRoot()
	if err != nil {
		return unitSpec{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return unitSpec{}, fmt.Errorf("resolve user home directory: %w", err)
	}
	bin, err := os.Executable()
	if err != nil {
		return unitSpec{}, fmt.Errorf("resolve executable: %w", err)
	}
	return newUnitSpec(runtime.GOOS, configRoot, home, bin), nil
}

// doc is the name the shipped index files this OS's unit under.
func (u unitSpec) doc() string {
	if u.darwin() {
		return serviceunit.PlistName
	}
	return serviceunit.ServiceName
}

// lookPath is where the service template's PATH reads the harness homes. It
// is a seam so tests pin the order and the dedup without shell PATH lookups.
var lookPath = exec.LookPath

// servicePath assembles the daemon's PATH: the fixed list the unit has
// always carried, in %h form because systemd expands the specifiers at exec
// and the rendered unit has to keep naming %h/go/bin (where go installs the
// CLIs the daemon runs lint with), then the directory of every harness
// binary LookPath finds, deduplicated, in harness.All() order.
func servicePath(home string) string {
	prefix := "%h/.local/bin:%h/go/bin:/usr/local/bin:/usr/bin:/bin"
	// The %h entries expand to the same homes the harness LookPath can find,
	// so the dedup must see the expanded forms to keep a harness home from
	// appearing twice in the rendered value.
	seen := map[string]bool{
		filepath.Join(home, ".local", "bin"): true,
		filepath.Join(home, "go", "bin"):     true,
		"/usr/local/bin":                     true,
		"/usr/bin":                           true,
		"/bin":                               true,
	}
	var extra []string
	for _, h := range harness.All() {
		found, err := lookPath(h.Binary)
		if err != nil {
			continue
		}
		dir := filepath.Dir(found)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		extra = append(extra, dir)
	}
	if len(extra) == 0 {
		return prefix
	}
	return prefix + ":" + strings.Join(extra, ":")
}

// shipped is what the index and the decision compare against: the unit as a
// template, since its rendered form differs per machine.
func (u unitSpec) shipped() []byte {
	if u.darwin() {
		return serviceunit.ShippedPlistTemplate()
	}
	return serviceunit.ShippedService()
}

// rendered is what gets written to u.path.
func (u unitSpec) rendered() []byte {
	if u.darwin() {
		return serviceunit.RenderPlist(serviceunit.ShippedPlistTemplate(), u.bin, u.home)
	}
	return serviceunit.RenderService(serviceunit.ShippedService(), u.bin, servicePath(u.home))
}

// read returns the installed bytes in the form the index hashes, and the
// file's mtime. A missing file is present=false, not an error.
func (u unitSpec) read() (data []byte, mtime time.Time, present bool, err error) {
	data, err = os.ReadFile(u.path)
	if os.IsNotExist(err) {
		return nil, time.Time{}, false, nil
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	if st, serr := os.Stat(u.path); serr == nil {
		mtime = st.ModTime()
	}
	if u.darwin() {
		data = serviceunit.CanonicalPlist(data, u.bin, u.home)
	} else {
		data = serviceunit.CanonicalService(data, u.bin, servicePath(u.home))
	}
	return data, mtime, true, nil
}

func (u unitSpec) decide(before func(doc, sha string) bool) (serviceunit.Action, error) {
	data, _, present, err := u.read()
	if err != nil {
		return "", err
	}
	return serviceunit.DecideUnitAction(u.doc(), data, present, u.shipped(), before), nil
}

// write replaces the unit by rename so a reader never sees half a file.
func (u unitSpec) write() error {
	dir := filepath.Dir(u.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".relevo-unit-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(u.rendered()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), u.path)
}

// refreshServiceUnit rewrites a unit an older release shipped unedited and
// leaves every other state alone. It never restarts the daemon: the new unit
// takes effect at the next restart, which `relevo doctor` says is due.
func refreshServiceUnit(u unitSpec, r serviceunit.Runner, before func(doc, sha string) bool) (serviceunit.Action, error) {
	act, err := u.decide(before)
	if err != nil {
		return "", err
	}
	if act != serviceunit.ActionStale {
		return act, nil
	}
	if err := u.write(); err != nil {
		return act, err
	}
	if !u.darwin() {
		if err := r.DaemonReload(); err != nil {
			return act, err
		}
	}
	return act, nil
}

// refreshServiceUnitAtStart is the daemon's once-per-image-start refresh,
// never fatal like refreshRoles: the daemon runs fine on an old unit.
func refreshServiceUnitAtStart() {
	u, err := osUnitSpec()
	if err != nil {
		slog.Warn("service unit not refreshed", "err", err)
		return
	}
	act, err := refreshServiceUnit(u, serviceunit.OSRunner{}, serviceunit.ShippedUnitBefore)
	if err != nil {
		slog.Warn("service unit not refreshed", "path", u.path, "err", err)
		return
	}
	switch act {
	case serviceunit.ActionStale:
		slog.Info("service unit refreshed; restart relevo.service to run it", "path", u.path)
	case serviceunit.ActionEdited:
		slog.Info(fmt.Sprintf("%s was edited; relevo service install --force replaces it", u.path))
	}
}

// unitEnv gives the doctor the installed unit, whose path only this package
// composes.
type unitEnv struct{ doctor.Env }

func (unitEnv) ServiceUnit() (doctor.ServiceUnit, error) {
	u, err := osUnitSpec()
	if err != nil {
		return doctor.ServiceUnit{}, err
	}
	data, mtime, present, err := u.read()
	if err != nil {
		return doctor.ServiceUnit{}, err
	}
	return doctor.ServiceUnit{Doc: u.doc(), Installed: data, Shipped: u.shipped(), MTime: mtime, Present: present}, nil
}
