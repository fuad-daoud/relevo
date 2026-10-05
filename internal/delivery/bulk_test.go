package delivery

import (
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// brokenClaimID is a valid mastermind id whose row will not decode: the
// unparseable case the bulk read must treat exactly as Live treats it.
const brokenClaimID = "pl_zzzzzzzzzzzz"

// syntheticBadRowKV hands out one unparseable row that the db itself refuses to
// store, and counts its removal. A real store can hold such a row -- an older
// or hand-edited one -- so the read path still has to handle it.
type syntheticBadRowKV struct {
	db.DBTxKV
	key     string
	deleted int
}

func (k *syntheticBadRowKV) KVKeys(prefix string) ([]string, error) {
	keys, err := k.DBTxKV.KVKeys(prefix)
	if err != nil {
		return nil, err
	}
	return append(keys, k.key), nil
}

func (k *syntheticBadRowKV) KVGet(key string) ([]byte, bool, error) {
	if key == k.key {
		return []byte("not json"), true, nil
	}
	return k.DBTxKV.KVGet(key)
}

func (k *syntheticBadRowKV) KVDelete(key string) error {
	if key == k.key {
		k.deleted++
	}
	return k.DBTxKV.KVDelete(key)
}

// TestClaimLiveAllMatchesLivePerRow is the bulk read's contract: LiveAll
// returns exactly what a Live call per mastermind would return. Each case in
// the fixture is one row's fate under Live -- live, stale, dead pid,
// unparseable, pane-keyed -- so the two paths cannot drift apart.
func TestClaimLiveAllMatchesLivePerRow(t *testing.T) {
	t.Parallel()

	f, d := testClaims(t)
	now := time.Now()

	live := Claim{MasterMind: otherClaimMasterMind, PID: 123, StartedAt: now, SeenAt: now}
	seedClaim(t, d, testClaimMasterMind, live)
	seedClaim(t, d, "pl_eeeeeeeeeeee", Claim{
		MasterMind: "pl_eeeeeeeeeeee", PID: 2,
		StartedAt: now.Add(-ClaimTTL - time.Second), SeenAt: now.Add(-ClaimTTL - time.Second),
	})
	// The dead-pid row: this pid is not the one alive reports as live. The id is
	// twelve characters after the prefix, so it is a mastermind id Live
	// examines rather than a pane-keyed row it skips.
	deadID := "pl_ffffffffffff"
	seedClaim(t, d, deadID, Claim{
		MasterMind: deadID, PID: 3, StartedAt: now, SeenAt: now,
	})
	seedClaim(t, d, paneClaimID, Claim{PID: 4242, StartedAt: now, SeenAt: now})

	// The unparseable row cannot be written through the db, which validates
	// JSON on the way in, so the store reads it from a handle that hands out one
	// synthetic bad row: a row no writer of this version could have left.
	broken := &syntheticBadRowKV{DBTxKV: db.TxKV{DB: d}, key: claimKey(brokenClaimID)}
	f.KV = broken

	// The store reports pid 3 dead and every other pid live, so the dead-pid
	// rule is exercised without depending on which pids exist here.
	f.Alive = func(pid int) bool { return pid != 3 }

	got, err := f.LiveAll(now)
	if err != nil {
		t.Fatalf("LiveAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("LiveAll = %+v, want only the one live claim", got)
	}
	if c := got[testClaimMasterMind]; c == nil || c.PID != 123 {
		t.Errorf("LiveAll[%s] = %+v, want the live claim", testClaimMasterMind, c)
	}
	for _, id := range []string{"pl_eeeeeeeeeeee", deadID, paneClaimID, brokenClaimID} {
		if c := got[id]; c != nil {
			t.Errorf("LiveAll[%s] = %+v, want absent", id, c)
		}
	}

	// Stale, dead-pid and unparseable rows are removed, exactly as Live removes
	// them; a pane-keyed row is skipped and left alone.
	for _, id := range []string{"pl_eeeeeeeeeeee", brokenClaimID, deadID} {
		if _, ok := claimRow(t, d, id); ok {
			t.Errorf("%s: the row must be removed by the bulk read", id)
		}
	}
	if broken.deleted == 0 {
		t.Error("an unparseable claim row must be removed by the bulk read")
	}
	if _, ok := claimRow(t, d, paneClaimID); !ok {
		t.Error("a pane-keyed row must be skipped, never rewritten")
	}

	// Every surviving claim is exactly what a per-row Live returns.
	for id, c := range got {
		single, err := f.Live(id, now)
		if err != nil {
			t.Fatalf("Live(%s): %v", id, err)
		}
		if single == nil || single.PID != c.PID {
			t.Errorf("Live(%s) = %+v, LiveAll said %+v", id, single, c)
		}
	}
}

// TestClaimLiveAllEmpty covers the store with no claim rows at all: an empty
// map, no error. A report must read every row not live from it.
func TestClaimLiveAllEmpty(t *testing.T) {
	t.Parallel()

	f, _ := testClaims(t)
	got, err := f.LiveAll(time.Now())
	if err != nil {
		t.Fatalf("LiveAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("LiveAll on an empty store = %+v, want no claims", got)
	}
}

// failingKeysKV fails the prefix scan, so the bulk read's error path is pinned:
// the error reaches the caller, which reads it as not-live rather than failing
// the report.
type failingKeysKV struct {
	db.DBTxKV
	err error
}

func (k *failingKeysKV) KVKeys(string) ([]string, error) { return nil, k.err }

func TestClaimLiveAllReportsAScanError(t *testing.T) {
	t.Parallel()

	d := testSecretDB(t)
	kv := &failingKeysKV{DBTxKV: db.TxKV{DB: d}, err: errors.New("scan failed")}
	f := &KVClaims{KV: kv, Alive: alwaysAlive}

	if _, err := f.LiveAll(time.Now()); err == nil {
		t.Fatal("LiveAll with a failing scan returned no error")
	}
}

// TestWaitLiveAllMatchesLivePerRow is LiveAll's wait-side twin: one registration
// per live binding name, with a stale registration removed and an empty name
// row skipped, exactly as Live decides them one at a time.
func TestWaitLiveAllMatchesLivePerRow(t *testing.T) {
	t.Parallel()

	d := testSecretDB(t)
	w := &KVWaitClaims{KV: db.TxKV{DB: d}, Alive: memWaitAlives}
	now := time.Now()

	live := WaitClaim{Name: "webshop", PID: 321, StartedAt: now, SeenAt: now}
	if err := w.Write(live, now); err != nil {
		t.Fatalf("Write(webshop): %v", err)
	}
	stale := WaitClaim{
		Name: "shop", PID: 322,
		StartedAt: now.Add(-WaitTTL - time.Second), SeenAt: now.Add(-WaitTTL - time.Second),
	}
	if err := w.Write(stale, now); err != nil {
		t.Fatalf("Write(shop): %v", err)
	}
	// A row keyed on the bare prefix, which is not a binding name.
	if err := d.KVPut(waitKey(""), []byte(`{"name":"","pid":1}`)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}

	got, err := w.LiveAll(now)
	if err != nil {
		t.Fatalf("LiveAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("LiveAll = %+v, want only the live registration", got)
	}
	if c := got["webshop"]; c == nil || c.PID != 321 {
		t.Errorf("LiveAll[webshop] = %+v, want the live registration", c)
	}
	if c := got["shop"]; c != nil {
		t.Errorf("LiveAll[shop] = %+v, want a stale registration absent", c)
	}
	single, err := w.Live("webshop", now)
	if err != nil {
		t.Fatalf("Live(webshop): %v", err)
	}
	if single == nil || single.PID != 321 {
		t.Errorf("Live(webshop) = %+v, LiveAll said %+v", single, got["webshop"])
	}
}

func TestWaitLiveAllEmpty(t *testing.T) {
	t.Parallel()

	w := testWaits(t)
	got, err := w.LiveAll(time.Now())
	if err != nil {
		t.Fatalf("LiveAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("LiveAll on an empty store = %+v, want no registrations", got)
	}
}

func TestWaitLiveAllReportsAScanError(t *testing.T) {
	t.Parallel()

	d := testSecretDB(t)
	w := &KVWaitClaims{KV: &failingKeysKV{DBTxKV: db.TxKV{DB: d}, err: errors.New("scan failed")}, Alive: alwaysAlive}

	if _, err := w.LiveAll(time.Now()); err == nil {
		t.Fatal("LiveAll with a failing scan returned no error")
	}
}

// TestClaimLiveAllReadsEachRowOnce pins the point of the bulk read: one scan of
// the claim namespace plus one read per row it holds, with no second pass.
func TestClaimLiveAllReadsEachRowOnce(t *testing.T) {
	t.Parallel()

	d := testSecretDB(t)
	kv := &countingKeysKV{DBTxKV: db.TxKV{DB: d}}
	f := &KVClaims{KV: kv, Alive: alwaysAlive}
	now := time.Now()

	ids := []string{testClaimMasterMind, otherClaimMasterMind, "pl_dddddddddddd"}
	for _, id := range ids {
		seedClaim(t, d, id, Claim{MasterMind: id, PID: 7, StartedAt: now, SeenAt: now})
	}
	kv.gets, kv.scans = 0, 0

	got, err := f.LiveAll(now)
	if err != nil {
		t.Fatalf("LiveAll: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("LiveAll = %d claims, want %d", len(got), len(ids))
	}
	if kv.scans != 1 {
		t.Errorf("namespace scans = %d, want exactly 1", kv.scans)
	}
	if kv.gets != len(ids) {
		t.Errorf("row reads = %d, want one per claim row (%d)", kv.gets, len(ids))
	}
}

// countingKeysKV counts the prefix scans and the row reads a bulk read issues.
type countingKeysKV struct {
	db.DBTxKV
	scans int
	gets  int
}

func (k *countingKeysKV) KVKeys(prefix string) ([]string, error) {
	k.scans++
	return k.DBTxKV.KVKeys(prefix)
}

func (k *countingKeysKV) KVGet(key string) ([]byte, bool, error) {
	k.gets++
	return k.DBTxKV.KVGet(key)
}
