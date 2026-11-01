package harness

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrResumeUnsupported reports a resume request for a harness relevo has no
// verified resume form for. The error names the kind.
var ErrResumeUnsupported = errors.New("resume is not supported for this harness")

// ErrSessionDeleteUnsupported reports a delete request for a harness whose
// sessions end with their process, so there is nothing left to delete. The
// error names the kind.
var ErrSessionDeleteUnsupported = errors.New("sessions of this harness end with their process")

// checkResumeSessionID validates a session id exactly as both resume forms
// must: it becomes one argv element, so it must be non-empty, free of
// whitespace, and not start with '-' -- a leading '-' would be read as a flag.
func checkResumeSessionID(sessionID string) error {
	if sessionID == "" {
		return errors.New("resume needs a session id")
	}
	if strings.ContainsAny(sessionID, " \t\n\r\v\f") || strings.HasPrefix(sessionID, "-") {
		return fmt.Errorf("invalid session id %q", sessionID)
	}
	return nil
}

// Resume renders the argv that continues an existing harness session with one
// more prompt. The returned slice is the argv after the binary. claude and agy
// continue the session in place; opencode forks, leaving the original untouched.
// contributorMode is the muse-only training-data guard from the candidate
// ("on", "off", or "" for unset, which renders "off"); other kinds ignore it.
func (h Harness) Resume(sessionID, prompt string, tier Tier, contributorMode string) ([]string, error) {
	if err := checkResumeSessionID(sessionID); err != nil {
		return nil, err
	}

	var base []string
	switch h.Kind {
	case "claude":
		base = []string{"-p", prompt, "--resume", sessionID,
			"--output-format", "stream-json", "--verbose"}
	case "agy":
		base = []string{"-p", prompt, "--conversation", sessionID,
			"--output-format", "stream-json"}
	case "opencode":
		base = []string{"run", prompt, "--session", sessionID, "--fork", "--format", "json", "--thinking", "--standalone"}
	case "muse":
		mode := contributorMode
		if mode == "" {
			mode = "off"
		}
		if mode != "on" && mode != "off" {
			return nil, fmt.Errorf("bad contributor_mode %q: want \"on\" or \"off\"", contributorMode)
		}
		base = []string{"exec", "--json", "--session-id", sessionID,
			"--contributor-mode", mode,
			"--disable-reminders", "--disable-request-user-input", prompt}
	case "codex":
		return nil, fmt.Errorf("%w: codex", ErrResumeUnsupported)
	default:
		return nil, fmt.Errorf("%w: harness %q", ErrResumeUnsupported, h.Kind)
	}

	perm, err := h.PermissionArgs(tier)
	if err != nil {
		return nil, err
	}
	return append(append([]string(nil), base...), perm...), nil
}

// ResumeBuild renders the argv that continues a lost headless builder's own
// session: l with its prompt fields filled in, followed by this kind's resume
// selector. l must be the Launch the round was started with, so a resumed round
// keeps its model, agent definition and permission flags. A kind with no
// verified resume selector returns ErrResumeUnsupported, read as "relaunch".
func (h Harness) ResumeBuild(sessionID string, l Launch, prompt string, budget time.Duration, dir, writableRoot, promptFile, chainDir, definition string) ([]string, error) {
	if err := checkResumeSessionID(sessionID); err != nil {
		return nil, err
	}

	var selector []string
	switch h.Kind {
	case "claude":
		selector = []string{"--resume", sessionID}
	case "agy":
		selector = []string{"--conversation", sessionID}
	case "opencode":
		selector = []string{"--session", sessionID, "--fork"}
	case "muse":
		selector = []string{"--session-id", sessionID}
	default:
		return nil, fmt.Errorf("%w: %s", ErrResumeUnsupported, h.Kind)
	}

	return append(l.PrintArgs(prompt, budget, dir, writableRoot, promptFile, chainDir, definition), selector...), nil
}

// DeleteSession renders the argv that deletes an existing harness session. The
// returned slice is the argv after the binary, as for Resume. opencode's
// --standalone matters: the background service could start and resume every
// session marked running. A kind whose sessions end with their process has
// nothing to delete.
func (h Harness) DeleteSession(sessionID string) ([]string, error) {
	if err := checkResumeSessionID(sessionID); err != nil {
		return nil, err
	}

	switch h.Kind {
	case "opencode":
		return []string{"session", "delete", "--standalone", sessionID}, nil
	default:
		return nil, fmt.Errorf("%w: harness %q", ErrSessionDeleteUnsupported, h.Kind)
	}
}
