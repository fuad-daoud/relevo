package relevo

// The join through the openers a daemon installs, where the first worker is
// opened from rows that hold no bucket: an Open that refuses an incomplete set
// is exactly the answer OpenSupervisor gives, and OpenNoBucket stands in for
// the second opener the wiring carries. The log under them moves bodies, so a
// `$ref` fetch is refused for exactly as long as the joiner holds no bucket,
// which is what an OpenSupervisor refusal would have kept this whole enable
// from ever reaching.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// noBucketLog is what the daemon's second opener returns while the stored set
// is incomplete: a log whose blob verbs refuse from the moment it opened, for
// as long as it runs. That is the worker behaviour: its hello is built from the
// machine-local rows when it starts, and a hello that named no bucket is a
// blob-verb refusal for the worker's whole life, not until the rows change under
// it.
type noBucketLog struct{ *bodyLog }

func (l *noBucketLog) GetBlob(key, staging string, max int64) (int64, error) {
	return 0, fmt.Errorf("the worker this handle stands for was started with no bucket: %w",
		relevosync.ErrNoR2)
}

var _ = synclog.BlobTransport((*noBucketLog)(nil))

// daemonWiring is the joiner's verb runner over the machine the fixture built:
// the shape of the wiring the daemon installs, compressed to the two openers
// this file is about. Blobs stays nil, which skips the join's size guard: what
// is under test is what the import runs on, not what a bucket would have room
// for.
func daemonWiring(t *testing.T, m *cfgMachine) *VerbRunner {
	t.Helper()
	log := m.bodyLog
	return &VerbRunner{
		Shared: m.mdb,
		Local:  m.local,
		Path:   m.mdb.Path(),
		Runner: &relevosync.Runner{
			Client: log, Local: m.local, Timeout: time.Second,
			ReplicaPath: t.TempDir() + "/replica.db",
		},
		Open: func(context.Context) (synclog.LogTransport, error) {
			stored, err := relevosync.ReadR2(m.local)
			if err != nil {
				return nil, err
			}
			if !stored.Complete() {
				return nil, relevosync.ErrNoR2
			}
			return log, nil
		},
		OpenNoBucket: func(context.Context) (synclog.LogTransport, bool, error) {
			stored, err := relevosync.ReadR2(m.local)
			if err != nil {
				return nil, false, err
			}
			if stored.Complete() {
				return log, false, nil
			}
			return &noBucketLog{log}, true, nil
		},
	}
}

// prodFleet is the join and the fleet it joins: A unlocked, holding stamped
// bucket credentials, and sharing two secrets with no bucket of its own, with a
// row whose body is over the blob threshold written after its secrets were
// stamped, so the `$ref` entry sits ABOVE the entries that grant the fetch.
// When withBucket is false the fleet shares no credentials at all, which is the
// fleet that refuses a joiner even with the document applied. A holds the
// secrets stamped, the way a person's `config secret set` does, and B starts
// with a client key it generated, unstamped, the way an install's is. The
// runner handed back is the joiner's, wired with the daemon's openers.
func prodFleet(t *testing.T, withBucket bool) (a, b *cfgMachine, bRunner *VerbRunner) {
	t.Helper()
	key := relevosync.DeriveKey([]byte(joinPassphrase), []byte("0123456789abcdef"), relevosync.DefaultKDF)
	pair := bodyPair{log: synclog.NewMemTransport("A"), mover: synclog.NewMemBlobMover()}
	if _, err := pair.log.KeyInit(context.Background(), keyMetaFor(t, joinPassphrase)); err != nil {
		t.Fatalf("KeyInit: %v", err)
	}
	a = newBodyCfgMachine(t, pair, "A", key, true)
	b = newBodyCfgMachine(t, pair, "B", nil, false)
	// The enable preflight needs B's own compress pass done, as joinWith runs
	// it: what the join exports goes through the history the pass prepared.
	if _, _, err := db.CompressHistoryOnce(b.mdb, t.TempDir(), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CompressHistoryOnce on B: %v", err)
	}
	bRunner = daemonWiring(t, b)
	// The secrets are stamped before the large value is written, so its entry
	// lands above the secrets entries the export writes (zeroJoinPair's order).
	a.setSecret(t, config.SecretTypesafe, canarySecret)
	a.setSecret(t, config.SecretClientKey, newClientKeyPEM(t))
	if err := b.cfg.PutSecretUnstamped(config.SecretClientKey, []byte(newClientKeyPEM(t))); err != nil {
		t.Fatalf("PutSecretUnstamped on B: %v", err)
	}
	if withBucket {
		if err := relevosync.SetR2(a.local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
			t.Fatalf("SetR2 on A: %v", err)
		}
	}
	// The value is written last, so its entry lands above the secrets entries
	// the export wrote for the secrets the joiner is owed. An importer that
	// walks the log in sequence order reaches the fetch before it can name a
	// bucket, which is the ordering the pre-pass exists to break.
	seedLargeValue(t, a.mdb, 200<<10)
	a.exportThrough(t)
	requirePrefassFixture(t, pair)
	return a, b, bRunner
}

