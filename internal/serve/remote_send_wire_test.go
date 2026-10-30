package serve

import (
	"path/filepath"
	"testing"
	"time"
)

// TestRuntimeAtWiresRemoteSend pins that the served runtime fills the
// remote-send hook: a served remote send runs the same uplink functions as
// the CLI runtime, never an empty hook.
func TestRuntimeAtWiresRemoteSend(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{DB: testServeDB(t), Root: root, Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rt := s.runtimeAt(filepath.Join(root, "bindings", "aaa"))
	if rt.RemoteSend.Send == nil || rt.RemoteSend.Ship == nil || rt.RemoteSend.Record == nil {
		t.Fatal("runtimeAt left the RemoteSend hook empty")
	}
}
