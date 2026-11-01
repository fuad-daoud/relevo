package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/fuad-daoud/relevo/internal/release"
)

type versionFlagValues struct {
	asJSON *bool
}

// cmdVersion prints the version line, or the same string as a document with
// --json. Positionals are refused so extra arguments are never silently
// ignored.
func cmdVersion(args []string) error {
	fs := flag.NewFlagSet("relevo version", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := versionFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return fail(codeUsage, "version takes no arguments, got %v", fs.Args())
	}
	if *v.asJSON {
		return printDoc(versionDoc{Version: buildVersion()})
	}
	fmt.Printf("relevo %s\n", buildVersion())
	return nil
}

// buildVersion prefers the ldflags stamp a release build carries, then the
// module version `go install` records, and admits to being an untagged build
// rather than inventing a number.
func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}

// releaseInputs gathers what release.Detect needs: the version buildVersion
// chose and where the executable lives. Disk reads only -- the check never
// touches the network.
func releaseInputs() release.Inputs {
	in := release.Inputs{Version: buildVersion(), Distribution: distribution}

	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			in.ExeDir = filepath.Dir(resolved)
		}
	}

	// FromModule is true only when the ldflags stamp was empty and the module
	// version was not: exactly buildVersion's own fallback order.
	if version == "" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
			in.FromModule = true
			in.VCS = release.HasVCSRevision(info.Settings)
		}
	}
	return in
}

// statusNotice is the line `relevo status` prints when a newer release exists,
// and "" whenever it does not. Pure: every input is an argument, so it is
// table-tested without a store or a network. It returns "" for every SevOK row
// of the doctor's release table, so status and doctor agree.
func statusNotice(running, latest string, ok bool, kind release.Kind) string {
	if !ok || kind == release.KindUnknown || kind == release.KindLocalBuild {
		return ""
	}
	if !release.NewerStrings(running, latest) {
		return ""
	}
	return fmt.Sprintf("relevo %s is behind %s -- run relevo doctor", running, latest)
}
