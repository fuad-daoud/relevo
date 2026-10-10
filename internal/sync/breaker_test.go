package sync

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// breakerNow is the fixed instant every breaker test runs on, so the backoff
// window is a boundary a test names rather than a sleep.
var breakerNow = time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)

// breakerUnder returns a breaker over a fresh local file with the enabled mark
// set, so the statusline can read a token, and the clock fixed.
func breakerUnder(t *testing.T) (*Breaker, Local) {
	t.Helper()
	_, _, local := openSplit(t)
	putMarker(t, local, KeyEnabled, `true`)
	b := NewBreaker(local)
	b.now = func() time.Time { return breakerNow }
	return b, local
}

// missCall simulates one call that went out and never came back: it writes the
// in-call marker such a call leaves and observes it. The number advances the way
// a real call's would.
func missCall(t *testing.T, b *Breaker, local db.KV) {
	t.Helper()
	st, err := b.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if err := WriteInCall(local, InCall{At: breakerNow, Verb: "export", N: st.Seen + 1}); err != nil {
		t.Fatalf("WriteInCall: %v", err)
	}
	if err := b.Observe(); err != nil {
		t.Fatalf("Observe: %v", err)
	}
}

// TestUnclearedMarkerCountsAtDaemonStart pins that a marker an in-flight call
// left behind is a death the next start counts, and that the same marker never
// counts twice.
func TestUnclearedMarkerCountsAtDaemonStart(t *testing.T) {
	t.Parallel()
	b, local := breakerUnder(t)

	// A daemon killed mid-call leaves the marker its call wrote behind.
	if err := WriteInCall(local, InCall{At: breakerNow, Verb: "export", N: 1}); err != nil {
		t.Fatalf("WriteInCall: %v", err)
	}
	if err := b.Observe(); err != nil {
		t.Fatalf("the next start: %v", err)
	}
	if got, err := b.Deaths(); err != nil || got != 1 {
		t.Fatalf("deaths = %d, %v; want the uncleared marker counted once", got, err)
	}
	// The marker is left in place, and a later start does not count it again.
	if err := b.Observe(); err != nil {
		t.Fatalf("a later start: %v", err)
	}
	if got, _ := b.Deaths(); got != 1 {
		t.Errorf("deaths after a later start = %d, want the one death", got)
	}
}

// TestBackoffDoublesFromOneMinute pins the delay each consecutive death earns,
// and that the clock -- not a sleep -- decides when the next attempt is due.
func TestBackoffDoublesFromOneMinute(t *testing.T) {
	t.Parallel()
	if got := Backoff(1); got != time.Minute {
		t.Errorf("Backoff(1) = %s, want %s", got, time.Minute)
	}
	if got, want := Backoff(2), 2*Backoff(1); got != want {
		t.Errorf("Backoff(2) = %s, want double the first delay (%s)", got, want)
	}
	if got := Backoff(0); got != 0 {
		t.Errorf("Backoff(0) = %s, want no delay", got)
	}
	if got := Backoff(1000); got <= 0 {
		t.Errorf("Backoff(1000) = %s, want a positive capped delay", got)
	}

	b, local := breakerUnder(t)
	missCall(t, b, local)
	if err := b.Begin("export"); !errors.Is(err, ErrBackingOff) {
		t.Errorf("Begin within the backoff = %v, want ErrBackingOff", err)
	}

	b.now = func() time.Time { return breakerNow.Add(time.Minute) }
	if err := b.Begin("export"); err != nil {
		t.Fatalf("Begin once the backoff elapsed = %v, want nil", err)
	}
	if err := b.End(); err != nil {
		t.Errorf("End: %v", err)
	}
}

