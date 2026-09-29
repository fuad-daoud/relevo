// Package dbtest builds one migrated database template per test binary and
// points db.Open at it, so fresh test databases are seeded from a copy instead
// of running the migrations.
package dbtest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Install migrates a template database in a fresh temp directory and points
// db.Open at it. cleanup clears the template and removes the directory so a
// test binary leaves nothing behind.
func Install() (cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "relevo-dbtest-")
	if err != nil {
		return nil, err
	}
	cleanup = func() {
		db.SetFreshTemplate("")
		_ = os.RemoveAll(dir)
	}

	tpl := filepath.Join(dir, "template.db")
	d, err := db.Open(tpl)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := d.Close(); err != nil {
		cleanup()
		return nil, err
	}
	// A copy of the main file without the template's WAL would miss the schema,
	// so a -wal sibling left with content is a failure, not a warning.
	if fi, serr := os.Stat(tpl + "-wal"); serr == nil && fi.Size() > 0 {
		cleanup()
		return nil, fmt.Errorf("dbtest: %s has a non-empty -wal sibling (%d bytes)", tpl, fi.Size())
	}

	db.SetFreshTemplate(tpl)
	return cleanup, nil
}

// Main installs the template, runs the tests, and clears the template before
// returning their exit code. A binary's TestMain is
// os.Exit(dbtest.Main(m)).
func Main(m *testing.M) int {
	cleanup, err := Install()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	code := m.Run()
	cleanup()
	return code
}
