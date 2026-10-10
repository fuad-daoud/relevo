package syncpipe

import (
	"github.com/fuad-daoud/relevo/internal/synclog"
	"github.com/fuad-daoud/relevo/internal/syncworker"
)

// The wire names the log's own columns and the daemon names Go fields, so every
// entry crosses this boundary twice. Both directions are written out rather
// than reflected over, because a field the reflection missed would be one that
// silently became its zero value on the far side of a pipe -- which is the
// failure a transport cannot detect and a reader would act on anyway.

// entriesToWire renders this origin's batch for an export. A batch arrives
// numbered whatever the writer guessed, and the transport numbers it again, so
// nothing the caller sent can name a position in the log.
func entriesToWire(entries []synclog.Entry) []syncworker.Entry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]syncworker.Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, syncworker.Entry{
			Origin:        e.Origin,
			Seq:           e.Seq,
			Batch:         e.Batch,
			Tbl:           e.Table,
			PK:            e.PK,
			Op:            string(e.Op),
			SchemaVersion: e.SchemaVersion,
			Body:          e.Body,
			At:            e.At,
		})
	}
	return out
}

// entriesFromWire reads back what an export numbered or a pull returned. The
// entries are taken as they arrive rather than checked here: the numbers are
// the transport's to hand out, and the importer already refuses to go backwards
// from the mark it holds.
func entriesFromWire(entries []syncworker.Entry) []synclog.Entry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]synclog.Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, synclog.Entry{
			Origin:        e.Origin,
			Seq:           e.Seq,
			Batch:         e.Batch,
			Table:         e.Tbl,
			PK:            e.PK,
			Op:            synclog.Op(e.Op),
			SchemaVersion: e.SchemaVersion,
			Body:          e.Body,
			At:            e.At,
		})
	}
	return out
}

// headFromWire reads back a page of an origin's latest state per row.
func headFromWire(rows []syncworker.HeadRow) []synclog.HeadRow {
	if len(rows) == 0 {
		return nil
	}
	out := make([]synclog.HeadRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, synclog.HeadRow{
			Table: r.Tbl,
			PK:    r.PK,
			Seq:   r.Seq,
			Hash:  r.Hash,
		})
	}
	return out
}

// statsFromWire reads back what the log holds, and what the worker has moved.
//
// The four byte counters are carried through rather than summed here: they are
// already cumulative, and adding to them a second time would count every call
// the worker made once per stats reply the daemon asked for.
func statsFromWire(s syncworker.Stats) synclog.Stats {
	return synclog.Stats{
		Entries:       s.Entries,
		Origins:       s.Origins,
		Seq:           s.Seq,
		TursoSent:     s.TursoSent,
		TursoReceived: s.TursoReceived,
		R2Put:         s.R2Put,
		R2Get:         s.R2Get,
	}
}
