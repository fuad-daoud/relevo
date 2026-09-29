package store

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

// TestArchiveSealsAndFreesTheName pins the archive: round files sealed into
// round_file, the directory gone, and the name free to bind again.
func TestArchiveSealsAndFreesTheName(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Save(newBinding("webshop", "/repo")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entry := LogEntry{TS: time.Unix(1, 0).UTC(), Round: 1, Direction: DirToBuilder, Kind: KindPrompt, Confirmed: true}
	if err := s.AppendLog("webshop", entry); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if err := os.WriteFile(s.PromptPath("webshop", 1), []byte("round 1 plan\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	dest, err := s.Archive("webshop")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if dest != "" {
		t.Errorf("Archive path = %q, want \"\" (nothing is tarred)", dest)
	}
	if _, err := s.Load("webshop"); !errors.Is(err, ErrNotFound) {
		t.Errorf("archived binding must be gone from the live set, got %v", err)
	}
	if _, err := os.Stat(s.Dir("webshop")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the binding directory must be gone after an archive, got %v", err)
	}

	archived, err := s.ListArchived()
	if err != nil {
		t.Fatalf("ListArchived: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, want exactly one", archived)
	}
	a := archived[0]
	if a.Binding.Name != "webshop" || a.RecordID == "" {
		t.Errorf("ArchivedBinding = %+v, want webshop with a record id", a)
	}
	if a.ArchivedAt.IsZero() {
		t.Error("ArchivedAt is zero, want the archive stamp")
	}

	events, err := s.ArchivedLog(a.RecordID)
	if err != nil {
		t.Fatalf("ArchivedLog: %v", err)
	}
	if len(events) != 1 || events[0].Kind != KindPrompt || events[0].Seq != 1 {
		t.Errorf("ArchivedLog = %+v, want the one plan entry", events)
	}

	// The round file survives as a sealed row, readable on the old path.
	body, err := s.ReadFile(s.PromptPath("webshop", 1))
	if err != nil || string(body) != "round 1 plan\n" {
		t.Errorf("ReadFile(plan) = %q, %v; want the sealed plan", body, err)
	}

	if err := s.Save(newBinding("webshop", "/repo")); err != nil {
		t.Errorf("archiving must free the name and the working tree: %v", err)
	}
}

// TestReadFileResolvesTheMostRecentlyArchivedRecord pins the archived half of
// sealedLookup, and ListArchived's oldest-first order.
func TestReadFileResolvesTheMostRecentlyArchivedRecord(t *testing.T) {
	s := New(t.TempDir())
	for _, round := range []string{"first", "second"} {
		if err := s.Save(newBinding("webshop", "/repo")); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if err := os.WriteFile(s.PromptPath("webshop", 1), []byte(round+"\n"), 0o644); err != nil {
			t.Fatalf("write plan: %v", err)
		}
		if _, err := s.Archive("webshop"); err != nil {
			t.Fatalf("Archive: %v", err)
		}
	}

	body, err := s.ReadFile(s.PromptPath("webshop", 1))
	if err != nil || string(body) != "second\n" {
		t.Errorf("ReadFile = %q, %v; want the newest archived record's plan", body, err)
	}

	archived, err := s.ListArchived()
	if err != nil || len(archived) != 2 {
		t.Fatalf("ListArchived = %+v, %v; want both records", archived, err)
	}
	if !archived[0].ArchivedAt.Before(archived[1].ArchivedAt) && !archived[0].ArchivedAt.Equal(archived[1].ArchivedAt) {
		t.Errorf("ListArchived is not oldest-first: %v then %v", archived[0].ArchivedAt, archived[1].ArchivedAt)
	}
}

// TestArchivedLogDecodesEntries pins ArchivedLog against a stored record:
// entry_json is authoritative and Seq, Confirmed, DeliveredAt and Route come
// from the promoted columns.
func TestArchivedLogDecodesEntries(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/repo")
	delivered := time.Date(2026, 9, 10, 10, 2, 0, 0, time.UTC)
	e1 := LogEntry{TS: time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC), Round: 1, Direction: DirToBuilder, Kind: KindPrompt, Confirmed: true}
	e2 := LogEntry{
		TS: time.Date(2026, 9, 10, 10, 1, 0, 0, time.UTC), Round: 1, Direction: DirToMasterMind,
		Kind: KindReport, Confirmed: true, DeliveredAt: &delivered, Route: "channel",
	}
	if err := s.WithLock(func(tx *Tx) error { return tx.SaveWithLog(b, e1, e2) }); err != nil {
		t.Fatalf("SaveWithLog: %v", err)
	}
	if _, err := s.Archive("webshop"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	archived, err := s.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v", archived, err)
	}
	entries, err := s.ArchivedLog(archived[0].RecordID)
	if err != nil {
		t.Fatalf("ArchivedLog: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].Seq != 1 || entries[1].Seq != 2 {
		t.Errorf("seqs = %d, %d; want 1, 2", entries[0].Seq, entries[1].Seq)
	}
	if !entries[1].Confirmed || entries[1].Route != "channel" || entries[1].DeliveredAt == nil {
		t.Errorf("entries[1] = %+v, want the promoted confirm columns", entries[1])
	}

	// The raw JSON survives verbatim.
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	events, err := d.EventsOf(archived[0].RecordID, 0)
	if err != nil || len(events) != 2 {
		t.Fatalf("EventsOf = %+v, %v", events, err)
	}
	var decoded LogEntry
	if err := json.Unmarshal([]byte(events[1].JSON), &decoded); err != nil {
		t.Fatalf("entry_json is not the stored entry: %v", err)
	}
	if decoded.Kind != KindReport {
		t.Errorf("entry_json kind = %q, want report", decoded.Kind)
	}
}
