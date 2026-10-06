package sync

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tornSidecarPath is the real sidecar a machine was left with: the file the sync
// engine kept beside a live database whose write-ahead log held 91 chain_event
// rows and whose own schema was complete, and whose sidecar's schema bytes stop
// immediately after the record for chain_event's automatic index.
//
// It is the artifact the pull's refusal was about. Nothing here reconstructs it:
// the fixture is the file as it sat on disk, and the table the driver named --
// chain_event, whose automatic index sits at root page 43 in that very database
// -- is what the rule below has to find in it without being told.
const tornSidecarPath = "testdata/torn-changes-sidecar"

// autoindexSeqTail is the ordinal an automatic index's name carries.
const autoindexSeqTail = "_1"

func readSidecar(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

// wholeSidecar is the torn one with the one missing record put back.
//
// The record is spliced in beside its own automatic index rather than appended,
// which is what makes it a faithful "the write finished" counterpart: the
// automatic index and the table it belongs to sit next to each other in the
// stored schema, and a file that carries both is one the engine can parse.
func wholeSidecar(t *testing.T) []byte {
	t.Helper()
	torn := readSidecar(t, tornSidecarPath)
	const table = "CREATE TABLE chain_event (chain_id TEXT NOT NULL, seq INTEGER NOT NULL)"
	marker := []byte(autoindexPrefix + "chain_event" + autoindexSeqTail)
	at := bytes.Index(torn, marker)
	if at < 0 {
		t.Fatalf("the fixture no longer names %s", marker)
	}
	return bytes.Replace(torn, marker, append([]byte(table), marker...), 1)
}

// TestTheRealSidecarIsRecognisedAsTorn is the defect, on the real file.
//
// The rule the sync engine refuses on is its own: an automatic index named for a
// table the schema does not name is not a schema a database can have. This is
// that rule over the sidecar this machine actually carried, and the table it
// reports is the table the driver reported.
func TestTheRealSidecarIsRecognisedAsTorn(t *testing.T) {
	data := readSidecar(t, tornSidecarPath)

	table, torn := orphanAutoindexTable(data)
	if !torn {
		t.Fatal("the real torn sidecar was not recognised")
	}
	if table != "chain_event" {
		t.Errorf("orphan table = %q, want %q", table, "chain_event")
	}
	// The refusal names the table because the automatic index names it. Pinning
	// the name against the index in the file is what keeps this from being a
	// lookup that happens to agree with the driver today.
	if !bytes.Contains(data, []byte(autoindexPrefix+table+autoindexSeqTail)) {
		t.Errorf("the fixture does not carry %s%s%s", autoindexPrefix, table, autoindexSeqTail)
	}
}

// TestTheRealSidecarIsCompleteForEveryOtherTable is the other half of the same
// reading, and the reason this is a torn write rather than a sidecar whose
// database never had a chain_event table: every automatic index in the file
// except that one has the table it belongs to.
func TestTheRealSidecarIsCompleteForEveryOtherTable(t *testing.T) {
	data := readSidecar(t, tornSidecarPath)
	defined := definedTables(data)

	checked := 0
	for i := 0; i+len(autoindexPrefix) < len(data); i++ {
		if !bytes.HasPrefix(data[i:], []byte(autoindexPrefix)) {
			continue
		}
		table := autoindexTableName(data[i+len(autoindexPrefix):])
		if table == "" {
			continue
		}
		checked++
		if !defined[table] && table != "chain_event" {
			t.Errorf("table %q has no CREATE TABLE in the fixture, so the fixture is not the one this test names", table)
		}
	}
	if checked == 0 {
		t.Fatal("the fixture names no automatic index at all")
	}
	if _, torn := orphanAutoindexTable(append(data, []byte(autoindexPrefix+"chain_event"+autoindexSeqTail)...)); !torn {
		t.Error("the rule stopped firing on the real file once the index appears twice")
	}
}

// TestAWholeSidecarIsNotTorn is the negative that keeps the removal narrow. The
// same bytes with the one record spliced back in parse, so nothing about them
// trips the rule.
func TestAWholeSidecarIsNotTorn(t *testing.T) {
	if table, torn := orphanAutoindexTable(wholeSidecar(t)); torn {
		t.Errorf("a whole sidecar read as torn, naming %q", table)
	}
}

// TestConvergeSidecarsRemovesTheRealSidecarAndNamesTheTable is the convergence
// itself: the file is gone, so the next open has the engine build one from the
// live database, and the report says which table said so.
func TestConvergeSidecarsRemovesTheRealSidecarAndNamesTheTable(t *testing.T) {
	path := writeSidecar(t, readSidecar(t, tornSidecarPath))

	repair, err := ConvergeSidecars(path)
	if err != nil {
		t.Fatalf("ConvergeSidecars: %v", err)
	}
	if repair.Path != path+sidecarSuffix {
		t.Errorf("repair path = %q, want %q", repair.Path, path+sidecarSuffix)
	}
	if repair.OrphanTable != "chain_event" {
		t.Errorf("repair names %q, want %q", repair.OrphanTable, "chain_event")
	}
	if repair.Bytes != int64(len(readSidecar(t, tornSidecarPath))) {
		t.Errorf("repair bytes = %d, want the sidecar's size", repair.Bytes)
	}
	if _, err := os.Stat(repair.Path); !os.IsNotExist(err) {
		t.Errorf("the sidecar is still there: stat err = %v", err)
	}
}

// TestConvergeSidecarsLeavesTheInfoFile pins the half of the pair this must not
// take. The -info file carries the client's identity and the fact that this
// database was uploaded as a fresh remote; removing it would make the next open a
// first open, and a first open against a remote applies the remote over the local
// file.
func TestConvergeSidecarsLeavesTheInfoFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "relevo.db")
	writeSidecar(t, readSidecar(t, tornSidecarPath))
	info := filepath.Join(dir, "relevo.db-info")
	const identity = `{"client_unique_id":"relevo-test"}`
	if err := os.WriteFile(info, []byte(identity), 0o600); err != nil {
		t.Fatalf("write the info file: %v", err)
	}

	if _, err := ConvergeSidecars(path); err != nil {
		t.Fatalf("ConvergeSidecars: %v", err)
	}
	got, err := os.ReadFile(info)
	if err != nil {
		t.Fatalf("the info file was taken: %v", err)
	}
	if string(got) != identity {
		t.Errorf("info file = %q, want it untouched", got)
	}
}

