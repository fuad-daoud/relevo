package main

import "testing"

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
