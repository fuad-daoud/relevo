package release

import "fmt"

// UpdateAction is what `relevo update` should do, decided by DecideUpdate
// before anything touches the network or the disk.
type UpdateAction int

const (
	UpdateCurrent        UpdateAction = iota // the running version is already the target
	UpdateReplace                            // download the target and replace the running binary
	UpdatePrintGoInstall                     // a `go install` binary: print the command
	UpdateRefuse                             // a local or unknown build without --release
	UpdateInvalid                            // a bad --to value: a usage error
)

// String names the action for `--check` output.
func (a UpdateAction) String() string {
	switch a {
	case UpdateCurrent:
		return "current"
	case UpdateReplace:
		return "replace"
	case UpdatePrintGoInstall:
		return "go-install"
	case UpdateRefuse:
		return "refuse"
	case UpdateInvalid:
		return "invalid"
	}
	return "unknown"
}

// UpdateRequest is every fact DecideUpdate reads.
type UpdateRequest struct {
	Kind         Kind   // release.Detect's classification of the running binary
	Running      string // buildVersion(): a clean tag, a describe string or "(devel)"
	Latest       string // latest published tag, or "" if the caller skipped the fetch
	To           string // the --to value, "" if not given; a leading "v" is optional
	ForceRelease bool   // --release: replace a local or unknown build anyway
}

// UpdateDecision is the pure outcome: what to do, the normalised tag to
// install or print, and the one sentence the CLI prints as is.
type UpdateDecision struct {
	Action  UpdateAction
	Target  string
	Message string
}

// DecideUpdate answers, without touching the network or the disk, what
// `relevo update` should do for req. Rules are ordered; the first match wins.
func DecideUpdate(req UpdateRequest) UpdateDecision {
	target := req.Latest
	if req.To != "" {
		target = req.To
		if target[0] != 'v' {
			target = "v" + target
		}
		if !IsReleaseTag(target) {
			return UpdateDecision{
				Action:  UpdateInvalid,
				Message: fmt.Sprintf(`--to must be a release tag like v0.13.0, got %q`, req.To),
			}
		}
	} else if req.Latest != "" && !IsReleaseTag(req.Latest) {
		// The latest tag becomes a URL path segment, so a malformed one is
		// refused here for every kind, before it can be downloaded or printed.
		return UpdateDecision{
			Action:  UpdateRefuse,
			Message: fmt.Sprintf("the latest release tag %q is not a release tag like v0.13.0; nothing was downloaded", req.Latest),
		}
	}

	switch req.Kind {
	case KindGoInstall:
		display := target
		if display == "" {
			display = "latest"
		}
		return UpdateDecision{
			Action:  UpdatePrintGoInstall,
			Target:  display,
			Message: "go install github.com/fuad-daoud/relevo/cmd/relevo@" + display,
		}
	case KindLocalBuild, KindUnknown:
		if !req.ForceRelease {
			return UpdateDecision{Action: UpdateRefuse, Message: localBuildRefusal(req.Running)}
		}
		if target == "" {
			return UpdateDecision{Action: UpdateRefuse, Message: "no release tag to update to"}
		}
		return replaceTo(req.Running, target)
	case KindRelease:
		if target == "" {
			return UpdateDecision{Action: UpdateRefuse, Message: "no release tag to update to"}
		}
		if req.To != "" {
			// An explicit --to allows a downgrade, so equal versions (numbers
			// and suffix) is the only case that is a no-op.
			rv, rok := ParseVersion(req.Running)
			tv, tok := ParseVersion(target)
			if rok && tok && rv == tv {
				return currentAt(req.Running, target)
			}
			return replaceTo(req.Running, target)
		}
		if NewerStrings(req.Running, target) {
			return replaceTo(req.Running, target)
		}
		return currentAt(req.Running, target)
	}

	return UpdateDecision{Action: UpdateRefuse, Message: localBuildRefusal(req.Running)}
}

// localBuildRefusal is the one sentence rule 3a and rule 5 share.
func localBuildRefusal(running string) string {
	return fmt.Sprintf("relevo %s is a local build; relevo update replaces only release binaries. relevo update --release replaces it with the latest release binary.", running)
}

func replaceTo(running, target string) UpdateDecision {
	return UpdateDecision{
		Action:  UpdateReplace,
		Target:  target,
		Message: fmt.Sprintf("relevo %s -> %s", running, target),
	}
}

func currentAt(running, target string) UpdateDecision {
	return UpdateDecision{
		Action:  UpdateCurrent,
		Target:  target,
		Message: fmt.Sprintf("relevo %s is current (latest %s)", running, target),
	}
}
