package syncworker

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
	turso "turso.tech/database/tursogo"
)

// rekeySpec is the spec a rekeying machine would send: a fresh salt and check
// for epoch 2, the epoch 1 key sealed under it, and one cut per origin in the
// log at the head the caller observed.
func rekeySpec(t *testing.T, from int, heads map[string]int) KeyRekeySpec {
	t.Helper()
	key := []byte("the rekeyed key")
	cuts := make(map[string]EpochCut, len(heads))
	for origin, seq := range heads {
		cuts[origin] = EpochCut{Epoch: from + 1, Seq: seq, Tag: synclog.EpochCutTag(key, origin, from+1, seq)}
	}
	return KeyRekeySpec{
		From: from,
		Key: KeyMeta{
			Salt: []byte("the rekeyed salt"), KDF: "argon2id",
			Check: []byte("the rekeyed check"), Present: true,
			Epoch: from + 1, Wrapped: map[int][]byte{from: []byte("the sealed epoch key")},
		},
		Cuts: cuts,
	}
}

// headsOf is what a rekeying machine observed before it asked: every origin's
// highest sequence, read off the backend it is about to hand the spec to.
func headsOf(t *testing.T, b *TursoBackend) map[string]int {
	t.Helper()
	state, err := b.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks before the rekey: %v", err)
	}
	return state.Heads
}

// metaRow is one row of the replica's meta table, and whether it is there.
func metaRow(t *testing.T, path, key string) (string, bool) {
	t.Helper()
	var value string
	err := openAt(t, path).QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return value, true
}

// TestKeyRekeyWritesTheEpochTheCallerNames pins the whole write: the epoch
// advances to the one the spec names, the new material replaces the old, the
// key it replaced is stored sealed, and every origin's cut lands at the head
// the caller observed -- with a tag that verifies under the new key.
func TestKeyRekeyWritesTheEpochTheCallerNames(t *testing.T) {
	t.Parallel()
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["u"]`, `{"title":"one"}`)}); err != nil {
		t.Fatalf("append: %v", err)
	}

	spec := rekeySpec(t, 1, headsOf(t, b))
	held, err := b.KeyRekey(spec)
	if err != nil {
		t.Fatalf("KeyRekey: %v", err)
	}
	if held.Epoch != 2 || !held.Present {
		t.Errorf("read-back = %+v, want epoch 2, present", held)
	}
	if string(held.Wrapped[1]) != "the sealed epoch key" {
		t.Errorf("read-back wrapped = %v, want the sealed epoch 1 key", held.Wrapped)
	}
	if !synclog.VerifyEpochCut([]byte("the rekeyed key"), "origin-a", 2, 1, spec.Cuts["origin-a"].Tag) {
		t.Fatal("the test's own cut tag does not verify; the pins below mean nothing")
	}
	if epoch, ok := metaRow(t, path, metaKeyEpochKey); !ok || epoch != "2" {
		t.Errorf("key_epoch = %q, %v; want 2", epoch, ok)
	}
	if wrapped, ok := metaRow(t, path, keyWrappedPrefix+"1"); !ok || wrapped == "" {
		t.Errorf("key_wrapped.1 = %q, %v; want the sealed key", wrapped, ok)
	}
	want := "2 1 " + encodeKeyValue(spec.Cuts["origin-a"].Tag)
	if cut, ok := metaRow(t, path, epochCutPrefix+"origin-a"); !ok || cut != want {
		t.Errorf("epoch_cut.origin-a = %q, %v; want %q", cut, ok, want)
	}
}

// TestKeyRekeyRefusesAMovedHeadAndWritesNothing pins the predict-and-verify:
// the cuts are checked against the log's own heads inside the transaction, so
// an origin whose head moved between the caller's read and the write refuses
// the whole rekey and leaves the remote's material as it was.
func TestKeyRekeyRefusesAMovedHeadAndWritesNothing(t *testing.T) {
	t.Parallel()
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["u"]`, `{"title":"one"}`)}); err != nil {
		t.Fatalf("append: %v", err)
	}
	stale := rekeySpec(t, 1, map[string]int{"origin-a": 1})
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["v"]`, `{"title":"two"}`)}); err != nil {
		t.Fatalf("the append that moved the head: %v", err)
	}

	if _, err := b.KeyRekey(stale); err == nil {
		t.Fatal("a rekey whose cuts named a head the log had moved past came back as a success")
	}
	if _, ok := metaRow(t, path, metaKeyEpochKey); ok {
		t.Error("a refused rekey wrote key_epoch")
	}
	if _, ok := metaRow(t, path, metaKDFSaltKey); ok {
		t.Error("a refused rekey wrote a salt")
	}
}

// TestKeyRekeyIsACompareAndSetOnTheEpoch pins that two machines rekeying at
// once ends with the epoch advanced exactly once: the second caller, still
// holding the epoch it read, changes nothing -- not the epoch, not the salt it
// wanted, not the cuts it predicted.
func TestKeyRekeyIsACompareAndSetOnTheEpoch(t *testing.T) {
	t.Parallel()
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["u"]`, `{"title":"one"}`)}); err != nil {
		t.Fatalf("append: %v", err)
	}
	winner := rekeySpec(t, 1, headsOf(t, b))
	if _, err := b.KeyRekey(winner); err != nil {
		t.Fatalf("the winning rekey: %v", err)
	}
	loser := rekeySpec(t, 1, headsOf(t, b))
	if _, err := b.KeyRekey(loser); err == nil {
		t.Fatal("a second rekey from an epoch the remote had left came back as a success")
	}
	if epoch, ok := metaRow(t, path, metaKeyEpochKey); !ok || epoch != "2" {
		t.Errorf("key_epoch = %q, %v; want 2, advanced exactly once", epoch, ok)
	}
	if salt, ok := metaRow(t, path, metaKDFSaltKey); !ok || salt != encodeKeyValue(winner.Key.Salt) {
		t.Errorf("kdf_salt = %q, want the winner's", salt)
	}
	if cut, ok := metaRow(t, path, epochCutPrefix+"origin-a"); ok {
		want := "2 1 " + encodeKeyValue(winner.Cuts["origin-a"].Tag)
		if cut != want {
			t.Errorf("epoch_cut.origin-a = %q, want the winner's %q", cut, want)
		}
	}
}

