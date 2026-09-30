package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// This file holds the delivery-state half of the round log: the admit marker a
// push route writes, and the scans that read it. They live beside log.go rather
// than in it only to keep both files under the size ceiling; the API the rest
// of relevo calls is declared in log.go next to its confirm and pending
// siblings.

// claimableForMasterMind is pendingForMasterMind's claimable sibling: the same
// oldest-first scan, but it also skips an entry a push route already admitted.
// An admitted entry is in that route's own queue, so a reader that claimed it
// would present a payload the user is about to read twice, and would confirm an
// entry the session has not taken.
func (s *Store) claimableForMasterMind(name string) (LogEntry, int, bool, error) {
	entries, err := s.readLog(name)
	if err != nil {
		return LogEntry{}, 0, false, err
	}

	for i, e := range entries {
		if e.Direction == DirToMasterMind && !e.Confirmed && e.AdmittedAt == nil {
			return e, i, true, nil
		}
	}

	return LogEntry{}, 0, false, nil
}

// claimableForMasterMindThrough is claimableForMasterMind's through-round
// sibling.
func (s *Store) claimableForMasterMindThrough(name string, round int) ([]PendingEntry, error) {
	entries, err := s.readLog(name)
	if err != nil {
		return nil, err
	}

	var pending []PendingEntry
	for i, e := range entries {
		if e.Direction == DirToMasterMind && !e.Confirmed && e.AdmittedAt == nil && (round <= 0 || e.Round <= round) {
			pending = append(pending, PendingEntry{Entry: e, Idx: i})
		}
	}

	return pending, nil
}

// admitIndex marks the idx'th event in Seq order as admitted by a push route.
//
// It patches the entry's JSON through a map[string]json.RawMessage exactly as
// confirmIndex does, so a key a newer relevo wrote survives. It writes no
// column: the JSON is authoritative for the admit, and the scans that read it
// already decode it. It is idempotent, so the route that retries a tick cannot
// move the stamp.
func (s *Store) admitIndex(name string, idx int) error {
	// This write does not pass through read, and the name still becomes a path
	// in the sibling helpers, so it takes the same first-line refusal.
	if err := ValidName(name); err != nil {
		return err
	}
	d, err := s.dbForWrite()
	if err != nil {
		return err
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil {
		return fmt.Errorf("read log for %q: %w", name, err)
	}
	var events []db.RecordEvent
	if ok {
		if events, err = d.EventsOf(rec.ID, 0); err != nil {
			return fmt.Errorf("read log for %q: %w", name, err)
		}
	}
	if idx < 0 || idx >= len(events) {
		return fmt.Errorf("admit entry %d for %q: log has %d entries", idx, name, len(events))
	}
	ev := events[idx]
	// A confirmed entry has already been read back, and an admitted one has no
	// stamp left to write: both are no-ops.
	if ev.Confirmed {
		return nil
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(ev.JSON), &m); err != nil {
		return fmt.Errorf("decode log entry %d for %q: %w", idx, name, err)
	}
	if _, admitted := m["admitted_at"]; admitted {
		return nil
	}

	now := time.Now().UTC()
	admitted, err := json.Marshal(now)
	if err != nil {
		return fmt.Errorf("encode admitted_at: %w", err)
	}
	m["admitted_at"] = admitted

	// Seq is written through so a reader of the JSON alone still sees it.
	encodedSeq, err := json.Marshal(ev.Seq)
	if err != nil {
		return fmt.Errorf("encode seq: %w", err)
	}
	m["seq"] = encodedSeq

	patched, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode log entry: %w", err)
	}

	return d.EventAdmit(rec.ID, ev.Seq, string(patched))
}
