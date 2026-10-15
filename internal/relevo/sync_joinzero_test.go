package relevo

// The zero-to-joined case, driven through the real steady pipeline on the
// in-memory log: one unlocked machine holding stamped secrets, config, hooks and
// an agent, and one machine that joins with nothing but a code and a passphrase.
//
// The log the joiner meets is arranged the way a real one is and not the way a
// convenient one would be. Machine A wrote its large value first, so the `$ref`
// row entry sits above the `doc:secrets` entry carrying the credentials that
// would let that body be fetched. An importer that walks the log in sequence
// order therefore reaches the fetch before the credentials exist and stalls the
// origin, which is the ordering the pre-pass exists to break.

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// bodyLog is one machine's view of the log and the object store: the shape the
// daemon builds, over the in-memory pair. It is what makes a `$ref` fetchable
// in this package's tests at all -- a plain MemTransport moves no bodies, so a
// value over the threshold could only ever travel inline and the ordering this
// file pins would never arise.
//
// It also refuses a fetch while the machine holds no bucket credentials, which
// is what the worker does: its bucket is named in the hello it was started with,
// and the hello is built out of the machine-local rows at the moment the worker
// opens. Without that refusal this fixture would fetch the body no matter what
// the joiner knew, and the pre-pass could be deleted without the case noticing.
type bodyLog struct {
	*synclog.MemTransport
	mover   *synclog.MemBlobMover
	staging string
	// local is the machine this handle belongs to; a fetch is refused while it
	// holds no complete credential set.
	local relevosync.Local
}

func (l *bodyLog) BlobStagingDir() string { return l.staging }

// PutBlob and GetBlob are the mover the exporter and importer take off the
// transport, so bodies are staged through a file and removed again exactly as
// they are in production rather than handed over as bytes.
func (l *bodyLog) PutBlob(key, staging string) (int64, bool, error) {
	return l.mover.PutBlob(key, staging)
}

func (l *bodyLog) GetBlob(key, staging string, max int64) (int64, error) {
	if l.local != nil {
		r2, err := relevosync.ReadR2(l.local)
		if err != nil {
			return 0, err
		}
		if !r2.Complete() {
			return 0, fmt.Errorf("the worker this handle stands for was started with no bucket: %w",
				relevosync.ErrNoR2)
		}
	}
	return l.mover.GetBlob(key, staging, max)
}

var _ synclog.BlobTransport = (*bodyLog)(nil)

// bodyPair is the log and the bucket two machines share.
type bodyPair struct {
	log   *synclog.MemTransport
	mover *synclog.MemBlobMover
}

// joinerLog returns one machine's handle on the pair, so a test can hold A's and
// B's over the same log and the same objects.
func (p bodyPair) joinerLog(t *testing.T, origin string, local relevosync.Local) *bodyLog {
	t.Helper()
	return &bodyLog{
		MemTransport: p.log.OnLog(origin), mover: p.mover,
		staging: t.TempDir(), local: local,
	}
}

// noiseBytes is n bytes that do not compress, so a size is a size on every path
// and the value really does exceed the threshold rather than only appearing to.
func noiseBytes(seed int64, n int) []byte {
	out := make([]byte, n)
	x := uint64(seed)*6364136223846793005 + 1442695040888963407
	for i := range out {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		out[i] = byte(x)
	}
	return out
}