// TestKeyRekeyRefusesASpecThatDoesNotHoldTogether pins the handler's own
// refusals: a rekey that names no key, no epoch it read, a target that is not
// the next epoch after it, or a cut fencing another epoch than the one being
// written is refused before the backend is reached.
func TestKeyRekeyRefusesASpecThatDoesNotHoldTogether(t *testing.T) {
	t.Parallel()
	b := NewTursoBackend(&localDriver{path: tempReplica(t)})
	w := startWorker(t, b)
	w.askHello()
	for name, req := range map[string]Request{
		"no key":       {ID: "1", Verb: VerbKeyRekey, Epoch: 1},
		"no epoch":     {ID: "2", Verb: VerbKeyRekey, KeyMeta: &KeyMeta{Epoch: 2, Present: true}},
		"not the next": {ID: "3", Verb: VerbKeyRekey, Epoch: 1, KeyMeta: &KeyMeta{Epoch: 3, Present: true}},
		"a foreign cut": {ID: "4", Verb: VerbKeyRekey, Epoch: 1,
			KeyMeta: &KeyMeta{Epoch: 2, Present: true},
			Cuts:    map[string]EpochCut{"origin-b": {Epoch: 5, Seq: 1, Tag: []byte("t")}}},
	} {
		resp := w.ask(req)
		if resp.OK {
			t.Errorf("%s: the request was answered, want a refusal", name)
		}
	}
}

// TestKeyRekeyFailsOnABrokenRemote pins that a rekey over a remote missing one
// of its own tables is a failure, never a write that skipped the part it could
// not read: the heads and the epoch are both read from the remote the spec is
// written onto.
func TestKeyRekeyFailsOnABrokenRemote(t *testing.T) {
	t.Parallel()
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	execOn(t, path, "DROP TABLE meta")
	if _, err := b.KeyRekey(rekeySpec(t, 1, nil)); err == nil {
		t.Error("a rekey over a remote with no meta table came back as a success")
	}

	path = tempReplica(t)
	b = backendFor(t, &localDriver{path: path}, "origin-a")
	execOn(t, path, "DROP TABLE log")
	if _, err := b.KeyRekey(rekeySpec(t, 1, nil)); err == nil {
		t.Error("a rekey over a remote with no log table came back as a success")
	}
}

