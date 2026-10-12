package sync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
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
	boundary := synclog.Boundary{Seq: 1, Tag: synclog.BoundaryTag(syncTestKey, "m1", 1)}
	if _, err := log.OnLog("m1").AppendSigned([]synclog.Entry{signed}, boundary); err != nil {
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

func TestDisableKeepsSignedFromRecords(t *testing.T) {
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
	if recs, _ := shared.SignedFroms(); recs["m2"] != 4 {
		t.Errorf("records = %v after disable, want m2 at 4 kept", recs)
	}
}

func TestSigningKeyReadErrorFailsTheTick(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	seedJoin(t, shared, joinBinding("b1", "m1"))
	raw, err := db.OpenRaw(local.Path())
	if err != nil {
		t.Fatal(err)
	}
	// A view whose value cannot be computed makes the key read fail rather than
	// find nothing.
	for _, stmt := range []string{
		`DROP TABLE secret`,
		`CREATE VIEW secret AS SELECT 'sync.key' AS name, abs(-9223372036854775808) AS value`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	_ = raw.Close()
	if _, err := SigningKey(local); err == nil {
		t.Fatal("SigningKey answered a locked machine for a key it could not read")
	}
	log := synclog.NewMemTransport("m1")

	runner := &Runner{Client: log, Local: local, Timeout: time.Second}
	res := runner.SyncOnce(context.Background(), shared)
	if res.Err == nil {
		t.Fatal("the tick ran locked over an unreadable key")
	}
	if pulled, _ := log.OnLog("m2").Pull(nil); len(pulled) != 0 {
		t.Errorf("%d entries were exported", len(pulled))
	}
	if marks, _ := shared.ImportMarks(); len(marks) != 0 {
		t.Errorf("marks = %v, want none moved", marks)
	}
	if has, _ := shared.HasOutboxEntries(); !has {
		t.Error("the outbox was cleared")
	}
}

func TestSteadyReportsABadSignatureEveryTickWithoutLatching(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	if err := SetKey(local, syncTestKey, time.Unix(0, 0)); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	log := synclog.NewMemTransport("m1")
	_, known := shared.SchemaVersions()
	forged, err := synclog.NewUpsert("m2", "binding", `["old"]`, known, farBody(t, "old"), time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	forged.MAC = []byte("not a mac")
	other := []byte("fedcba9876543210fedcba9876543210")
	boundary := synclog.Boundary{Seq: 1, Tag: synclog.BoundaryTag(other, "m2", 1)}
	if _, err := log.OnLog("m2").AppendSigned([]synclog.Entry{forged}, boundary); err != nil {
		t.Fatal(err)
	}

	runner := &Runner{Client: log, Local: local, Timeout: time.Second}
	for tick := 1; tick <= 2; tick++ {
		res := runner.SyncOnce(context.Background(), shared)
		if res.Err != nil {
			t.Fatalf("tick %d: %v", tick, res.Err)
		}
		if len(res.Trouble.Held) != 1 || !strings.Contains(res.Trouble.Held[0], "boundary") {
			t.Errorf("tick %d: Trouble = %+v, want the held origin reported", tick, res.Trouble)
		}
	}
	if latched, _ := NewBreaker(local).Latched(); latched {
		t.Error("a bad signature latched the machine")
	}
}

func TestImportFailsWhenTheSigningKeyCannotBeRead(t *testing.T) {
	t.Parallel()
	shared, local := joinPair(t, "m1")
	raw, err := db.OpenRaw(local.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE secret`,
		`CREATE VIEW secret AS SELECT 'sync.key' AS name, abs(-9223372036854775808) AS value`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	_ = raw.Close()
	log := synclog.NewMemTransport("m1")

	runner := &Runner{Client: log, Local: local, Timeout: time.Second}
	if _, err := runner.drainImport(context.Background(), shared, NewStopTransport(log)); err == nil {
		t.Fatal("the import ran locked over an unreadable key")
	}
}
