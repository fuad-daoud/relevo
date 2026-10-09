package sync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
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

func (b *blackhole) Export(ctx context.Context) error { return b.wait(ctx) }

// exportFake records the final-export calls a turn-off makes and serves the one
// scripted failure a test names.
type exportFake struct {
	// Calls is every export the turn-off made, in order.
	Calls []string
	// Err is what the export fails with; nil is a success.
	Err error
}

func (f *exportFake) Export(context.Context) error {
	f.Calls = append(f.Calls, "export")
	return f.Err
}

// disableFixture is a machine that has been enabled: the token stored and the
// mark written, so a turn-off under test has something real to take away.
func disableFixture(t *testing.T, export func(context.Context) error) (*Disabler, Local) {
	t.Helper()

	_, _, local := openSplit(t)
	if err := SetToken(local, []byte(tokenFixture), tokenNow); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	if err := MarkEnabled(local, true, tokenNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	return &Disabler{
		Local:       local,
		FinalExport: export,
		Now:         func() time.Time { return tokenNow },
		Timeout:     time.Second,
	}, local
}

// TestDisableKeepsLocalUsable pins what a turn-off must not do. It removes the
// token and the mark and nothing else: the local files keep every row they
// hold, the other local marks are untouched, and the machine still reads as a
// database a daemon can serve. A user who turns sync off has left cloud sync,
// not lost their record.
func TestDisableKeepsLocalUsable(t *testing.T) {
	t.Parallel()

	fake := &exportFake{}
	disabler, local := disableFixture(t, fake.Export)
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
// contract. The final export is best-effort and bounded: a remote that cannot
// be reached is the reason a machine leaves, and a turn-off that refused to
// finish would leave it pushing at a remote it cannot reach. The failure is
// kept as a warning so the caller can say so, and the other five steps still
// run.
func TestDisableFinalPushFailureStillDisables(t *testing.T) {
	t.Parallel()

	fake := &exportFake{Err: errors.New("dial tcp: no route to host")}
	disabler, local := disableFixture(t, fake.Export)

	res, err := disabler.Disable(context.Background())
	if err != nil {
		t.Fatalf("Disable with an unreachable remote = %v, want the rest to run", err)
	}
	if res.FinalPush {
		t.Error("the final export reported success against a failing remote")
	}
	if res.FinalPushErr == nil {
		t.Error("the failure was dropped instead of reported as a warning")
	}
	if len(res.Steps) != 6 {
		t.Fatalf("steps = %v, want all six", res.Steps)
	}
	if on, _ := Enabled(local); on {
		t.Error("a failed final export left the machine marked on")
	}
	if _, ok, _ := ReadToken(local); ok {
		t.Error("a failed final export left the token in place")
	}
}

// TestDisableDeletesToken pins that the credential goes, and that it goes after
// the mark rather than before. A token deleted first leaves a window in which
// the machine is still marked on and can no longer authenticate -- a machine
// that reports itself as syncing while every tick fails with an authorisation
// error is worse than one that is plainly off.
func TestDisableDeletesToken(t *testing.T) {
	t.Parallel()

	fake := &exportFake{}
	disabler, local := disableFixture(t, fake.Export)

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

// TestDisableRunsTheSixStepsInOrder pins the whole order as one thing, and the
// final export's failure as something that does not reorder it. It is the order
// the turn-off exists to guarantee, so it is pinned once as a sequence rather
// than only in pairs.
func TestDisableRunsTheSixStepsInOrder(t *testing.T) {
	t.Parallel()

	fake := &exportFake{Err: errors.New("the remote is unreachable")}
	stopped, dropped := 0, 0
	disabler, _ := disableFixture(t, fake.Export)
	disabler.Stop = func() error { stopped++; return nil }
	disabler.Drop = func() error { dropped++; return nil }

	res, err := disabler.Disable(context.Background())
	if err != nil {
		t.Fatalf("Disable: %v", err)
	}
	want := []string{stepFinalPush, stepMarkOff, stepDeleteToke, stepStopWorker, stepDeleteReplica, stepDropClient}
	if len(res.Steps) != len(want) {
		t.Fatalf("steps = %v, want %v", res.Steps, want)
	}
	for i := range want {
		if res.Steps[i] != want[i] {
			t.Fatalf("steps = %v, want %v", res.Steps, want)
		}
	}
	if !res.Stopped || stopped != 1 {
		t.Errorf("the worker was stopped %d times, want exactly once", stopped)
	}
	if !res.Dropped || dropped != 1 {
		t.Errorf("the client was dropped %d times, want exactly once", dropped)
	}
	if len(fake.Calls) != 1 || fake.Calls[0] != "export" {
		t.Errorf("the client recorded %v, want exactly the final export", fake.Calls)
	}
}

// TestDisableDropsTheClientLast pins that dropping the client is the last step
// and that a failure there is reported rather than swallowed: a caller told the
// turn-off worked must not be holding a client it believes was released.
func TestDisableDropsTheClientLast(t *testing.T) {
	t.Parallel()

	dropped := 0
	disabler, local := disableFixture(t, (&exportFake{}).Export)
	disabler.Drop = func() error { dropped++; return errors.New("the client is busy") }

	res, err := disabler.Disable(context.Background())
	if err == nil {
		t.Fatal("Disable with a failing drop returned nil, want the failure reported")
	}
	if dropped != 1 {
		t.Errorf("the drop was attempted %d times, want once", dropped)
	}
	if len(res.Steps) != 6 || res.Steps[5] != stepDropClient {
		t.Errorf("steps = %v, want the drop last", res.Steps)
	}
	// The steps before it still ran, so a machine whose client will not drop is
	// still off and still holds no token.
	if on, _ := Enabled(local); on {
		t.Error("a failing drop left the machine marked on")
	}
	if _, ok, _ := ReadToken(local); ok {
		t.Error("a failing drop left the token in place")
	}
}

// TestDisableDeletesReplicaAndTokenButKeepsImportedRows is the turn-off's whole
// blast radius on one machine: the replica and the driver's files named after
// it go, the token goes, the mark goes -- and the shared file keeps every row,
// because a row imported from another origin is read-only history once this
// machine stops syncing.
//
// The mutation is a deletion that stops at the replica's own name or that
// reaches past its prefix: the driver files survive, or a row the shared file
// holds is taken with them.
func TestDisableDeletesReplicaAndTokenButKeepsImportedRows(t *testing.T) {
	t.Parallel()

	sharedPath, shared, local := openSplit(t)
	if _, err := shared.RecordPut(db.Record{
		Owner: "alice", Name: "webshop", State: "open", JSON: "{}",
		CreatedAt: tokenNow, UpdatedAt: tokenNow,
	}); err != nil {
		t.Fatalf("seed an imported row: %v", err)
	}
	if err := SetToken(local, []byte(tokenFixture), tokenNow); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	if err := MarkEnabled(local, true, tokenNow); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}

	replica := filepath.Join(filepath.Dir(sharedPath), "relevo-sync.db")
	files := []string{replica, replica + "-wal", replica + "-shm", replica + ".lock"}
	for _, name := range files {
		if err := os.WriteFile(name, []byte("driver bytes"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	d := &Disabler{Local: local, ReplicaPath: replica, Now: func() time.Time { return tokenNow }, Timeout: time.Second}
	res, err := d.Disable(context.Background())
	if err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if len(res.Deleted) != len(files) {
		t.Errorf("deleted %v, want the replica and its three driver files", res.Deleted)
	}
	for _, name := range files {
		if _, err := os.Stat(name); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived the turn-off: %v", name, err)
		}
	}
	if _, err := os.Stat(sharedPath); err != nil {
		t.Errorf("the shared file went with the replica: %v", err)
	}
	if on, _ := Enabled(local); on {
		t.Error("the machine is still marked on")
	}
	if _, ok, _ := ReadToken(local); ok {
		t.Error("the token survived the turn-off")
	}

	rows, err := shared.RecordList("alice")
	if err != nil {
		t.Fatalf("RecordList: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("the turn-off left %d shared rows, want the one it imported", len(rows))
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

	// No export, no stop and no drop: a machine with nothing open.
	disabler := &Disabler{Local: local, Now: func() time.Time { return tokenNow }, Timeout: time.Second}
	res, err := disabler.Disable(context.Background())
	if err != nil {
		t.Fatalf("Disable over a wedged machine: %v", err)
	}
	if res.FinalPush {
		t.Error("a machine with no handle reported a successful final export")
	}
	if res.FinalPushErr != nil {
		t.Errorf("a skipped export was reported as a failure: %v", res.FinalPushErr)
	}
	// All six steps still ran, so the report reads whole even though one was
	// skipped rather than performed.
	if len(res.Steps) != 6 {
		t.Errorf("steps = %v, want all six", res.Steps)
	}
	if on, _ := Enabled(local); on {
		t.Error("the machine is still marked on")
	}
	if _, ok, _ := ReadToken(local); ok {
		t.Error("the token survived the turn-off")
	}
}

// TestDisableWithoutAHandleStillTurnsOff pins the machine with nothing open. A
// turn-off has no export to make, so the attempt is skipped rather than failed:
// the machine is off, holds no token, and the report says the export did not
// happen instead of claiming it did.
func TestDisableWithoutAHandleStillTurnsOff(t *testing.T) {
	t.Parallel()

	disabler, local := disableFixture(t, nil)

	res, err := disabler.Disable(context.Background())
	if err != nil {
		t.Fatalf("Disable with no handle: %v", err)
	}
	if res.FinalPush {
		t.Error("a machine with no handle reported a successful final export")
	}
	if res.FinalPushErr != nil {
		t.Errorf("a skipped export was reported as a failure: %v", res.FinalPushErr)
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
	disabler, _ := disableFixture(t, hole.Export)
	disabler.Timeout = 20 * time.Millisecond

	start := time.Now()
	res, err := disabler.Disable(context.Background())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Disable against a blackholed remote: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("the final export took %s, want the bound to end it", elapsed)
	}
	if res.FinalPushErr == nil {
		t.Error("a blackholed export was reported as a success")
	}
}
