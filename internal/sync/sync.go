// Package sync is the scaffolding cloud sync needs before a byte moves:
// machine-local rows, the statusline's four tokens over them, and the client
// interface tests fake instead of dialing.
package sync

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Every row sync reads must never sync, so all bind to the machine-local file
// beside the shared database; nothing here takes a shared handle, which writes
// rows into the file that leaves the machine.
type Local = *db.DB

// The markers a tick writes and the statusline reads back, every key in the local
// file's `sync` namespace: local by the namespace rule, never seen by a second machine.
const (
	KeyEnabled   = "sync.enabled"
	KeyBacklog   = "sync.backlog"
	KeyLastTick  = "sync.last_tick"
	KeyAttention = "sync.attention"
	KeyTimes     = "sync.times"
	KeyTrouble   = "sync.trouble"
	// KeyMarksPublished is when this machine last published its import marks to the
	// log; a marker because the throttle reads it and an old daemon has no field.
	KeyMarksPublished = "sync.marks.published"
)

// LocalHandle is the machine-local file beside d, or an error on a handle opened
// without one: with no local file no row is safe to write, so the seam fails.
func LocalHandle(d *db.DB) (Local, error) {
	if l := d.Local(); l != nil {
		return l, nil
	}
	return nil, fmt.Errorf("sync: no local file beside the shared database: %w", db.ErrInvalid)
}

// State is everything the statusline's token depends on, read from local markers
// with no network handle in reach.
type State struct {
	// Enabled is whether sync is turned on for this installation.
	Enabled bool
	// Backlog is the count of local operations a push has not sent yet.
	Backlog int64
	// LastTickOK is whether the most recent tick succeeded.
	LastTickOK bool
	// Attention is whether the handle reported an error a human must act on.
	Attention bool
	// LatchCause is the fixed sentence the latch marker carries, empty when nothing
	// is latched: the cause behind the statusline's err token.
	LatchCause string
	// LastExport and LastImport are when the steady pipeline last completed each
	// half, zero when never.
	LastExport time.Time
	LastImport time.Time
	// Trouble is what the last import reported besides applied entries.
	Trouble Trouble
	// Attempt is the last steady attempt the daemon recorded, the zero value
	// when none was.
	Attempt Attempt
	// Rebuild is the last automatic replica rebuild, zero when never: every state
	// reader says what the machine did to recover.
	Rebuild Rebuild
}

// tick is the marker one finished tick writes; a failed tick leaves OK false and
// no attention marker: the machine is behind, not broken.
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

// Times is when the steady pipeline last finished each half, recorded together
// because one attempt runs both; a machine that never completed an attempt
// carries neither.
type Times struct {
	Export time.Time `json:"export"`
	Import time.Time `json:"import"`
}

// Trouble is what the last import reported besides applied entries, each entry
// in the importer's own sentence: a held origin, a dropped batch, a gap. A report
// rather than a failure, which is why it is stored rather than raised.
type Trouble struct {
	Held    []string `json:"held,omitempty"`
	Dropped []string `json:"dropped,omitempty"`
	Gaps    []string `json:"gaps,omitempty"`
}

// Empty reports whether an import reported no trouble at all.
func (t Trouble) Empty() bool {
	return len(t.Held) == 0 && len(t.Dropped) == 0 && len(t.Gaps) == 0
}

// Holds is every report a reader must act on now: held origins and gaps, never
// the historic dropped reports.
func (t Trouble) Holds() []string {
	return append(append([]string(nil), t.Held...), t.Gaps...)
}

// The four tokens the statusline shows, one per state needing a human.
const (
	TokenOff    = "sync:off"
	TokenOK     = "sync:ok"
	TokenBehind = "sync:behind"
	TokenErr    = "sync:err"
)

// BacklogThreshold is the unpushed count above which a succeeding last tick still
// reads as behind.
const BacklogThreshold int64 = 100

// Token is the whole statusline mapping: off never reads as behind, attention
// outranks a failed tick, reported trouble reads as behind, and a failed tick
// outranks a backlog.
func Token(s State) string {
	switch {
	case !s.Enabled:
		return TokenOff
	case s.Attention:
		return TokenErr
	case !s.Trouble.Empty():
		return TokenBehind
	case !s.LastTickOK:
		return TokenBehind
	case s.Attempt.Failed():
		return TokenBehind
	case s.Backlog > BacklogThreshold:
		return TokenBehind
	default:
		return TokenOK
	}
}

// StatusToken is the token for a machine as the statusline reads it: local markers
// mapped by Token, touching nothing but kv.
func StatusToken(kv db.KV) (string, error) {
	s, err := ReadState(kv)
	if err != nil {
		return "", err
	}
	return Token(s), nil
}

// ReadState reads the markers out of kv; an absent marker leaves its field at the
// default its name carries.
func ReadState(kv db.KV) (State, error) {
	var (
		s     State
		t     tick
		a     attention
		tm    Times
		tr    Trouble
		at    Attempt
		reads = []struct {
			key string
			out any
		}{
			{KeyEnabled, &s.Enabled},
			{KeyBacklog, &s.Backlog},
			{KeyLastTick, &t},
			{KeyAttention, &a},
			{KeyTimes, &tm},
			{KeyTrouble, &tr},
			{KeyLastAttempt, &at},
			{KeyRebuild, &s.Rebuild},
		}
	)
	for _, r := range reads {
		if err := marker(kv, r.key, r.out); err != nil {
			return State{}, err
		}
	}
	s.LastTickOK = t.OK
	s.Attention = a.Message != ""
	s.LatchCause = a.Message
	s.LastExport, s.LastImport, s.Trouble, s.Attempt = tm.Export, tm.Import, tr, at
	return s, nil
}

