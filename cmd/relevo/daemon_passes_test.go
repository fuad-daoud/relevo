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
