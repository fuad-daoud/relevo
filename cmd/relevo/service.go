package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/serviceunit"
)

// newServiceRunner is the init-system seam: tests swap it so nothing under go
// test execs systemctl or launchctl.
var newServiceRunner = func() serviceunit.Runner { return serviceunit.OSRunner{} }

// serviceFlagSet declares the bare `service` dispatcher's flags: none.
func serviceFlagSet(*flag.FlagSet) {}

type serviceInstallFlagValues struct {
	force  *bool
	asJSON *bool
}

func serviceInstallFlagSet(fs *flag.FlagSet) *serviceInstallFlagValues {
	v := &serviceInstallFlagValues{}
	v.force = fs.Bool("force", false, "replace a unit that was edited by hand")
	v.asJSON = fs.Bool("json", false, "print what the install did as a JSON document")
	return v
}

type serviceUninstallFlagValues struct {
	asJSON *bool
}

func serviceUninstallFlagSet(fs *flag.FlagSet) *serviceUninstallFlagValues {
	v := &serviceUninstallFlagValues{}
	v.asJSON = fs.Bool("json", false, "print what the uninstall did as a JSON document")
	return v
}

// cmdService dispatches `relevo service install|uninstall`.
func cmdService(args []string) error {
	const usage = `usage: relevo service install [--force] [--json]
       relevo service uninstall [--json]`

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}
	switch args[0] {
	case "install":
		return cmdServiceInstall(args[1:])
	case "uninstall":
		return cmdServiceUninstall(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		return fail(codeUsage, "relevo service: unknown command %q", args[0])
	}
}

func cmdServiceInstall(args []string) error {
	fs := flag.NewFlagSet("relevo service install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := serviceInstallFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fail(codeUsage, "service install takes no arguments, got %v", fs.Args())
	}
	u, err := osUnitSpec()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	doc, err := installServiceUnit(u, newServiceRunner(), *v.force)
	if err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(doc)
	}
	fmt.Printf("relevo service: %s %s\n", doc.Outcome, doc.Unit)
	return nil
}

// installServiceUnit writes the unit and starts it. An edited unit is kept
// unless force: the user's changes are theirs.
func installServiceUnit(u unitSpec, r serviceunit.Runner, force bool) (ServiceDoc, error) {
	act, err := u.decide(serviceunit.ShippedUnitBefore)
	if err != nil {
		return ServiceDoc{}, fail(codeInternal, "%v", err)
	}
	if act == serviceunit.ActionEdited && !force {
		return ServiceDoc{}, fail(codeConflict, "%s was edited; relevo service install --force replaces it", u.path)
	}
	outcome := "unchanged"
	if act != serviceunit.ActionCurrent {
		if err := u.write(); err != nil {
			return ServiceDoc{}, fail(codeInternal, "%v", err)
		}
		outcome = "wrote"
	}
	if err := startServiceUnit(u, r); err != nil {
		return ServiceDoc{}, fail(codeInternal, "%v", err)
	}
	return serviceDocOf("install", u.path, outcome), nil
}

func startServiceUnit(u unitSpec, r serviceunit.Runner) error {
	if u.darwin() {
		// A loaded agent must be unloaded before launchd reads the new plist;
		// nothing loaded is not a failure.
		_ = r.Unload(u.path)
		return r.Load(u.path)
	}
	if err := r.DaemonReload(); err != nil {
		return err
	}
	if err := r.Enable(); err != nil {
		return err
	}
	return r.Restart()
}

func cmdServiceUninstall(args []string) error {
	fs := flag.NewFlagSet("relevo service uninstall", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := serviceUninstallFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fail(codeUsage, "service uninstall takes no arguments, got %v", fs.Args())
	}
	u, err := osUnitSpec()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	doc, err := uninstallServiceUnit(u, newServiceRunner())
	if err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(doc)
	}
	fmt.Printf("relevo service: %s %s\n", doc.Outcome, doc.Unit)
	return nil
}

// uninstallServiceUnit stops and removes the unit, never the binary. Stopping
// a unit that is not running is not a failure, so it is only a warning.
func uninstallServiceUnit(u unitSpec, r serviceunit.Runner) (ServiceDoc, error) {
	arg := ""
	if u.darwin() {
		arg = u.path
	}
	if err := r.Unload(arg); err != nil {
		fmt.Fprintf(os.Stderr, "relevo: unit not stopped: %v\n", err)
	}
	outcome := "removed"
	if err := os.Remove(u.path); err != nil {
		if !os.IsNotExist(err) {
			return ServiceDoc{}, fail(codeInternal, "%v", err)
		}
		outcome = "absent"
	}
	if !u.darwin() {
		if err := r.DaemonReload(); err != nil {
			fmt.Fprintf(os.Stderr, "relevo: daemon-reload failed: %v\n", err)
		}
	}
	return serviceDocOf("uninstall", u.path, outcome), nil
}