// A joiner with nothing but the code and the passphrase ends holding the
// fleet's credentials and the body of the row whose `$ref` sits above the
// entry that grants the fetch. The join has to get past Open's refusal with
// OpenNoBucket, apply the document through the first worker, and hand the
// import a worker that can fetch.
func TestAProductionJoinFetchesTheRefBodyAndHoldsTheFleetCredentials(t *testing.T) {
	t.Parallel()
	_, b, bRunner := prodFleet(t, true)
	res := bRunner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	if !res.OK {
		t.Fatalf("join refused: (%s) %s", res.Code, res.Message)
	}
	stored, err := relevosync.ReadR2(b.local)
	if err != nil {
		t.Fatalf("ReadR2 on B: %v", err)
	}
	if stored != verbFixtureR2 {
		t.Errorf("B's R2 credentials after the join = %+v, want the fleet's", stored)
	}
	if got := roundFileBody(t, b.mdb, "big.md"); len(got) != 200<<10 {
		t.Errorf("B's big.md is %d bytes after the join, want %d: the body was never fetched",
			len(got), 200<<10)
	}
}

// A fleet that shares no bucket credentials and a joiner that passed none both
// hold no complete set once the document has been applied. The enable refuses
// naming both fixes -- the machine's own flags or the inviter's document -- and
// every row the join had stored is back exactly as it was, including the ones it
// wrote over. The message is what pins the refusal to the fleet's answer rather
// than to this machine's opening rows: an opener refusal reads "no R2
// credentials on this machine", and this fleet's case is not that.
func TestAProductionJoinWithNoFleetBucketRefusesAndLeavesTheRowsAsTheyWere(t *testing.T) {
	t.Parallel()
	_, b, bRunner := prodFleet(t, false)
	before := stagedRows(t, b.local)

	res := bRunner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	if res.OK || res.Code != wire.SyncCodeNoR2 {
		t.Fatalf("join = (ok %v, code %q), want refused as %q", res.OK, res.Code, wire.SyncCodeNoR2)
	}
	for _, want := range []string{"--r2-endpoint", "db sync invite"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("the refusal does not name %q: %s", want, res.Message)
		}
	}
	sameRows(t, before, stagedRows(t, b.local))
}

// A wrong passphrase is refused before the pre-pass is ever run: the bytes the
// join had staged, the key it would have stored and the rows the document would
// have applied all read exactly as they did when the join began.
func TestAWrongPassphraseThroughTheDaemonsOpenersLeavesEveryRowAsItWas(t *testing.T) {
	t.Parallel()
	_, b, bRunner := prodFleet(t, true)
	before := stagedRows(t, b.local)

	res := bRunner.Run(context.Background(), joinVerb(t, "not the passphrase"),
		[]byte(joinToken+"not the passphrase"))
	if res.OK || res.Code != wire.SyncCodeWrongPassphrase {
		t.Fatalf("join = (ok %v, code %q), want refused as %q", res.OK, res.Code, wire.SyncCodeWrongPassphrase)
	}
	sameRows(t, before, stagedRows(t, b.local))
}

