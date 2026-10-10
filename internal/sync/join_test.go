package sync

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The enable-as-join cases: the preflight's fixed order, the join's resumption
// from its marker, its bounded chunks, and the refusal a remote that is not a
// log gets before any mark is written. Every case runs against a real split
// pair and the in-memory log, so no CI test reaches a network or spawns a
// worker.

// joinRemote is the remote an enable in these cases is pointed at. It is never
// dialled: the transport is the in-memory log.
const joinRemote = "libsql://join.invalid"

// joinToken is the token the enable cases store. It is long and unmistakable so
// a redaction assertion elsewhere has something to grep for.
const joinToken = "FIXTURE-JOIN-TOKEN-0123456789abcdef0123456789abcdef"

// joinPair opens a split pair as one installation and returns the shared handle
// with the machine-local file attached.
func joinPair(t *testing.T, origin string) (*db.DB, Local) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo.db")
	shared, err := db.OpenSplit(path, db.Options{Origin: origin})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	return shared, local
}

// passGate runs the compress pass the origin gate names, so a case that wants
// the gate to pass has a database that may leave the machine.
func passGate(t *testing.T, shared *db.DB) {
	t.Helper()
	if _, _, err := db.CompressHistoryOnce(shared, t.TempDir(), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
}

// seedJoin writes the rows a case starts from. The statements go through the
// file's own triggers, so an owned row is one this installation owns by its
// origin column rather than by the test naming it.
func seedJoin(t *testing.T, shared *db.DB, statements ...string) {
	t.Helper()
	raw, err := db.OpenRaw(shared.Path())
	if err != nil {
		t.Fatalf("db.OpenRaw: %v", err)
	}
	defer func() { _ = raw.Close() }()
	for _, stmt := range statements {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("seed %s: %v", stmt, err)
		}
	}
}

// joinBinding is one owned root row, so an export has something of this
// installation's to propose.
func joinBinding(id, origin string) string {
	return fmt.Sprintf(`INSERT INTO binding (id, name, cwd, builder_mode, created_at, ingest_source, origin)
		VALUES ('%s', '%s', '/x', 'local', 't', 'manual', '%s')`, id, id, origin)
}

// joinRecord and joinRoundFile are a parent with a child carrying a compressed
// body, so the bulk content a round file holds is a row like any other.
func joinRecord(id, origin string) string {
	return fmt.Sprintf(`INSERT INTO binding_record (id, owner, name, state, round, cwd, record_json,
		created_at, updated_at, origin)
		VALUES ('%s', 'o', 'n', 'open', 1, '/z', '{}', 't', 't', '%s')`, id, origin)
}

func joinRoundFile(recordID string) string {
	return fmt.Sprintf(`INSERT INTO round_file (record_id, name, round, body, bytes, sha256, mtime, sealed_at)
		VALUES ('%s', 'f', 1, X'0001', 1, 's', 't', 't')`, recordID)
}

// joinLog is the log a join case drives: the in-memory transport with a record
// of every append and the ability to refuse one, so a test can stop a join in
// the middle of its export.
type joinLog struct {
	*synclog.MemTransport
	batches [][]synclog.Entry
	failAt  int
	err     error
}

// Append records the batch and forwards it, or refuses the batch named by
// failAt and forwards nothing. The refusal is the ordinary transport failure a
// worker that went away produces.
func (l *joinLog) Append(entries []synclog.Entry) ([]synclog.Entry, error) {
	if l.failAt > 0 && len(l.batches)+1 >= l.failAt {
		return nil, l.err
	}
	l.batches = append(l.batches, entries)
	return l.MemTransport.Append(entries)
}

// entries is every entry the log was handed, in the order the appends carried
// them.
func (l *joinLog) entries() []synclog.Entry {
	var out []synclog.Entry
	for _, batch := range l.batches {
		out = append(out, batch...)
	}
	return out
}

// runningLog is a transport whose every call refuses, which is what a remote
// that is not a relevo log looks like from the enable's side.
type runningLog struct{ err error }

