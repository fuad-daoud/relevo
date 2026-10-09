package db

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// splitNow is the pass's stamp in the split fixtures: a fixed time so a backup
// file name is the same on every run.
var splitNow = time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)

// openSplitTest opens the pair directly and links them, so a pass test holds a
// real handle on both files even when the owner-hop switch is installed. The
// hop is a dial, and a pass needs the two pools it moves rows between.
func openSplitTest(t *testing.T) (*DB, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "relevo.db")
	local, err := openDirect(SplitPath(path), Options{})
	if err != nil {
		t.Fatalf("openDirect local: %v", err)
	}
	shared, err := openDirect(path, Options{})
	if err != nil {
		if cerr := local.Close(); cerr != nil {
			t.Fatalf("close local: %v", cerr)
		}
		t.Fatalf("openDirect shared: %v", err)
	}
	shared.local = local
	t.Cleanup(func() { _ = shared.Close() })
	return shared, dir
}

// seedSplitFixture fills the shared file with one row of every scope the
// classification names: shared history that must not move, every local table,
// and kv rows on both sides of the allowlist.
func seedSplitFixture(t *testing.T, d *DB) {
	t.Helper()
	if _, err := d.RecordPut(Record{Name: "api", State: "active", Round: 1, CWD: "/work/api"}); err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := d.sqlDB.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT OR REPLACE INTO secret (name, value, updated_at) VALUES (?, ?, ?)`,
		"client.key", []byte("pem-bytes"), "2026-10-03T09:00:00.000Z")
	exec(`INSERT OR REPLACE INTO secret (name, value, updated_at) VALUES (?, ?, ?)`,
		"typesafe", []byte("token"), "2026-10-03T09:00:00.000Z")
	exec(`INSERT OR REPLACE INTO config_doc (name, body, updated_at) VALUES (?, ?, ?)`,
		"candidates", `{"c":1}`, "2026-10-03T09:00:00.000Z")
	exec(`INSERT OR REPLACE INTO config_revision (at, source, message, version, changes, snapshot)
		VALUES (?, ?, ?, ?, ?, ?)`, "2026-10-03T09:00:00.000Z", "seed", "", 1, "[]", "{}")
	exec(`UPDATE config_meta SET version = 7 WHERE id = 1`)
	exec(`INSERT OR REPLACE INTO session_consent (harness_kind, session_id, answer, answer_at)
		VALUES (?, ?, ?, ?)`, "claude", "sess-1", "yes", "2026-10-03T09:00:00.000Z")
	exec(`INSERT OR REPLACE INTO ingest_cursor (source, byte_offset, head_sha, whole_sha, updated_at)
		VALUES (?, ?, ?, ?, ?)`, "/home/x/a.jsonl", 128, "sha-a", nil, "2026-10-03T09:00:00.000Z")
	exec(`INSERT INTO config_import (name, source_path, body, imported_at) VALUES (?, ?, ?, ?)`,
		"agents", "/home/x/agents.toml", []byte("[agent]"), "2026-10-03T09:00:00.000Z")
	for key, value := range map[string]string{
		"daemon":               `{"pid":42}`,
		"serve.clients":        `["x"]`,
		"claim/mm-1":           `{"held":true}`,
		"planner.pruned_at":    `"2026-10-03T09:00:00Z"`,
		"ingested.archive.r1":  `true`,
		"release-check":        `{"v":"1"}`,
		"opencode-active:acme": `"yes"`,
	} {
		exec(`INSERT OR REPLACE INTO kv (key, value_json, updated_at) VALUES (?, ?, ?)`,
			key, value, "2026-10-03T09:00:00.000Z")
	}
}

// splitCount is one table's row count on one of the two files.
func splitCount(t *testing.T, d *DB, table string) int {
	t.Helper()
	var n int
	query := "SELECT COUNT(*) FROM " + table
	if err := d.sqlDB.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// splitFileBytes is the bytes of a file and its write-ahead-log sibling, so a
// test that claims a file was not written sees the main file and the log.
func splitFileBytes(t *testing.T, path string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("read %s%s: %v", path, suffix, err)
		}
		out[suffix] = data
	}
	return out
}

// splitLocalFile names the local file a split open created, in either mode. A
// direct open reports the path it is open on; a handle that came back through
// the owner hop carries no path of its own, so there the name the split was
// asked for is the only one pointing at the file -- and it is the same name the
// direct handle reports, so the assertion is the same in both modes.
func splitLocalFile(t *testing.T, path string, local *DB) string {
	t.Helper()
	if p := local.Path(); p != "" {
		return p
	}
	return SplitPath(path)
}

// TestSplitOpen pins that one split open yields two files at the same schema
// version: the same migrations run on both, so placement is a routing
// decision and never a schema fork.
func TestSplitOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := OpenSplit(path, Options{})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	local := d.Local()
	if local == nil {
		t.Fatal("Local() = nil, want the machine-local file beside the database")
	}
	if got := SplitPath(path); got != filepath.Join(filepath.Dir(path), "relevo-local.db") {
		t.Errorf("SplitPath = %s, want the -local name beside the database", got)
	}
	if _, serr := os.Stat(splitLocalFile(t, path, local)); serr != nil {
		t.Fatalf("stat the local file: %v", serr)
	}

	want := embeddedVersion(t)
	sharedHave, sharedKnow := d.SchemaVersions()
	localHave, localKnow := local.SchemaVersions()
	if sharedHave != want || localHave != want {
		t.Errorf("schema versions = shared %d, local %d, want both %d", sharedHave, localHave, want)
	}
	if sharedKnow != localKnow {
		t.Errorf("embedded migrations = shared %d, local %d, want the same binary's", sharedKnow, localKnow)
	}
	if sharedHave != localHave {
		t.Errorf("schema versions differ: shared %d, local %d", sharedHave, localHave)
	}
}

// TestSplitRoutesEveryScope pins the classification itself: every shared table
// the history owns is shared, and every machine-local namespace, key and table
// is local. The table is the whole contract, so a row read through the wrong
// file is a row this lets leave the machine.
func TestSplitRoutesEveryScope(t *testing.T) {
	sharedTables := []string{
		"binding_record", "chains", "binding_event", "chain_event", "chain_member",
		"chain_check", "round_file", "installation", "repo", "mastermind",
		"binding", "round", "event", "artifact", "transcript",
	}
	for _, table := range sharedTables {
		if got := splitTableFile(table); got != splitShared {
			t.Errorf("table %s = %v, want shared", table, got)
		}
	}
	localTables := []string{
		"secret", "session_consent", "ingest_cursor", "config_import",
		"config_doc", "config_revision", "config_meta",
	}
	for _, table := range localTables {
		if got := splitTableFile(table); got != splitLocal {
			t.Errorf("table %s = %v, want local", table, got)
		}
	}

	localKeys := []string{
		"daemon", "serve.daemon", "serve.clients", "serve.ui", "serve.ledger",
		"planner/mm-1", "planner.pruned_at", "mastermind/mm-1", "claim/mm-1",
		"hooks.log", "ledger", "availability", "latency", "ui",
		"agents-manifest", "release-check", "ingested.archive.r1",
		"mirror-dedupe.v1", "mirror-dedupe.v2", "zstd-compress.v1",
		"origin-backfill.v1", "split-local.v1", "sync.state",
	}
	for _, key := range localKeys {
		if got := splitKeyFile(key); got != splitLocal {
			t.Errorf("kv key %s = %v, want local", key, got)
		}
	}
	sharedKeys := []string{"opencode-active:acme", "servers.json", "daemons", "planners"}
	for _, key := range sharedKeys {
		if got := splitKeyFile(key); got != splitShared {
			t.Errorf("kv key %s = %v, want shared", key, got)
		}
	}
}

// TestSplitMovesLocalRowsOnly pins the pass end to end: shared history stays
// shared, every local table and kv namespace lands in the local file, and the
// shared file keeps only the key the classification calls shared.
func TestSplitMovesLocalRowsOnly(t *testing.T) {
	d, dir := openSplitTest(t)
	local := d.Local()
	seedSplitFixture(t, d)

	stats, ran, err := SplitOnce(d, dir, splitNow)
	if err != nil {
		t.Fatalf("SplitOnce: %v", err)
	}
	if !ran {
		t.Fatal("ran = false, want true")
	}
	if stats.RowsMoved == 0 || stats.KVKeysMoved == 0 {
		t.Errorf("stats = %+v, want moved rows and moved keys", stats)
	}

	for _, table := range []string{"secret", "config_doc", "config_revision", "config_meta",
		"session_consent", "ingest_cursor", "config_import"} {
		if n := splitCount(t, d, table); n != 0 {
			t.Errorf("shared %s rows = %d, want 0", table, n)
		}
		if n := splitCount(t, local, table); n == 0 {
			t.Errorf("local %s rows = 0, want the moved rows", table)
		}
	}
	if n := splitCount(t, d, "binding_record"); n != 1 {
		t.Errorf("shared binding_record rows = %d, want the history row left in place", n)
	}
	if n := splitCount(t, local, "binding_record"); n != 0 {
		t.Errorf("local binding_record rows = %d, want 0", n)
	}

	for _, key := range []string{"daemon", "serve.clients", "claim/mm-1", "planner.pruned_at",
		"ingested.archive.r1", "release-check"} {
		if _, ok, kerr := d.KVGet(key); kerr != nil || ok {
			t.Errorf("shared kv %s = (_, %v, %v), want it moved", key, ok, kerr)
		}
		if _, ok, kerr := local.KVGet(key); kerr != nil || !ok {
			t.Errorf("local kv %s = (_, %v, %v), want the row moved", key, ok, kerr)
		}
	}
	if _, ok, err := d.KVGet("opencode-active:acme"); err != nil || !ok {
		t.Errorf("shared kv opencode-active:acme = (_, %v, %v), want it left in place", ok, err)
	}
	if _, ok, err := local.KVGet("opencode-active:acme"); err != nil || ok {
		t.Errorf("local kv opencode-active:acme = (_, %v, %v), want it not moved", ok, err)
	}

	// The secret the remote builder authenticates with is the sharpest case:
	// a pass that leaves it behind has still leaked the identity.
	if _, ok, err := d.SecretGet("client.key"); err != nil || ok {
		t.Errorf("shared secret client.key = (_, %v, %v), want it moved", ok, err)
	}
	value, ok, err := local.SecretGet("client.key")
	if err != nil || !ok {
		t.Fatalf("local secret client.key = (_, %v, %v), want the row moved", ok, err)
	}
	if string(value) != "pem-bytes" {
		t.Errorf("local client.key = %q, want the stored bytes", value)
	}
}

// TestSplitIsOneTime pins the kv short-circuit: a second call after a finished
// pass reports ran false and writes nothing to either file.
func TestSplitIsOneTime(t *testing.T) {
	d, dir := openSplitTest(t)
	local := d.Local()
	seedSplitFixture(t, d)
	if _, _, err := SplitOnce(d, dir, splitNow); err != nil {
		t.Fatalf("SplitOnce: %v", err)
	}

	sharedBefore := splitFileBytes(t, d.path)
	localBefore := splitFileBytes(t, local.path)

	stats, ran, err := SplitOnce(d, dir, splitNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("second SplitOnce: %v", err)
	}
	if ran {
		t.Error("second run reported ran = true, want false while the marker is present")
	}
	if stats.BackupPath != "" || stats.RowsMoved != 0 || stats.KVKeysMoved != 0 {
		t.Errorf("second run returned %+v, want zero stats", stats)
	}
	if after := splitFileBytes(t, d.path); !sameBytes(sharedBefore, after) {
		t.Error("the second pass wrote to the shared file")
	}
	if after := splitFileBytes(t, local.path); !sameBytes(localBefore, after) {
		t.Error("the second pass wrote to the local file")
	}

	// A row that arrives after the marker is not moved until the marker is
	// cleared, so a retry re-moves exactly what it is asked for and nothing else.
	if err := local.KVPut("daemon", []byte(`{"pid":99}`)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	if _, ran, err := SplitOnce(d, dir, splitNow); err != nil || ran {
		t.Errorf("run with the marker present = (%v, %v), want a no-op", ran, err)
	}
}

// TestSplitDonePinsTheGuardRead pins the read the re-exec guard stands on:
// false before the pass, true once the marker is written, and false on a
// handle with no local file.
func TestSplitDonePinsTheGuardRead(t *testing.T) {
	d, dir := openSplitTest(t)
	if d.SplitDone() {
		t.Error("SplitDone = true before the pass, want false")
	}
	seedSplitFixture(t, d)
	if _, _, err := SplitOnce(d, dir, splitNow); err != nil {
		t.Fatalf("SplitOnce: %v", err)
	}
	if !d.SplitDone() {
		t.Error("SplitDone = false after the pass, want true")
	}
	var nilDB *DB
	if nilDB.SplitDone() {
		t.Error("SplitDone on a nil handle = true, want false")
	}
}

// TestSplitKeepsLocalRowsOverDifferingStrays pins the conflict rule: a
// shared stray whose local counterpart already holds different content is
// converged away without overwriting the live row, and the pass counts and
// names it. A stray carrying the same content under a new stamp is not a
// conflict.
func TestSplitKeepsLocalRowsOverDifferingStrays(t *testing.T) {
	d, dir := openSplitTest(t)
	seedSplitFixture(t, d)
	if _, _, err := SplitOnce(d, dir, splitNow); err != nil {
		t.Fatalf("SplitOnce: %v", err)
	}

	execShared := func(query string, args ...any) {
		t.Helper()
		if _, err := d.sqlDB.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	execShared(`INSERT OR REPLACE INTO secret (name, value, updated_at) VALUES (?, ?, ?)`,
		"client.key", []byte("stray-bytes"), "2026-10-09T09:00:00.000Z")
	execShared(`INSERT OR REPLACE INTO secret (name, value, updated_at) VALUES (?, ?, ?)`,
		"typesafe", []byte("token"), "2026-10-09T10:00:00.000Z")
	execShared(`INSERT OR REPLACE INTO kv (key, value_json, updated_at) VALUES (?, ?, ?)`,
		"daemon", `{"pid":7}`, "2026-10-09T09:00:00.000Z")

	stats, ran, err := SplitOnce(d, dir, splitNow.Add(time.Hour))
	if err != nil {
		t.Fatalf("second SplitOnce: %v", err)
	}
	if !ran {
		t.Error("second run reported ran = false, want true while strays wait")
	}
	if stats.Conflicts != 2 {
		t.Errorf("conflicts = %d, want 2 (client.key and daemon)", stats.Conflicts)
	}
	wantKeys := []string{"secret/client.key", "kv/daemon"}
	if len(stats.ConflictKeys) != len(wantKeys) {
		t.Fatalf("conflict keys = %v, want %v", stats.ConflictKeys, wantKeys)
	}
	for i, want := range wantKeys {
		if stats.ConflictKeys[i] != want {
			t.Errorf("conflict key %d = %q, want %q", i, stats.ConflictKeys[i], want)
		}
	}

	// The live rows kept their values.
	if value, ok, err := d.Local().SecretGet("client.key"); err != nil || !ok {
		t.Fatalf("local client.key = %q, %t, %v; want the live row", value, ok, err)
	} else if string(value) != "pem-bytes" {
		t.Errorf("local client.key = %q, want the live bytes, not the stray", value)
	}
	if value, ok, err := d.Local().KVGet("daemon"); err != nil || !ok {
		t.Fatalf("local daemon = %q, %t, %v; want the live document", value, ok, err)
	} else if string(value) != `{"pid":42}` {
		t.Errorf("local daemon = %q, want the live document, not the stray", value)
	}

	// The strays are converged away all the same.
	if _, ok, err := d.SecretGet("client.key"); err != nil {
		t.Fatalf("shared client.key read: %v", err)
	} else if ok {
		t.Error("the conflicting shared client.key is still in the shared file")
	}
	if _, ok, err := d.KVGet("daemon"); err != nil {
		t.Fatalf("shared daemon read: %v", err)
	} else if ok {
		t.Error("the conflicting shared daemon key is still in the shared file")
	}
}

// TestSplitTakesBackupFirst pins the ordering: the backup exists beside the
// database and holds the rows before the move, and a backup that cannot be
// written converts nothing and records nothing.
func TestSplitTakesBackupFirst(t *testing.T) {
	d, dir := openSplitTest(t)
	seedSplitFixture(t, d)

	stats, ran, err := SplitOnce(d, dir, splitNow)
	if err != nil {
		t.Fatalf("SplitOnce: %v", err)
	}
	if !ran {
		t.Fatal("ran = false, want true")
	}
	if _, serr := os.Stat(stats.BackupPath); serr != nil {
		t.Fatalf("stat the backup: %v", serr)
	}
	backup, err := OpenReadOnly(stats.BackupPath)
	if err != nil {
		t.Fatalf("OpenReadOnly(%s): %v", stats.BackupPath, err)
	}
	defer func() { _ = backup.Close() }()
	if n := splitCount(t, backup, "secret"); n != 2 {
		t.Errorf("backup secret rows = %d, want the two rows the pass moved", n)
	}
	if n := splitCount(t, backup, "binding_record"); n != 1 {
		t.Errorf("backup binding_record rows = %d, want 1", n)
	}

	failing, failingDir := openSplitTest(t)
	seedSplitFixture(t, failing)
	occupied := filepath.Join(failingDir, "relevo.db.pre-split-"+splitNow.UTC().Format("20060102-150405"))
	if err := os.WriteFile(occupied, []byte("occupied"), 0o600); err != nil {
		t.Fatalf("occupy the backup path: %v", err)
	}
	if _, _, err := SplitOnce(failing, failingDir, splitNow); err == nil {
		t.Fatal("SplitOnce = nil error, want the backup failure")
	}
	if _, ok, kerr := failing.Local().KVGet(splitKVKey); kerr != nil || ok {
		t.Errorf("local kv %s = (ok %v, err %v), want absent", splitKVKey, ok, kerr)
	}
	if n := splitCount(t, failing, "secret"); n != 2 {
		t.Errorf("shared secret rows = %d, want 2 (nothing converted)", n)
	}
}

// TestSplitFailureWritesNoMarker pins the rollback: a failure between two
// tables returns with the marker absent and the failing table's rows still in
// the shared file, and the next start retries from the top.
func TestSplitFailureWritesNoMarker(t *testing.T) {
	d, dir := openSplitTest(t)
	local := d.Local()
	seedSplitFixture(t, d)

	restore := splitBeforeTable
	splitBeforeTable = func(table string) error {
		if table == "config_doc" {
			return errFake
		}
		return nil
	}
	t.Cleanup(func() { splitBeforeTable = restore })

	if _, ran, err := SplitOnce(d, dir, splitNow); err == nil {
		t.Fatal("SplitOnce = nil error, want the injected failure")
	} else if ran {
		t.Error("the failed pass reported ran = true")
	}
	if _, ok, kerr := local.KVGet(splitKVKey); kerr != nil || ok {
		t.Errorf("local kv %s = (ok %v, err %v), want absent after a failed pass", splitKVKey, ok, kerr)
	}
	if n := splitCount(t, d, "config_doc"); n != 1 {
		t.Errorf("shared config_doc rows = %d, want the source row left intact", n)
	}

	splitBeforeTable = restore
	if _, ran, err := SplitOnce(d, dir, splitNow.Add(time.Hour)); err != nil || !ran {
		t.Fatalf("retry = (%v, %v), want the pass to run", ran, err)
	}
	if n := splitCount(t, local, "config_doc"); n != 1 {
		t.Errorf("local config_doc rows = %d, want the retried row moved", n)
	}
	if _, ok, err := local.KVGet(splitKVKey); err != nil || !ok {
		t.Errorf("local kv %s = (_, %v, %v), want the retry to record itself", splitKVKey, ok, err)
	}
}

func sameBytes(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for name, want := range a {
		got, ok := b[name]
		if !ok || len(got) != len(want) {
			return false
		}
		for i := range want {
			if got[i] != want[i] {
				return false
			}
		}
	}
	return true
}
