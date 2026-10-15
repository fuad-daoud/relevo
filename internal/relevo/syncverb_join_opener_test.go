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
	"fmt"
	"strings"
	"testing"
	"time"

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
		OpenNoBucket: func(context.Context) (synclog.LogTransport, error) {
			stored, err := relevosync.ReadR2(m.local)
			if err != nil {
				return nil, err
			}
			if stored.Complete() {
				return log, nil
			}
			return &noBucketLog{log}, nil
		},
	}
}

// prodFleet is the join and the fleet it joins: A unlocked, holding stamped
// bucket credentials, with a row whose body is over the blob threshold written
// after its secrets were stamped, so the `$ref` entry sits ABOVE the entries
// that grant the fetch. When withBucket is false the fleet shares no
// credentials at all, which is the fleet that refuses a joiner. The runner
// handed back is the joiner's, wired with the daemon's openers.
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
	if withBucket {
		if err := relevosync.SetR2(a.local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
			t.Fatalf("SetR2 on A: %v", err)
		}
	}
	// The value is written last, so its entry lands above the secrets entries
	// the export wrote for the credentials the joiner is owed. An importer that
	// walks the log in sequence order reaches the fetch before it can name a
	// bucket, which is the ordering the pre-pass exists to break.
	seedLargeValue(t, a.mdb, 200<<10)
	a.exportThrough(t)
	if withBucket {
		requirePrefassFixture(t, pair)
	}
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
