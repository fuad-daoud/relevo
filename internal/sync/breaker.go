package sync

// The breaker is what a daemon remembers about a worker that will not answer:
// the marker a call leaves behind, the consecutive deaths it counts, the delay
// it waits between attempts, and the latch that stops the machine and asks a
// human to look. It is state over the machine-local kv and a clock, so the whole
// policy is testable with no process and no network.

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// KeyDeaths is where the breaker keeps its consecutive-death count.
const KeyDeaths = "sync.deaths"

// BackoffBase is the delay the first death earns; each later consecutive death
// doubles it. A minute is long enough that a remote briefly unreachable is not
// hammered and short enough that a machine does not sit idle over a blip.
const BackoffBase = time.Minute

// LatchAfter is the consecutive death that latches the machine. Three is where a
// fault stops being plausibly transient.
const LatchAfter = 3

// The fixed sentences a latch carries. They are written in this tree: a latch is
// shown on the statusline and in the sync view, so neither a worker's nor a
// remote's own words may reach them.
const (
	latchDeaths  = "sync: the worker stopped answering three times in a row"
	latchRefused = "sync: the remote refused this machine's sync log"
	latchMissing = "sync: a body the sync log names is missing from the bucket"
)

// ErrLatched reports a call refused because the machine is latched: it stops
// until the latch is cleared.
var ErrLatched = errors.New("sync: the breaker is latched")

// ErrBackingOff reports a call refused because the delay since the last death
// has not elapsed.
var ErrBackingOff = errors.New("sync: the breaker is backing off")

// BreakerState is what the breaker keeps between starts.
type BreakerState struct {
	// Deaths is the consecutive death count since the last success or clear.
	Deaths int `json:"deaths"`
	// Seen is the highest in-call number already counted, so a marker a start
	// finds is counted once and not again by the next start.
	Seen int `json:"seen"`
	// At is when the most recent death was counted; the backoff is measured
	// from it.
	At time.Time `json:"at,omitempty"`
}

// Breaker counts a worker's deaths and decides when a call may run again.
type Breaker struct {
	kv  db.KV
	now func() time.Time
}

// NewBreaker returns a breaker over the machine-local kv. The clock is time.Now
// until a test replaces it.
func NewBreaker(kv db.KV) *Breaker {
	return &Breaker{kv: kv, now: time.Now}
}

// State returns the persisted counts, zero when nothing has been recorded.
func (b *Breaker) State() (BreakerState, error) {
	body, ok, err := b.kv.KVGet(KeyDeaths)
	if err != nil {
		return BreakerState{}, fmt.Errorf("sync: breaker state: %w", err)
	}
	if !ok {
		return BreakerState{}, nil
	}
	var st BreakerState
	if err := json.Unmarshal(body, &st); err != nil {
		return BreakerState{}, fmt.Errorf("sync: breaker state: %w", err)
	}
	return st, nil
}

// Deaths returns the consecutive death count.
func (b *Breaker) Deaths() (int, error) {
	st, err := b.State()
	if err != nil {
		return 0, err
	}
	return st.Deaths, nil
}

// Latched reports whether the machine is latched. The latch lives in the
// attention marker, so the statusline reads it without asking the breaker.
func (b *Breaker) Latched() (bool, error) {
	cause, err := b.LatchCause()
	if err != nil {
		return false, err
	}
	return cause != "", nil
}

// LatchCause returns the fixed sentence a latch carries, or "" when there is
// none.
func (b *Breaker) LatchCause() (string, error) {
	msg, err := ReadAttention(b.kv)
	if err != nil {
		return "", err
	}
	return msg, nil
}

