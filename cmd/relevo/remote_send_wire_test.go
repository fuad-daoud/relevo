package main

import (
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestBuildRuntimeWiresRemoteSend pins that the shared runtime builder fills
// the remote-send hook: a runtime with no hook fails a remote send loudly
// instead of skipping the server, so the wiring must never leave it empty.
func TestBuildRuntimeWiresRemoteSend(t *testing.T) {
	root := t.TempDir()
	d, err := db.OpenSplit(filepath.Join(root, "relevo.db"), db.Options{Origin: "01ORIGIN"})
	if err != nil {
		t.Fatalf("OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	rt, err := newRuntimeOn(root, d)
	if err != nil {
		t.Fatalf("newRuntimeOn: %v", err)
	}
	if rt.RemoteSend.Send == nil || rt.RemoteSend.Ship == nil || rt.RemoteSend.Record == nil {
		t.Fatal("buildRuntime left the RemoteSend hook empty")
	}
}
