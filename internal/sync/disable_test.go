package sync

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// blackhole answers nothing until its context expires, which is what a remote
// that accepts a connection and then goes silent looks like from here. It is
// the client a test needs to prove the turn-off's bound is load-bearing.
type blackhole struct {
	calls atomic.Int64
	// block is how long a call pretends to work before answering. It is set far
	// past any timeout under test, so the context is always what ends the call.
	block time.Duration
}

func (b *blackhole) wait(ctx context.Context) error {
	b.calls.Add(1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(b.block):
		return nil
	}
}

func (b *blackhole) Push(ctx context.Context) error { return b.wait(ctx) }

// pushFake records the final-push calls a turn-off makes and serves the one
// scripted failure a test names.
type pushFake struct {
	// Calls is every push the turn-off made, in order.
	Calls []string
	// Err is what a push fails with; nil is a success.
	Err error
}

func (f *pushFake) Push(context.Context) error {
	f.Calls = append(f.Calls, "push")
	return f.Err
}

// disableFixture is a machine that has been enabled: the token stored and the
// mark written, so a turn-off under test has something real to take away.
func disableFixture(t *testing.T, client pusher) (*Disabler, Local) {
	t.Helper()

	_, _, local := openSplit(t)
	if err := SetToken(local, []byte(tokenFixture), tokenNow); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	if err := MarkEnabled(local, true, tokenNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	return &Disabler{
		Local:   local,
		Client:  client,
		Now:     func() time.Time { return tokenNow },
		Timeout: time.Second,
	}, local
}

// TestDisableKeepsLocalUsable pins what a turn-off must not do. It removes the
// token and the mark and nothing else: the local files keep every row they
// hold, the other local marks are untouched, and the machine still reads as a
// database a daemon can serve. A user who turns sync off has left cloud sync,
// not lost their record.
func TestDisableKeepsLocalUsable(t *testing.T) {
	t.Parallel()

	disabler, local := disableFixture(t, &pushFake{})
	// A local marker that has nothing to do with sync, to show the turn-off is
	// not a wipe of the machine-local file.
	if err := local.KVPut("ledger.round", []byte(`{"round":7}`)); err != nil {
		t.Fatalf("write an unrelated local marker: %v", err)
	}

	if _, err := disabler.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	if on, err := Enabled(local); err != nil || on {
		t.Errorf("Enabled after disable = %v, %v; want false", on, err)
	}
	if body, ok, err := local.KVGet("ledger.round"); err != nil || !ok || !strings.Contains(string(body), `"round":7`) {
		t.Errorf("an unrelated local marker was disturbed: %q, %v, %v", body, ok, err)
	}
	// The section is left readable, so a re-enable starts from what was
	// configured rather than from nothing.
	settings, err := ReadSettings(local)
	if err != nil {
		t.Fatalf("ReadSettings: %v", err)
	}
	if settings.Enabled {
		t.Error("the section still reads enabled")
	}
}

// TestDisableFinalPushFailureStillDisables pins the first step's whole
// contract. The final push is best-effort and bounded: a remote that cannot be
// reached is the reason a machine leaves, and a turn-off that refused to finish
// would leave it pushing at a remote it cannot reach. The failure is kept as a
// warning so the caller can say so, and the other three steps still run.
func TestDisableFinalPushFailureStillDisables(t *testing.T) {
	t.Parallel()

	fake := &pushFake{Err: errors.New("dial tcp: no route to host")}
	disabler, local := disableFixture(t, fake)

	res, err := disabler.Disable(context.Background())
	if err != nil {
		t.Fatalf("Disable with an unreachable remote = %v, want the rest to run", err)
	}
	if res.FinalPush {
		t.Error("the final push reported success against a failing remote")
	}
	if res.FinalPushErr == nil {
		t.Error("the failure was dropped instead of reported as a warning")
	}
	if len(res.Steps) != 4 {
		t.Fatalf("steps = %v, want all four", res.Steps)
	}
	if on, _ := Enabled(local); on {
		t.Error("a failed final push left the machine marked on")
	}
	if _, ok, _ := ReadToken(local); ok {
		t.Error("a failed final push left the token in place")
	}
}

// TestDisableDeletesToken pins that the credential goes, and that it goes after
// the mark rather than before. A token deleted first leaves a window in which
// the machine is still marked on and can no longer authenticate -- a machine
// that reports itself as syncing while every tick fails with an authorisation
// error is worse than one that is plainly off.
func TestDisableDeletesToken(t *testing.T) {
	t.Parallel()

	fake := &pushFake{}
	disabler, local := disableFixture(t, fake)

	res, err := disabler.Disable(context.Background())
	if err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if _, ok, err := ReadToken(local); err != nil {
		t.Fatalf("ReadToken after disable: %v", err)
	} else if ok {
		t.Error("the token is still in the local secret table")
	}
	markAt, tokenAt := -1, -1
	for i, step := range res.Steps {
		switch step {
		case stepMarkOff:
			markAt = i
		case stepDeleteToke:
			tokenAt = i
		}
	}
	if markAt < 0 || tokenAt < 0 {
		t.Fatalf("steps = %v, want both the mark and the token delete", res.Steps)
	}
	if tokenAt < markAt {
		t.Errorf("steps = %v, want the token deleted after the mark", res.Steps)
	}
}

// TestDisableRunsTheFourStepsInOrder pins the whole order as one thing, and the
// final push's failure as something that does not reorder it. It is the order
// the turn-off exists to guarantee, so it is pinned once as a sequence rather
// than only in pairs.
func TestDisableRunsTheFourStepsInOrder(t *testing.T) {
	t.Parallel()

	fake := &pushFake{Err: errors.New("the remote is unreachable")}
	closed := 0
	disabler, _ := disableFixture(t, fake)
	disabler.Close = func() error { closed++; return nil }

	res, err := disabler.Disable(context.Background())
	if err != nil {
		t.Fatalf("Disable: %v", err)
	}
	want := []string{stepFinalPush, stepMarkOff, stepDeleteToke, stepClose}
	if len(res.Steps) != len(want) {
		t.Fatalf("steps = %v, want %v", res.Steps, want)
	}
	for i := range want {
		if res.Steps[i] != want[i] {
			t.Fatalf("steps = %v, want %v", res.Steps, want)
		}
	}
	if !res.Closed || closed != 1 {
		t.Errorf("the handle was closed %d times, want exactly once", closed)
	}
	if len(fake.Calls) != 1 || fake.Calls[0] != "push" {
		t.Errorf("the client recorded %v, want exactly the final push", fake.Calls)
	}
}

// TestDisableClosesTheHandleLast pins that closing is the last step and that a
// close failure is reported rather than swallowed: a caller told the turn-off
// worked must not be holding a handle it believes was released.
func TestDisableClosesTheHandleLast(t *testing.T) {
	t.Parallel()

	closed := 0
	disabler, local := disableFixture(t, &pushFake{})
	disabler.Close = func() error { closed++; return errors.New("the handle is busy") }

	res, err := disabler.Disable(context.Background())
	if err == nil {
		t.Fatal("Disable with a failing close returned nil, want the failure reported")
	}
	if closed != 1 {
		t.Errorf("the close was attempted %d times, want once", closed)
	}
	if len(res.Steps) != 4 || res.Steps[3] != stepClose {
		t.Errorf("steps = %v, want the close last", res.Steps)
	}
	// The steps before it still ran, so a machine whose handle will not close is
	// still off and still holds no token.
	if on, _ := Enabled(local); on {
		t.Error("a failing close left the machine marked on")
	}
	if _, ok, _ := ReadToken(local); ok {
		t.Error("a failing close left the token in place")
	}
}

// TestDisableCompletesTheWedgedMachine pins the escape out of the machine a
// crashed enable left: marked on, token stored, and no handle because the enable
// died before opening one.
//
// What is pinned is that a turn-off over that state completes with no handle and
// no network, marks the machine off, and forgets the token. A machine that no
// enable can finish is still a machine a reader has to be able to stop syncing.
func TestDisableCompletesTheWedgedMachine(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)
	// Everything a crashed enable leaves: the remote stored, the token stored
	// and the mark on, with nothing open behind it.
	if err := PutSettings(local, Settings{RemoteURL: "libsql://example.invalid"}, tokenNow); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := SetToken(local, []byte(tokenFixture), tokenNow); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	if err := MarkEnabled(local, true, tokenNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	if on, err := Enabled(local); err != nil || !on {
		t.Fatalf("the fixture is not marked on: %v, %v", on, err)
	}

	// No client and no Close: a machine with nothing open.
	disabler := &Disabler{Local: local, Now: func() time.Time { return tokenNow }, Timeout: time.Second}
	res, err := disabler.Disable(context.Background())
	if err != nil {
		t.Fatalf("Disable over a wedged machine: %v", err)
	}
	if res.FinalPush {
		t.Error("a machine with no handle reported a successful final push")
	}
	if res.FinalPushErr != nil {
		t.Errorf("a skipped push was reported as a failure: %v", res.FinalPushErr)
	}
	// All four steps still ran, so the report reads whole even though one was
	// skipped rather than performed.
	if len(res.Steps) != 4 {
		t.Errorf("steps = %v, want all four", res.Steps)
	}
	if on, _ := Enabled(local); on {
		t.Error("the machine is still marked on")
	}
	if _, ok, _ := ReadToken(local); ok {
		t.Error("the token survived the turn-off")
	}
}

// TestDisableWithoutAHandleStillTurnsOff pins the machine with nothing open. A
// turn-off has no client to push through, so the attempt is skipped rather than
// failed: the machine is off, holds no token, and the report says the push did
// not happen instead of claiming it did.
func TestDisableWithoutAHandleStillTurnsOff(t *testing.T) {
	t.Parallel()

	disabler, local := disableFixture(t, nil)

	res, err := disabler.Disable(context.Background())
	if err != nil {
		t.Fatalf("Disable with no handle: %v", err)
	}
	if res.FinalPush {
		t.Error("a machine with no handle reported a successful final push")
	}
	if res.FinalPushErr != nil {
		t.Errorf("a skipped push was reported as a failure: %v", res.FinalPushErr)
	}
	if on, _ := Enabled(local); on {
		t.Error("the machine is still marked on")
	}
}

// TestDisableFinalPushIsBounded pins that the attempt cannot hang a turn-off. A
// remote that accepts a connection and then goes silent costs one bounded wait,
// so a machine can always stop syncing even on the network that is failing it.
func TestDisableFinalPushIsBounded(t *testing.T) {
	t.Parallel()

	hole := &blackhole{block: time.Hour}
	disabler, _ := disableFixture(t, hole)
	disabler.Timeout = 20 * time.Millisecond

	start := time.Now()
	res, err := disabler.Disable(context.Background())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Disable against a blackholed remote: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("the final push took %s, want the bound to end it", elapsed)
	}
	if res.FinalPushErr == nil {
		t.Error("a blackholed push was reported as a success")
	}
}