// ReadAttention returns the attention marker's message, or "" when unset. It is the
// fixed text Runner wrote, never a remote's body, so a caller renders it freely.
func ReadAttention(kv db.KV) (string, error) {
	var a attention
	if err := marker(kv, KeyAttention, &a); err != nil {
		return "", err
	}
	return a.Message, nil
}

// WriteTimes records when the steady pipeline last finished each half: the writer
// ReadState pairs with, so the two agree on the marker's shape.
func WriteTimes(kv db.KV, t Times) error {
	body, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("sync: times marker: %w", err)
	}
	if err := kv.KVPut(KeyTimes, body); err != nil {
		return fmt.Errorf("sync: times marker: %w", err)
	}
	return nil
}

// WriteTrouble records what the last import reported besides applied entries, on
// every successful attempt -- empty included, so a caught-up machine stops showing
// old trouble.
func WriteTrouble(kv db.KV, t Trouble) error {
	body, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("sync: trouble marker: %w", err)
	}
	if err := kv.KVPut(KeyTrouble, body); err != nil {
		return fmt.Errorf("sync: trouble marker: %w", err)
	}
	return nil
}

// ClearDroppedTrouble removes the cumulative dropped reports, which accumulate
// until the operator's retry says seen: a dropped batch moved its origin's mark
// past it and is never offered again. Held and gap reports are per-run and stay.
func ClearDroppedTrouble(kv db.KV) error {
	var tr Trouble
	if err := marker(kv, KeyTrouble, &tr); err != nil {
		return err
	}
	if len(tr.Dropped) == 0 {
		return nil
	}
	tr.Dropped = nil
	if tr.Held == nil && tr.Gaps == nil {
		if err := kv.KVDelete(KeyTrouble); err != nil {
			return fmt.Errorf("sync: trouble marker: %w", err)
		}
		return nil
	}
	body, err := json.Marshal(tr)
	if err != nil {
		return fmt.Errorf("sync: trouble marker: %w", err)
	}
	if err := kv.KVPut(KeyTrouble, body); err != nil {
		return fmt.Errorf("sync: trouble marker: %w", err)
	}
	return nil
}

// KeyInCall is the marker a pipe call writes before asking and clears on reply; a
// leftover names a call that never came back, the one death a killed daemon cannot
// otherwise count.
const KeyInCall = "sync.incall"

// InCall is what the in-call marker holds; the number lets a leftover be counted
// exactly once, past the highest already counted.
type InCall struct {
	At   time.Time `json:"at"`
	Verb string    `json:"verb"`
	N    int       `json:"n"`
}

func WriteInCall(kv db.KV, call InCall) error {
	body, err := json.Marshal(call)
	if err != nil {
		return fmt.Errorf("sync: in-call marker: %w", err)
	}
	if err := kv.KVPut(KeyInCall, body); err != nil {
		return fmt.Errorf("sync: in-call marker: %w", err)
	}
	return nil
}

func ClearInCall(kv db.KV) error {
	if err := kv.KVDelete(KeyInCall); err != nil {
		return fmt.Errorf("sync: in-call marker: %w", err)
	}
	return nil
}

// ReadInCall returns the in-call marker a call left behind and whether one is
// set. An unreadable marker is a failure, not an absent one, so a machine whose
// marker cannot be read is never reported as having no call in flight.
func ReadInCall(kv db.KV) (InCall, bool, error) {
	body, ok, err := kv.KVGet(KeyInCall)
	if err != nil {
		return InCall{}, false, fmt.Errorf("sync: in-call marker: %w", err)
	}
	if !ok {
		return InCall{}, false, nil
	}
	var call InCall
	if err := json.Unmarshal(body, &call); err != nil {
		return InCall{}, false, fmt.Errorf("sync: in-call marker: %w", err)
	}
	return call, true, nil
}

// SetAttention writes the marker reading as sync:err. The message is fixed text
// from this tree: neither a worker's nor a remote's words may reach the surfaces.
func SetAttention(kv db.KV, message string, at time.Time) error {
	body, err := json.Marshal(attention{At: at.UTC(), Message: message})
	if err != nil {
		return fmt.Errorf("sync: attention marker: %w", err)
	}
	if err := kv.KVPut(KeyAttention, body); err != nil {
		return fmt.Errorf("sync: attention marker: %w", err)
	}
	return nil
}

// ClearLatch removes the latched-error marker: the one path unlatching a machine,
// run by the retry verb and by nothing else on its own.
func ClearLatch(kv db.KV) error {
	if err := kv.KVDelete(KeyAttention); err != nil {
		return fmt.Errorf("sync: attention marker: %w", err)
	}
	return nil
}

// marker reads one key out of kv into out, which must be a pointer; absent is not
// an error, leaving the caller's default zero value.
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
