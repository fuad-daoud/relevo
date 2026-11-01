// Package store owns relevo's on-disk state: the bindings and the append-only
// round log.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
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
// on a remote relevo server. "" is the pane-shape binding, kept loadable so an
// old bind.json still reads back unchanged.
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

// ErrInvalidFeature is ValidFeature's class: a --feature label outside the
// accepted shape. Like a binding name it is the caller's own argument, so the
// CLI classifies it as usage.
var ErrInvalidFeature = errors.New("invalid feature label")

// ErrInvalidTicket is ValidTicket's and ParseTicket's class: a --ticket
// outside the accepted input forms. The caller's own argument, so usage.
var ErrInvalidTicket = errors.New("invalid ticket")

// ValidFeature reports whether s is a valid --feature label: 1..64 bytes,
// every byte in [A-Za-z0-9._ -], no leading or trailing space.
func ValidFeature(s string) error {
	const errText = "feature: 1-64 chars of letters, digits, '.', '_', '-' and spaces"

	if len(s) == 0 || len(s) > 64 {
		return invalidOf(ErrInvalidFeature, "%s", errText)
	}
	if s[0] == ' ' || s[len(s)-1] == ' ' {
		return invalidOf(ErrInvalidFeature, "%s", errText)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '.', c == '_', c == ' ', c == '-':
		default:
			return invalidOf(ErrInvalidFeature, "%s", errText)
		}
	}
	return nil
}

// ValidTicket reports whether s is a valid stored --ticket form: "#N" or
// "project/path#N" (2..N segments, each 1..100 chars of [A-Za-z0-9._-]) at
// most 256 bytes, an issue key like ABC-123 at most 64 bytes, or an http(s)
// URL at most 512 bytes whose remainder is one printable, non-whitespace
// token. N is 1..9 digits with no leading zero.
func ValidTicket(s string) error {
	const errText = "ticket: #N, project/path#N, an issue key like ABC-123, or an http(s) URL"

	if len(s) == 0 {
		return invalidOf(ErrInvalidTicket, "%s", errText)
	}
	if validIssueKey(s) {
		if len(s) > 64 {
			return invalidOf(ErrInvalidTicket, "%s", errText)
		}
		return nil
	}
	if isOpaqueURL(s) {
		if len(s) > 512 {
			return invalidOf(ErrInvalidTicket, "%s", errText)
		}
		if !opaqueURLPrintable(s) {
			return invalidOf(ErrInvalidTicket, "%s", errText)
		}
		return nil
	}
	if len(s) > 256 {
		return invalidOf(ErrInvalidTicket, "%s", errText)
	}
	hash := strings.LastIndex(s, "#")
	if hash < 0 {
		return invalidOf(ErrInvalidTicket, "%s", errText)
	}
	num, prefix := s[hash+1:], s[:hash]
	if !validIssueNumber(num) {
		return invalidOf(ErrInvalidTicket, "%s", errText)
	}
	if prefix == "" {
		return nil
	}
	if !validProjectPath(prefix) {
		return invalidOf(ErrInvalidTicket, "%s", errText)
	}
	return nil
}

// ParseTicket turns one of the accepted --ticket input forms into the stored
// form: a bare number ("N" or "#N"), "project/path#N", an issue key like
// ABC-123 stored verbatim, or an http(s) URL stored whole. ownerRepo
// ("owner/repo", as git.OwnerRepo returns, or "") fills the repository prefix
// of the number forms when the input names none; a repository typed into the
// input wins over the hint, and the key and URL forms never take it. An input
// that looks like an issue URL but does not parse as one is stored whole when
// it carries an http(s) scheme, and refused without one.
func ParseTicket(raw, ownerRepo string) (string, error) {
	const errText = "ticket: a number, #N, project/path#N, an issue key like ABC-123, or an http(s) URL"

	s := strings.TrimSpace(raw)
	if repo, num, ok := issueURLTicket(s); ok {
		stored := repo + "#" + num
		if err := ValidTicket(stored); err != nil {
			return "", invalidOf(ErrInvalidTicket, "%s", errText)
		}
		return stored, nil
	}
	if isOpaqueURL(s) {
		if err := ValidTicket(s); err != nil {
			return "", invalidOf(ErrInvalidTicket, "%s", errText)
		}
		return s, nil
	}
	if validIssueKey(s) {
		if err := ValidTicket(s); err != nil {
			return "", invalidOf(ErrInvalidTicket, "%s", errText)
		}
		return s, nil
	}
	repo, num, err := splitTicket(s)
	if err != nil {
		return "", invalidOf(ErrInvalidTicket, "%s", errText)
	}
	if repo == "" {
		repo = ownerRepo
	}
	stored := "#" + num
	if repo != "" {
		stored = repo + "#" + num
	}
	if err := ValidTicket(stored); err != nil {
		return "", invalidOf(ErrInvalidTicket, "%s", errText)
	}
	return stored, nil
}

