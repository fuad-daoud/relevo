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
func (h Harness) Resume(sessionID, prompt string, tier Tier) ([]string, error) {
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
// session: the builder-grade print form l with the prompt, budget, directory
// and state filled in, followed by this kind's resume selector. l must be the
// Launch the round was started with, so a resumed round keeps its model, agent
// definition and permission flags. A kind with no verified resume selector
// returns ErrResumeUnsupported, which the caller reads as "relaunch".
func (h Harness) ResumeBuild(sessionID string, l Launch, prompt string, budget time.Duration, dir, state string) ([]string, error) {
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
	default:
		return nil, fmt.Errorf("%w: %s", ErrResumeUnsupported, h.Kind)
	}

	return append(l.PrintArgs(prompt, budget, dir, state), selector...), nil
}

// DeleteSession renders the argv that deletes an existing harness session. The
// returned slice is the argv after the binary, as for Resume. opencode's
// --standalone matters: going through the background service could start that
// service, and on startup it resumes every session marked running. A kind
// whose sessions end with their process has nothing to delete.
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
