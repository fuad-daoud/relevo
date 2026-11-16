package verb

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/blobstore"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// stagedRows is localState plus every row the secrets document can land on and
// the join marker, which a refused join has to leave as it found them: the five
// allow-listed machine-local secrets so a refusal after the pre-pass cannot keep
// the fleet's values, and the replaced row an arriving client key that lands
// over a held one writes.
func stagedRows(t *testing.T, l relevosync.Local) map[string]string {
	t.Helper()
	out := localState(t, l)
	for _, name := range []string{
		relevosync.SecretClientKey,
		relevosync.SecretR2Endpoint, relevosync.SecretR2Bucket, relevosync.SecretR2KeyID, relevosync.SecretR2Secret,
	} {
		value, present, err := l.SecretGet(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out[name] = "<absent>"
		if present {
			out[name] = string(value)
		}
	}
	value, present, err := l.ConfigGet(config.ClientKeyReplacedKey)
	if err != nil {
		t.Fatalf("read %s: %v", config.ClientKeyReplacedKey, err)
	}
	out[config.ClientKeyReplacedKey] = "<absent>"
	if present {
		out[config.ClientKeyReplacedKey] = string(value)
	}
	value, present, err = l.KVGet(relevosync.KeyJoin)
	if err != nil {
		t.Fatalf("read %s: %v", relevosync.KeyJoin, err)
	}
	out[relevosync.KeyJoin] = "<absent>"
	if present {
		out[relevosync.KeyJoin] = string(value)
	}
	return out
}

func sameRows(t *testing.T, before, after map[string]string) {
	t.Helper()
	for name, want := range before {
		if after[name] != want {
			t.Errorf("%s changed across a refused join:\n before %q\n after  %q", name, want, after[name])
		}
	}
}

func TestAJoinRefusedForNoBucketCredentialsLeavesAnEmptyMachineEmpty(t *testing.T) {
	t.Parallel()
	runner, local := keylessFleet(t)
	before := stagedRows(t, local)

	res := runner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	if res.OK || res.Code != wire.SyncCodeNoR2 {
		t.Fatalf("join = (ok %v, code %q), want refused as %q", res.OK, res.Code, wire.SyncCodeNoR2)
	}
	sameRows(t, before, stagedRows(t, local))
}

// quotaStats reports a fixed Turso total while moving nothing itself: the
// bootstrap figure the open worker already read.
type quotaStats struct {
	*synclog.MemTransport
	total int64
}

func (q *quotaStats) Stats() (synclog.Stats, error) {
	return synclog.Stats{TursoReceived: q.total}, nil
}

func TestAJoinRefusedForTheQuotaRestoresRowsThatAlreadyExisted(t *testing.T) {
	t.Parallel()
	runner, local, log := joinMachine(t, joinPassphrase)
	now := time.Unix(0, 0).UTC()
	if err := relevosync.PutSettings(local, relevosync.Settings{RemoteURL: "libsql://was-here.invalid", QuotaTursoSync: 1000}, now); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := relevosync.SetToken(local, []byte("A-TOKEN-THIS-MACHINE-ALREADY-HAD"), now); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	old := relevosync.R2Secrets{Endpoint: "https://old.r2.cloudflarestorage.com", Bucket: "old", KeyID: "oldk", Secret: "olds"}
	if err := relevosync.SetR2(local, old, now); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	store := blobstore.NewMemBlobStore(nil)
	if err := store.Put(context.Background(), blobstore.SegmentKey("m2", 0), bytes.NewReader(make([]byte, 10)), 10); err != nil {
		t.Fatalf("put segment: %v", err)
	}
	runner.Blobs = func(relevosync.R2Secrets) blobstore.BlobStore { return store }
	runner.Open = func(context.Context) (synclog.LogTransport, error) {
		return &quotaStats{MemTransport: log, total: 5000}, nil
	}
	before := stagedRows(t, local)

	res := runner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	if res.OK || res.Code != wire.SyncCodeOverQuota {
		t.Fatalf("join = (ok %v, code %q: %s), want refused as %q", res.OK, res.Code, res.Message, wire.SyncCodeOverQuota)
	}
	sameRows(t, before, stagedRows(t, local))
}

func TestAJoinRefusedAtThePreflightRestoresTheStagedRows(t *testing.T) {
	t.Parallel()
	runner, local := keylessFleet(t)
	before := stagedRows(t, local)
	verb, tail := joinVerbWithR2(t, "http://not-allowed.invalid")

	if res := runner.Run(context.Background(), verb, tail); res.OK {
		t.Fatal("a join with an invalid bucket endpoint was accepted")
	}
	sameRows(t, before, stagedRows(t, local))
}
