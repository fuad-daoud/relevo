package sync

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// migrateRefusal is the wrapped shape a migrate refusal reaches the breaker in:
// the class sentinel under the call's own context, the way the worker raises it.
func migrateRefusal() error { return fmt.Errorf("hello: %w", ErrMigrateWaiting) }

// TestMigrateRefusalWaitsWithoutLatching pins that the migrate refusal sets a
// bounded wait instead of a latch or a death, and that the wait alone makes an
// otherwise healthy machine read behind.
func TestMigrateRefusalWaitsWithoutLatching(t *testing.T) {
	t.Parallel()
	b, local := breakerUnder(t)
	putMarker(t, local, KeyLastTick, `{"at":"2026-10-08T09:29:00Z","ok":true}`)

	if err := b.Refused(migrateRefusal()); err != nil {
		t.Fatalf("Refused: %v", err)
	}
	if latched, _ := b.Latched(); latched {
		t.Fatal("the migrate refusal latched the machine")
	}
	st, err := b.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if st.Deaths != 0 || st.Seen != 0 {
		t.Errorf("deaths = %d, seen = %d; want the refusal counted as neither", st.Deaths, st.Seen)
	}
	if want := breakerNow.Add(10 * time.Minute); !st.WaitUntil.Equal(want) {
		t.Errorf("WaitUntil = %v, want %v", st.WaitUntil, want)
	}
	if tok, _ := StatusToken(local); tok != TokenBehind {
		t.Errorf("token while waiting = %q, want %q", tok, TokenBehind)
	}
}

// TestMigrateWaitingNamesTheWayOut pins the fixed sentence: it is written in
// this tree and names the one action that ends the wait, so a remote's own
// words never reach the reader.
func TestMigrateWaitingNamesTheWayOut(t *testing.T) {
	t.Parallel()
	want := "sync: the remote is not migrated yet; run `relevo db sync migrate` on one machine once every machine runs this release"
	if got := ErrMigrateWaiting.Error(); got != want {
		t.Errorf("ErrMigrateWaiting = %q, want %q", got, want)
	}
}

// TestMigrateWaitKeepsTheDeathCount pins that a wait neither resets nor adds to
// the deaths already counted: the remote answered, so the wait is not a death,
// and the deaths before it still stand.
func TestMigrateWaitKeepsTheDeathCount(t *testing.T) {
	t.Parallel()
	b, local := breakerUnder(t)
	missCall(t, b, local)
	missCall(t, b, local)

	if err := b.Refused(migrateRefusal()); err != nil {
		t.Fatalf("Refused: %v", err)
	}
	st, err := b.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if st.Deaths != 2 || st.Seen != 2 {
		t.Errorf("deaths = %d, seen = %d; want the two counted deaths kept", st.Deaths, st.Seen)
	}
	if latched, _ := b.Latched(); latched {
		t.Error("the migrate refusal latched a machine two deaths in")
	}
	if st.WaitUntil.IsZero() {
		t.Error("the migrate refusal did not set a wait")
	}
}

// TestBeginWaitsOutTheMigrationWindow pins that a call inside the wait refuses
// as waiting for migration -- not backing off -- and writes no in-call marker,
// and that the first hello at the window's end is the retry.
func TestBeginWaitsOutTheMigrationWindow(t *testing.T) {
	t.Parallel()
	b, local := breakerUnder(t)
	if err := b.Refused(migrateRefusal()); err != nil {
		t.Fatalf("Refused: %v", err)
	}

	err := b.Begin("hello")
	if !errors.Is(err, ErrMigrateWaiting) {
		t.Fatalf("Begin inside the wait = %v, want ErrMigrateWaiting", err)
	}
	if errors.Is(err, ErrBackingOff) {
		t.Errorf("Begin inside the wait reads as backing off: %v", err)
	}
	if _, ok, rerr := ReadInCall(local); rerr != nil || ok {
		t.Errorf("a refused Begin left an in-call marker (ok=%v, err=%v)", ok, rerr)
	}

	b.now = func() time.Time { return breakerNow.Add(10*time.Minute - time.Second) }
	if err := b.Begin("hello"); !errors.Is(err, ErrMigrateWaiting) {
		t.Errorf("Begin a second before the window ends = %v, want ErrMigrateWaiting", err)
	}

	b.now = func() time.Time { return breakerNow.Add(10 * time.Minute) }
	if err := b.Begin("hello"); err != nil {
		t.Fatalf("Begin at the window's end = %v, want the retry to proceed", err)
	}
	call, ok, err := ReadInCall(local)
	if err != nil || !ok {
		t.Fatalf("the retry wrote no in-call marker (ok=%v, err=%v)", ok, err)
	}
	if call.Verb != "hello" || call.N != 1 {
		t.Errorf("in-call marker = %+v, want the first hello", call)
	}
	if err := b.End(); err != nil {
		t.Errorf("End: %v", err)
	}
}