// issueURLTicket splits an issue URL into its project path and issue number:
// the last "/issues/" must be followed by a valid issue number, and the path
// before it, minus one trailing "-" segment (GitLab's "/-/issues/N"), must be
// a project path. ok is false when the input is not an issue URL that parses;
// the caller decides what such an input becomes.
func issueURLTicket(s string) (repo, num string, ok bool) {
	trimmed := strings.TrimRight(s, "/")
	i := strings.LastIndex(trimmed, "/issues/")
	if i < 0 {
		return "", "", false
	}
	num = trimmed[i+len("/issues/"):]
	if !validIssueNumber(num) {
		return "", "", false
	}
	repo = projectPathFromURL(trimmed[:i])
	if repo == "" {
		return "", "", false
	}
	return repo, num, true
}

// splitTicket splits an accepted number-form input into its repository (""
// when the input names none) and its issue number.
func splitTicket(s string) (repo, num string, err error) {
	if hash := strings.LastIndex(s, "#"); hash >= 0 {
		prefix, num := s[:hash], s[hash+1:]
		if !validIssueNumber(num) {
			return "", "", errTicketForm
		}
		if prefix == "" {
			return "", num, nil
		}
		if !validProjectPath(prefix) {
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

// projectPathFromURL returns the project path of an issue URL's prefix: the
// URL's path after the host, or the whole prefix when it carries no scheme
// ("owner/repo" is then already the path). GitLab's trailing "-" project
// segment is dropped. It returns "" when the result is not a project path of
// at least two segments.
func projectPathFromURL(raw string) string {
	s := raw
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		slash := strings.Index(s, "/")
		if slash < 0 {
			return ""
		}
		s = s[slash+1:]
	}
	segs := strings.Split(strings.Trim(s, "/"), "/")
	if len(segs) > 0 && segs[len(segs)-1] == "-" {
		segs = segs[:len(segs)-1]
	}
	if len(segs) < 2 {
		return ""
	}
	for _, seg := range segs {
		if !validOwnerRepoName(seg) {
			return ""
		}
	}
	return strings.Join(segs, "/")
}

// validProjectPath reports whether s is a /-joined path of at least two
// segments, each validOwnerRepoName.
func validProjectPath(s string) bool {
	segs := strings.Split(s, "/")
	if len(segs) < 2 {
		return false
	}
	for _, seg := range segs {
		if !validOwnerRepoName(seg) {
			return false
		}
	}
	return true
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

// isOpaqueURL reports whether s starts with an http or https scheme and
// carries something after it.
func isOpaqueURL(s string) bool {
	rest, ok := strings.CutPrefix(s, "http://")
	if ok {
		return rest != ""
	}
	rest, ok = strings.CutPrefix(s, "https://")
	return ok && rest != ""
}

// opaqueURLPrintable reports whether every rune after s's http(s) scheme is
// printable and not whitespace. A ticket is printed verbatim by the status
// views and stored by a server as its client sent it, so a space would split
// a status line and a control character would reach the terminal. Invalid
// UTF-8 decodes to utf8.RuneError, which unicode.IsPrint accepts, so that is
// refused on its own.
func opaqueURLPrintable(s string) bool {
	rest, ok := strings.CutPrefix(s, "http://")
	if !ok {
		rest, ok = strings.CutPrefix(s, "https://")
	}
	if !ok {
		return false
	}
	for _, r := range rest {
		if r == utf8.RuneError || unicode.IsSpace(r) || unicode.IsControl(r) || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// validIssueKey reports whether s is an issue key like ABC-123: two or more
// chars of [A-Z0-9] led by a letter, a dash, then one or more digits.
func validIssueKey(s string) bool {
	if len(s) < 4 || s[0] < 'A' || s[0] > 'Z' {
		return false
	}
	i := 1
	for i < len(s) && isUpperOrDigit(s[i]) {
		i++
	}
	if i < 2 || i >= len(s) || s[i] != '-' {
		return false
	}
	i++
	if i == len(s) {
		return false
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isUpperOrDigit(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
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
