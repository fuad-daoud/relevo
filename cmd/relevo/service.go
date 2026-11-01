package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/serviceunit"
	"github.com/fuad-daoud/relevo/internal/shipped"
)

// newServiceRunner is the init-system seam: tests swap it so go test never
// execs systemctl or launchctl.
var newServiceRunner = func() serviceunit.Runner { return serviceunit.OSRunner{} }

// serviceFlagSet exists so the registry's parity test finds exactly one
// installer.
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

// refuseInEnvSandbox keeps an env sandbox off the host's service: the sandbox
// moves XDG paths, not the host's managers, so acting here would land on the
// developer's own relevo.service and put the host daemon on the sandbox's
// untagged binary.
func refuseInEnvSandbox(verb string) error {
	if !inEnvSandbox() {
		return nil
	}
	return fail(codeRefused, "relevo service %s: refused inside env sandbox %q; it would act on the host's %s",
		verb, os.Getenv("RELEVO_SANDBOX"), serviceunit.ServiceName)
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
	if err := refuseInEnvSandbox("install"); err != nil {
		return err
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

// installServiceUnit writes the unit and starts it; an edited unit is kept
// unless force.
func installServiceUnit(u unitSpec, r serviceunit.Runner, force bool) (ServiceDoc, error) {
	act, err := u.decide(shipped.UnitBefore)
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
	if err := refuseInEnvSandbox("uninstall"); err != nil {
		return err
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
// a unit that is not running is only a warning.
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