func (r *runningLog) Append([]synclog.Entry) ([]synclog.Entry, error) { return nil, r.err }
func (r *runningLog) Pull(map[string]int) ([]synclog.Entry, error)    { return nil, r.err }
func (r *runningLog) Head(string) ([]synclog.HeadRow, error)          { return nil, r.err }
func (r *runningLog) Stats() (synclog.Stats, error)                   { return synclog.Stats{}, r.err }

// r2Fixture is the complete set of bucket credentials an enable needs. The
// values are not credentials: nothing here reaches a bucket, and the enable
// under test is refused before any store is built from them.
var r2Fixture = R2Secrets{
	Endpoint: "https://acct.r2.cloudflarestorage.com",
	Bucket:   "relevo-sync",
	KeyID:    "key-1",
	Secret:   "secret-1",
}

func storeR2(tb testing.TB, local Local) {
	tb.Helper()
	if err := SetR2(local, r2Fixture, time.Unix(0, 0).UTC()); err != nil {
		tb.Fatalf("SetR2: %v", err)
	}
}

// runEnable drives one enable with the given log and token, at a fixed clock.
func runEnable(tb testing.TB, shared *db.DB, local Local, token []byte, transport synclog.LogTransport) (EnableResult, error) {
	tb.Helper()
	storeR2(tb, local)
	enabler := &Enabler{
		Request: EnableRequest{
			Local:  local,
			Shared: shared,
			URL:    joinRemote,
			Token:  token,
		},
		Transport: transport,
		Now:       func() time.Time { return time.Unix(0, 0).UTC() },
	}
	return enabler.Enable()
}

