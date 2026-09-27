// Package store owns relevo's on-disk state: the bindings and the append-only
// round log.
package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// State is a binding's display and control state.
type State string

const (
	StateActive   State = "active"    // someone is working
	StateNeedsYou State = "needs_you" // stalled on a human decision
	StateBroken   State = "broken"    // the builder process is gone
	StateDone     State = "done"      // mastermind declared the work verified
	// StatePaused is the state between ACTIVE and DONE: worktree released,
	// branch and log kept, restored by `relevo bind --resume`.
	StatePaused State = "paused"
)

// Mode is the shape of a builder: a process relevo runs itself, or one hosted
// on a remote relevo server. "" is a binding from before pane builders were
// removed, kept loadable so an old bind.json still reads back unchanged.
type Mode string

const (
	ModeHeadless Mode = "headless"
	ModeRemote   Mode = "remote"
)

// Edge is read from records written before `relevo edge` was removed; nothing
// writes it.
type Edge struct {
	ID      string    `json:"id"`
	Round   int       `json:"round"`
	When    string    `json:"when"`
	Then    string    `json:"then"`
	Target  string    `json:"target"`
	Prompt  string    `json:"prompt"`
	Mode    string    `json:"mode"`
	AddedAt time.Time `json:"added_at"`
	Fired   bool      `json:"fired,omitempty"`
	FiredAt time.Time `json:"fired_at,omitempty"`
	Result  string    `json:"result,omitempty"`
}

// ValidFeature reports whether s is a valid --feature label: 1..64 bytes,
// every byte in [A-Za-z0-9._ -], no leading or trailing space.
func ValidFeature(s string) error {
	const errText = "feature: 1-64 chars of letters, digits, '.', '_', '-' and spaces"

	if len(s) == 0 || len(s) > 64 {
		return fmt.Errorf("%s", errText)
	}
	if s[0] == ' ' || s[len(s)-1] == ' ' {
		return fmt.Errorf("%s", errText)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '.', c == '_', c == ' ', c == '-':
		default:
			return fmt.Errorf("%s", errText)
		}
	}
	return nil
}

// SameBinding reports whether two bindings hold the same state, by comparing
// their serialised forms.
//
// A hand-written field-by-field Equal was rejected: it keeps compiling when a
// field is added and silently stops noticing changes to it. Comparing the
// serialised form asks what the caller means -- would this write a different
// bind.json -- and cannot drift as fields come and go. A marshal error reports
// "not the same", so a caller saves rather than skipping a write it needed.
func SameBinding(a, b Binding) bool {
	ra, err := json.Marshal(a)
	if err != nil {
		return false
	}
	rb, err := json.Marshal(b)
	if err != nil {
		return false
	}

	return bytes.Equal(ra, rb)
}
