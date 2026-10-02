// Package mastermind owns relevo's mastermind identity: the records saying which
// harness process and session a mastermind is, and the rules that mint,
// validate and resolve them.
package mastermind

import (
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"time"
)

// The sentinel errors every caller switches on.
var (
	ErrNotFound     = errors.New("mastermind not found")
	ErrNameTaken    = errors.New("mastermind name taken")
	ErrSessionTaken = errors.New("mastermind session taken")
	ErrHostTaken    = errors.New("mastermind host taken")
	ErrInUse        = errors.New("mastermind in use")
	ErrInvalid      = errors.New("invalid mastermind")
	// ErrNoMasterMind's text is its own fix.
	ErrNoMasterMind = errors.New("no relevo MasterMind for this session: is the relevo plugin enabled (relevo doctor)? Or run relevo mastermind init")
)

// ErrUnknownMasterMind reports a --mastermind or $RELEVO_MASTERMIND value that
// matches no record, unlike resolving nothing.
type ErrUnknownMasterMind struct {
	Ref string
}

func (e ErrUnknownMasterMind) Error() string {
	return fmt.Sprintf("unknown mastermind %q: no mastermind record has that id or name", e.Ref)
}

// SessionRef is one earlier session of a mastermind, kept so history joins
// after the mastermind moves on. To is when the move happened; From is when
// that session began.
type SessionRef struct {
	SessionID string    `json:"session_id"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
}

// Record is one relevo mastermind: the identity every binding, claim and db row
// keys on.
type Record struct {
	// Format is the on-disk format this record was written at; write
	// refuses to overwrite a Format it does not know.
	Format int `json:"format,omitempty"`

	ID            string       `json:"id"`
	Name          string       `json:"name"`
	HarnessKind   string       `json:"harness_kind"`
	SessionID     string       `json:"session_id"`
	Sessions      []SessionRef `json:"sessions"`
	HostPID       int          `json:"host_pid"`
	HostStartedAt int64        `json:"host_started_at"`
	CWD           string       `json:"cwd"`
	CreatedAt     time.Time    `json:"created_at"`
	SeenAt        time.Time    `json:"seen_at"`
}

// MaxNameLen is the longest name ValidName accepts.
const MaxNameLen = 32

// MaxSessions caps a record's history: appending a 21st drops the oldest.
const MaxSessions = 20

var (
	idRe = regexp.MustCompile(`^mm_[a-z2-7]{12}$`)
	// legacyPlRe is the `pl_` shape NewID minted before the rename: ids already
	// written keep their spelling (state already written).
	legacyPlRe = regexp.MustCompile(`^pl_[a-z2-7]{12}$`)
	// legacyIDRe is the shape of every mastermind id already in a real
	// relevo.db: a 26-character Crockford base32 ULID (no I, L, O or U).
	legacyIDRe        = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	nameRe            = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	namePrefixRe      = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	opencodeSessionRe = regexp.MustCompile(`^ses_[A-Za-z0-9]+$`)
)

// base32Lower is RFC 4648 base32 lowercased, padding off: the [a-z2-7]
// alphabet ids are drawn from.
var base32Lower = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

const idChars = 12

// NewID mints a mastermind id: `mm_` plus 12 lowercase base32 characters drawn
// from rand (crypto/rand in production; a test pins the reader).
func NewID(rand io.Reader) (string, error) {
	// 12 base32 characters carry 60 bits, so 8 bytes is enough to draw them from.
	var buf [8]byte
	if _, err := io.ReadFull(rand, buf[:]); err != nil {
		return "", fmt.Errorf("mastermind: mint id: %w", err)
	}
	return "mm_" + base32Lower.EncodeToString(buf[:])[:idChars], nil
}

// ValidID reports whether id names a mastermind record: the `mm_` shape NewID
// mints, the historical `pl_` shape it minted before the rename, or a legacy
// ULID. New records mint `mm_`; existing ones keep their ids (D4).
func ValidID(id string) error {
	if !idRe.MatchString(id) && !legacyPlRe.MatchString(id) && !legacyIDRe.MatchString(id) {
		return fmt.Errorf("mastermind id %q must be mm_ followed by 12 characters of [a-z2-7], the historical pl_ shape, or a 26-character Crockford base32 ULID: %w", id, ErrInvalid)
	}
	return nil
}

// ValidName reports whether name has the shape [a-z][a-z0-9-]{0,31}.
func ValidName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("mastermind name %q must match [a-z][a-z0-9-]{0,31}: %w", name, ErrInvalid)
	}
	return nil
}

// validNamePrefix reports whether agent is a valid name fragment that still
// leaves room for a "-<n>" suffix.
func validNamePrefix(agent string) bool {
	if !namePrefixRe.MatchString(agent) {
		return false
	}
	return len(agent)+2 <= MaxNameLen
}

// DefaultName is the first free `<base>-<n>`, where base is agent when it
// can be a name prefix and kind otherwise.
func DefaultName(agent, kind string, taken func(string) bool) string {
	base := agent
	if !validNamePrefix(base) {
		base = kind
	}
	for n := 1; ; n++ {
		name := fmt.Sprintf("%s-%d", base, n)
		if taken == nil || !taken(name) {
			return name
		}
	}
}

// Validate checks the required fields, the opencode session rule, and that
// host_started_at is 0 whenever host_pid is 0.
func (r Record) Validate() error {
	if err := ValidID(r.ID); err != nil {
		return err
	}
	if err := ValidName(r.Name); err != nil {
		return err
	}
	if r.HarnessKind == "" {
		return fmt.Errorf("mastermind: harness_kind is required: %w", ErrInvalid)
	}
	if r.SessionID == "" {
		return fmt.Errorf("mastermind: session_id is required: %w", ErrInvalid)
	}
	if r.HarnessKind == "opencode" && !opencodeSessionRe.MatchString(r.SessionID) {
		return fmt.Errorf("mastermind: opencode session_id %q must match ^ses_[A-Za-z0-9]+$: %w", r.SessionID, ErrInvalid)
	}
	if r.CWD == "" || !filepath.IsAbs(r.CWD) {
		return fmt.Errorf("mastermind: cwd %q must be an absolute path: %w", r.CWD, ErrInvalid)
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("mastermind: created_at is required: %w", ErrInvalid)
	}
	if r.SeenAt.IsZero() {
		return fmt.Errorf("mastermind: seen_at is required: %w", ErrInvalid)
	}
	if r.HostPID < 0 {
		return fmt.Errorf("mastermind: host_pid %d must be >= 0: %w", r.HostPID, ErrInvalid)
	}
	if r.HostPID == 0 && r.HostStartedAt != 0 {
		return fmt.Errorf("mastermind: host_started_at must be 0 when host_pid is 0: %w", ErrInvalid)
	}
	return nil
}

// appendSession appends ref to a record's history, dropping the oldest entry
// past MaxSessions. It always returns a fresh slice.
func appendSession(sessions []SessionRef, ref SessionRef) []SessionRef {
	out := make([]SessionRef, 0, len(sessions)+1)
	out = append(out, sessions...)
	out = append(out, ref)
	if len(out) > MaxSessions {
		out = out[len(out)-MaxSessions:]
	}
	return out
}

// sessionStartedAt is when the current session began: the last recorded
// move's end, or the record's own creation if it never moved.
func (r Record) sessionStartedAt() time.Time {
	if n := len(r.Sessions); n > 0 {
		return r.Sessions[n-1].To
	}
	return r.CreatedAt
}
