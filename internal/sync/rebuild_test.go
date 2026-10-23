package sync

// The corrupt-replica recovery, tested with no file it does not make, no
// network and no worker: the classifier is a pure function, the record and the
// cap are kv reads and writes, and the move runs over a temp directory with a
// transport double standing in for the worker.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// rebuildNow is the instant the record and cap tests measure from.
var rebuildNow = time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)

// TestIsReplicaCorruptClassifiesTheDriverAndTheNullBatch pins the classifier
// both ways: the two exact strings the issue reported classify, and so does
// the malformed-image wording, while a remote refusal, a blackholed call, a
// quota refusal, an auth refusal, and the same corrupt words arriving from
// the daemon's own file with no worker marker behind them do not.
//
// Mutation: dropping the worker-marker requirement fails the daemon's-own-file
// case; deleting a needle from the table fails that needle's case.
func TestIsReplicaCorruptClassifiesTheDriverAndTheNullBatch(t *testing.T) {
	t.Parallel()

	nullBatch := `syncworker: read an entry: sql: Scan error on column index 2, name "batch": converting NULL to int is unsupported`
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"the torn page the contabo log showed, wrapped as the importer wraps it",
			fmt.Errorf("synclog: import: read the floors: %s: turso: database is corrupt: range 25445.. out of bounds for buffer size 4096",
				WorkerRefusedText),
			true},
		{"the NULL batch read the issue reported, under the worker's refusal",
			fmt.Errorf("synclog: export: append: %s: %s", WorkerRefusedText, nullBatch),
			true},
		{"a malformed image under the worker's refusal",
			fmt.Errorf("synclog: import: read the floors: %s: database disk image is malformed", WorkerRefusedText),
			true},
		{"the marker in one link of the chain and the needles in another",
			fmt.Errorf("%s: %w", WorkerRefusedText,
				errors.New(`sql: Scan error on column index 2, name "batch": converting NULL to int is unsupported`)),
			true},
		{"the same corrupt words from the daemon's own file, no worker marker",
			errors.New("db: tx: turso: database is corrupt: range 25445.. out of bounds for buffer size 4096"),
			false},
		{"a remote's constraint refusal through the worker",
			fmt.Errorf("synclog: export: append: %s: sync: push: the remote refused this machine's change set: SQLITE_CONSTRAINT: FOREIGN KEY constraint failed",
				WorkerRefusedText),
			false},
		{"a call the network blackholed",
			errors.New("sync: a step ran past its deadline"),
			false},
		{"a quota refusal through the worker",
			fmt.Errorf("synclog: export: append: %s: %w", WorkerRefusedText, ErrQuotaReached),
			false},
		{"an auth refusal",
			fmt.Errorf("synclog: import: %w", ErrAuthRefused),
			false},
		{"nothing at all", nil, false},
	} {
		if got := IsReplicaCorrupt(tc.err); got != tc.want {
			t.Errorf("%s: IsReplicaCorrupt = %t, want %t (%v)", tc.name, got, tc.want, tc.err)
		}
	}
}

// TestRebuildDueWaitsTheWindow pins the cap: a machine that never rebuilt is
// due at once, one that rebuilt this instant is not, one whose record is a
// second short of the window is not, one whose record is exactly the window
// old is, and a record too corrupt to parse reads as none and is due.
//
// Mutation: flipping the window comparison to a strict `>` makes the boundary
// case fail.
func TestRebuildDueWaitsTheWindow(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)

	if due, err := RebuildDue(local, rebuildNow); err != nil || !due {
		t.Errorf("no record: due = %t, %v; want due", due, err)
	}
	if err := WriteRebuild(local, rebuildNow, ""); err != nil {
		t.Fatalf("WriteRebuild: %v", err)
	}
	if due, err := RebuildDue(local, rebuildNow); err != nil || due {
		t.Errorf("a record at now: due = %t, %v; want not due", due, err)
	}
	if err := WriteRebuild(local, rebuildNow.Add(-RebuildWindow+time.Second), ""); err != nil {
		t.Fatalf("WriteRebuild: %v", err)
	}
	if due, err := RebuildDue(local, rebuildNow); err != nil || due {
		t.Errorf("a record a second short of the window: due = %t, %v; want not due", due, err)
	}
	if err := WriteRebuild(local, rebuildNow.Add(-RebuildWindow), ""); err != nil {
		t.Fatalf("WriteRebuild: %v", err)
	}
	if due, err := RebuildDue(local, rebuildNow); err != nil || !due {
		t.Errorf("a record exactly the window old: due = %t, %v; want due", due, err)
	}
	// A record the machine cannot read back -- valid JSON, since the kv takes
	// nothing else, but not a record -- reads as none.
	if err := local.KVPut(KeyRebuild, []byte(`{"at":"not a time"}`)); err != nil {
		t.Fatalf("KVPut(corrupt): %v", err)
	}
	if due, err := RebuildDue(local, rebuildNow); err != nil || !due {
		t.Errorf("a corrupt record: due = %t, %v; want due, reading as no record", due, err)
	}
}