// TestConvergeSidecarsLeavesAWholeSidecar is the steady state, and it has to be
// free of writes: a machine syncing normally must pay two reads and nothing else.
func TestConvergeSidecarsLeavesAWholeSidecar(t *testing.T) {
	path := writeSidecar(t, wholeSidecar(t))
	before, err := os.Stat(path + sidecarSuffix)
	if err != nil {
		t.Fatalf("stat the sidecar: %v", err)
	}

	repair, err := ConvergeSidecars(path)
	if err != nil {
		t.Fatalf("ConvergeSidecars: %v", err)
	}
	if repair.Path != "" {
		t.Errorf("repair = %+v, want the zero repair", repair)
	}
	after, err := os.Stat(path + sidecarSuffix)
	if err != nil {
		t.Fatalf("the sidecar was removed: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the sidecar was rewritten: modtime %v, was %v", after.ModTime(), before.ModTime())
	}
}

// TestConvergeSidecarsOnNothingToConverge covers the two ordinary answers: a
// machine with no sidecar, and a path nothing was named for.
func TestConvergeSidecarsOnNothingToConverge(t *testing.T) {
	if repair, err := ConvergeSidecars(""); err != nil || repair.Path != "" {
		t.Errorf("ConvergeSidecars(\"\") = %+v, %v; want the zero repair", repair, err)
	}
	repair, err := ConvergeSidecars(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil || repair.Path != "" {
		t.Errorf("ConvergeSidecars on a bare path = %+v, %v; want the zero repair", repair, err)
	}
}

// TestOrphanAutoindexTableNamesTheTableBehindTheIndex is the rule over bytes it
// was written for: the stored name has no terminator, so the read has to know
// where the table stops and the index's own ordinal begins.
func TestOrphanAutoindexTableNamesTheTableBehindTheIndex(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want string
	}{
		{"plain", "sqlite_autoindex_event_1event", "event"},
		{"a table whose name holds an underscore", "sqlite_autoindex_turso_sync_last_change_id_1turso_sync_last_change_id", "turso_sync_last_change_id"},
		{"more than one index on the table", "sqlite_autoindex_event_2event", "event"},
		{"two digits read as the ordinal", "sqlite_autoindex_event_11event", "event"},
		{"a defined table is not an orphan", "CREATE TABLE event (a)sqlite_autoindex_event_1", ""},
		{"no index at all", "CREATE TABLE event (a)", ""},
		{"no ordinal means no automatic index", "sqlite_autoindex_a_b_c", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, torn := orphanAutoindexTable([]byte(tc.data))
			if want := tc.want != ""; torn != want {
				t.Fatalf("torn = %v, want %v (table %q)", torn, want, got)
			}
			if got != tc.want {
				t.Errorf("table = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDefinedTablesReadsTheStoredSQL pins the other half of the rule: the table
// set comes from the CREATE TABLE text the file stores, quoted names included,
// and a table named by a CREATE INDEX is not a table.
func TestDefinedTablesReadsTheStoredSQL(t *testing.T) {
	got := definedTables([]byte(
		"CREATE TABLE kv (key TEXT PRIMARY KEY)" +
			`CREATE TABLE "odd name" (a TEXT)` +
			"CREATE INDEX chains_origin_owner_name_uidx ON chains (origin, owner, name)" +
			"CREATE TABLE IF NOT EXISTS config_meta (id INTEGER)"))

	for _, want := range []string{"kv", "odd name", "config_meta"} {
		if !got[want] {
			t.Errorf("%q was not read as a table; got %v", want, keysOf(got))
		}
	}
	if got["chains"] {
		t.Errorf("an index made chains look like a table; got %v", keysOf(got))
	}
}

func keysOf(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return strings.Join(out, ",")
}

// writeSidecar puts data on disk as the sidecar of a database at path, and
// returns the database's path.
func writeSidecar(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo.db")
	if err := os.WriteFile(path+sidecarSuffix, data, 0o600); err != nil {
		t.Fatalf("write the sidecar: %v", err)
	}
	return path
}
