package db

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// schema12Fixture opens a database migrated only through 012, then opens it
// normally so 013 runs: the pass sees rows that predate the codec columns.
func schema12Fixture(t *testing.T) (*DB, string) {
	t.Helper()
	return schema12FixtureOpened(t, Open)
}

// schema12DirectFixture is schema12Fixture on a direct handle. The pass test
// asserts that the vacuum it runs succeeded, and a vacuum refuses a dialled
// handle, so that test needs a direct handle even under the owner-mode switch.
func schema12DirectFixture(t *testing.T) (*DB, string) {
	t.Helper()
	return schema12FixtureOpened(t, func(path string) (*DB, error) {
		return openDirect(path, Options{})
	})
}

func schema12FixtureOpened(t *testing.T, open func(string) (*DB, error)) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo.db")
	sqlDB := rawSQLDB(t, path)
	if err := applyMigrations(sqlDB, migrationFilesUpTo(t, 12)); err != nil {
		t.Fatalf("applyMigrations through 012: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close the raw handle: %v", err)
	}

	d, err := open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, path
}

// insertPlainRoundFile writes a row holding its value as a pre-codec database
// did: the codec column keeps its default 0.
func insertPlainRoundFile(t *testing.T, d *DB, recordID, name, body string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := d.sqlDB.Exec(`INSERT INTO round_file (record_id, name, round, body, body_codec, bytes, sha256, mtime, sealed_at)
		VALUES (?,?,?,?,0,?,?,?,?)`,
		recordID, name, 1, []byte(body), len(body), hashBytes([]byte(body)), formatMTime(now), formatTime(now)); err != nil {
		t.Fatalf("insert round_file %s: %v", name, err)
	}
}

