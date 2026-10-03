package main

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/proc"
)

// TestNewRuntimeAlwaysWiresTheTransport pins that a runtime built with no
// servers still carries a transport: the catch-up path needs one the moment a
// server is added while the daemon runs, and an empty servers section still
// means no remote client.
func TestNewRuntimeAlwaysWiresTheTransport(t *testing.T) {
	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	if rt.Transport == nil {
		t.Error("Transport = nil, want a transport regardless of the servers section")
	}
	if rt.Remote != nil {
		t.Error("Remote != nil, want no client with no servers configured")
	}
}

// TestNewRuntimeGivesBuildersTheStateTmpDir pins that the daemon's process
// runner names the state root's own temp dir, so a round's temporary files land
// there instead of the system temp the daemon inherited.
func TestNewRuntimeGivesBuildersTheStateTmpDir(t *testing.T) {
	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	r, ok := rt.Runner.(*proc.Runner)
	if !ok {
		t.Fatalf("Runner = %T, want *proc.Runner", rt.Runner)
	}
	if !strings.HasSuffix(r.TmpDir, "/tmp") || strings.Count(r.TmpDir, "/") < 2 {
		t.Errorf("Runner.TmpDir = %q, want the state root's own tmp directory", r.TmpDir)
	}
}
