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

// TestEnableRefusesUploadPrereqs pins the three assertions the upload path
// makes, in the order it makes them, and separates the two questions the
// preflight and the seed copy ask of them. The seed copy asserts all three of
// the shape it prepared; the preflight asserts only the one it cannot bring
// about, so a live file at 4096-byte pages in WAL with an undrained log does not
// refuse -- that is the ordinary shape of a live database, and the seed copy
// drains the log as part of writing the copy.
func TestEnableRefusesUploadPrereqs(t *testing.T) {
	t.Run("a log the seed copy drains is not a refusal", testALogTheSeedCopyDrainsIsNotARefusal)
	t.Run("every assertion", testEveryUploadAssertion)
	t.Run("only the page size is beyond the seed path", testOnlyPageSizeIsBeyondTheSeedPath)
}

// testALogTheSeedCopyDrainsIsNotARefusal drives the live shape the defect was
// reported from: a database at 4096-byte pages in WAL whose log still holds
// pages. The seed copy brings that about by draining the log, so refusing it
// named a fix the enable path was about to perform on itself.
func testALogTheSeedCopyDrainsIsNotARefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d := directOpen(t, path, Options{})
	// A real undrained log, grown by real writes rather than a forged -wal: the
	// assertions below have to hold on a file the seed path can actually drain,
	// and a hand-written one is not a file either engine will checkpoint.
	seedLateEnableHistory(t, d)

	shape, err := d.UploadShape()
	if err != nil {
		t.Fatalf("UploadShape: %v", err)
	}
	if shape.WALBytes == 0 {
		t.Fatal("the log holds nothing, so this is not the live shape the check must survive")
	}
	if shape.Failing() == "" {
		t.Error("Failing() on a live log that holds pages, want the seed copy's own assertion")
	}
	if failing := shape.UnfixableBySeed(); failing != "" {
		t.Errorf("UnfixableBySeed() = %q, want none: the seed copy drains the log", failing)
	}

	// And the preflight agrees with the narrower question.
	for _, r := range EnablePreflight(d).Refusals {
		if r.Check == checkUploadShape {
			t.Errorf("the seed copy drains this log, yet the check refuses: %q", r.Detail)
		}
	}

	// The fix that shape asks for is the one the enable path then runs: the seed
	// copy drains the very log the check declined to refuse on, and leaves the
	// shape the upload path asserts.
	seed := filepath.Join(t.TempDir(), "seed.db")
	if err := d.SeedCopy(seed); err != nil {
		t.Fatalf("SeedCopy over the undrained log: %v", err)
	}
	drained, err := d.UploadShape()
	if err != nil {
		t.Fatalf("UploadShape after the seed copy: %v", err)
	}
	if drained.WALBytes != 0 {
		t.Errorf("the log still holds %d bytes after the seed copy, want none", drained.WALBytes)
	}
	if failing := drained.Failing(); failing != "" {
		t.Errorf("the live shape after the seed copy = %q, want none", failing)
	}
}

// testEveryUploadAssertion pins the three assertions and the order the upload
// path makes them in, which is the seed copy's own assertion of the shape it
// prepared. The engine this build opens with always runs WAL at 4096-byte
// pages, so the other two are pinned through Failing.
func testEveryUploadAssertion(t *testing.T) {
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
}

// testOnlyPageSizeIsBeyondTheSeedPath pins the narrower question the preflight
// asks. The journal mode and the log are what prepareUploadShape brings about,
// so they are a shape the seed copy reaches on its own and never a refusal the
// preflight makes; a page size is fixed at creation and no pragma changes one.
func testOnlyPageSizeIsBeyondTheSeedPath(t *testing.T) {
	for _, tc := range []struct {
		name  string
		shape UploadShape
		want  string
	}{
		{name: "the asserted shape", shape: UploadShape{JournalMode: seedJournalMode, PageSize: seedPageSize}},
		{name: "not wal", shape: UploadShape{JournalMode: "delete", PageSize: seedPageSize}},
		{name: "log holds pages", shape: UploadShape{JournalMode: seedJournalMode, PageSize: seedPageSize, WALBytes: 4 << 20}},
		{name: "wrong page size", shape: UploadShape{JournalMode: seedJournalMode, PageSize: 8192}, want: "page_size is 8192, not 4096"},
		{
			name:  "a wrong page size outranks what the seed fixes",
			shape: UploadShape{JournalMode: "delete", PageSize: 8192, WALBytes: 4096},
			want:  "page_size is 8192, not 4096",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if failing := tc.shape.UnfixableBySeed(); failing != tc.want {
				t.Fatalf("UnfixableBySeed() = %q, want %q", failing, tc.want)
			}
		})
	}

	// The page size is read off the file, not out of a pragma the pass can set,
	// so it is the one refusal that has to name a rebuild -- and it has to say
	// the seed brings about the rest, so the message names both.
	if !strings.Contains(fixUploadShape, "rebuilt at 4096 bytes") {
		t.Errorf("the upload-shape fix %q does not name the rebuild that clears it", fixUploadShape)
	}
	if !strings.Contains(fixUploadShape, "SeedCopy") {
		t.Errorf("the upload-shape fix %q does not say the seed brings about the rest", fixUploadShape)
	}
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

	// A live database at 4096-byte pages in WAL, holding an ordinary undrained
	// log: the seed copy drains that log as part of writing the copy, so the
	// preflight has nothing left to refuse on and the enable proceeds.
	if preflight := EnablePreflight(d); !preflight.OK() {
		t.Fatalf("refusals = %+v, want none: the seed copy brings about the shape the check would refuse", preflight.Refusals)
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