// Begin opens one call: it counts a marker a previous call left behind, refuses
// while the machine is latched or backing off, and writes the in-call marker the
// reply clears.
func (b *Breaker) Begin(verb string) error {
	if err := b.Observe(); err != nil {
		return err
	}
	latched, err := b.Latched()
	if err != nil {
		return err
	}
	if latched {
		return ErrLatched
	}
	due, err := b.Due()
	if err != nil {
		return err
	}
	if !due {
		return ErrBackingOff
	}
	st, err := b.State()
	if err != nil {
		return err
	}
	return WriteInCall(b.kv, InCall{At: b.now().UTC(), Verb: verb, N: st.Seen + 1})
}

// End clears the in-call marker: a reply arrived, so no call is in flight.
func (b *Breaker) End() error { return ClearInCall(b.kv) }

// Observe counts the death the in-call marker names, unless that marker has
// already been counted, and leaves the marker for the next daemon to find. A
// call that missed runs it as the worker is dropped, and a fresh daemon runs it
// at start; the number in the marker is what keeps one death from counting
// twice.
func (b *Breaker) Observe() error {
	call, ok, err := ReadInCall(b.kv)
	if err != nil || !ok {
		return err
	}
	st, err := b.State()
	if err != nil {
		return err
	}
	if call.N <= st.Seen {
		return nil
	}
	st.Deaths++
	st.Seen = call.N
	st.At = call.At
	if st.At.IsZero() {
		st.At = b.now().UTC()
	}
	if err := b.save(st); err != nil {
		return err
	}
	if st.Deaths >= LatchAfter {
		return b.latch(latchDeaths)
	}
	return nil
}

// Success starts the count over. It does not clear an existing latch: only Clear
// unlatches a machine.
func (b *Breaker) Success() error {
	st, err := b.State()
	if err != nil {
		return err
	}
	st.Deaths = 0
	st.At = time.Time{}
	return b.save(st)
}

// Refused records a reply the worker sent and declined. A refusal that will
// repeat on every attempt latches at once, because waiting out three deaths it
// can already predict only delays the human who has to act; anything else is
// left to the ordinary call path.
func (b *Breaker) Refused(err error) error {
	if !IsPermanentRefusal(err) {
		return nil
	}
	if errors.Is(err, synclog.ErrBlobMissing) {
		return b.latch(latchMissing)
	}
	return b.latch(latchRefused)
}

// Due reports whether a call may run now: a latched machine never is, a machine
// with no deaths is due at once, and a machine that has died waits out the
// backoff.
func (b *Breaker) Due() (bool, error) {
	latched, err := b.Latched()
	if err != nil {
		return false, err
	}
	if latched {
		return false, nil
	}
	st, err := b.State()
	if err != nil {
		return false, err
	}
	if st.Deaths == 0 {
		return true, nil
	}
	return !b.now().Before(st.At.Add(Backoff(st.Deaths))), nil
}

// Clear unlatches the machine and starts the count over, which is the retry an
// operator asked for: the latch goes and the next three deaths are measured
// fresh.
func (b *Breaker) Clear() error {
	if err := ClearLatch(b.kv); err != nil {
		return err
	}
	if err := ClearInCall(b.kv); err != nil {
		return err
	}
	st, err := b.State()
	if err != nil {
		return err
	}
	st.Deaths = 0
	st.Seen = 0
	st.At = time.Time{}
	return b.save(st)
}

// Backoff is the delay earned by consecutive deaths: the base for the first,
// doubled for each after it.
func Backoff(deaths int) time.Duration {
	if deaths <= 0 {
		return 0
	}
	if deaths > maxBackoffShift {
		deaths = maxBackoffShift
	}
	return BackoffBase << (deaths - 1)
}

// maxBackoffShift caps the exponent so a machine that has died many times does
// not overflow a duration.
const maxBackoffShift = 20

func (b *Breaker) latch(message string) error {
	return SetAttention(b.kv, message, b.now())
}

func (b *Breaker) save(st BreakerState) error {
	body, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("sync: breaker state: %w", err)
	}
	if err := b.kv.KVPut(KeyDeaths, body); err != nil {
		return fmt.Errorf("sync: breaker state: %w", err)
	}
	return nil
}
