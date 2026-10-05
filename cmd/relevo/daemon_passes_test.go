package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// daemonPassNow is the passes' stamp in the startup fixture: a fixed time so a
// backup file name is the same on every run.
var daemonPassNow = time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)

// openDaemonPassSplit opens the shared file and its machine-local companion the
// way a daemon start does, so the passes under test hold a real handle on both.
func openDaemonPassSplit(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestDaemonStartupRunsTheLocalSplit pins that a daemon start moves the
// machine-local rows out of the shared file, which is the fix the
// shared-secrets refusal names and the only thing that can make it. The shared
// file is what that refusal reads and what an upload would carry, so the
// assertion is that the secrets are gone from it and present in the local file
// -- not that the pass reported having run.
func TestDaemonStartupRunsTheLocalSplit(t *testing.T) {
	d := openDaemonPassSplit(t)

	for _, name := range []string{"client.key", "serve.tls.cert"} {
		if err := d.Tx(func(t *db.Tx) error { return t.SecretPut(name, []byte(name), daemonPassNow) }); err != nil {
			t.Fatalf("SecretPut %s: %v", name, err)
		}
	}
	before, err := d.SecretNames()
	if err != nil {
		t.Fatalf("SecretNames before the passes: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("the shared file holds %v before the passes, want both secrets", before)
	}

	backups := t.TempDir()
	daemonEnablePath(d, backups, "01ORIGIN", daemonPassNow)

	after, err := d.SecretNames()
	if err != nil {
		t.Fatalf("SecretNames after the passes: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("a daemon start left %v in the shared file", after)
	}
	local := d.Local()
	if local == nil {
		t.Fatal("Local() = nil, want the machine-local file the pass moved rows into")
	}
	localNames, err := local.SecretNames()
	if err != nil {
		t.Fatalf("SecretNames on the local file: %v", err)
	}
	if len(localNames) != 2 {
		t.Errorf("the local file holds %v, want both secrets moved out of the shared one", localNames)
	}

	// The split backs the shared file up before it moves anything, so a start
	// that moved rows must have left the pre-split copy behind.
	if path := findPreSplitBackup(t, backups); path == "" {
		t.Errorf("the pass moved rows out of the shared file but wrote no backup under %s", backups)
	}

	// A second start is a no-op: neither pass backs up again nor moves again.
	second := t.TempDir()
	daemonEnablePath(d, second, "01ORIGIN", daemonPassNow.Add(time.Hour))
	if path := findPreSplitBackup(t, second); path != "" {
		t.Errorf("a second start backed the shared file up again: %s", path)
	}
	if names, err := local.SecretNames(); err != nil || len(names) != 2 {
		t.Errorf("the local file after a second start = (%v, %v), want both secrets unchanged", names, err)
	}
}

// TestDaemonStartupConvergesRowsWrittenAfterTheSplit pins the startup wiring
// for the converge pass. Another installation sharing the database can write a
// machine-local row into the shared file after the split has run, and the
// readers on this machine are bound to the local file; so a later start has to
// pick that row up rather than trust a marker written before it existed. The
// marker itself is not re-stamped, so a start that converges twice leaves one
// marker and the second start's own backup to show for it.
func TestDaemonStartupConvergesRowsWrittenAfterTheSplit(t *testing.T) {
	d := openDaemonPassSplit(t)
	seed := []struct {
		name, value, key, body string
	}{
		{"serve.tls.cert", "certificate-from-another-installation", "serve.daemon", `{"pid":1}`},
		{"client.key", "pem-from-another-installation", "serve.clients", `["stray"]`},
	}
	for _, s := range seed {
		if err := d.Tx(func(tx *db.Tx) error { return tx.SecretPut(s.name, []byte(s.value), daemonPassNow) }); err != nil {
			t.Fatalf("seed secret %s: %v", s.name, err)
		}
		if err := d.KVPut(s.key, []byte(s.body)); err != nil {
			t.Fatalf("seed kv %s: %v", s.key, err)
		}
	}

	backups := t.TempDir()
	daemonEnablePath(d, backups, "01ORIGIN", daemonPassNow)
	// The strays are gone, so the next one written lands in a pair whose marker
	// is already there. That is the case the startup wiring has to reach: a
	// second start, not the first.
	for _, s := range seed {
		if err := d.Tx(func(tx *db.Tx) error { return tx.SecretPut(s.name, []byte(s.value), daemonPassNow) }); err != nil {
			t.Fatalf("seed the later secret %s: %v", s.name, err)
		}
		if err := d.KVPut(s.key, []byte(s.body)); err != nil {
			t.Fatalf("seed the later kv %s: %v", s.key, err)
		}
	}

	// The converge pass backs the shared file up before it moves anything, and
	// the backup name carries the start's second, so the second start gets its
	// own.
	daemonEnablePath(d, backups, "01ORIGIN", daemonPassNow.Add(time.Hour))

	for _, s := range seed {
		if _, ok, err := d.SecretGet(s.name); err != nil {
			t.Fatalf("SecretGet(%s) on the shared handle: %v", s.name, err)
		} else if ok {
			t.Errorf("the stray secret %s is still readable on the shared file", s.name)
		}
		if _, ok, err := d.KVGet(s.key); err != nil {
			t.Fatalf("KVGet(%s) on the shared handle: %v", s.key, err)
		} else if ok {
			t.Errorf("the stray key %s is still readable on the shared file", s.key)
		}
		value, ok, err := d.Local().SecretGet(s.name)
		if err != nil || !ok {
			t.Errorf("the local file's %s = %q, %t, %v; want the stray row", s.name, value, ok, err)
		}
	}

	// A third start has nothing left to converge, so it takes no second backup
	// for this pass and leaves the marker exactly where the first start put it.
	before, ok, err := d.Local().KVGet("split-local.v1")
	if err != nil || !ok {
		t.Fatalf("read the split marker: %v (present %t)", err, ok)
	}
	third := t.TempDir()
	daemonEnablePath(d, third, "01ORIGIN", daemonPassNow.Add(2*time.Hour))
	if path := findPreSplitBackup(t, third); path != "" {
		t.Errorf("a start with nothing to converge backed the shared file up: %s", path)
	}
	after, _, err := d.Local().KVGet("split-local.v1")
	if err != nil {
		t.Fatalf("re-read the split marker: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("the marker changed on a later start:\nbefore %s\nafter  %s", before, after)
	}
}

// TestDaemonStartupStampsOriginsToo pins the other half of the same startup
// path: the passes beside the split stamp this installation's origin on the rows
// an older database wrote, so the origin gate has nothing left to refuse on.
func TestDaemonStartupStampsOriginsToo(t *testing.T) {
	d := openDaemonPassSplit(t)

	if _, err := d.RecordPut(db.Record{Name: "api", State: "active", Round: 1, CWD: "/work/api"}); err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	counts, err := db.CountEmptyOrigins(d)
	if err != nil {
		t.Fatalf("CountEmptyOrigins before the passes: %v", err)
	}
	if counts.Empty() {
		t.Fatal("the gate passes on a database whose rows carry no origin")
	}

	daemonEnablePath(d, t.TempDir(), "01ORIGIN", daemonPassNow)

	stamped, err := db.CountEmptyOrigins(d)
	if err != nil {
		t.Fatalf("CountEmptyOrigins after the passes: %v", err)
	}
	if !stamped.Empty() {
		t.Errorf("a daemon start left %s unstamped", stamped)
	}
}

// findPreSplitBackup names the pre-split copy a pass wrote under dir, or "" when
// it wrote none. The pass names that file itself, so the test looks for its name
// rather than inventing a second one.
func findPreSplitBackup(t *testing.T, dir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "relevo.db.pre-split-*"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}
