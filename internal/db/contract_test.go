package db

import (
	"bytes"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// update rewrites testdata/schema.golden.
var update = flag.Bool("update", false, "rewrite testdata/schema.golden")

// TestContractSchema pins C10: the database schema a fresh db.Open produces.
// sqlite_master's CREATE TABLE/INDEX statements are static text fixed by the
// embedded migrations, and SchemaVersions is appended as a plain line, since a
// fresh database's (have, know) are both host- and clock-independent.
func TestContractSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relevo.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	rows, err := d.sqlDB.Query(`SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var buf bytes.Buffer
	for rows.Next() {
		var typ, name, tblName string
		var sqlText sql.Null[string]
		if err := rows.Scan(&typ, &name, &tblName, &sqlText); err != nil {
			t.Fatalf("scan sqlite_master row: %v", err)
		}
		// An implicit index (a UNIQUE column's autoindex) has a NULL sql
		// column; every explicit CREATE TABLE/INDEX has its own text.
		fmt.Fprintf(&buf, "%s %s %s\n%s\n\n", typ, name, tblName, sqlText.V)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite_master: %v", err)
	}

	have, know := d.SchemaVersions()
	fmt.Fprintf(&buf, "schema_versions have=%d know=%d\n", have, know)

	path2 := filepath.Join("testdata", "schema.golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path2), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path2, buf.Bytes(), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path2, err)
		}
		return
	}

	want, err := os.ReadFile(path2)
	if err != nil {
		t.Fatalf("missing golden file %s: re-run with 'go test ./internal/db -run Contract -update' to generate", path2)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("golden mismatch in %s: re-run with 'go test ./internal/db -run Contract -update' to update\n--- got ---\n%s\n--- want ---\n%s",
			path2, buf.String(), string(want))
	}
}
