package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/db"
)

// dbForWrite returns the store's database handle, opening it -- and applying
// its migrations -- on first use: the path every write takes. Path helpers,
// WithLock alone and DaemonRunning never call it, so a lock-only or path-only
// store opens no database.
//
// A database whose schema is newer than this binary's is refused with
// db.ErrNewerSchema.
func (s *Store) dbForWrite() (*db.DB, error) {
	if s.shared != nil {
		return s.shared, nil
	}
	s.dbOnce.Do(func() {
		if err := os.MkdirAll(s.root, bindingDirMode); err != nil {
			s.dbErr = fmt.Errorf("open store db %s: %w", s.DBPath(), err)
			return
		}
		d, err := db.Open(s.DBPath())
		if err != nil {
			s.dbErr = fmt.Errorf("open store db %s: %w", s.DBPath(), err)
			return
		}
		if d.Newer() {
			_ = d.Close()
			s.dbErr = db.ErrNewerSchema
			return
		}
		s.dbh = d
	})
	return s.dbh, s.dbErr
}

// dbForRead returns the store's database handle without creating the database:
// (nil, nil) when <root>/relevo.db does not exist, which every read path treats
// as "no rows". Only a write, or the import of a present legacy file, creates
// it.
func (s *Store) dbForRead() (*db.DB, error) {
	if s.shared != nil {
		return s.shared, nil
	}
	if _, err := os.Stat(s.DBPath()); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open store db %s: %w", s.DBPath(), err)
	}
	return s.dbForWrite()
}

// DB is the exported form of dbForWrite, opening and migrating on first use.
func (s *Store) DB() (*db.DB, error) { return s.dbForWrite() }

// DBIfExists is the exported form of dbForRead.
func (s *Store) DBIfExists() (*db.DB, error) { return s.dbForRead() }

// recordEventOf projects a LogEntry onto the binding_event columns.
func recordEventOf(e LogEntry, entryJSON string) db.RecordEvent {
	return db.RecordEvent{
		Seq:         e.Seq,
		TS:          e.TS,
		Round:       e.Round,
		Direction:   string(e.Direction),
		Kind:        string(e.Kind),
		Confirmed:   e.Confirmed,
		DeliveredAt: e.DeliveredAt,
		Route:       e.Route,
		JSON:        entryJSON,
	}
}

// recordEventsOf projects decoded entries and their exact JSON onto the event
// rows EventReplaceAll takes, in order.
func recordEventsOf(entries []LogEntry, entryJSON [][]byte) ([]db.RecordEvent, error) {
	if len(entries) != len(entryJSON) {
		return nil, fmt.Errorf("%d entries but %d lines", len(entries), len(entryJSON))
	}
	evs := make([]db.RecordEvent, 0, len(entries))
	for i, e := range entries {
		evs = append(evs, recordEventOf(e, string(entryJSON[i])))
	}
	return evs, nil
}

// logEntriesOf decodes stored events back into LogEntry values. entry_json is
// authoritative for the content; Seq, Confirmed, DeliveredAt and Route come
// from the promoted columns.
func logEntriesOf(events []db.RecordEvent) ([]LogEntry, error) {
	if len(events) == 0 {
		return nil, nil
	}
	out := make([]LogEntry, 0, len(events))
	for _, ev := range events {
		var e LogEntry
		if err := json.Unmarshal([]byte(ev.JSON), &e); err != nil {
			return nil, fmt.Errorf("decode log entry: %w", err)
		}
		e.Seq = ev.Seq
		e.Confirmed = ev.Confirmed
		e.DeliveredAt = ev.DeliveredAt
		e.Route = ev.Route
		out = append(out, e)
	}
	return out, nil
}
