package db

import (
	"errors"
	"fmt"
	"os"
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
	// next open's gate then passes on the counts alone.
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

// stampEveryOrigin fills in the origin the gate counts, on the tables the
// backfill does not itself reach.
func stampEveryOrigin(t *testing.T, d *DB, origin string) {
	t.Helper()
	for _, table := range []string{"repo", "mastermind", "chains"} {
		if _, err := d.sqlDB.Exec(`UPDATE `+table+` SET origin = ? WHERE origin = ''`, origin); err != nil {
			t.Fatalf("stamp %s: %v", table, err)
		}
	}
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

// TestEnableRefusesUploadPrereqs pins the three assertions the upload path
// makes, in the order it makes them, and drives one of them end to end: a log
// that still holds pages is refused with the byte count and the writer that
// drains it. The engine this build opens with always runs WAL at 4096-byte
// pages, so the other two are pinned through the assertion they are.
func TestEnableRefusesUploadPrereqs(t *testing.T) {
	t.Run("a log that still holds pages", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "relevo.db")
		d := directOpen(t, path, Options{})
		held := []byte("pages not yet checkpointed")
		if err := os.WriteFile(path+"-wal", held, 0o600); err != nil {
			t.Fatalf("write -wal: %v", err)
		}

		refusal := refusalFor(t, EnablePreflight(d), checkUploadShape)
		assertWants(t, refusal, fmt.Sprintf("write-ahead log still holds %d bytes", len(held)), "SeedCopy")
	})

	t.Run("every assertion", func(t *testing.T) {
		held := UploadShape{JournalMode: seedJournalMode, PageSize: seedPageSize, WALBytes: 4096}
		for _, tc := range []struct {
			name  string
			shape UploadShape
			want  string
		}{
			{name: "not wal", shape: UploadShape{JournalMode: "delete", PageSize: seedPageSize}, want: `journal_mode is "delete", not wal`},
			{name: "wrong page size", shape: UploadShape{JournalMode: seedJournalMode, PageSize: 8192}, want: "page_size is 8192, not 4096"},
			{name: "log holds pages", shape: held, want: "the write-ahead log still holds 4096 bytes"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if failing := tc.shape.Failing(); failing != tc.want {
					t.Fatalf("Failing() = %q, want %q", failing, tc.want)
				}
				if tc.shape.Satisfied() {
					t.Error("Satisfied() on a shape that fails an assertion")
				}
			})
		}

		ok := UploadShape{JournalMode: seedJournalMode, PageSize: seedPageSize}
		if failing := ok.Failing(); failing != "" {
			t.Errorf("Failing() on the asserted shape = %q, want none", failing)
		}
		if !ok.Satisfied() {
			t.Error("Satisfied() on the asserted shape")
		}
	})
}

// seedLateEnableHistory writes the history a database enabling late already
// holds: a converted body per round file and a chunk of incompressible-looking
// bytes, so the seed copy has bulk to carry and a second conversion would show.
func seedLateEnableHistory(t *testing.T, d *DB) string {
	t.Helper()
	id, err := d.RecordPut(testRecord("webshop"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	body := string(bigHistoryValue())
	for i := range 24 {
		name := "00" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "-run.jsonl"
		now := time.Now().UTC()
		if _, err := d.sqlDB.Exec(`INSERT INTO round_file (record_id, name, round, body, body_codec, bytes, sha256, mtime, sealed_at)
			VALUES (?,?,?,?,0,?,?,?,?)`,
			id, name, 1, []byte(body), len(body), hashBytes([]byte(body)), formatMTime(now), formatTime(now)); err != nil {
			t.Fatalf("insert round_file %s: %v", name, err)
		}
	}
	return id
}

// TestLateEnableSeedsOnce pins the late-enable path: a database that already
// converted its history writes its seed copy once, converts nothing a second
// time, and refuses to write that copy again.
func TestLateEnableSeedsOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "relevo.db")
	d := directOpen(t, path, Options{Origin: "inst-a"})

	seedLateEnableHistory(t, d)
	if _, _, err := CompressHistoryOnce(d, t.TempDir(), time.Now()); err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	stampEveryOrigin(t, d, "inst-a")

	if preflight := EnablePreflight(d); len(preflight.Refusals) != 1 || preflight.Refusals[0].Check != checkUploadShape {
		t.Fatalf("refusals = %+v, want only the undrained log", preflight.Refusals)
	}

	seed := filepath.Join(dir, "seed.db")
	if err := d.SeedCopy(seed); err != nil {
		t.Fatalf("SeedCopy: %v", err)
	}
	assertSeedIsAWholeDatabase(t, seed)

	// The copy is an upload artefact, never a replacement: the source keeps
	// every row under the codec the pass already gave it, so enabling late cost
	// an upload rather than a conversion.
	var files, stillPlain int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM round_file`).Scan(&files); err != nil {
		t.Fatalf("count round_file: %v", err)
	}
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM round_file WHERE body_codec <> ?`, codecZstd).Scan(&stillPlain); err != nil {
		t.Fatalf("count plain round_file rows: %v", err)
	}
	if files != 24 || stillPlain != 0 {
		t.Errorf("after the seed copy: %d round_file rows, %d still plain, want 24 and 0", files, stillPlain)
	}
	empty, err := CountEmptyOrigins(d)
	if err != nil {
		t.Fatalf("CountEmptyOrigins after the seed copy: %v", err)
	}
	if !empty.Empty() {
		t.Errorf("the seed copy stamped origins: %s", empty)
	}

	// A second seed over the first is the rework the path refuses.
	err = d.SeedCopy(seed)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("a second SeedCopy = %v, want a refusal", err)
	}
	if !strings.Contains(err.Error(), "written twice") {
		t.Errorf("the second-seed refusal %q does not say the seed would be written twice", err)
	}

	// A path the engine's literal cannot express is refused before anything is
	// created, so the failure names the destination rather than a private copy.
	quoted := filepath.Join(dir, "it's-a-seed.db")
	err = d.SeedCopy(quoted)
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "the path contains a quote") {
		t.Fatalf("SeedCopy at a quoted path = %v, want the literal refusal", err)
	}
	if matches, gerr := filepath.Glob(filepath.Join(dir, "*it*")); gerr != nil || len(matches) != 0 {
		t.Errorf("the quoted refusal left %v behind", matches)
	}
}

// assertSeedIsAWholeDatabase checks the copy is a database in its own right and
// not an empty file the upload would accept and the pull would reject.
func assertSeedIsAWholeDatabase(t *testing.T, seed string) {
	t.Helper()
	seedDB, err := OpenReadOnly(seed)
	if err != nil {
		t.Fatalf("open the seed copy: %v", err)
	}
	defer func() { _ = seedDB.Close() }()

	var files int
	if err := seedDB.sqlDB.QueryRow(`SELECT COUNT(*) FROM round_file`).Scan(&files); err != nil {
		t.Fatalf("count round_file in the seed copy: %v", err)
	}
	if files != 24 {
		t.Errorf("the seed copy holds %d round_file rows, want 24", files)
	}
	var empty int
	if err := seedDB.sqlDB.QueryRow(`SELECT COUNT(*) FROM round_file WHERE body_codec <> ?`, codecZstd).Scan(&empty); err != nil {
		t.Fatalf("count plain rows in the seed copy: %v", err)
	}
	if empty != 0 {
		t.Errorf("the seed copy holds %d plain rows, want a converted history", empty)
	}
}