// seedLargeValue gives machine A a round file whose body is over the blob
// threshold, so the entry the exporter writes for it carries a `$ref` rather
// than the bytes. It is the row a joiner cannot fetch until the credentials
// arrive, which is the whole reason the pre-pass exists.
//
// The body is written over a raw handle rather than through the daemon because
// this is a row the exporter finds, not one a person creates: the value has to
// be sitting in the file already for the export to walk it.
func seedLargeValue(t *testing.T, mdb *db.DB, n int) {
	t.Helper()
	// The record goes in through the database's own write path because the round
	// file's foreign key is the record's, and a raw insert of one without the
	// other is refused by the schema rather than by anything this case is about.
	id := ""
	if err := mdb.Tx(func(tx *db.Tx) error {
		var err error
		id, err = tx.RecordPut(db.Record{
			ID: "01A", Owner: "owner", Name: "binding", State: "done", Round: 1,
			CWD: "/tmp", JSON: `{"k":"v"}`,
		})
		return err
	}); err != nil {
		t.Fatalf("seed the record: %v", err)
	}
	raw, err := db.OpenRaw(mdb.Path())
	if err != nil {
		t.Fatalf("open for writing: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`INSERT INTO mastermind (id, harness_kind, session_id, first_seen, last_seen, origin)
		VALUES ('m1', 'agy', 's', 't', 't', ?)`, mdb.Origin()); err != nil {
		t.Fatalf("seed the owner: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO round_file (record_id, name, round, body, bytes, sha256, mtime, sealed_at, body_codec)
		VALUES (?, 'big.md', 1, ?, 1, 'seed', 't', 't', 1)`, id, noiseBytes(7, n)); err != nil {
		t.Fatalf("seed the round file: %v", err)
	}
}

// zeroJoinPair is A unlocked and holding everything the join is owed, and B not
// yet joined. A's large value is written before its secrets are stamped, so the
// `$ref` row entry is on the log above the `doc:secrets` entry.
func zeroJoinPair(t *testing.T) (a, b *cfgMachine, rel string, pair bodyPair) {
	t.Helper()
	// The hooks document only carries a script once it has been captured from a
	// config root, so the whole case runs under one: A writes the script there,
	// the export captures it, and the join writes B's copy back into it.
	root := filepath.Join(t.TempDir(), "relevo")
	t.Setenv("XDG_CONFIG_HOME", filepath.Dir(root))
	rel = writeHookScript(t, root, "PreToolUse", "check.sh", "#!/bin/sh\nexit 0\n")

	pair = bodyPair{log: synclog.NewMemTransport("A"), mover: synclog.NewMemBlobMover()}
	a = newBodyCfgMachine(t, pair, "A", cfgSyncKey, true)
	b = newBodyCfgMachine(t, pair, "B", cfgSyncKey, false)
	a.set(t, config.Candidates, cfgCandidate)
	a.set(t, config.Hooks, `{"events":{"PreToolUse":[["`+rel+`"]]}}`)
	a.set(t, config.Agents, `{"my-native":`+nativeAgentBody+`}`)
	a.setSecret(t, config.SecretTypesafe, canarySecret)
	if err := relevosync.SetR2(a.local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2 on A: %v", err)
	}
	// The value is written last, so its entry lands above the secrets entries
	// A exported before it. That is the ordering the pre-pass has to survive.
	seedLargeValue(t, a.mdb, 200<<10)
	a.exportThrough(t)
	return a, b, rel, pair
}

// newBodyCfgMachine is newCfgMachine over a body-carrying log, so a value over
// the threshold travels as a `$ref` rather than inline.
func newBodyCfgMachine(t *testing.T, pair bodyPair, origin string, key []byte, enabled bool) *cfgMachine {
	t.Helper()
	// The store is opened with the origin named rather than through store.New,
	// whose origin comes from an installation file this fixture does not have.
	// A log handle that disagrees with the machine's own origin is refused by
	// the appender, so the two have to be the same string.
	mdb, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: origin})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = mdb.Close() })
	local, err := relevosync.LocalHandle(mdb)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	if enabled {
		if err := relevosync.MarkEnabled(local, true, time.Unix(0, 0).UTC()); err != nil {
			t.Fatalf("MarkEnabled: %v", err)
		}
	}
	if err := relevosync.SetKey(local, key, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	log := pair.joinerLog(t, origin, local)
	return &cfgMachine{
		mdb: mdb, local: local, cfg: config.Open(mdb), log: log.MemTransport,
		bodyLog: log,
		runner: &relevosync.Runner{
			Client: log, Local: local, Timeout: time.Second,
			ReplicaPath: t.TempDir() + "/replica.db",
		},
	}
}

// exportThrough exports over the body-carrying transport, so a value over the
// threshold is uploaded and its entry written as a `$ref`.
func (m *cfgMachine) exportThrough(t *testing.T) {
	t.Helper()
	if _, err := exportOnce(m.mdb, m.bodyLog); err != nil {
		t.Fatalf("export: %v", err)
	}
}

