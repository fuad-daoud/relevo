package db

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// refusalFor returns the first refusal of a preflight whose check is named,
// and fails when there is none: a test that cannot find the refusal it is about
// must not pass by looking at the wrong check's message.
func refusalFor(t *testing.T, p Preflight, check string) PreflightRefusal {
	t.Helper()
	for _, r := range p.Refusals {
		if r.Check == check {
			return r
		}
	}
	t.Fatalf("no %q refusal in %+v", check, p.Refusals)
	return PreflightRefusal{}
}

// assertWants fails unless the refusal's message names every fragment. A
// refusal is read by someone deciding what to run, so the count, the table and
// the command are all part of what it says.
func assertWants(t *testing.T, r PreflightRefusal, fragments ...string) {
	t.Helper()
	for _, want := range fragments {
		if !strings.Contains(r.Detail, want) {
			t.Errorf("%s refusal %q does not name %q", r.Check, r.Detail, want)
		}
	}
}

// TestEnableRefusesEmptyOrigin pins the origin gate: a database holding one
// unstamped row in each origin-carrying table refuses enable, and the refusal
// names every table with its count and the backfill that fixes it. Stamping the
// rows passes the gate.
func TestEnableRefusesEmptyOrigin(t *testing.T) {
	d := directOpenTestDB(t)
	seedOneEmptyOriginRowPerTable(t, d)

	counts, err := CountEmptyOrigins(d)
	if err != nil {
		t.Fatalf("CountEmptyOrigins: %v", err)
	}
	if got := len(counts.Tables); got != len(originGateTables) {
		t.Fatalf("counted %d tables, want %d", got, len(originGateTables))
	}

	preflight := EnablePreflight(d)
	refusal := refusalFor(t, preflight, checkEmptyOrigin)
	assertWants(t, refusal, "binding_record=1", "binding=1", "repo=1", "mastermind=1", "chains=1", "BackfillOriginOnce")
	if preflight.EmptyOrigin.Empty() {
		t.Error("EmptyOrigin reports empty while every table holds an unstamped row")
	}
	if err := preflight.Err(); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%d checks refused", len(preflight.Refusals))) {
		t.Errorf("Err() = %v, want the joined refusals and their count", err)
	}

	// The gate only counts, so the rows stay until something stamps them; the
	// pass the refusal names is that something, and its own counts reach zero.
	stampEveryOrigin(t, d, "inst-a")
	stamped, err := CountEmptyOrigins(d)
	if err != nil {
		t.Fatalf("CountEmptyOrigins after stamping: %v", err)
	}
	if !stamped.Empty() {
		t.Errorf("counts after stamping = %s, want none", stamped)
	}
	if stamped.String() != "no rows with an empty origin" {
		t.Errorf("String() on an empty count = %q", stamped)
	}
}

// seedOneEmptyOriginRowPerTable writes one row into each origin-carrying table
// through an unscoped handle, so every one of them is stamped with the handle's
// empty origin. That is what a database predating the backfill looks like.
func seedOneEmptyOriginRowPerTable(t *testing.T, d *DB) {
	t.Helper()
	day := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)

	if _, err := d.RecordPut(testRecord("webshop")); err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	upsertBinding(t, d, newTestBinding("webshop", day))
	upsertRepo(t, d, Repo{OriginURL: ptr("https://example.test/a.git"), FirstSeen: day})
	if _, err := d.UpsertMasterMind(MasterMind{HarnessKind: "claude", SessionID: "sess-1", FirstSeen: day, LastSeen: day}); err != nil {
		t.Fatalf("UpsertMasterMind: %v", err)
	}
	if _, err := d.sqlDB.Exec(`INSERT INTO chains (id, origin, owner, name, status, phase, step, plan, plans,
		plan_paths, builder, created_at, updated_at) VALUES (?, '', '', 'ship', 'open', 'plan', 'step', 1, 1, '[]',
		'claude', ?, ?)`, NewID(), formatTime(day), formatTime(day)); err != nil {
		t.Fatalf("insert chains: %v", err)
	}
}

// stampEveryOrigin clears the origin the gate counts, by running the pass the
// gate's own refusal names. There is nothing to reach for besides that pass: a
// table outside it would leave a count standing that the fix text cannot clear.
func stampEveryOrigin(t *testing.T, d *DB, origin string) {
	t.Helper()
	if _, _, err := BackfillOriginOnce(d, origin, time.Now()); err != nil {
		t.Fatalf("BackfillOriginOnce: %v", err)
	}
}