// TestEnablePreflightOrder pins the order the enable's checks run in: a machine
// already on, then the token, then the remote, then the section-versus-flag
// conflict, then the origin gate. Every case but the last is set up so each of
// the checks behind it would also fail, so a check that was skipped or moved
// later answers with a different sentinel and the case fails.
func TestEnablePreflightOrder(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0).UTC()
	stored := func(t *testing.T, local Local) {
		t.Helper()
		if err := SetToken(local, []byte(joinToken), now); err != nil {
			t.Fatalf("SetToken: %v", err)
		}
		storeR2(t, local)
	}
	pointed := func(t *testing.T, local Local) {
		t.Helper()
		stored(t, local)
		if err := PutSettings(local, Settings{RemoteURL: "libsql://stored.invalid"}, now); err != nil {
			t.Fatalf("PutSettings: %v", err)
		}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, local Local)
		url   string
		want  error
	}{
		{"already enabled wins over every later check", func(t *testing.T, local Local) {
			t.Helper()
			if err := MarkEnabled(local, true, now); err != nil {
				t.Fatalf("MarkEnabled: %v", err)
			}
		}, "", ErrAlreadyEnabled},
		{"no token wins over the remote and the gate", func(*testing.T, Local) {}, "", ErrNoToken},
		{"a stored token passes the token check and the missing remote decides", stored, "", ErrNoRemote},
		{"a conflicting url wins over the gate", pointed, "libsql://flag.invalid", ErrRemoteConflict},
		{"the origin gate is last", pointed, "", db.ErrPreflightRefused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			shared, local := joinPair(t, "m1")
			tc.setup(t, local)
			_, err := EnableRequest{Local: local, Shared: shared, URL: tc.url}.Preflight()
			if !errors.Is(err, tc.want) {
				t.Fatalf("Preflight = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestEnablePreflightResolvesTheRemote pins the passing case: with every check
// cleared the preflight hands back the remote the enable works from.
func TestEnablePreflightResolvesTheRemote(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	passGate(t, shared)
	storeR2(t, local)
	plan, err := EnableRequest{
		Local:  local,
		Shared: shared,
		URL:    joinRemote,
		Token:  []byte(joinToken),
	}.Preflight()
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if plan.Remote != joinRemote {
		t.Errorf("remote = %q, want %q", plan.Remote, joinRemote)
	}
	if plan.R2 != r2Fixture {
		t.Errorf("R2 = %+v, want the stored credentials", plan.R2)
	}
}

// TestJoinResumesFromTheJoinMarker pins the join's interruption point: a join
// that stops mid-export leaves the machine off with the marker set, a second
// enable resumes from the marker, and the rows the first run already converged
// are not sent again.
//
// The first run's transport refuses its second append, so the first chunk lands
// and the rest does not. On the resume, head already carries the first chunk,
// which is what keeps it out of the second run's batch.
func TestJoinResumesFromTheJoinMarker(t *testing.T) {
	const total = 300
	shared, local := joinPair(t, "m1")
	passGate(t, shared)
	statements := make([]string, 0, total)
	for i := 0; i < total; i++ {
		statements = append(statements, joinBinding(fmt.Sprintf("b%03d", i), "m1"))
	}
	seedJoin(t, shared, statements...)

	log := synclog.NewMemTransport("m1")
	first := &joinLog{MemTransport: log, failAt: 2, err: errors.New("the worker went away mid-export")}
	interrupted, err := runEnable(t, shared, local, []byte(joinToken), first)
	if err == nil {
		t.Fatal("an enable whose transport refused its second append succeeded")
	}
	if interrupted.Resumed {
		t.Error("the first enable reported resuming from a marker nobody had written")
	}
	if _, ok, rerr := ReadJoin(local); rerr != nil || !ok {
		t.Fatalf("join marker = (ok %v, err %v), want a join in progress", ok, rerr)
	}
	if on, _ := Enabled(local); on {
		t.Error("the interrupted enable left the machine marked on, want it off until the join finishes")
	}
	before, err := log.Stats()
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}
	if before.Entries == 0 || before.Entries >= total {
		t.Fatalf("the log holds %d entries after the interruption, want a partial export", before.Entries)
	}

	// The resume supplies no token: the first run stored one, and the second
	// enable is meant to read it rather than be handed it again.
	second := &joinLog{MemTransport: log}
	resumed, err := runEnable(t, shared, local, nil, second)
	if err != nil {
		t.Fatalf("the resumed enable: %v", err)
	}
	if !resumed.Resumed {
		t.Error("the resumed enable did not report resuming from the marker")
	}
	if on, _ := Enabled(local); !on {
		t.Fatal("the resumed enable did not mark the machine on")
	}
	if _, ok, _ := ReadJoin(local); ok {
		t.Error("the finished enable left the join marker behind")
	}
	after, err := log.Stats()
	if err != nil {
		t.Fatalf("read the log after the resume: %v", err)
	}
	if after.Entries != total {
		t.Fatalf("the log holds %d entries after the resume, want %d: a converged row was sent again",
			after.Entries, total)
	}
	if resumed.Appended == 0 || resumed.Appended >= total {
		t.Errorf("the resume appended %d entries, want only the rows head did not yet carry", resumed.Appended)
	}
}

// TestJoinExportsInBoundedResumableChunks pins the export's shape: the first
// export is split into appends no larger than the chunk bound, a bulk body
// travels in one of them like any row, and a re-run of the converged file
// proposes nothing.
func TestJoinExportsInBoundedResumableChunks(t *testing.T) {
	const total = 300
	shared, local := joinPair(t, "m1")
	passGate(t, shared)
	statements := []string{joinRecord("r1", "m1"), joinRoundFile("r1")}
	for i := 0; i < total; i++ {
		statements = append(statements, joinBinding(fmt.Sprintf("b%03d", i), "m1"))
	}
	seedJoin(t, shared, statements...)

	log := &joinLog{MemTransport: synclog.NewMemTransport("m1")}
	res, err := runEnable(t, shared, local, []byte(joinToken), log)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if len(log.batches) < 2 {
		t.Fatalf("the export made %d appends, want the chunk bound to split it", len(log.batches))
	}
	for i, batch := range log.batches {
		// The bound the reconciler carries: one append is one chunk, and a run
		// that stopped sending whole tables would show up here.
		if len(batch) > 256 {
			t.Fatalf("append %d carried %d entries, want at most the chunk bound of 256", i, len(batch))
		}
	}
	if !carriesRoundFileBody(log.entries()) {
		t.Error("no append carried the round_file's body, want bulk content to travel like any row")
	}

	// The re-run is the resume's other half: head now carries every row the
	// first run proposed, so a second walk finds no difference to append.
	again, err := synclog.NewReconciler(shared, log).Reconcile()
	if err != nil {
		t.Fatalf("reconcile the converged file: %v", err)
	}
	if again.Batches != 0 || again.Upserts != 0 || again.Deletes != 0 {
		t.Fatalf("the re-run = %+v, want nothing appended for a file head already carries", again)
	}
	if res.Appended == 0 {
		t.Error("the first export appended nothing, want the file's rows")
	}
}

// carriesRoundFileBody reports whether any recorded entry is a round_file
// upsert carrying a body, which is the bulk content a round file holds.
func carriesRoundFileBody(entries []synclog.Entry) bool {
	for _, e := range entries {
		if e.Table == "round_file" && e.Op == synclog.OpUpsert && len(e.Body) > 0 {
			return true
		}
	}
	return false
}

// TestEnableRefusedRemoteWritesNoMark pins the refusal a remote that is not a
// relevo log gets: the enable fails at its first transport call, the failure
// says why, and neither the enabled mark nor a join marker is written.
func TestEnableRefusedRemoteWritesNoMark(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	passGate(t, shared)

	refusal := fmt.Errorf("sync: refusing remote %s: it is not a relevo sync log: %w", joinRemote, ErrRemoteRefused)
	_, err := runEnable(t, shared, local, []byte(joinToken), &runningLog{err: refusal})
	if err == nil {
		t.Fatal("an enable against a refused remote succeeded")
	}
	if !strings.Contains(err.Error(), "not a relevo sync log") {
		t.Errorf("the refusal %q does not say why", err)
	}
	if on, _ := Enabled(local); on {
		t.Error("the refused enable marked the machine on")
	}
	if _, ok, _ := ReadJoin(local); ok {
		t.Error("the refused enable left a join marker behind")
	}
}

// stoppedLog is the in-memory log with a pull that stops the enable on its
// first call, the way a disable's preempt does, and then lets the pipeline
// reach its last step: a stop that lands mid-join must keep the enabled mark
// off rather than write it and clear it again.
type stoppedLog struct {
	*synclog.MemTransport
	once sync.Once
	stop func()
}

func (s *stoppedLog) Pull(marks map[string]int) ([]synclog.Entry, error) {
	s.once.Do(s.stop)
	return s.MemTransport.Pull(marks)
}

// TestACancelledJoinDoesNotTurnTheMachineOn pins the other half of the
// disable-during-a-join contract: a join a stop released must not run its
// trailing MarkEnabled(true) when it settles. The transport flips the stop
// during the join's first pull, so the pipeline is free to finish and only the
// guard keeps the machine off.
func TestACancelledJoinDoesNotTurnTheMachineOn(t *testing.T) {
	shared, local := joinPair(t, "m1")
	passGate(t, shared)
	storeR2(t, local)

	stopped := false
	log := &stoppedLog{
		MemTransport: synclog.NewMemTransport("m1"),
		stop:         func() { stopped = true },
	}
	_, err := (&Enabler{
		Request:   EnableRequest{Local: local, Shared: shared, URL: joinRemote, Token: []byte(joinToken)},
		Transport: log,
		Stopped:   func() bool { return stopped },
		Now:       func() time.Time { return time.Unix(0, 0).UTC() },
	}).Enable()
	if !errors.Is(err, ErrEnableStopped) {
		t.Fatalf("Enable = %v, want ErrEnableStopped for a join a stop released", err)
	}
	if on, err := Enabled(local); err != nil || on {
		t.Fatalf("Enabled = %v, %v; want the released machine left off", on, err)
	}
}