// seedPlainHistory writes the fixture's rows as plain values: a compressible
// file, a small one, an empty one, and a transcript whose JSON compresses and
// whose rendered text does not.
func seedPlainHistory(t *testing.T, d *DB) string {
	t.Helper()
	id, err := d.RecordPut(testRecord("webshop"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}

	big := string(bigHistoryValue())
	insertPlainRoundFile(t, d, id, "001-big.jsonl", big)
	insertPlainRoundFile(t, d, id, "002-small.md", "small\n")
	insertPlainRoundFile(t, d, id, "003-empty", "")

	now := formatTime(time.Now().UTC())
	if _, err := d.sqlDB.Exec(`INSERT INTO transcript (id, owner_kind, owner_id, seq, ts, record_json, record_json_codec, rendered, rendered_codec)
		VALUES (?,?,?,?,?,?,0,?,0)`, NewID(), OwnerMasterMind, "sess-plain", 0, now, big, "line\n"); err != nil {
		t.Fatalf("insert transcript: %v", err)
	}
	return id
}

// seedFullyCompressible writes rows whose every converted column compresses,
// so a finished pass leaves no candidate behind.
func seedFullyCompressible(t *testing.T, d *DB) {
	t.Helper()
	id, err := d.RecordPut(testRecord("webshop"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	big := string(bigHistoryValue())
	insertPlainRoundFile(t, d, id, "001-big.jsonl", big)
	now := formatTime(time.Now().UTC())
	if _, err := d.sqlDB.Exec(`INSERT INTO transcript (id, owner_kind, owner_id, seq, ts, record_json, record_json_codec, rendered, rendered_codec)
		VALUES (?,?,?,?,?,?,0,?,0)`, NewID(), OwnerMasterMind, "sess-big", 0, now, big, big); err != nil {
		t.Fatalf("insert transcript: %v", err)
	}
}

// historyManifest hashes every value the read points return, so a pass that
// changed one byte of readable history, or added or dropped a row, fails a
// comparison.
func historyManifest(t *testing.T, d *DB) map[string]string {
	t.Helper()
	out := map[string]string{}

	for _, k := range roundFileKeys(t, d) {
		body, _, ok, err := d.RoundFileGet(k[0], k[1])
		if err != nil || !ok {
			t.Fatalf("RoundFileGet(%s/%s) = (ok %v, err %v)", k[0], k[1], ok, err)
		}
		out["file:"+k[0]+"/"+k[1]] = hashBytes(body)
	}

	for _, o := range transcriptOwners(t, d) {
		recs, err := d.Transcript(o[0], o[1], 0, 0)
		if err != nil {
			t.Fatalf("Transcript(%s/%s): %v", o[0], o[1], err)
		}
		for _, r := range recs {
			out[fmt.Sprintf("tx:%s/%s/%d", o[0], o[1], r.Seq)] = hashBytes([]byte(r.RecordJSON + "\x00" + r.Rendered))
		}
	}
	return out
}

func roundFileKeys(t *testing.T, d *DB) [][2]string {
	t.Helper()
	rows, err := d.sqlDB.Query(`SELECT record_id, name FROM round_file ORDER BY record_id, name`)
	if err != nil {
		t.Fatalf("query round_file: %v", err)
	}
	out, err := collectRows(rows, func(s rowScanner) ([2]string, error) {
		var k [2]string
		err := s.Scan(&k[0], &k[1])
		return k, err
	})
	if err != nil {
		t.Fatalf("scan round_file: %v", err)
	}
	return out
}

func transcriptOwners(t *testing.T, d *DB) [][2]string {
	t.Helper()
	rows, err := d.sqlDB.Query(`SELECT DISTINCT owner_kind, owner_id FROM transcript ORDER BY owner_kind, owner_id`)
	if err != nil {
		t.Fatalf("query transcript owners: %v", err)
	}
	out, err := collectRows(rows, func(s rowScanner) ([2]string, error) {
		var k [2]string
		err := s.Scan(&k[0], &k[1])
		return k, err
	})
	if err != nil {
		t.Fatalf("scan transcript owners: %v", err)
	}
	return out
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// manifestDiff names every key whose value differs or that only one side has.
func manifestDiff(before, after map[string]string) string {
	var diffs []string
	for k, v := range before {
		if after[k] != v {
			diffs = append(diffs, k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			diffs = append(diffs, k)
		}
	}
	sort.Strings(diffs)
	return strings.Join(diffs, ", ")
}

func tableStats(stats CompressStats, name string) CompressTableStats {
	for _, ts := range stats.Tables {
		if ts.Table == name {
			return ts
		}
	}
	return CompressTableStats{}
}

func codecOf(t *testing.T, d *DB, query string) int {
	t.Helper()
	var codec int
	if err := d.sqlDB.QueryRow(query).Scan(&codec); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return codec
}

// TestCompressHistoryOnce pins the pass end to end on a schema-12 fixture:
// compressible rows move to codec 1, small rows keep codec 0 and their value,
// every decoded value reads back byte-identical, the backup holds the same
// history, and the stats and kv record the run.
func TestCompressHistoryOnce(t *testing.T) {
	// The pass ends by vacuuming, and the test asserts that vacuum succeeded;
	// a vacuum refuses a dialled handle, so the fixture is opened directly.
	d, _ := schema12DirectFixture(t)
	seedPlainHistory(t, d)
	before := historyManifest(t, d)

	stats, ran, err := CompressHistoryOnce(d, t.TempDir(), time.Now())
	if err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	if !ran {
		t.Fatal("ran = false, want true")
	}
	if stats.BackupPath == "" {
		t.Fatal("BackupPath is empty, want the pre-pass backup")
	}
	if _, ok, kerr := d.KVGet(compressKVKey); kerr != nil || !ok {
		t.Fatalf("kv %s = (ok %v, err %v), want present", compressKVKey, ok, kerr)
	}
	if diff := manifestDiff(before, historyManifest(t, d)); diff != "" {
		t.Errorf("readable history changed for: %s", diff)
	}
	assertCodecs(t, d)
	assertBackupMatches(t, stats.BackupPath, before)
	assertPassStats(t, stats)
}

func assertCodecs(t *testing.T, d *DB) {
	t.Helper()
	cases := []struct {
		label, query string
		want         int
	}{
		{"big round file", `SELECT body_codec FROM round_file WHERE name = '001-big.jsonl'`, codecZstd},
		{"small round file", `SELECT body_codec FROM round_file WHERE name = '002-small.md'`, codecPlain},
		{"empty round file", `SELECT body_codec FROM round_file WHERE name = '003-empty'`, codecPlain},
		{"big record json", `SELECT record_json_codec FROM transcript WHERE seq = 0`, codecZstd},
		{"small rendered", `SELECT rendered_codec FROM transcript WHERE seq = 0`, codecPlain},
	}
	for _, c := range cases {
		if got := codecOf(t, d, c.query); got != c.want {
			t.Errorf("%s codec = %d, want %d", c.label, got, c.want)
		}
	}
}

func assertBackupMatches(t *testing.T, path string, want map[string]string) {
	t.Helper()
	backup, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly(%s): %v", path, err)
	}
	defer func() { _ = backup.Close() }()
	if diff := manifestDiff(want, historyManifest(t, backup)); diff != "" {
		t.Errorf("the backup's readable history differs for: %s", diff)
	}
}

func assertPassStats(t *testing.T, stats CompressStats) {
	t.Helper()
	files := tableStats(stats, "round_file")
	if files.RowsCompressed != 1 || files.ColumnsCompressed != 1 || files.ColumnsKeptPlain != 2 {
		t.Errorf("round_file stats = %+v, want 1 row / 1 column compressed and 2 kept plain", files)
	}
	if files.BytesOut >= files.BytesIn {
		t.Errorf("round_file bytes out = %d, want fewer than in %d", files.BytesOut, files.BytesIn)
	}
	if tx := tableStats(stats, "transcript"); tx.ColumnsCompressed != 1 || tx.ColumnsKeptPlain != 1 {
		t.Errorf("transcript stats = %+v, want 1 compressed and 1 kept plain", tx)
	}
	if stats.CheckpointErr != "" || stats.VacuumErr != "" {
		t.Errorf("finish errors = %q/%q, want none", stats.CheckpointErr, stats.VacuumErr)
	}
}

// TestCompressHistoryOnceIsOneTime pins the kv short-circuit: a second call
// after a finished pass reports ran false and writes nothing.
func TestCompressHistoryOnceIsOneTime(t *testing.T) {
	d, _ := schema12Fixture(t)
	seedPlainHistory(t, d)
	if _, _, err := CompressHistoryOnce(d, t.TempDir(), time.Now()); err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}

	stats, ran, err := CompressHistoryOnce(d, t.TempDir(), time.Now())
	if err != nil {
		t.Fatalf("second CompressHistoryOnce: %v", err)
	}
	if ran {
		t.Error("second call ran = true, want false while the kv row is present")
	}
	if stats.BackupPath != "" || len(stats.Tables) != 0 {
		t.Errorf("second call returned %+v, want zero stats", stats)
	}
}

// TestCompressHistoryOnceResumesPartialConversion pins that a started-then-
// abandoned pass resumes: rows already at codec 1 are skipped, a plain row that
// arrived meanwhile is converted, and every decoded value is unchanged.
func TestCompressHistoryOnceResumesPartialConversion(t *testing.T) {
	d, _ := schema12Fixture(t)
	id := seedPlainHistory(t, d)
	if _, _, err := CompressHistoryOnce(d, t.TempDir(), time.Now()); err != nil {
		t.Fatalf("first CompressHistoryOnce: %v", err)
	}
	if err := d.KVDelete(compressKVKey); err != nil {
		t.Fatalf("KVDelete: %v", err)
	}
	insertPlainRoundFile(t, d, id, "004-new.jsonl", string(bigHistoryValue()))

	before := historyManifest(t, d)
	stats, ran, err := CompressHistoryOnce(d, t.TempDir(), time.Now())
	if err != nil {
		t.Fatalf("resumed CompressHistoryOnce: %v", err)
	}
	if !ran {
		t.Fatal("resumed ran = false, want true")
	}
	if got := tableStats(stats, "round_file").RowsCompressed; got != 1 {
		t.Errorf("round_file rows compressed on resume = %d, want 1 (the new row only)", got)
	}
	if diff := manifestDiff(before, historyManifest(t, d)); diff != "" {
		t.Errorf("readable history changed on resume for: %s", diff)
	}
	if got := codecOf(t, d, `SELECT body_codec FROM round_file WHERE name = '004-new.jsonl'`); got != codecZstd {
		t.Errorf("new row codec = %d, want %d", got, codecZstd)
	}
}

// TestCompressHistoryOnceFullyConvertedTakesNoBackup pins the no-candidate
// path: a database whose every converted column is already a frame writes the
// stats without a backup.
func TestCompressHistoryOnceFullyConvertedTakesNoBackup(t *testing.T) {
	d, _ := schema12Fixture(t)
	seedFullyCompressible(t, d)
	if _, _, err := CompressHistoryOnce(d, t.TempDir(), time.Now()); err != nil {
		t.Fatalf("first CompressHistoryOnce: %v", err)
	}
	if err := d.KVDelete(compressKVKey); err != nil {
		t.Fatalf("KVDelete: %v", err)
	}

	dir := t.TempDir()
	stats, ran, err := CompressHistoryOnce(d, dir, time.Now())
	if err != nil {
		t.Fatalf("second CompressHistoryOnce: %v", err)
	}
	if !ran {
		t.Fatal("ran = false, want true after the kv row was removed")
	}
	if stats.BackupPath != "" {
		t.Errorf("BackupPath = %q, want none when there is no candidate", stats.BackupPath)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("backup dir holds %d files, want none", len(entries))
	}
}

// TestCompressHistoryOnceEmptyDBWritesKV pins that a fresh database with
// nothing to convert records the pass and takes no backup.
func TestCompressHistoryOnceEmptyDBWritesKV(t *testing.T) {
	d := openTestDB(t)
	dir := t.TempDir()

	stats, ran, err := CompressHistoryOnce(d, dir, time.Now())
	if err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	if !ran || stats.BackupPath != "" {
		t.Errorf("(ran %v, BackupPath %q), want (true, \"\")", ran, stats.BackupPath)
	}
	value, ok, err := d.KVGet(compressKVKey)
	if err != nil || !ok {
		t.Fatalf("kv %s = (ok %v, err %v), want present", compressKVKey, ok, err)
	}
	var stored CompressStats
	if err := json.Unmarshal(value, &stored); err != nil {
		t.Fatalf("unmarshal the kv value: %v", err)
	}
	if stored.DoneAt.IsZero() {
		t.Error("recorded done_at is zero, want the pass's stamp")
	}
}

// TestCompressHistoryOnceBackupFailureLeavesNoKV pins the ordering: a backup
// that cannot be written returns the error with nothing converted and no kv row.
func TestCompressHistoryOnceBackupFailureLeavesNoKV(t *testing.T) {
	d, _ := schema12Fixture(t)
	seedPlainHistory(t, d)

	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	occupied := filepath.Join(dir, "relevo.db.pre-zstd-"+now.UTC().Format("20060102-150405"))
	if err := os.WriteFile(occupied, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("occupy the backup path: %v", err)
	}

	if _, _, err := CompressHistoryOnce(d, dir, now); err == nil {
		t.Fatal("CompressHistoryOnce = nil error, want the backup failure")
	}
	if _, ok, err := d.KVGet(compressKVKey); err != nil || ok {
		t.Errorf("kv %s = (ok %v, err %v), want absent", compressKVKey, ok, err)
	}
	if got := codecOf(t, d, `SELECT body_codec FROM round_file WHERE name = '001-big.jsonl'`); got != codecPlain {
		t.Errorf("codec = %d, want %d (nothing converted)", got, codecPlain)
	}
}

// TestCompressHistoryOnceRefusesUnknownCodec pins that an unknown codec on a
// candidate row stops the pass with ErrInvalid and no kv row.
func TestCompressHistoryOnceRefusesUnknownCodec(t *testing.T) {
	d, _ := schema12Fixture(t)
	seedPlainHistory(t, d)
	if _, err := d.sqlDB.Exec(`UPDATE round_file SET body_codec = 9 WHERE name = '001-big.jsonl'`); err != nil {
		t.Fatalf("set an unknown codec: %v", err)
	}

	if _, _, err := CompressHistoryOnce(d, t.TempDir(), time.Now()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CompressHistoryOnce err = %v, want ErrInvalid", err)
	}
	if _, ok, err := d.KVGet(compressKVKey); err != nil || ok {
		t.Errorf("kv %s = (ok %v, err %v), want absent", compressKVKey, ok, err)
	}
}
