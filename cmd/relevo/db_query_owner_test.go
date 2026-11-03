//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// TestDBQueryReadsThroughTheOwnerWhenItIsUp pins the route: when an owner is
// already serving the state root's socket, the query reaches it rather than
// opening the file itself. The counting listener is what proves the difference;
// both routes read the same rows.
func TestDBQueryReadsThroughTheOwnerWhenItIsUp(t *testing.T) {
	stateHome := seedQueryRootAt(t, shortStateRoot(t))
	served := startTestOwner(t, stateHome)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT label FROM probe ORDER BY n`, "--json"})
	})
	if err != nil {
		t.Fatalf("db query through the owner: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(string(stdout), `"one"`) || !strings.Contains(string(stdout), `"two"`) {
		t.Errorf("stdout = %q, want the owner's two rows", stdout)
	}
	if got := atomic.LoadInt32(&served.ln.accepts); got == 0 {
		t.Error("the query read no connection through the owner")
	}
}

// TestDBQueryByteCapStopsTheOwnerStream pins the byte cap through the owner:
// the same over-budget value that stops the direct read stops the owner stream
// too, so nothing larger is ever sent.
func TestDBQueryByteCapStopsTheOwnerStream(t *testing.T) {
	stateHome := seedQueryRootAt(t, shortStateRoot(t))
	startTestOwner(t, stateHome)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"db", "query", `SELECT zeroblob(1000000) AS b`, "--max-bytes", "1024"})
	})
	if err != nil {
		t.Fatalf("db query --max-bytes through the owner: %v (stderr: %s)", err, stderr)
	}
	if strings.Contains(string(stdout), "<blob") {
		t.Errorf("stdout printed an over-budget value:\n%s", stdout)
	}
	if !strings.Contains(string(stderr), "truncated at 1024 bytes (raise --max-bytes)") {
		t.Errorf("stderr = %q, want the byte truncation note", stderr)
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