// TestKeyRekeyReportsAFailedTransfer pins that a rekey whose pull or push did
// not reach the remote is a failure the caller sees, the way a key_init is: a
// replica that holds one side of a rekey alone would hand the next machine a
// remote it cannot reconcile.
func TestKeyRekeyReportsAFailedTransfer(t *testing.T) {
	t.Parallel()
	driver := &localDriver{path: tempReplica(t)}
	b := backendFor(t, driver, "origin-a")
	driver.pullErr = errors.New("the remote is not answering")
	if _, err := b.KeyRekey(rekeySpec(t, 1, nil)); err == nil || !contains(err.Error(), "rekey the key") {
		t.Errorf("a rekey whose pull failed = %v, want a refusal naming the rekey", err)
	}

	driver = &localDriver{path: tempReplica(t)}
	b = backendFor(t, driver, "origin-a")
	driver.pushErr = errors.New("the remote is not answering")
	if _, err := b.KeyRekey(rekeySpec(t, 1, nil)); err == nil || !contains(err.Error(), "push the rekey") {
		t.Errorf("a rekey whose push failed = %v, want a refusal naming the push", err)
	}
}

// TestKeyRekeyCarriesTheRaceOverThePipe pins that the race crosses the pipe as
// its own class: a daemon reading the refusal knows the remote rekeyed under
// it, not that the remote failed to answer.
func TestKeyRekeyCarriesTheRaceOverThePipe(t *testing.T) {
	t.Parallel()
	b := backendFor(t, &localDriver{path: tempReplica(t)}, "origin-a")
	w := startWorker(t, b)
	w.askHello()

	init := w.ask(Request{ID: "1", Verb: VerbKeyInit, KeyMeta: &KeyMeta{
		Salt: []byte("the first salt"), KDF: "argon2id", Check: []byte("the first check"), Present: true,
	}})
	if !init.OK {
		t.Fatalf("key_init = %+v, want ok", init)
	}
	spec := rekeySpec(t, 1, nil)
	first := w.ask(Request{ID: "2", Verb: VerbKeyRekey, Epoch: 1, KeyMeta: &spec.Key, Cuts: spec.Cuts})
	if !first.OK {
		t.Fatalf("the first key_rekey = %+v, want ok", first)
	}
	loser := rekeySpec(t, 1, nil)
	second := w.ask(Request{ID: "3", Verb: VerbKeyRekey, Epoch: 1, KeyMeta: &loser.Key})
	if second.OK {
		t.Fatal("the second key_rekey from epoch 1 was answered, want a refusal")
	}
	if second.Code != CodeEpochRace {
		t.Errorf("the second key_rekey carried code %q, want %q", second.Code, CodeEpochRace)
	}
}

type sharedRemoteDriver struct {
	path         string
	db           *sql.DB
	remoteDb     *sql.DB
	pushNoop     bool
	skipNextPull bool
}

