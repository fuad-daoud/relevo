package sync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

var syncTestKey = []byte("0123456789abcdef0123456789abcdef")

func TestSteadySignsWithTheStoredKey(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	seedJoin(t, shared, joinBinding("b1", "m1"))
	if err := SetKey(local, syncTestKey, time.Unix(0, 0)); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	log := synclog.NewMemTransport("m1")

	runner := &Runner{Client: log, Local: local, Timeout: time.Second}
	if res := runner.SyncOnce(context.Background(), shared); res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
	pulled, err := log.OnLog("m2").Pull(nil)
	if err != nil || len(pulled) == 0 {
		t.Fatalf("pull = %d entries, %v", len(pulled), err)
	}
	for _, e := range pulled {
		if !synclog.VerifyMAC(syncTestKey, e) {
			t.Errorf("%s %s was exported unsigned", e.Table, e.PK)
		}
	}
}

func TestSteadyImportsAndReportsTroubleWhenLockedOnASignedOrigin(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	log := synclog.NewMemTransport("m1")
	_, known := shared.SchemaVersions()
	signed, err := synclog.NewUpsert("m1", "binding", `["old"]`, known, farBody(t, "old"), time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	signed.MAC = synclog.EntryMAC(syncTestKey, signed)
	if _, err := log.OnLog("m1").Append([]synclog.Entry{signed}); err != nil {
		t.Fatal(err)
	}
	seedFar(t, log, "m2", "b2", known)
	seedJoin(t, shared, joinBinding("b1", "m1"))

	runner := &Runner{Client: log, Local: local, Timeout: time.Second}
	res := runner.SyncOnce(context.Background(), shared)
	if res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
	if res.Applied != 1 {
		t.Errorf("Applied = %d, want the import to go on", res.Applied)
	}
	if len(res.Trouble.Held) != 1 || !strings.Contains(res.Trouble.Held[0], "run relevo db sync unlock") {
		t.Errorf("Trouble = %+v, want the locked-origin sentence", res.Trouble)
	}
	if has, _ := shared.HasOutboxEntries(); !has {
		t.Error("the outbox was truncated")
	}
}

func TestDisableClearsTheSignedFromRecords(t *testing.T) {
	t.Parallel()
	d, _ := disableFixture(t, nil)
	_, shared, _ := openSplit(t)
	if err := shared.LowerSignedFrom("m2", 4); err != nil {
		t.Fatal(err)
	}
	d.Shared = shared
	if _, err := d.Disable(context.Background()); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if recs, _ := shared.SignedFroms(); len(recs) != 0 {
		t.Errorf("records = %v after disable", recs)
	}
}