// TestDueIsFalseWhileWaitingForMigration pins that Due agrees with Begin: the
// liveness gate reads Due alone, so a waiting machine must not read as due
// until the window ends.
func TestDueIsFalseWhileWaitingForMigration(t *testing.T) {
	t.Parallel()
	b, _ := breakerUnder(t)
	if err := b.Refused(migrateRefusal()); err != nil {
		t.Fatalf("Refused: %v", err)
	}
	if due, err := b.Due(); err != nil || due {
		t.Errorf("Due inside the wait = %v, %v; want false", due, err)
	}
	b.now = func() time.Time { return breakerNow.Add(10 * time.Minute) }
	if due, err := b.Due(); err != nil || !due {
		t.Errorf("Due at the window's end = %v, %v; want true", due, err)
	}
}

// TestSuccessEndsTheMigrationWait pins that a succeeded call ends the wait at
// once: the remote answered, so the next call need not wait out the window.
func TestSuccessEndsTheMigrationWait(t *testing.T) {
	t.Parallel()
	b, local := breakerUnder(t)
	if err := b.Refused(migrateRefusal()); err != nil {
		t.Fatalf("Refused: %v", err)
	}
	if err := b.Success(); err != nil {
		t.Fatalf("Success: %v", err)
	}
	st, err := b.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if !st.WaitUntil.IsZero() {
		t.Errorf("WaitUntil after Success = %v, want zero", st.WaitUntil)
	}
	if err := b.Begin("hello"); err != nil {
		t.Errorf("Begin right after a success = %v, want the wait gone", err)
	}
	if err := b.End(); err != nil {
		t.Errorf("End: %v", err)
	}
	if s, err := ReadState(local); err != nil || !s.MigrationWait.IsZero() {
		t.Errorf("ReadState after Success = %+v, %v; want no migration wait", s, err)
	}
}

// TestClearEndsTheMigrationWait pins that the operator's retry ends the wait
// too, so a migrate run by hand is followed by an attempt without waiting out
// the window.
func TestClearEndsTheMigrationWait(t *testing.T) {
	t.Parallel()
	b, _ := breakerUnder(t)
	if err := b.Refused(migrateRefusal()); err != nil {
		t.Fatalf("Refused: %v", err)
	}
	if err := b.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	st, err := b.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if !st.WaitUntil.IsZero() {
		t.Errorf("WaitUntil after Clear = %v, want zero", st.WaitUntil)
	}
	if err := b.Begin("hello"); err != nil {
		t.Errorf("Begin right after a clear = %v, want the wait gone", err)
	}
	if err := b.End(); err != nil {
		t.Errorf("End: %v", err)
	}
}

// TestMigrateWaitingIsNotAPermanentRefusal pins that the migrate refusal stays
// out of the permanent class: a permanent refusal latches at once, and this one
// must wait instead.
func TestMigrateWaitingIsNotAPermanentRefusal(t *testing.T) {
	t.Parallel()
	if IsPermanentRefusal(ErrMigrateWaiting) {
		t.Error("ErrMigrateWaiting is a permanent refusal")
	}
	if IsPermanentRefusal(migrateRefusal()) {
		t.Error("a wrapped ErrMigrateWaiting is a permanent refusal")
	}
}

// TestReadStateShowsTheMigrationWait pins that the wait reaches State from the
// breaker's marker, that an absent marker reads as no wait, and that a latch
// still outranks the wait on the statusline.
func TestReadStateShowsTheMigrationWait(t *testing.T) {
	t.Parallel()
	b, local := breakerUnder(t)
	putMarker(t, local, KeyLastTick, `{"at":"2026-10-08T09:29:00Z","ok":true}`)

	s, err := ReadState(local)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if !s.MigrationWait.IsZero() {
		t.Errorf("MigrationWait with no marker = %v, want zero", s.MigrationWait)
	}
	if tok, _ := StatusToken(local); tok != TokenOK {
		t.Fatalf("token before any refusal = %q, want %q", tok, TokenOK)
	}

	if err := b.Refused(migrateRefusal()); err != nil {
		t.Fatalf("Refused: %v", err)
	}
	s, err = ReadState(local)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if want := breakerNow.Add(10 * time.Minute); !s.MigrationWait.Equal(want) {
		t.Errorf("MigrationWait = %v, want %v", s.MigrationWait, want)
	}
	if tok, _ := StatusToken(local); tok != TokenBehind {
		t.Errorf("token while waiting = %q, want %q", tok, TokenBehind)
	}

	// A latch needs a human on this machine, so it outranks the wait even while
	// the wait is still set.
	if err := b.Refused(fmt.Errorf("push: %w", ErrRemoteSchema)); err != nil {
		t.Fatalf("Refused: %v", err)
	}
	s, err = ReadState(local)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if !s.Attention || s.MigrationWait.IsZero() {
		t.Fatalf("State = %+v; want both the latch and the wait present", s)
	}
	if tok, _ := StatusToken(local); tok != TokenErr {
		t.Errorf("token latched while waiting = %q, want %q", tok, TokenErr)
	}
}