func (d *sharedRemoteDriver) Open(_ context.Context, _ Spec) (*sql.DB, error) {
	db, err := sql.Open("sqlite", d.path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	d.db = db
	return db, nil
}

func (d *sharedRemoteDriver) Push(ctx context.Context) error {
	if d.pushNoop {
		return nil
	}
	rows, err := d.db.QueryContext(ctx, "SELECT key, value FROM meta")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		if _, err := d.remoteDb.ExecContext(ctx, "INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", k, v); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (d *sharedRemoteDriver) Pull(ctx context.Context) error {
	if d.skipNextPull {
		d.skipNextPull = false
		return nil
	}
	rows, err := d.remoteDb.QueryContext(ctx, "SELECT key, value FROM meta")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		if _, err := d.db.ExecContext(ctx, "INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", k, v); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (d *sharedRemoteDriver) Stats(context.Context) (turso.TursoSyncDbStats, error) {
	return turso.TursoSyncDbStats{}, nil
}

// TestTwoSameFromRekeysEndWithExactlyOneWinnerAndLoserReportsRace pins that when two
// workers attempt same-From rekeys against a shared remote, exactly one winner's
// material ends up on the remote and the loser reports CodeEpochRace.
func TestTwoSameFromRekeysEndWithExactlyOneWinnerAndLoserReportsRace(t *testing.T) {
	t.Parallel()
	remotePath := tempReplica(t)
	remoteDb := openAt(t, remotePath)
	if _, err := remoteDb.Exec(
		"CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);\n" +
			"INSERT INTO meta (key, value) VALUES ('format', '3'), ('kdf_salt', 'dGVzdA=='), ('kdf', 'argon2id'), ('key_check', 'dGVzdA==');",
	); err != nil {
		t.Fatalf("init remote: %v", err)
	}

	path1 := tempReplica(t)
	path2 := tempReplica(t)

	d1 := &sharedRemoteDriver{path: path1, remoteDb: remoteDb}
	d2 := &sharedRemoteDriver{path: path2, remoteDb: remoteDb}

	b1 := backendFor(t, d1, "origin-a")
	b2 := backendFor(t, d2, "origin-b")

	if err := d1.Pull(context.Background()); err != nil {
		t.Fatalf("d1 pull: %v", err)
	}
	if err := d2.Pull(context.Background()); err != nil {
		t.Fatalf("d2 pull: %v", err)
	}

	spec1 := rekeySpec(t, 1, nil)
	spec1.Key.Salt = []byte("winner salt")
	spec1.Key.Check = []byte("winner check")

	spec2 := rekeySpec(t, 1, nil)
	spec2.Key.Salt = []byte("loser salt")
	spec2.Key.Check = []byte("loser check")

	held1, err := b1.KeyRekey(spec1)
	if err != nil {
		t.Fatalf("worker 1 KeyRekey: %v", err)
	}
	if !bytes.Equal(held1.Check, spec1.Key.Check) {
		t.Fatalf("worker 1 check = %v, want %v", held1.Check, spec1.Key.Check)
	}

	_, err = b2.KeyRekey(spec2)
	if err == nil {
		t.Fatal("worker 2 rekey succeeded, want epoch_race")
	}
	if code := RefusalCodeOf(err); code != CodeEpochRace {
		t.Errorf("worker 2 error code = %q, want %q; err: %v", code, CodeEpochRace, err)
	}

	var remoteCheck string
	if err := remoteDb.QueryRow("SELECT value FROM meta WHERE key = ?", metaKeyCheckKey).Scan(&remoteCheck); err != nil {
		t.Fatalf("read remote check: %v", err)
	}
	if remoteCheck != encodeKeyValue(spec1.Key.Check) {
		t.Errorf("remote check = %q, want winner's %q", remoteCheck, encodeKeyValue(spec1.Key.Check))
	}
}

// TestConcurrentSameFromRekeysLoserDetectsOverwrittenPush pins that when Worker 2
// passed its local CAS before Worker 1 pushed, but Worker 1 won the remote, Worker 2's
// post-push pull detects that the remote does not hold its key and reports CodeEpochRace.
func TestConcurrentSameFromRekeysLoserDetectsOverwrittenPush(t *testing.T) {
	t.Parallel()
	remotePath := tempReplica(t)
	remoteDb := openAt(t, remotePath)
	if _, err := remoteDb.Exec(
		"CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);\n" +
			"INSERT INTO meta (key, value) VALUES ('format', '3'), ('kdf_salt', 'dGVzdA=='), ('kdf', 'argon2id'), ('key_check', 'dGVzdA==');",
	); err != nil {
		t.Fatalf("init remote: %v", err)
	}

	path1 := tempReplica(t)
	path2 := tempReplica(t)

	d1 := &sharedRemoteDriver{path: path1, remoteDb: remoteDb}
	d2 := &sharedRemoteDriver{path: path2, remoteDb: remoteDb}

	b1 := backendFor(t, d1, "origin-a")
	b2 := backendFor(t, d2, "origin-b")

	if err := d1.Pull(context.Background()); err != nil {
		t.Fatalf("d1 pull: %v", err)
	}
	if err := d2.Pull(context.Background()); err != nil {
		t.Fatalf("d2 pull: %v", err)
	}

	spec1 := rekeySpec(t, 1, nil)
	spec1.Key.Salt = []byte("winner salt")
	spec1.Key.Check = []byte("winner check")

	spec2 := rekeySpec(t, 1, nil)
	spec2.Key.Salt = []byte("loser salt")
	spec2.Key.Check = []byte("loser check")

	if _, err := b1.KeyRekey(spec1); err != nil {
		t.Fatalf("worker 1 KeyRekey: %v", err)
	}

	d2.skipNextPull = true
	d2.pushNoop = true
	_, err := b2.KeyRekey(spec2)
	if err == nil {
		t.Fatal("worker 2 KeyRekey succeeded, want epoch_race")
	}
	if code := RefusalCodeOf(err); code != CodeEpochRace {
		t.Errorf("worker 2 code = %q, want %q; err: %v", code, CodeEpochRace, err)
	}
}