// TestRebuildRecordRoundTrips pins the record's shape: what was written reads
// back as it was written, and the aside is absent from the stored bytes when
// there is none, so a machine that rebuilt with nothing to name carries no
// empty field.
func TestRebuildRecordRoundTrips(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)

	aside := filepath.Join(t.TempDir(), asideDirName)
	if err := WriteRebuild(local, rebuildNow, aside); err != nil {
		t.Fatalf("WriteRebuild: %v", err)
	}
	got, err := ReadRebuild(local)
	if err != nil {
		t.Fatalf("ReadRebuild: %v", err)
	}
	if !got.At.Equal(rebuildNow.UTC()) || got.Aside != aside {
		t.Errorf("ReadRebuild = %+v, want at %s and aside %s", got, rebuildNow.UTC(), aside)
	}

	if err := WriteRebuild(local, rebuildNow, ""); err != nil {
		t.Fatalf("WriteRebuild: %v", err)
	}
	body, ok, err := local.KVGet(KeyRebuild)
	if err != nil || !ok {
		t.Fatalf("KVGet(KeyRebuild) = %q, %v, want the record", body, err)
	}
	if strings.Contains(string(body), "aside") {
		t.Errorf("the record carries an aside that was never set: %s", body)
	}
	if got, err := ReadRebuild(local); err != nil || got.Aside != "" {
		t.Errorf("ReadRebuild = %+v, %v; want an empty aside", got, err)
	}
}

// TestLatchCorruptReplicaNamesTheManualVerb pins the latch sentence: the
// machine reads as latched through the breaker's own surface, and the cause
// names the retry verb with the rebuild flag, which is the way out the cap
// leaves.
//
// Mutation: shortening the sentence to drop the command name fails the
// contains check.
func TestLatchCorruptReplicaNamesTheManualVerb(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)

	if err := LatchCorruptReplica(local, rebuildNow); err != nil {
		t.Fatalf("LatchCorruptReplica: %v", err)
	}
	b := NewBreaker(local)
	cause, err := b.LatchCause()
	if err != nil {
		t.Fatalf("LatchCause: %v", err)
	}
	if cause != latchReplicaCorrupt {
		t.Errorf("LatchCause = %q, want the fixed sentence %q", cause, latchReplicaCorrupt)
	}
	if latched, err := b.Latched(); err != nil || !latched {
		t.Errorf("Latched = %t, %v; want latched", latched, err)
	}
	if !strings.Contains(cause, "relevo db sync retry --rebuild") {
		t.Errorf("the latch cause %q does not name the manual rebuild", cause)
	}
}

// closeProbe is the transport double a move test hands the runner: it records
// its own Close and, inside it, whether the replica file was still there, so
// the order -- worker stopped before anything moved -- is asserted and not
// assumed.
type closeProbe struct {
	synclog.LogTransport
	closed      bool
	fileAtClose bool
	path        string
}

func (p *closeProbe) Close() error {
	p.closed = true
	_, err := os.Stat(p.path)
	p.fileAtClose = err == nil
	return nil
}

// seedSyncFiles writes the replica and the driver files named after it into
// dir, each marked with the round that seeded it so two copies tell apart.
func seedSyncFiles(t *testing.T, dir string, round int, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(fmt.Sprintf("copy %d", round)), 0o600); err != nil {
			t.Fatalf("seed %s: %v", n, err)
		}
	}
}

// goneFromState asserts none of names is left in dir and each is in aside.
func goneFromState(t *testing.T, state, aside string, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(state, n)); !os.IsNotExist(err) {
			t.Errorf("%s is still in the state directory: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(aside, n)); err != nil {
			t.Errorf("%s is not in the aside folder: %v", n, err)
		}
	}
}

