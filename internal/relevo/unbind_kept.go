package relevo

import (
	"errors"
	"fmt"
	"strings"
)

// worktreeKeptError is ErrWorktreeKept with the wording of one refusal.
type worktreeKeptError struct {
	msg    string
	reason string
}

func (e *worktreeKeptError) Error() string { return e.msg }

func (e *worktreeKeptError) Unwrap() error { return ErrWorktreeKept }

// NewWorktreeKept builds an ErrWorktreeKept refusal that reads as msg.
func NewWorktreeKept(msg string) error { return &worktreeKeptError{msg: msg} }

func newWorktreeKeptWithReason(msg, reason string) error {
	return &worktreeKeptError{msg: msg, reason: reason}
}

// KeptReasonOf is the reason an ErrWorktreeKept refusal carries, or "".
func KeptReasonOf(err error) string {
	var ke *worktreeKeptError
	if errors.As(err, &ke) {
		return ke.reason
	}

	return ""
}

// KeptHint is the one next step after a kept-worktree refusal, for a caller
// that prints the refusal raw.
func KeptHint(name string) string {
	return fmt.Sprintf("commit or discard the work and unbind again, or `relevo unbind %s --force` archives it anyway", name)
}

// keptMarker opens the reason in a server's kept message.
const keptMarker = " kept ("

// keptTailMarkers start the clauses after the reason that carry their own
// parentheses, which would otherwise end the reason at the wrong ")". An old
// server's force hints carry none, so the last ")" already cuts them.
var keptTailMarkers = []string{"; its builder process"}

// KeptReason extracts the bare reason from a server's kept message, whether
// it is the short form or the old long form with hints.
func KeptReason(msg string) string {
	text := strings.TrimSpace(msg)
	start := strings.Index(text, keptMarker)
	if start < 0 {
		return text
	}
	rest := text[start+len(keptMarker):]
	for _, m := range keptTailMarkers {
		if i := strings.Index(rest, m); i >= 0 {
			rest = rest[:i]
		}
	}
	end := strings.LastIndex(rest, ")")
	if end < 0 {
		return text
	}
	if reason := strings.TrimSpace(rest[:end]); reason != "" {
		return reason
	}

	return text
}
