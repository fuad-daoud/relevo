//go:build unix

package db_test

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestDialedLiveScanReadsOverlaysOverTheWire pins the path short-lived clients
// actually use: overlays written through the direct handle's LocalOrSelf read
// back identically through the dialled handle's. A read that bypassed
// LocalOrSelf would answer from the shared file, which holds none of these
// keys.
func TestDialedLiveScanReadsOverlaysOverTheWire(t *testing.T) {
	p := serveSplit(t)
	seed := map[string]string{
		"live/01WIREA": `{"stream_offset":11}`,
		"live/01WIREB": `{"stream_offset":22}`,
		"live0probe":   `{"stream_offset":4242}`,
		"ledger":       `{"entries":[]}`,
	}
	for key, value := range seed {
		if err := p.shared.LocalOrSelf().KVPut(key, []byte(value)); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}

	dialled, err := db.Dial(p.sock)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = dialled.Close() })

	got, err := dialled.LocalOrSelf().KVScanLive()
	if err != nil {
		t.Fatalf("KVScanLive over the wire: %v", err)
	}
	want := map[string]string{
		"live/01WIREA": `{"stream_offset":11}`,
		"live/01WIREB": `{"stream_offset":22}`,
	}
	if len(got) != len(want) {
		t.Fatalf("keys over the wire = %q, want %q", got, want)
	}
	for key, value := range want {
		if string(got[key]) != value {
			t.Errorf("key %s over the wire = %q, want %q", key, got[key], value)
		}
	}
}