// TestAJoinerEndsUpHoldingEverythingTheFleetShares is the round's convergence
// case. B joins with only a code and a passphrase and ends holding A's shared
// config, its rendered hooks, its agent, its typesafe key and its bucket
// credentials -- including the body of the row whose `$ref` sits above the
// document that grants the fetch.
//
// It fails without the pre-pass: B reaches the `$ref` at a sequence below the
// secrets entry, has no bucket to fetch it with, and the origin stalls.
func TestAJoinerEndsUpHoldingEverythingTheFleetShares(t *testing.T) {
	a, b, rel, pair := zeroJoinPair(t)
	requirePrefassFixture(t, pair)

	// The join carries nothing but the code: no bucket credentials, which is
	// what makes the pre-pass the only thing that can unlock the body above.
	b.joinWith(t, true, b.bodyLog)
	b.attempt(t)

	if got := b.body(t, config.Candidates); !strings.Contains(got, "sonnet") {
		t.Errorf("B's candidates after the join = %s, want A's", got)
	}
	if _, ok := hooksOf(t, b).Files[rel]; !ok {
		t.Errorf("B holds no copy of the hook script %s after the join: %+v", rel, hooksOf(t, b).Files)
	}
	if got := b.body(t, config.Agents); !strings.Contains(got, "my-native") {
		t.Errorf("B's agents after the join = %s, want A's", got)
	}
	if got := b.secret(t, config.SecretTypesafe); got != canarySecret {
		t.Errorf("B's typesafe after the join = %q, want A's", got)
	}
	stored, err := relevosync.ReadR2(b.local)
	if err != nil {
		t.Fatalf("ReadR2 on B: %v", err)
	}
	if stored != verbFixtureR2 {
		t.Errorf("B's R2 credentials after the join = %+v, want A's", stored)
	}
	if got := roundFileBody(t, b.mdb, "big.md"); len(got) != 200<<10 {
		t.Errorf("B's big.md is %d bytes after the join, want %d: the body was never fetched",
			len(got), 200<<10)
	}

	// A's own values must not come back as B's edits. A value a peer sent is
	// not this machine's edit, so it does not stamp, and a stamped one would be
	// re-emitted with a newer stamp and start a round of every machine
	// overwriting every other.
	b.attempt(t)
	a.attempt(t)
	assertBReEmittedNothing(t, a, b)
}

// requirePrefassFixture fails the test if the log does not have the ordering
// this file exists to pin. Without it a broken fixture would make the case pass
// for the wrong reason: no `$ref` above the secrets entry is an ordering the
// ordinary import already handles, so the join would succeed with the pre-pass
// deleted and the test would prove nothing.
func requirePrefassFixture(t *testing.T, pair bodyPair) {
	t.Helper()
	refSeq, secretsSeq := 0, 0
	entries, err := pair.log.OnLog("log-reader").Pull(nil)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	for _, e := range entries {
		if e.Origin != "A" {
			continue
		}
		if strings.Contains(string(e.Body), "$ref") && refSeq == 0 {
			refSeq = e.Seq
		}
		if e.Table == "doc:"+config.Secrets && secretsSeq == 0 {
			secretsSeq = e.Seq
		}
	}
	if refSeq == 0 {
		t.Fatal("no entry on the log carries a $ref, so nothing has to be fetched before the credentials arrive")
	}
	if secretsSeq == 0 {
		t.Fatal("no entry on the log names the secrets document, so there is nothing to apply first")
	}
	if refSeq >= secretsSeq {
		t.Fatalf("the $ref is at %d and the secrets document at %d: the ordinary import reaches the "+
			"credentials first, so this fixture does not exercise the pre-pass", refSeq, secretsSeq)
	}
}

// assertBReEmittedNothing checks that the joiner did not hand the fleet's own
// values back as its edits, which is the echo a machine that stamped what it
// applied would start. It is checked on the log rather than on the revision
// count: adopting a shared config legitimately leaves revisions, and what must
// not follow is a newer stamp on this machine re-exporting what it was given.
func assertBReEmittedNothing(t *testing.T, a, b *cfgMachine) {
	t.Helper()
	for _, e := range wholeLog(t, a.log) {
		if e.Origin == b.mdb.Origin() && e.Table == "doc:"+config.Secrets {
			t.Errorf("B re-exported a secret it applied: %s", e.Body)
		}
	}
	for _, name := range []string{config.SecretTypesafe, relevosync.SecretR2Bucket} {
		if _, ok := b.writeStamp(t, "secrets/"+name); ok {
			t.Errorf("B stamped %s, which it adopted rather than set", name)
		}
	}
}

// roundFileBody is the bytes a machine holds for one round file, which is the
// only place a fetched body can land.
func roundFileBody(t *testing.T, mdb *db.DB, name string) []byte {
	t.Helper()
	raw, err := db.OpenRawReadOnly(mdb.Path())
	if err != nil {
		t.Fatalf("open for reading: %v", err)
	}
	defer func() { _ = raw.Close() }()
	var body []byte
	if err := raw.QueryRow(`SELECT body FROM round_file WHERE name = ?`, name).Scan(&body); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		t.Fatalf("read %s: %v", name, err)
	}
	return body
}