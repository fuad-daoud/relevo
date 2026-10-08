// Package sync is the scaffolding cloud sync needs before a byte moves: the
// machine-local rows sync reads (the settings, the token, the tick markers),
// the statusline's four tokens over those markers, and the client interface
// every test fakes instead of dialing.
package sync

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

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
	KeyTimes     = "sync.times"
	KeyTrouble   = "sync.trouble"
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
	// such as an authorisation the remote refuses, or a latched breaker.
	Attention bool
	// LatchCause is the fixed sentence the latch marker carries, empty when
	// nothing is latched. It is the cause behind the statusline's err token.
	LatchCause string
	// LastExport and LastImport are when the steady pipeline last completed
	// each half of the exchange, zero when it never has.
	LastExport time.Time
	LastImport time.Time
	// Trouble is what the last import reported besides applied entries.
	Trouble Trouble
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

// Times is when the steady pipeline last finished each half of the exchange.
// The two are recorded together because one attempt runs both; a machine that
// has never completed an attempt carries neither.
type Times struct {
	Export time.Time `json:"export"`
	Import time.Time `json:"import"`
}

// Trouble is what the last import reported besides applied entries: the
// origins a newer writer held, the batches a refusal dropped, and the sequence
// gaps a hole left. Each entry is the importer's own sentence, so the status
// surface names the installation and the sequence a reader has to act on. It
// is a report rather than a failure, which is why it is stored rather than
// raised.
type Trouble struct {
	Held    []string `json:"held,omitempty"`
	Dropped []string `json:"dropped,omitempty"`
	Gaps    []string `json:"gaps,omitempty"`
}

// Empty reports whether an import reported no trouble at all.
func (t Trouble) Empty() bool {
	return len(t.Held) == 0 && len(t.Dropped) == 0 && len(t.Gaps) == 0
}

// Stats is what the remote reports about the local change set: the operations a
// push has not sent yet, the last successful push and pull, the bytes each way,
// and the server revision. The revision is opaque and must never be parsed.
type Stats struct {
	CdcOperations        int64  `json:"cdc_operations"`
	LastPullUnixTime     int64  `json:"last_pull_unix_time"`
	LastPushUnixTime     int64  `json:"last_push_unix_time"`
	NetworkSentBytes     int64  `json:"network_sent_bytes"`
	NetworkReceivedBytes int64  `json:"network_received_bytes"`
	Revision             string `json:"revision"`
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
// outranks a failed tick, trouble the last import reported is a machine that is
// behind, and a failed tick outranks a backlog.
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
		tm    Times
		tr    Trouble
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
	s.LastExport, s.LastImport, s.Trouble = tm.Export, tm.Import, tr
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

// WriteTimes records when the steady pipeline last finished each half of the
// exchange. It is the writer the reader in ReadState pairs with, so the two
// cannot disagree about the marker's shape.
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

// WriteTrouble records what the last import reported besides applied entries.
// It is written on every successful attempt, empty included, so a machine that
// caught up stops showing the trouble a previous attempt reported.
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

// KeyInCall is the marker a pipe call writes before it asks for anything and
// clears when its reply arrives. A marker left behind names a call that never
// came back, which is the one death a daemon that was itself killed cannot
// otherwise count.
const KeyInCall = "sync.incall"

// InCall is what the in-call marker holds. The number is what lets a leftover
// marker be counted exactly once: a start counts the call only while its number
// is past the highest one already counted.
type InCall struct {
	At   time.Time `json:"at"`
	Verb string    `json:"verb"`
	N    int       `json:"n"`
}

// WriteInCall writes the in-call marker before a pipe call goes out.
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

// ClearInCall removes the in-call marker once a reply has arrived.
func ClearInCall(kv db.KV) error {
	if err := kv.KVDelete(KeyInCall); err != nil {
		return fmt.Errorf("sync: in-call marker: %w", err)
	}
	return nil
}

// ReadInCall returns the in-call marker a call left behind, and whether one is
// set. An unreadable marker is a failure rather than an absent one: a machine
// whose marker cannot be read must not be reported as having no call in flight.
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

// SetAttention writes the marker that makes the statusline read sync:err. The
// message is fixed text written in this tree: the statusline and the sync view
// both show it, and neither a worker's nor a remote's own words may reach them.
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

// ClearLatch removes the latched-error marker. It is the one path that unlatches
// a machine: the retry verb runs it, and neither a tick nor a call clears the
// marker on its own.
func ClearLatch(kv db.KV) error {
	if err := kv.KVDelete(KeyAttention); err != nil {
		return fmt.Errorf("sync: attention marker: %w", err)
	}
	return nil
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