// answerWiring is the joiner's runner over the joiner the fixture built, with
// the opener's answer pinned: Open counts what it built, and OpenNoBucket
// hands the same healthy log back while answering bucketless whatever the
// stored rows say. What decides whether the verify worker keeps driving is the
// opener's word, not a re-read of the rows after the fact.
func answerWiring(t *testing.T, m *cfgMachine) (*VerbRunner, *openerCount) {
	t.Helper()
	count := &openerCount{}
	runner := &VerbRunner{
		Shared: m.mdb,
		Local:  m.local,
		Path:   m.mdb.Path(),
		Runner: &relevosync.Runner{
			Client: m.bodyLog, Local: m.local, Timeout: time.Second,
			ReplicaPath: t.TempDir() + "/replica.db",
		},
		Open: func(context.Context) (synclog.LogTransport, error) {
			count.opens++
			return m.bodyLog, nil
		},
		OpenNoBucket: func(context.Context) (synclog.LogTransport, bool, error) {
			return m.bodyLog, true, nil
		},
	}
	return runner, count
}

// openerCount is what the wiring's Open answered with.
type openerCount struct {
	opens int
}

// TestABucketlessAnswerReleasesTheVerifyWorkerEvenOverCompleteRows pins that a
// complete stored set does not change what the opener answered. The first
// worker is what the passphrase check drove, and bucketless is that worker's
// answer about how it started: the enable releases it on that word and lets
// the Enabler rebuild through Open. The rows merely match the word; the pin is
// that the word is who decides.
func TestABucketlessAnswerReleasesTheVerifyWorkerEvenOverCompleteRows(t *testing.T) {
	t.Parallel()
	_, b, _ := prodFleet(t, true)
	// The joiner's set is complete the moment the opener reads it, so the rows
	// alone would say the verify worker can keep driving. The opener's word
	// says the worker it built is bucketless, and that word decides.
	if err := relevosync.SetR2(b.local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2 on B: %v", err)
	}
	bRunner, count := answerWiring(t, b)

	res := bRunner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	if !res.OK {
		t.Fatalf("join refused: (%s) %s", res.Code, res.Message)
	}
	if count.opens != 1 {
		t.Errorf("Open was called %d times, want exactly 1: the verify worker was released on the opener's answer and the Enabler rebuilt", count.opens)
	}
}

// TestAJoinedMachineAnswersTheFleetWithNoSecretsEntries pins the joiner's half
// of the document's round trip. The fleet's values arrived sealed from A and
// landed as B's own held rows; nothing a joiner held was typed here, so B's
// first export appends no `secrets` entry at all and B carries no write stamp
// for a bucket credential. An echo under a fresh stamp would spread the joiner's
// envelope -- or, worse, a stale value -- back through the fleet as an edit.
func TestAJoinedMachineAnswersTheFleetWithNoSecretsEntries(t *testing.T) {
	t.Parallel()
	_, b, bRunner := prodFleet(t, true)
	res := bRunner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	if !res.OK {
		t.Fatalf("join refused: (%s) %s", res.Code, res.Message)
	}

	// B's first export. The join's reconcile carries rows only, so this exporter
	// is the thing that answers the fleet for the first time, and it walks the
	// document the join left signalled.
	if _, err := exportOnce(b.mdb, b.bodyLog); err != nil {
		t.Fatalf("B's first export: %v", err)
	}

	// The log the pair shares, read the way a third machine would: B's entries,
	// everything every origin other than A's appended.
	appended, err := b.log.OnLog("A").Pull(nil)
	if err != nil {
		t.Fatalf("read B's entries off the log: %v", err)
	}
	for _, e := range appended {
		name, ok := synclog.DocName(e.Table)
		if !ok || name != "secrets" {
			continue
		}
		t.Errorf("B's first export appended a %s entry under %q: the joiner answered the fleet", e.Table, e.PK)
	}
	for _, name := range []string{relevosync.SecretR2Endpoint, relevosync.SecretR2Bucket, relevosync.SecretR2KeyID, relevosync.SecretR2Secret} {
		if _, ok, err := b.local.ConfigWriteStamp(relevosync.SecretStampKey(name)); err != nil {
			t.Fatalf("ConfigWriteStamp(%s) on B: %v", name, err)
		} else if ok {
			t.Errorf("B holds a write stamp for %s; the join stamped what nobody typed", name)
		}
	}
}

