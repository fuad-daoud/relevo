package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// `db query` is the one reader that must keep reading the shared file. It is
// the supported replacement for `sqlite3 relevo.db`, so its whole value is that
// it shows what is actually in that file -- including, on a machine that has run
// the split, the fact that the secrets and the machine-local rows are not there.
// A query that quietly followed the split to the local file would answer "this
// machine has no candidates" on a machine with eleven of them.

// seedQueryPair opens the split pair `db query` reads and writes one row into
// each file under a key that is on the shared side of the classification, so
// the query can tell the two apart.
func seedQueryPair(t *testing.T) string {
	t.Helper()
	seedQueryRoot(t)

	path := machineDBPath()
	d, err := db.OpenSplit(path, db.Options{})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	defer func() { _ = d.Close() }()

	if err := d.KVPut("record.of.the.shared.file", []byte(`"history"`)); err != nil {
		t.Fatalf("seed the shared file: %v", err)
	}
	if err := d.Local().KVPut("serve.daemon", []byte(`{"pid":4242}`)); err != nil {
		t.Fatalf("seed the local file: %v", err)
	}
	return path
}

// TestDBQueryReadsTheSharedFileNotTheLocalOne pins the whole contract on one
// query: the row the shared file holds comes back, and the row only the local
// file holds does not.
func TestDBQueryReadsTheSharedFileNotTheLocalOne(t *testing.T) {
	seedQueryPair(t)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT key FROM kv ORDER BY key`, "--json"})
	})
	if err != nil {
		t.Fatalf("db query: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), "record.of.the.shared.file") {
		t.Errorf("the shared file's row is missing from the answer:\n%s", stdout)
	}
	if strings.Contains(string(stdout), "serve.daemon") {
		t.Errorf("the answer carries a row that only the local file holds:\n%s", stdout)
	}
}

// TestDBQueryShowsTheSharedSecretsAreGone pins the reading that matters on a
// machine that has run the split: the secrets table in the shared file is
// empty, and the query says so rather than answering from the local file where
// they are.
func TestDBQueryShowsTheSharedSecretsAreGone(t *testing.T) {
	path := seedQueryPair(t)

	// Put a secret in the local file, which is where it belongs after the split.
	d, err := db.OpenSplit(path, db.Options{})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	if err := d.Local().Tx(func(tx *db.Tx) error {
		return tx.SecretPut("typesafe", []byte("token"), time.Now().UTC())
	}); err != nil {
		t.Fatalf("seed the local file's secret: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT name FROM secret ORDER BY name`, "--json"})
	})
	if err != nil {
		t.Fatalf("db query: %v (stderr: %s)", err, stderr)
	}
	if strings.Contains(string(stdout), "typesafe") {
		t.Errorf("the query answered with the local file's secret:\n%s", stdout)
	}
}

// TestDBQueryOnAnUnsplitDatabaseStillReadsIt pins the other direction: a machine
// whose split never ran has no local file, and its rows are all in the shared
// one, so the query finds them there.
func TestDBQueryOnAnUnsplitDatabaseStillReadsIt(t *testing.T) {
	seedQueryRoot(t)

	path := machineDBPath()
	// db.Open, not a raw pool: the kv table comes from the migrations.
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := d.KVPut("record.of.the.shared.file", []byte(`"history"`)); err != nil {
		t.Fatalf("seed kv: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, serr := os.Stat(db.SplitPath(path)); !os.IsNotExist(serr) {
		t.Fatalf("the fixture has a local file (err %v), so this is not the unsplit case", serr)
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT key FROM kv ORDER BY key`, "--json"})
	})
	if err != nil {
		t.Fatalf("db query: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), "record.of.the.shared.file") {
		t.Errorf("the shared file's row is missing on an unsplit database:\n%s", stdout)
	}
}

// TestTheQueryOpenCarriesNoLocalRouting pins the handle itself. The query reads
// whatever it is handed through d, so a handle whose own accessor was pointed at
// the local file would answer every statement from it.
func TestTheQueryOpenCarriesNoLocalRouting(t *testing.T) {
	seedQueryPair(t)

	ctx := t.Context()
	d, err := openDBQuery(ctx)
	if err != nil {
		t.Fatalf("openDBQuery: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if d.Path() != machineDBPath() {
		t.Errorf("the query handle is open on %s, want the shared file %s", d.Path(), machineDBPath())
	}
	// The count is read out of the row rather than by counting rows: a COUNT
	// query answers with exactly one row whatever it says.
	var local int64 = -1
	if err := d.QueryReadOnly(ctx, `SELECT COUNT(*) FROM kv WHERE key = 'serve.daemon'`, func(_ []string, values []any) error {
		n, ok := values[0].(int64)
		if !ok {
			t.Fatalf("the count column is %T, want int64", values[0])
		}
		local = n
		return nil
	}); err != nil {
		t.Fatalf("count the local file's row through the query handle: %v", err)
	}
	if local != 0 {
		t.Errorf("the query handle counted %d rows the local file alone holds", local)
	}
}

// TestTheQueryOverTheOwnerReadsTheSharedFile pins the same rule on the other
// open path: when the daemon holds the file, the query dials the owner, and a
// dialled handle carries a local handle of its own. The query must still read
// the shared pool, or `db query` would report a different database depending on
// whether a daemon happened to be running.
func TestTheQueryOverTheOwnerReadsTheSharedFile(t *testing.T) {
	stateHome := shortStateRoot(t)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(stateHome, "config"))

	served := bindTestOwner(t, stateHome)
	served.serve(t)
	if err := served.db.KVPut("record.of.the.shared.file", []byte(`"history"`)); err != nil {
		t.Fatalf("seed the shared file: %v", err)
	}
	if err := served.db.Local().KVPut("serve.daemon", []byte(`{"pid":4242}`)); err != nil {
		t.Fatalf("seed the local file: %v", err)
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT key FROM kv ORDER BY key`, "--json"})
	})
	if err != nil {
		t.Fatalf("db query over the owner: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), "record.of.the.shared.file") {
		t.Errorf("the shared file's row is missing from the answer:\n%s", stdout)
	}
	if strings.Contains(string(stdout), "serve.daemon") {
		t.Errorf("the answer over the owner carries a row only the local file holds:\n%s", stdout)
	}
}
