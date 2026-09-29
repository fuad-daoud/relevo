package delivery

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// testClaimMasterMind is a valid mastermind id (pl_ plus 12 characters of [a-z2-7]),
// the shape Live, Write and Remove all key on.
const testClaimMasterMind = "pl_aaaaaaaabbbb"

// otherClaimMasterMind is a second valid id, for the "a different mastermind's
// claim is not this one's" cases.
const otherClaimMasterMind = "pl_ccccccccdddd"

// paneClaimID is a pane-keyed claim's key: the pane id with ":" as "_", which is
// what the old pane-keyed claim file name held.
const paneClaimID = "wG_pQ"

// claimRow reads one claim row back, reporting whether it is there.
func claimRow(t *testing.T, d *db.DB, mastermindID string) (Claim, bool) {
	t.Helper()
	raw, ok, err := d.KVGet(claimKey(mastermindID))
	if err != nil {
		t.Fatalf("KVGet(%s): %v", claimKey(mastermindID), err)
	}
	if !ok {
		return Claim{}, false
	}
	var c Claim
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("decode %s: %v", claimKey(mastermindID), err)
	}
	return c, true
}

// seedClaim writes one claim row the way an older relevo would have, so a test
// can pin what Live and Remove do with it.
func seedClaim(t *testing.T, d *db.DB, mastermindID string, c Claim) {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal claim: %v", err)
	}
	if err := d.KVPut(claimKey(mastermindID), raw); err != nil {
		t.Fatalf("KVPut(%s): %v", claimKey(mastermindID), err)
	}
}

func alwaysAlive(int) bool { return true }
func neverAlive(int) bool  { return false }

func TestClaimLiveAbsent(t *testing.T) {
	t.Parallel()

	f, _ := testClaims(t)
	c, err := f.Live(testClaimMasterMind, time.Now())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if c != nil {
		t.Fatalf("Live on an absent claim = %+v, want nil", c)
	}
}

// TestClaimKeyedByMasterMindID is the required case: a claim is
// written to the claim/<mastermind-id> row, is found by that id, and is not
// found by another mastermind's id.
func TestClaimKeyedByMasterMindID(t *testing.T) {
	t.Parallel()

	f, d := testClaims(t)
	now := time.Now()

	claim := Claim{MasterMind: testClaimMasterMind, PID: 123, HostPID: 99, HostStartedAt: 42, StartedAt: now, SeenAt: now}
	if err := f.Write(claim, now); err != nil {
		t.Fatalf("Write: %v", err)
	}

	stored, ok := claimRow(t, d, testClaimMasterMind)
	if !ok {
		t.Fatalf("the claim must live in the %s row", claimKey(testClaimMasterMind))
	}
	if stored.PID != 123 || stored.HostPID != 99 || stored.HostStartedAt != 42 {
		t.Errorf("stored claim = %+v", stored)
	}

	got, err := f.Live(testClaimMasterMind, now)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got == nil || got.PID != 123 {
		t.Fatalf("Live = %+v, want the written claim", got)
	}

	other, err := f.Live(otherClaimMasterMind, now)
	if err != nil {
		t.Fatalf("Live for another mastermind: %v", err)
	}
	if other != nil {
		t.Fatalf("another mastermind's id found a claim: %+v", other)
	}
}

// TestClaimLiveIgnoresPaneKeyedRow: a pane-keyed claim is not a claim
// this version wrote. Live must ignore it (it would otherwise have to answer a
// question about a pane it no longer has) and leave the row where it is:
// nothing in this version rewrites or removes a pane-keyed row.
func TestClaimLiveIgnoresPaneKeyedRow(t *testing.T) {
	t.Parallel()

	f, d := testClaims(t)
	now := time.Now()

	// A live, current pane-keyed claim: exactly what an older relevo mcp
	// still running during the upgrade has on record.
	seedClaim(t, d, paneClaimID, Claim{PID: 4242, StartedAt: now, SeenAt: now})

	// Live never asks about a pane name, and asking about the pane name must
	// not remove the row either.
	got, err := f.Live("wG:pQ", now)
	if err != nil {
		t.Fatalf("Live(pane): %v", err)
	}
	if got != nil {
		t.Fatalf("Live(pane name) = %+v, want nil", got)
	}
	if _, ok := claimRow(t, d, paneClaimID); !ok {
		t.Fatal("Live must leave a pane-keyed claim alone")
	}
}

