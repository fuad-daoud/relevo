package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// TestApplyInRefusesAnArrivingSyncKeyAndLeavesTheStoredSection pins that the
// sync section is refused inside the importer's transaction too, as a refusal
// and not a store error, with the stored section as it was.
func TestApplyInRefusesAnArrivingSyncKeyAndLeavesTheStoredSection(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedDocConfig(t, s)
	before, err := s.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}

	{
		const key = "sync"
		var refusal error
		err := s.db.Tx(func(tx *db.Tx) error {
			var applyErr error
			refusal, applyErr = s.Doc().ApplyIn(tx, key, json.RawMessage(`{"enabled":true,"remote_url":"libsql://peer.turso.io"}`), "peer")
			return applyErr
		})
		if err != nil {
			t.Fatalf("ApplyIn(%q) store error: %v", key, err)
		}
		if refusal == nil || !strings.Contains(refusal.Error(), "never syncs") {
			t.Errorf("ApplyIn(%q) refusal = %v, want the sync section refused", key, refusal)
		}
		if refusal != nil && !errors.Is(refusal, ErrInvalidValue) {
			t.Errorf("ApplyIn(%q) refusal = %v, want ErrInvalidValue", key, refusal)
		}
	}

	after, err := s.Current()
	if err != nil {
		t.Fatalf("Current after: %v", err)
	}
	if !bytes.Equal(after[Sync], before[Sync]) {
		t.Errorf("sync section = %s, want %s unchanged", after[Sync], before[Sync])
	}
}
