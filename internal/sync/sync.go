// Package sync is the scaffolding cloud sync needs before a byte moves: the
// machine-local rows sync reads (the settings, the token, the tick markers),
// the statusline's four tokens over those markers, and the client interface
// every test fakes instead of dialing.
package sync

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// ErrSyncUnavailable is the one refusal the verbs that would move bytes raise
// while this build carries no sync engine. The verbs keep their names, keep
// their place on the owner socket and keep answering; none of them reaches a
// remote, so each refuses with this rather than with a fault the reader would
// look for on their own machine. Status and the turn-off are unaffected:
// neither of them moves a change set.
var ErrSyncUnavailable = errors.New("sync is not available in this build")

// Every row sync reads is one that must never sync, so all of them bind to the
// machine-local file beside the shared database. A shared handle writes config,
// secret and kv rows into the file that leaves the machine, which is why
// nothing here takes one: the seam is the local handle or nothing.
type Local = *db.DB

// The markers a tick writes and the statusline reads back. Every key is in the
// local file's `sync` namespace, so a marker is local by the namespace rule
// rather than by a per-key decision, and a second machine never sees one.
const (
	KeyEnabled   = "sync.enabled"
	KeyBacklog   = "sync.backlog"
	KeyLastTick  = "sync.last_tick"
	KeyAttention = "sync.attention"
)

// LocalHandle is the machine-local file beside d, or an error on a handle
// opened without one: a caller that cannot name the local file has no row that
// is safe to write, so the seam fails rather than falling back to shared.
func LocalHandle(d *db.DB) (Local, error) {
	if l := d.Local(); l != nil {
		return l, nil
	}
	return nil, fmt.Errorf("sync: no local file beside the shared database: %w", db.ErrInvalid)
}

// State is everything the statusline's token depends on, read out of the local
// markers with no network handle in reach.
type State struct {
	// Enabled is whether sync is turned on for this installation.
	Enabled bool
	// Backlog is the count of local operations a push has not sent yet.
	Backlog int64
	// LastTickOK is whether the most recent tick succeeded.
	LastTickOK bool
	// Attention is whether the handle reported an error a human must act on,
	// such as an authorisation the remote refuses.
	Attention bool
}

// tick is the marker one finished tick writes. A tick that failed leaves OK
// false and no attention marker: the machine is behind, not broken.
type tick struct {
	At time.Time `json:"at"`
	OK bool      `json:"ok"`
}

// attention is the marker an error a human must act on writes. Its message is
// never the remote's raw body, and never a credential.
type attention struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

// The four tokens the statusline shows, one per state that needs a human. The
// renderer formats them; detail lives in the sync view.
const (
	TokenOff    = "sync:off"
	TokenOK     = "sync:ok"
	TokenBehind = "sync:behind"
	TokenErr    = "sync:err"
)

// BacklogThreshold is the unpushed operation count above which a machine whose
// last tick succeeded is behind rather than ok.
const BacklogThreshold int64 = 100

// Token is the whole statusline mapping: state in, token out. The order is the
// rule -- a machine that is off is never behind, an error needing attention
// outranks a failed tick, and a failed tick outranks a backlog.
func Token(s State) string {
	switch {
	case !s.Enabled:
		return TokenOff
	case s.Attention:
		return TokenErr
	case !s.LastTickOK:
		return TokenBehind
	case s.Backlog > BacklogThreshold:
		return TokenBehind
	default:
		return TokenOK
	}
}

// StatusToken is the token for a machine as the statusline reads it: the local
// markers mapped by Token. It touches nothing but kv, so it answers with the
// network blackholed and no client open.
func StatusToken(kv db.KV) (string, error) {
	s, err := ReadState(kv)
	if err != nil {
		return "", err
	}
	return Token(s), nil
}

// ReadState reads the markers out of kv. An absent marker leaves its field at
// the default its name carries: not enabled, no backlog, no tick recorded, and
// no error needing attention.
func ReadState(kv db.KV) (State, error) {
	var (
		s     State
		t     tick
		a     attention
		reads = []struct {
			key string
			out any
		}{
			{KeyEnabled, &s.Enabled},
			{KeyBacklog, &s.Backlog},
			{KeyLastTick, &t},
			{KeyAttention, &a},
		}
	)
	for _, r := range reads {
		if err := marker(kv, r.key, r.out); err != nil {
			return State{}, err
		}
	}
	s.LastTickOK = t.OK
	s.Attention = a.Message != ""
	return s, nil
}

// ReadAttention returns the message the attention marker carries, or "" when
// no marker is set. The message is the fixed text Runner wrote, never a body
// the remote chose, so a caller can render it on a screen or put it in a log
// without asking what a remote was allowed to say.
func ReadAttention(kv db.KV) (string, error) {
	var a attention
	if err := marker(kv, KeyAttention, &a); err != nil {
		return "", err
	}
	return a.Message, nil
}

// ReadSnapshot returns what the remote last reported, and whether a measured
// tick ever recorded it. The pair is the whole answer, because a machine that
// has measured nothing and a machine whose change set is empty are both a zero
// Stats, and only the marker distinguishes them.
func ReadSnapshot(kv db.KV) (Stats, bool, error) {
	// The presence test is its own read rather than a side effect of the decode:
	// a measured Stats is not required to be non-zero, so an empty remote and an
	// unmeasured machine are the same bytes and only the marker's existence
	// tells them apart.
	body, ok, err := kv.KVGet(KeyStats)
	if err != nil {
		return Stats{}, false, fmt.Errorf("sync: marker %s: %w", KeyStats, err)
	}
	if !ok {
		return Stats{}, false, nil
	}
	var s Stats
	if err := json.Unmarshal(body, &s); err != nil {
		return Stats{}, false, fmt.Errorf("sync: marker %s: %w", KeyStats, err)
	}
	return s, true, nil
}

// marker reads one key out of kv into out, which must be a pointer. An absent
// key is not an error: it leaves out at the zero value the caller reads as the
// marker's default.
func marker(kv db.KV, key string, out any) error {
	body, ok, err := kv.KVGet(key)
	if err != nil {
		return fmt.Errorf("sync: marker %s: %w", key, err)
	}
	if !ok {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("sync: marker %s: %w", key, err)
	}
	return nil
}