func TestPaneKeyedClaimDead(t *testing.T) {
	t.Parallel()

	cases := []struct {
		label string
		raw   string
		want  bool
	}{
		{"dead pid", `{"pane":"wG:pQ","pid":111}`, true},
		{"live pid", `{"pane":"wG:pQ","pid":4242}`, false},
		{"no pid", `{"pane":"wG:pQ"}`, false},
		{"zero pid", `{"pane":"wG:pQ","pid":0}`, false},
		{"unparseable", `not json`, false},
	}
	for _, c := range cases {
		if got := paneKeyedClaimDead([]byte(c.raw), func(pid int) bool { return pid == 4242 }); got != c.want {
			t.Errorf("%s: paneKeyedClaimDead = %v, want %v", c.label, got, c.want)
		}
	}
}

func TestClaimLiveRemovesStaleTTL(t *testing.T) {
	t.Parallel()

	f, d := testClaims(t)
	start := time.Now()
	seedClaim(t, d, testClaimMasterMind, Claim{MasterMind: testClaimMasterMind, PID: 123, StartedAt: start, SeenAt: start})

	later := start.Add(ClaimTTL + time.Second)
	got, err := f.Live(testClaimMasterMind, later)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got != nil {
		t.Fatalf("Live past TTL = %+v, want nil", got)
	}
	if _, ok := claimRow(t, d, testClaimMasterMind); ok {
		t.Error("a stale claim row must be removed")
	}
}

// TestClaimLiveRemovesDeadPID is the plan's required case: a claim with a
// dead pid must read as not-live and the row must be gone afterward.
func TestClaimLiveRemovesDeadPID(t *testing.T) {
	t.Parallel()

	f, d := testClaims(t)
	f.Alive = neverAlive
	now := time.Now()
	seedClaim(t, d, testClaimMasterMind, Claim{MasterMind: testClaimMasterMind, PID: 999, StartedAt: now, SeenAt: now})

	got, err := f.Live(testClaimMasterMind, now)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got != nil {
		t.Fatalf("Live on a dead pid = %+v, want nil", got)
	}
	if _, ok := claimRow(t, d, testClaimMasterMind); ok {
		t.Error("a dead-pid claim row must be removed")
	}
}

// invalidJSONKV hands out unparseable bytes for one key -- a row no writer of
// this version could have written -- and counts its removal.
type invalidJSONKV struct {
	db.DBTxKV
	key     string
	deleted int
}

func (k *invalidJSONKV) KVGet(key string) ([]byte, bool, error) {
	if key == k.key {
		return []byte("not json"), true, nil
	}
	return k.DBTxKV.KVGet(key)
}

func (k *invalidJSONKV) KVDelete(key string) error {
	if key == k.key {
		k.deleted++
	}
	return k.DBTxKV.KVDelete(key)
}

func TestClaimLiveRemovesUnparseable(t *testing.T) {
	t.Parallel()

	d := testSecretDB(t)
	kv := &invalidJSONKV{DBTxKV: db.TxKV{DB: d}, key: claimKey(testClaimMasterMind)}
	f := &KVClaims{KV: kv, Alive: alwaysAlive}

	got, err := f.Live(testClaimMasterMind, time.Now())
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got != nil {
		t.Fatalf("Live on unparseable json = %+v, want nil", got)
	}
	if kv.deleted == 0 {
		t.Error("an unparseable claim row must be removed")
	}
}

func TestClaimLiveEmptyMasterMind(t *testing.T) {
	t.Parallel()

	f, _ := testClaims(t)
	if _, err := f.Live("", time.Now()); err == nil {
		t.Fatal("Live with an empty mastermind must error")
	}
}

