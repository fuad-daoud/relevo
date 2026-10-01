package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// seedReadVerbStore builds a binding with one completed round: a plan entry
// and a report entry in its log, and a report file on disk. It is store-only
// -- a t.TempDir(), no harness and no network -- because the point of these
// tests is that the two shared read helpers print what the client verbs print
// while leaving the owner's state untouched.
func seedReadVerbStore(t *testing.T) (*store.Store, relevo.Runtime, []store.LogEntry) {
	t.Helper()

	s := store.New(t.TempDir())
	if err := s.Save(store.Binding{
		Name: "api", CWD: t.TempDir(), Round: 2, State: store.StateActive,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, e := range []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true, Note: "round one"},
	} {
		if err := s.AppendLog("api", e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	if err := os.MkdirAll(s.OutDir("api"), 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	if err := os.WriteFile(s.ReportPath("api", 1), []byte("# report body\n"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}

	log, err := s.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	return s, relevo.Runtime{Store: s, Now: time.Now}, log
}

// assertIngestMirrorEmpty asserts that the database at path holds no ingest
// mirror rows: binding, event and round are all empty. A database that does
// not exist passes too, which is what makes this a port of the old "must not
// create a database" stat: relevo.db is now the store's own record file, which
// the fixture's Save creates, and these read verbs must still write nothing
// into the ingest mirror (P3a round 3, B3).
func assertIngestMirrorEmpty(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		t.Fatalf("stat %s: %v", path, err)
	}
	d, err := openDB(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer d.Close()

	stats, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	for _, tbl := range []string{"binding", "event", "round"} {
		if n := stats.Rows[tbl]; n != 0 {
			t.Errorf("ingest mirror %s has %d rows, want 0", tbl, n)
		}
	}
}

// TestPrintLogAndPrintShowWriteNothing is `relevo serve log`/`show`'s
// read-only contract (#216) at the helper level: with markViewed=false the
// same text a client verb prints comes out, no viewed mark appears, and
// printShow with allowDB=false creates no database. markViewed=true is the
// control that proves the assertion can fail.
func TestPrintLogAndPrintShowWriteNothing(t *testing.T) {
	s, rt, log := seedReadVerbStore(t)

	stdout, _, err := captureOutput(t, func() error {
		return printLog(rt, "api", 0, 0, false, false, false)
	})
	if err != nil {
		t.Fatalf("printLog: %v", err)
	}
	for _, e := range log {
		if line := relevo.LogLine(e); !strings.Contains(string(stdout), line) {
			t.Errorf("printLog output:\n%s\nlacks %q", stdout, line)
		}
	}
	if _, ok := s.ViewedAt("api"); ok {
		t.Error("printLog with markViewed=false must not stamp .viewed")
	}

	stdout, stderr, err := captureOutput(t, func() error {
		return printShow(rt, relevo.ShowOptions{Name: "api", Section: relevo.ShowReport}, false, false, "alice/")
	})
	if err != nil {
		t.Fatalf("printShow: %v", err)
	}
	if !strings.Contains(string(stdout), "# report body") {
		t.Errorf("printShow stdout = %q, want the report text", stdout)
	}
	if !strings.Contains(string(stderr), "alice/api round 1 of 1 · report") {
		t.Errorf("printShow stderr = %q, want the owner-prefixed header", stderr)
	}
	if _, ok := s.ViewedAt("api"); ok {
		t.Error("printShow with markViewed=false must not stamp .viewed")
	}
	assertIngestMirrorEmpty(t, s.DBPath())

	// Control: markViewed=true does stamp, so the assertion above is not
	// passing for want of a sidecar either way.
	if _, _, err := captureOutput(t, func() error {
		return printLog(rt, "api", 0, 0, false, false, true)
	}); err != nil {
		t.Fatalf("printLog control: %v", err)
	}
	if _, ok := s.ViewedAt("api"); !ok {
		t.Error("printLog with markViewed=true must stamp .viewed (the control)")
	}
}

// TestPrintShowAllowDBFalseRefusesAnUnknownName pins the other half of
// allowDB=false: a binding that is not live is store.ErrNotFound, not a
// database read that would create relevo.db as a side effect.
func TestPrintShowAllowDBFalseRefusesAnUnknownName(t *testing.T) {
	s, rt, _ := seedReadVerbStore(t)

	err := printShow(rt, relevo.ShowOptions{Name: "nosuch", Section: relevo.ShowReport}, false, false, "")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("printShow(allowDB=false) on an unknown name = %v, want store.ErrNotFound", err)
	}
	assertIngestMirrorEmpty(t, s.DBPath())
}