// TestRebuildReplicaMovesTheFilesAndKeepsOneAside pins the move: the worker is
// closed while the replica file is still present, the replica and its driver
// files leave the state directory and land in the aside folder, the record
// names the folder, and a second rebuild leaves exactly one aside folder
// holding the second copy -- the prune took the first.
//
// Mutations: removing the pre-move prune leaves the first copy's files beside
// the second's and fails the aside's contents; moving the main file before its
// siblings fails the order of Moved.
func TestRebuildReplicaMovesTheFilesAndKeepsOneAside(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)
	state := t.TempDir()
	replica := filepath.Join(state, "relevo-sync.db")
	names := []string{"relevo-sync.db", "relevo-sync.db-wal", "relevo-sync.db-shm"}
	aside := filepath.Join(state, asideDirName)
	probe := &closeProbe{path: replica}
	runner := &Runner{Client: probe, Local: local, ReplicaPath: replica}

	seedSyncFiles(t, state, 1, names...)
	out, err := runner.RebuildReplica(rebuildNow)
	if err != nil {
		t.Fatalf("RebuildReplica: %v", err)
	}
	if !probe.closed || !probe.fileAtClose {
		t.Errorf("the worker was closed=%t with the file present=%t, want closed while present",
			probe.closed, probe.fileAtClose)
	}
	goneFromState(t, state, aside, names...)
	if out.Aside != aside || len(out.Moved) != len(names) || out.Moved[len(out.Moved)-1] != replica {
		t.Errorf("RebuildResult = aside %q moved %v; want aside %q and the main file moved last",
			out.Aside, out.Moved, aside)
	}
	got, err := ReadRebuild(local)
	if err != nil {
		t.Fatalf("ReadRebuild: %v", err)
	}
	if !got.At.Equal(rebuildNow.UTC()) || got.Aside != aside {
		t.Errorf("the record = %+v, want at %s and aside %s", got, rebuildNow.UTC(), aside)
	}

	// The retry bootstrapped a fresh replica: this copy is what a clean
	// machine holds, with no write-ahead file to its name. It must replace
	// the first copy, not sit beside it.
	seedSyncFiles(t, state, 2, "relevo-sync.db", "relevo-sync.db-wal")
	if _, err := runner.RebuildReplica(rebuildNow.Add(time.Hour)); err != nil {
		t.Fatalf("second RebuildReplica: %v", err)
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		t.Fatalf("ReadDir(state): %v", err)
	}
	var asides []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), asideDirName) {
			asides = append(asides, e.Name())
		}
	}
	if len(asides) != 1 || asides[0] != asideDirName {
		t.Fatalf("the state directory holds aside folders %v, want exactly %s", asides, asideDirName)
	}
	kept, err := os.ReadDir(aside)
	if err != nil {
		t.Fatalf("ReadDir(aside): %v", err)
	}
	var keptNames []string
	for _, e := range kept {
		keptNames = append(keptNames, e.Name())
	}
	if len(keptNames) != 2 || keptNames[0] != "relevo-sync.db" || keptNames[1] != "relevo-sync.db-wal" {
		t.Errorf("the aside holds %v, want exactly the second copy's two files", keptNames)
	}
	body, err := os.ReadFile(filepath.Join(aside, "relevo-sync.db"))
	if err != nil {
		t.Fatalf("read the kept copy: %v", err)
	}
	if string(body) != "copy 2" {
		t.Errorf("the kept copy is %q, want the second copy", body)
	}
}

// TestRebuildReplicaRefusesWithNoReplicaPath pins the refusal: a runner with
// no replica path has nothing to move, reports it, and writes no record, so
// the cap is not spent on a rebuild that did not happen.
func TestRebuildReplicaRefusesWithNoReplicaPath(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)

	runner := &Runner{Local: local}
	out, err := runner.RebuildReplica(rebuildNow)
	if !errors.Is(err, errNoReplica) {
		t.Fatalf("RebuildReplica with no path = %v, want errNoReplica", err)
	}
	if out.Aside != "" || len(out.Moved) != 0 {
		t.Errorf("RebuildResult = %+v, want the zero value", out)
	}
	got, err := ReadRebuild(local)
	if err != nil {
		t.Fatalf("ReadRebuild: %v", err)
	}
	if !got.At.IsZero() || got.Aside != "" {
		t.Errorf("a refused rebuild wrote a record: %+v", got)
	}
}

