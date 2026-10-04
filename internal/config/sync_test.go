package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// The bodies the sync section tests store and refuse.
const (
	syncStored = `{"enabled":true,"remote_url":"libsql://relevo-example.turso.io","namespace":"relevo","idle_seconds":300}`
)

func TestSyncSectionValidation(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	if _, err := s.Put(Sync, []byte(syncStored)); err != nil {
		t.Fatalf("Put(sync): %v", err)
	}
	body, ok, err := s.Body(Sync)
	if err != nil {
		t.Fatalf("Body(sync): %v", err)
	}
	if !ok || string(body) != syncStored {
		t.Fatalf("Body(sync) = %q, %v; want the stored body", body, ok)
	}

	for _, bad := range []string{
		`{"enabled":"yes"}`,
		`{"enabled":true`,
		`{"enable":true}`,
		`{"idle_seconds":-1}`,
		`{"backlog_threshold":-1}`,
		`{"remote_url":"ftp://relevo-example.turso.io"}`,
		`{"remote_url":"relevo-example.turso.io"}`,
		`{"namespace":"two/segments"}`,
		`{} {"enabled":true}`,
	} {
		if _, err := s.Put(Sync, []byte(bad)); err == nil {
			t.Errorf("Put(sync, %s) = nil, want a refusal", bad)
		}
	}

	body, ok, err = s.Body(Sync)
	if err != nil {
		t.Fatalf("Body(sync): %v", err)
	}
	if !ok || string(body) != syncStored {
		t.Fatalf("a refused body was written: %q, %v", body, ok)
	}
}

func TestSyncSectionDelete(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	if _, err := s.Put(Sync, []byte(syncStored)); err != nil {
		t.Fatalf("Put(sync): %v", err)
	}
	if err := s.Delete(Sync); err != nil {
		t.Fatalf("Delete(sync): %v", err)
	}
	if ok, err := s.Has(Sync); err != nil {
		t.Fatalf("Has(sync): %v", err)
	} else if ok {
		t.Fatal("Has(sync) = true after Delete")
	}
	if _, err := s.Put(Sync, []byte(`{"enabled":false}`)); err != nil {
		t.Fatalf("Put(sync) after Delete: %v", err)
	}
	if ok, err := s.Has(Sync); err != nil {
		t.Fatalf("Has(sync): %v", err)
	} else if !ok {
		t.Fatal("Has(sync) = false after a second Put")
	}
}

// TestUnknownSectionWritesNothing pins that a name outside the registry is
// refused by every entry point that takes a document, and that the refusal
// leaves the stored sections exactly as they were.
func TestUnknownSectionWritesNothing(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	if _, err := s.Put(Candidates, []byte(placementCandidates)); err != nil {
		t.Fatalf("Put(candidates): %v", err)
	}
	before, err := s.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}

	if _, err := s.Put(Section("synk"), []byte(`{}`)); err == nil {
		t.Error("Put(synk) = nil, want a refusal")
	} else if !strings.Contains(err.Error(), "unknown config section") {
		t.Errorf("Put(synk) error = %v, want it to name the unknown section", err)
	}

	doc := map[Section]json.RawMessage{Section("synk"): json.RawMessage(`{}`)}
	if _, err := s.PutDoc(doc); err == nil {
		t.Error("PutDoc with an unknown section = nil, want a refusal")
	}

	after, err := s.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("stored sections = %v, want the %d before the refusal", after, len(before))
	}
	if _, ok := after[Section("synk")]; ok {
		t.Error("the refused section was stored")
	}
}