// TestEnableRefusesSharedSecrets pins that the shared file is what the check
// reads: a secret row still sitting there refuses enable and is named, and the
// split that moves it is the fix. Once the split has run, the same check passes
// -- and it passes because the shared file is empty, not because the check
// looked at the local file the move filled.
func TestEnableRefusesSharedSecrets(t *testing.T) {
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := OpenSplit(path, Options{})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if err := d.Tx(func(t *Tx) error { return t.SecretPut("client.key", []byte("k"), now) }); err != nil {
		t.Fatalf("SecretPut: %v", err)
	}
	if err := d.Tx(func(t *Tx) error { return t.SecretPut("typesafe", []byte("s"), now) }); err != nil {
		t.Fatalf("SecretPut: %v", err)
	}

	refusal := refusalFor(t, EnablePreflight(d), checkSharedSecrets)
	assertWants(t, refusal, "2 secret row(s)", "client.key", "typesafe", "SplitOnce")

	if _, _, err := SplitOnce(d, t.TempDir(), now); err != nil {
		t.Fatalf("SplitOnce: %v", err)
	}
	names, err := d.SecretNames()
	if err != nil {
		t.Fatalf("SecretNames after the split: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("the shared file still holds %v after the split", names)
	}
	for _, r := range EnablePreflight(d).Refusals {
		if r.Check == checkSharedSecrets {
			t.Errorf("the shared file is empty, yet the check still refuses: %q", r.Detail)
		}
	}
}

// TestEnableRefusesCompressIncomplete pins the compress-marker check: with no
// finished-pass kv row the enable refuses and names the marker, and the pass
// itself clears the refusal.
func TestEnableRefusesCompressIncomplete(t *testing.T) {
	d := directOpenTestDB(t)
	if _, ok, err := d.KVGet(compressKVKey); err != nil || ok {
		t.Fatalf("KVGet(%s) = (%v, %v), want an absent marker on a fresh database", compressKVKey, ok, err)
	}

	refusal := refusalFor(t, EnablePreflight(d), checkCompressDone)
	assertWants(t, refusal, compressKVKey, "CompressHistoryOnce")

	if _, _, err := CompressHistoryOnce(d, t.TempDir(), time.Now()); err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	for _, r := range EnablePreflight(d).Refusals {
		if r.Check == checkCompressDone {
			t.Errorf("the pass finished, yet the check still refuses: %q", r.Detail)
		}
	}
}

// TestPreflightReadsTheCompressMarkerWhereThePassWroteIt pins the compress
// check against the split. CompressHistoryOnce records its finish through
// LocalOrSelf, so post-split the marker is a row of the machine-local file and
// not of the shared one; a check reading the shared handle asked a file the
// marker was never in, and refused forever over a database the pass had already
// converted. The shared handle is still the right place for the secrets check
// above -- that one is what guards an upload -- so this test drives the pair
// itself rather than a handle opened without one.
func TestPreflightReadsTheCompressMarkerWhereThePassWroteIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := OpenSplit(path, Options{})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if _, _, err := CompressHistoryOnce(d, t.TempDir(), time.Now()); err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	// The marker is where the pass put it, and not on the shared file: that is
	// what makes the two files disagree if the check reads the wrong one.
	if _, ok, err := d.KVGet(compressKVKey); err != nil || ok {
		t.Fatalf("KVGet(%s) on the shared handle = (_, %v, %v), want absent: the pass writes local", compressKVKey, ok, err)
	}

	// The pass ran, so the check that reads the file it wrote must not refuse.
	for _, r := range EnablePreflight(d).Refusals {
		if r.Check == checkCompressDone {
			t.Errorf("the pass finished and recorded its marker, yet the check refuses: %q", r.Detail)
		}
	}

	// And it is still a real check: a marker nobody wrote still refuses, and it
	// names the pass that writes one.
	fresh, err := OpenSplit(filepath.Join(t.TempDir(), "fresh.db"), Options{})
	if err != nil {
		t.Fatalf("OpenSplit the fresh pair: %v", err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	assertWants(t, refusalFor(t, EnablePreflight(fresh), checkCompressDone), compressKVKey, "CompressHistoryOnce")
}

// TestPreflightErrIsARefusalNotAFailure pins the class Err hands a caller: the
// joined error matches ErrPreflightRefused so a caller can branch on it, while
// the line it renders is still exactly the sentence the refusals make. The class
// is for the caller; repeating it in the message would only duplicate the code
// the CLI already prints.
func TestPreflightErrIsARefusalNotAFailure(t *testing.T) {
	d := directOpenTestDB(t)
	seedOneEmptyOriginRowPerTable(t, d)
	for _, name := range []string{"client.key", "typesafe"} {
		if err := d.Tx(func(t *Tx) error { return t.SecretPut(name, []byte(name), time.Now()) }); err != nil {
			t.Fatalf("SecretPut %s: %v", name, err)
		}
	}

	preflight := EnablePreflight(d)
	err := preflight.Err()
	if err == nil {
		t.Fatal("Err() = nil, want the refusals")
	}
	if !errors.Is(err, ErrPreflightRefused) {
		t.Errorf("errors.Is(Err(), ErrPreflightRefused) = false for %v", err)
	}
	if !strings.HasPrefix(err.Error(), "db: enable preflight: ") {
		t.Errorf("Err() = %q, want the preflight's own sentence", err)
	}

	// Every refusal keeps its own wording and its own fix: the fix is the whole
	// reason the line is worth reading.
	for _, r := range preflight.Refusals {
		if !strings.Contains(err.Error(), r.Detail) {
			t.Errorf("Err() dropped the %s refusal's own text: %v", r.Check, err)
		}
	}
	for _, fix := range []string{fixEmptyOrigin, fixSharedSecs, fixCompress} {
		if !strings.Contains(err.Error(), fix) {
			t.Errorf("Err() = %v, want it to name the fix %q", err, fix)
		}
	}

	// A preflight that passed is no error at all, so the class cannot be
	// mistaken for one.
	if passing := (Preflight{}); passing.Err() != nil {
		t.Errorf("Err() on a passing preflight = %v, want nil", passing.Err())
	}
}