func TestClaimWriteRefusesSecondLiveWriter(t *testing.T) {
	t.Parallel()

	f, _ := testClaims(t)
	now := time.Now()
	first := Claim{MasterMind: testClaimMasterMind, PID: 111, StartedAt: now, SeenAt: now}
	if err := f.Write(first, now); err != nil {
		t.Fatalf("Write first: %v", err)
	}

	second := Claim{MasterMind: testClaimMasterMind, PID: 222, StartedAt: now, SeenAt: now}
	if err := f.Write(second, now); err == nil {
		t.Fatal("Write from a second pid over a live claim must be refused")
	} else if !errors.Is(err, ErrClaimHeld) {
		t.Errorf("Write error = %v, want ErrClaimHeld", err)
	}
}

// TestClaimWriteRefusesANonMasterMindID: the old pane-keyed shape must never be
// written again, so a claim whose key is not a mastermind id is refused rather
// than silently creating a pane-keyed row.
func TestClaimWriteRefusesANonMasterMindID(t *testing.T) {
	t.Parallel()

	f, d := testClaims(t)
	now := time.Now()
	err := f.Write(Claim{MasterMind: "wG:pQ", PID: 111, StartedAt: now, SeenAt: now}, now)
	if err == nil {
		t.Fatal("Write with a non-mastermind key must be refused")
	}
	if _, ok := claimRow(t, d, "wG:pQ"); ok {
		t.Error("a refused Write must leave no row behind")
	}
}

func TestClaimWriteSameWriterRefreshes(t *testing.T) {
	t.Parallel()

	f, _ := testClaims(t)
	now := time.Now()
	claim := Claim{MasterMind: testClaimMasterMind, PID: 111, StartedAt: now, SeenAt: now}
	if err := f.Write(claim, now); err != nil {
		t.Fatalf("Write first: %v", err)
	}

	later := now.Add(time.Second)
	claim.SeenAt = later
	if err := f.Write(claim, later); err != nil {
		t.Fatalf("Write refresh from the same pid must succeed: %v", err)
	}

	got, err := f.Live(testClaimMasterMind, later)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got == nil || !got.SeenAt.Equal(later) {
		t.Fatalf("Live after refresh = %+v, want SeenAt %v", got, later)
	}
}

func TestClaimWriteOverwritesStaleClaim(t *testing.T) {
	t.Parallel()

	f, _ := testClaims(t)
	start := time.Now()
	first := Claim{MasterMind: testClaimMasterMind, PID: 111, StartedAt: start, SeenAt: start}
	if err := f.Write(first, start); err != nil {
		t.Fatalf("Write first: %v", err)
	}

	later := start.Add(ClaimTTL + time.Second)
	second := Claim{MasterMind: testClaimMasterMind, PID: 222, StartedAt: later, SeenAt: later}
	if err := f.Write(second, later); err != nil {
		t.Fatalf("Write over a stale claim must succeed: %v", err)
	}

	got, err := f.Live(testClaimMasterMind, later)
	if err != nil {
		t.Fatalf("Live: %v", err)
	}
	if got == nil || got.PID != 222 {
		t.Fatalf("Live after overwrite = %+v, want pid 222", got)
	}
}

func TestClaimRemoveOnlyMatchingPID(t *testing.T) {
	t.Parallel()

	f, _ := testClaims(t)
	now := time.Now()
	claim := Claim{MasterMind: testClaimMasterMind, PID: 111, StartedAt: now, SeenAt: now}
	if err := f.Write(claim, now); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := f.Remove(testClaimMasterMind, 222); err != nil {
		t.Fatalf("Remove with a mismatched pid must not error: %v", err)
	}
	if got, err := f.Live(testClaimMasterMind, now); err != nil || got == nil {
		t.Fatalf("a mismatched-pid Remove must not remove the claim; Live = %+v, err = %v", got, err)
	}

	if err := f.Remove(testClaimMasterMind, 111); err != nil {
		t.Fatalf("Remove with a matching pid: %v", err)
	}
	if got, err := f.Live(testClaimMasterMind, now); err != nil || got != nil {
		t.Fatalf("a matching-pid Remove must remove the claim; Live = %+v, err = %v", got, err)
	}
}

func TestClaimRemoveAbsentIsNotAnError(t *testing.T) {
	t.Parallel()

	f, _ := testClaims(t)
	if err := f.Remove(testClaimMasterMind, 111); err != nil {
		t.Fatalf("Remove on an absent claim must not error: %v", err)
	}
}
