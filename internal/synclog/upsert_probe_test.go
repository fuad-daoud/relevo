package synclog

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The importer has exactly one safe way to write a row it already holds, and
// this pins that the engine running this build has it. An upsert a row already
// exists must change is `INSERT ... ON CONFLICT(pk) DO UPDATE`; the only other
// spelling SQLite offers is `INSERT OR REPLACE`, which deletes the row first and
// so cascades to every child of binding_record, round_file and chain_*.
//
// The probe runs on a real file rather than in memory because the answer that
// matters is the one the shipping engine gives: this table and this statement
// are the ones the importer's write path is built on, and an engine that refuses
// them cannot import a row whose parent it has already applied.
func TestLogRequiresOnConflictUpsert(t *testing.T) {
	t.Parallel()
	_, path := exporterFile(t, "m1")

	const scratch = `CREATE TABLE synclog_probe (
		id TEXT PRIMARY KEY,
		label TEXT NOT NULL
	)`
	seed(t, path, scratch, `INSERT INTO synclog_probe (id, label) VALUES ('a', 'first')`)

	raw, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("open the file: %v", err)
	}
	defer func() { _ = raw.Close() }()

	const upsert = `INSERT INTO synclog_probe (id, label) VALUES ('a', 'second')
		ON CONFLICT(id) DO UPDATE SET label = excluded.label`
	if _, err := raw.Exec(upsert); err != nil {
		t.Fatalf("the engine cannot write the one safe upsert: %v", err)
	}

	var label string
	if err := raw.QueryRow(`SELECT label FROM synclog_probe WHERE id = 'a'`).Scan(&label); err != nil {
		t.Fatalf("read the probe row back: %v", err)
	}
	if label != "second" {
		t.Fatalf("label = %q, want the update to have written the row in place", label)
	}
	var rows int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM synclog_probe`).Scan(&rows); err != nil {
		t.Fatalf("count the probe rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("the probe table holds %d rows, want the upsert to have updated rather than replaced", rows)
	}
}