// docSecretsKeys reads one origin's secrets-document item keys off the log,
// in entry order, with a handle that answers as a machine other than origin.
func docSecretsKeys(t *testing.T, log *synclog.MemTransport, origin string) []string {
	t.Helper()
	entries, err := log.OnLog(origin + "-reader").Pull(nil)
	if err != nil {
		t.Fatalf("read %s's doc entries: %v", origin, err)
	}
	var keys []string
	for _, e := range entries {
		if e.Origin != origin {
			continue
		}
		name, ok := synclog.DocName(e.Table)
		if !ok || name != "secrets" {
			continue
		}
		// A doc entry's primary key is the item key as a one-element array.
		var pk []string
		if err := json.Unmarshal([]byte(e.PK), &pk); err != nil || len(pk) != 1 {
			t.Fatalf("read a %s item key: %v", e.Table, err)
		}
		keys = append(keys, pk[0])
	}
	return keys
}

// TestAJoinRefusedForTheFleetsMissingBucketCanRetryAndThenHoldsTheFleetsSecrets
// pins that a refused join can be joined again. The first attempt ends with
// ErrNoR2AfterJoin after the pre-pass applied the fleet's document, the inviter
// then takes on and exports bucket credentials, and the retry with the same code
// and passphrase lands on the document as a first join would: the refusal owes
// the rows it had applied and the bookkeeping they were read by, both.
func TestAJoinRefusedForTheFleetsMissingBucketCanRetryAndThenHoldsTheFleetsSecrets(t *testing.T) {
	t.Parallel()
	a, b, bRunner := prodFleet(t, false)
	res := bRunner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	if res.OK || res.Code != wire.SyncCodeNoR2 {
		t.Fatalf("first join = (ok %v, code %q), want refused as %q", res.OK, res.Code, wire.SyncCodeNoR2)
	}

	// The fleet's document said what it said before the refusal. Its next export
	// gains only the four bucket credentials: a fleet whose unchanged values
	// re-seal into new envelopes would re-issue typesafe and the client key
	// under fresh stamps, and the retry would then land on the fleet's answer
	// to values it had a moment ago instead of its redeliveries.
	before := docSecretsKeys(t, b.log, "A")

	// The fleet takes on the credentials a person typed and shares them the way
	// it shares its other secrets.
	if err := relevosync.SetR2(a.local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2 on A: %v", err)
	}
	a.exportThrough(t)
	fresh := docSecretsKeys(t, b.log, "A")[len(before):]
	if want := []string{
		relevosync.SecretR2Bucket, relevosync.SecretR2Endpoint,
		relevosync.SecretR2KeyID, relevosync.SecretR2Secret,
	}; !slices.Equal(fresh, want) {
		t.Errorf("A's export issued %v, want exactly the four bucket credentials; the fleet re-issued what it already carried", fresh)
	}

	res = bRunner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	if !res.OK {
		t.Fatalf("retry refused: (%s) %s", res.Code, res.Message)
	}
	stored, err := relevosync.ReadR2(b.local)
	if err != nil {
		t.Fatalf("ReadR2 on B: %v", err)
	}
	if stored != verbFixtureR2 {
		t.Errorf("B's R2 credentials after the retry = %+v, want the fleet's", stored)
	}
	if got := b.secret(t, config.SecretTypesafe); got != canarySecret {
		t.Errorf("B's typesafe after the retry = %q, want the fleet's", got)
	}
	if got := b.secret(t, config.SecretClientKey); got != a.secret(t, config.SecretClientKey) {
		t.Errorf("B's client key after the retry is not the fleet's: %q", got)
	}
}
