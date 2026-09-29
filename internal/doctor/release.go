package doctor

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/release"
)

func parseSemver(s string) (major, minor, patch int, err error) {
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return 0, 0, 0, fmt.Errorf("invalid semver: %s", s)
	}
	major, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, 0, err
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, 0, err
	}
	if len(parts) >= 3 {
		patchStr := parts[2]
		if idx := strings.IndexAny(patchStr, "-+"); idx != -1 {
			patchStr = patchStr[:idx]
		}
		patch, err = strconv.Atoi(patchStr)
		if err != nil {
			return 0, 0, 0, err
		}
	}
	return major, minor, patch, nil
}

func semverAtLeast(v, floor string) (bool, error) {
	maj1, min1, pat1, err := parseSemver(v)
	if err != nil {
		return false, err
	}
	maj2, min2, pat2, err := parseSemver(floor)
	if err != nil {
		return false, err
	}
	if maj1 != maj2 {
		return maj1 > maj2, nil
	}
	if min1 != min2 {
		return min1 > min2, nil
	}
	return pat1 >= pat2, nil
}

// releaseCheck is the one row about relevo itself: which install this is,
// and whether the daemon's cached check has seen a newer release. Never
// SevFail -- a stale relevo runs fine -- and SevOK whenever relevo cannot
// prove anything (unrefreshed cache, offline, unclassifiable, or a (devel)
// build).
func releaseCheck(env Env) Check {
	running, latest, ok, kind := env.ReleaseState()

	if !ok || kind == release.KindUnknown {
		return Check{Name: "release", Severity: SevOK, Detail: "not checked"}
	}
	if _, rok := release.ParseVersion(running); !rok {
		return Check{Name: "release", Severity: SevOK, Detail: "not checked"}
	}
	if _, lok := release.ParseVersion(latest); !lok {
		return Check{Name: "release", Severity: SevOK, Detail: "not checked"}
	}
	if kind == release.KindLocalBuild {
		return Check{Name: "release", Severity: SevOK, Detail: fmt.Sprintf("local build %s; nothing to update to", running)}
	}
	if !release.NewerStrings(running, latest) {
		return Check{Name: "release", Severity: SevOK, Detail: fmt.Sprintf("%s is current", running)}
	}
	return Check{
		Name:     "release",
		Severity: SevWarn,
		Detail:   fmt.Sprintf("%s is behind %s", running, latest),
		Fix:      releaseFix(kind, latest, runtime.GOOS, runtime.GOARCH),
	}
}

// releaseFix names the update path of the variant that is actually
// installed. KindUnknown and KindLocalBuild never reach a Fix: both are SevOK.
func releaseFix(kind release.Kind, latest, goos, goarch string) string {
	switch kind {
	case release.KindGoInstall:
		return "go install github.com/fuad-daoud/relevo/cmd/relevo@latest"
	case release.KindRelease:
		archive, checksums := release.AssetURLs(latest, goos, goarch)
		return fmt.Sprintf("relevo update (or download %s, check it against %s, and replace this relevo binary with the one inside)", archive, checksums)
	}
	return ""
}

// daemonCheck builds the daemon row while the daemon is running. Four
// states: no record (predates version tracking), a binary the daemon
// refused, a version behind the CLI (transient during a re-exec), and equal.
func daemonCheck(env Env) Check {
	cli, _, _, _ := env.ReleaseState()
	info, ok, err := env.DaemonInfo()
	if err != nil {
		ok = false // unreadable is "no record": tell the human to restart once
	}

	switch {
	case !ok:
		return Check{
			Name:     "daemon",
			Severity: SevWarn,
			Detail:   "running, but started before relevo recorded its version: it will not follow upgrades until restarted once",
			Fix:      "systemctl --user restart relevo.service, or make service",
		}
	case info.ReexecFailed != nil:
		return Check{
			Name:     "daemon",
			Severity: SevWarn,
			Detail: fmt.Sprintf("runs %s; the relevo binary at %s failed preflight (%s) and was not loaded",
				info.Version, info.Exe, info.ReexecFailed.Reason),
			Fix: "fix the error above; the daemon retries when the file changes",
		}
	case info.Version != cli:
		return Check{
			Name:     "daemon",
			Severity: SevOK,
			Detail:   fmt.Sprintf("runs %s; switching to %s within seconds", info.Version, cli),
		}
	default:
		return Check{
			Name:     "daemon",
			Severity: SevOK,
			Detail:   fmt.Sprintf("running %s", info.Version),
		}
	}
}
