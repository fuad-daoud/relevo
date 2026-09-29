// Package store owns relevo's on-disk state: the bindings and the append-only
// round log.
package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
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

// ValidTicket reports whether s is a valid stored --ticket form: "#N" or
// "owner/repo#N", where N is 1..9 digits with no leading zero, each of owner
// and repo is 1..100 chars of [A-Za-z0-9._-], and the whole is at most 128
// bytes.
func ValidTicket(s string) error {
	const errText = "ticket: #N or owner/repo#N"

	if len(s) == 0 || len(s) > 128 {
		return fmt.Errorf("%s", errText)
	}
	hash := strings.LastIndex(s, "#")
	if hash < 0 {
		return fmt.Errorf("%s", errText)
	}
	num, prefix := s[hash+1:], s[:hash]
	if !validIssueNumber(num) {
		return fmt.Errorf("%s", errText)
	}
	if prefix == "" {
		return nil
	}
	if !validOwnerRepo(prefix) {
		return fmt.Errorf("%s", errText)
	}
	return nil
}

// ParseTicket turns one of the four accepted --ticket input forms into the
// stored form: a bare number ("N" or "#N"), "owner/repo#N", or a
// ".../issues/N" URL. ownerRepo ("owner/repo", as git.OwnerRepo returns, or
// "") fills the repository prefix when the input names none; a repository
// typed into the input wins over the hint.
func ParseTicket(raw, ownerRepo string) (string, error) {
	const errText = "ticket: a number, #N, owner/repo#N, or a .../issues/N URL"

	repo, num, err := splitTicket(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("%s", errText)
	}
	if repo == "" {
		repo = ownerRepo
	}
	stored := "#" + num
	if repo != "" {
		stored = repo + "#" + num
	}
	if err := ValidTicket(stored); err != nil {
		return "", fmt.Errorf("%s", errText)
	}
	return stored, nil
}

// splitTicket splits an accepted input form into its repository ("" when the
// input names none) and its issue number.
func splitTicket(s string) (repo, num string, err error) {
	trimmed := strings.TrimRight(s, "/")
	if i := strings.LastIndex(trimmed, "/issues/"); i >= 0 {
		num = trimmed[i+len("/issues/"):]
		if !validIssueNumber(num) {
			return "", "", errTicketForm
		}
		repo = ownerRepoFromURL(trimmed[:i])
		if repo == "" {
			return "", "", errTicketForm
		}
		return repo, num, nil
	}
	if hash := strings.LastIndex(s, "#"); hash >= 0 {
		prefix, num := s[:hash], s[hash+1:]
		if !validIssueNumber(num) {
			return "", "", errTicketForm
		}
		if prefix == "" {
			return "", num, nil
		}
		if !validOwnerRepo(prefix) {
			return "", "", errTicketForm
		}
		return prefix, num, nil
	}
	if validIssueNumber(s) {
		return "", s, nil
	}
	return "", "", errTicketForm
}

var errTicketForm = fmt.Errorf("ticket: unrecognised form")

// ownerRepoFromURL returns "owner/repo" from the last two path segments of a
// URL, or "" when the URL does not carry them.
func ownerRepoFromURL(raw string) string {
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	slash := strings.Index(s, "/")
	if slash < 0 {
		return ""
	}
	segs := strings.Split(strings.Trim(s[slash+1:], "/"), "/")
	if len(segs) < 2 {
		return ""
	}
	owner, repo := segs[len(segs)-2], segs[len(segs)-1]
	if !validOwnerRepoName(owner) || !validOwnerRepoName(repo) {
		return ""
	}
	return owner + "/" + repo
}

// validOwnerRepo reports whether s is exactly "owner/repo" with both parts
// valid.
func validOwnerRepo(s string) bool {
	owner, repo, ok := strings.Cut(s, "/")
	if !ok || strings.Contains(repo, "/") {
		return false
	}
	return validOwnerRepoName(owner) && validOwnerRepoName(repo)
}

// validOwnerRepoName reports whether s is 1..100 chars of [A-Za-z0-9._-].
func validOwnerRepoName(s string) bool {
	if len(s) < 1 || len(s) > 100 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// validIssueNumber reports whether n is 1..9 digits with no leading zero.
func validIssueNumber(n string) bool {
	if len(n) < 1 || len(n) > 9 || n[0] == '0' {
		return false
	}
	for i := 0; i < len(n); i++ {
		if n[i] < '0' || n[i] > '9' {
			return false
		}
	}
	return true
}

// RemoteLink names the other copy of a remote binding: the installation that
// holds it and that installation's record id. A binding created by a new client
// against a new server carries one on both sides -- the client's row points at
// the server's record, the server's row at the client's -- so a reader can
// tell the two copies apart from one binding the mastermind drives.
type RemoteLink struct {
	// Installation is the other installation's id, the value in its rows'
	// origin column.
	Installation string `json:"installation,omitempty"`
	// ID is the other installation's binding_record id.
	ID string `json:"id,omitempty"`
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