// TestRebuildReplicaRecordsNothingWhenTheMoveFails pins behaviour 6: a move
// that fails leaves the machine readable -- the replica file is still where it
// was -- and writes no record, so the cap is not spent and the next attempt
// tries again.
func TestRebuildReplicaRecordsNothingWhenTheMoveFails(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)
	state := t.TempDir()
	replica := filepath.Join(state, "relevo-sync.db")
	names := []string{"relevo-sync.db", "relevo-sync.db-wal", "relevo-sync.db-shm"}
	seedSyncFiles(t, state, 1, names...)

	// A state directory nobody can write can hold no aside folder, so no move
	// can land in it. The mode goes back before the temp dir is cleaned up.
	if err := os.Chmod(state, 0o500); err != nil {
		t.Fatalf("Chmod(state): %v", err)
	}
	defer func() { _ = os.Chmod(state, 0o700) }()

	runner := &Runner{Client: &closeProbe{path: replica}, Local: local, ReplicaPath: replica}
	if _, err := runner.RebuildReplica(rebuildNow); err == nil {
		t.Fatal("RebuildReplica over an unwritable state directory succeeded")
	}
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(state, n)); err != nil {
			t.Errorf("%s did not survive the failed move: %v", n, err)
		}
	}
	got, err := ReadRebuild(local)
	if err != nil {
		t.Fatalf("ReadRebuild: %v", err)
	}
	if !got.At.IsZero() || got.Aside != "" {
		t.Errorf("a failed rebuild wrote a record: %+v", got)
	}
}

// TestRebuildCorruptLatchesInsideTheWindowAndRebuildsAfterIt pins the policy:
// a first corruption rebuilds and records; a second one an hour later latches
// instead, with the files untouched; one that arrives with the window spent
// rebuilds again, because a fault that waited a day out is a new fault.
//
// Mutation: dropping the RebuildDue check makes the second call move the files
// and fail the not-moved assertion.
func TestRebuildCorruptLatchesInsideTheWindowAndRebuildsAfterIt(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)
	state := t.TempDir()
	replica := filepath.Join(state, "relevo-sync.db")
	names := []string{"relevo-sync.db", "relevo-sync.db-wal", "relevo-sync.db-shm"}
	aside := filepath.Join(state, asideDirName)
	probe := &closeProbe{path: replica}
	runner := &Runner{Client: probe, Local: local, ReplicaPath: replica}

	seedSyncFiles(t, state, 1, names...)
	out, rebuilt, err := runner.RebuildCorrupt(rebuildNow)
	if err != nil || !rebuilt {
		t.Fatalf("first RebuildCorrupt = rebuilt %t, %v; want rebuilt", rebuilt, err)
	}
	if out.Aside != aside || !out.At.Equal(rebuildNow.UTC()) {
		t.Errorf("first result = %+v, want aside %s at %s", out, aside, rebuildNow.UTC())
	}
	goneFromState(t, state, aside, names...)

	// The retry bootstrapped a fresh replica, and the corruption came back an
	// hour later: inside the window, so the machine latches and nothing moves.
	seedSyncFiles(t, state, 2, "relevo-sync.db", "relevo-sync.db-wal")
	latched, rebuiltAgain, err := runner.RebuildCorrupt(rebuildNow.Add(time.Hour))
	if err != nil || rebuiltAgain {
		t.Fatalf("second RebuildCorrupt = rebuilt %t, %v; want latched instead", rebuiltAgain, err)
	}
	if latched.Aside != "" || len(latched.Moved) != 0 {
		t.Errorf("the latched call reported a move: %+v", latched)
	}
	if _, err := os.Stat(filepath.Join(state, "relevo-sync.db")); err != nil {
		t.Errorf("the latched call moved the replica: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "relevo-sync.db-wal")); err != nil {
		t.Errorf("the latched call moved the write-ahead file: %v", err)
	}
	b := NewBreaker(local)
	if l, err := b.Latched(); err != nil || !l {
		t.Errorf("Latched = %t, %v; want the corruption to have latched the machine", l, err)
	}
	if cause, err := b.LatchCause(); err != nil || !strings.Contains(cause, "retry --rebuild") {
		t.Errorf("LatchCause = %q, %v; want the manual rebuild named", cause, err)
	}

	// A corruption that waited the window out is a new fault: the machine
	// rebuilds again, and the fresh replica this simulates is what the
	// previous rebuild's retry left behind.
	seedSyncFiles(t, state, 3, "relevo-sync.db", "relevo-sync.db-wal", "relevo-sync.db-shm")
	if _, rebuiltThird, err := runner.RebuildCorrupt(rebuildNow.Add(RebuildWindow)); err != nil || !rebuiltThird {
		t.Fatalf("third RebuildCorrupt = rebuilt %t, %v; want rebuilt", rebuiltThird, err)
	}
	goneFromState(t, state, aside, names...)
}