// TestThreeDeathsLatchSyncErr pins that three deaths in a row latch the machine
// with a fixed cause, and that a success between deaths starts the count over
// rather than latching early.
func TestThreeDeathsLatchSyncErr(t *testing.T) {
	t.Parallel()
	b, local := breakerUnder(t)

	for i := 0; i < 2; i++ {
		missCall(t, b, local)
	}
	if latched, _ := b.Latched(); latched {
		t.Fatal("the machine latched before the third death")
	}
	if err := b.Success(); err != nil {
		t.Fatalf("Success: %v", err)
	}
	if got, _ := b.Deaths(); got != 0 {
		t.Fatalf("deaths after a success = %d, want the count reset", got)
	}

	for i := 0; i < 3; i++ {
		missCall(t, b, local)
	}
	latched, err := b.Latched()
	if err != nil {
		t.Fatalf("Latched: %v", err)
	}
	if !latched {
		t.Fatal("three consecutive deaths did not latch the machine")
	}
	if cause, _ := b.LatchCause(); cause != latchDeaths {
		t.Errorf("latch cause = %q, want %q", cause, latchDeaths)
	}
	if tok, _ := StatusToken(local); tok != TokenErr {
		t.Errorf("token = %q, want %q", tok, TokenErr)
	}
}

// TestPermanentRefusalLatches pins that a refusal which will repeat on every
// attempt latches at once, rather than being counted as one death and shown as
// sync:behind for two attempts more.
func TestPermanentRefusalLatches(t *testing.T) {
	t.Parallel()
	b, local := breakerUnder(t)

	refusal := fmt.Errorf("push: %w", ErrRemoteSchema)
	if err := b.Refused(refusal); err != nil {
		t.Fatalf("Refused: %v", err)
	}
	if latched, _ := b.Latched(); !latched {
		t.Fatal("a permanent refusal did not latch the machine")
	}
	if got, _ := b.Deaths(); got != 0 {
		t.Errorf("deaths = %d, want the refusal latched instead of counted as a death", got)
	}
	if tok, _ := StatusToken(local); tok != TokenErr {
		t.Errorf("token = %q, want %q rather than %q", tok, TokenErr, TokenBehind)
	}
	if cause, _ := b.LatchCause(); cause != latchRefused {
		t.Errorf("latch cause = %q, want %q", cause, latchRefused)
	}
}

// TestLatchPersistsUntilCleared pins that nothing but the clear path unlatches a
// machine: not a success, not a later start, not a refusal that is not
// permanent.
func TestLatchPersistsUntilCleared(t *testing.T) {
	t.Parallel()
	b, local := breakerUnder(t)

	for i := 0; i < 3; i++ {
		missCall(t, b, local)
	}
	if latched, _ := b.Latched(); !latched {
		t.Fatal("three consecutive deaths did not latch the machine")
	}

	if err := b.Success(); err != nil {
		t.Fatalf("Success: %v", err)
	}
	if err := b.Observe(); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if err := b.Refused(errors.New("a network failure")); err != nil {
		t.Fatalf("Refused: %v", err)
	}
	if err := b.Begin("export"); !errors.Is(err, ErrLatched) {
		t.Errorf("Begin while latched = %v, want ErrLatched", err)
	}
	if latched, _ := b.Latched(); !latched {
		t.Fatal("something other than the clear path unlatched the machine")
	}
	if tok, _ := StatusToken(local); tok != TokenErr {
		t.Errorf("token while latched = %q, want %q", tok, TokenErr)
	}

	if err := b.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if latched, _ := b.Latched(); latched {
		t.Error("Clear did not unlatch the machine")
	}
	if tok, _ := StatusToken(local); tok == TokenErr {
		t.Errorf("token after Clear = %q, want the latch gone", tok)
	}
}

// A missing body latches with its own sentence, since the remote did not refuse
// anything and the way out is restoring the object.
func TestMissingBodyLatchesWithItsOwnCause(t *testing.T) {
	t.Parallel()
	b, _ := breakerUnder(t)
	if err := b.Refused(fmt.Errorf("import: %w", synclog.ErrBlobMissing)); err != nil {
		t.Fatalf("Refused: %v", err)
	}
	if cause, _ := b.LatchCause(); cause != latchMissing {
		t.Errorf("latch cause = %q, want %q", cause, latchMissing)
	}
}
