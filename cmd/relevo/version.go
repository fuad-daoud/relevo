package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/fuad-daoud/relevo/internal/release"
)

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

// releaseInputs gathers what release.Detect needs about the running binary:
// the version buildVersion chose, whether the module rather than an ldflags
// stamp supplied it, and where the executable lives. Disk reads only -- the
// release check never touches the network to learn who it is.
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

// statusNotice is the line `relevo status` prints above the rows when the
// cached check says a newer release exists, and "" whenever it does not.
// Pure: every input is an argument, so it is table-tested without a
// store, a daemon or a network (CI launches no harness).
//
// It returns "" for every SevOK row of the doctor's release table, so the
// statusline stays quiet exactly where `relevo doctor` says "not checked",
// "nothing to update to" or "is current" -- and never claims an update
// relevo cannot prove.
func statusNotice(running, latest string, ok bool, kind release.Kind) string {
	if !ok || kind == release.KindUnknown || kind == release.KindLocalBuild {
		return ""
	}
	if !release.NewerStrings(running, latest) {
		return ""
	}
	return fmt.Sprintf("relevo %s is behind %s -- run relevo doctor", running, latest)
}
