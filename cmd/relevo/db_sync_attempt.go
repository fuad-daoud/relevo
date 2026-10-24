package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/sanitize"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// dbSyncAttemptDoc is the daemon's last steady attempt as `db sync status`
// prints it.
type dbSyncAttemptDoc struct {
	Start     string `json:"start"`
	End       string `json:"end"`
	Exported  int    `json:"exported"`
	Applied   int    `json:"applied"`
	Pulls     int    `json:"pulls"`
	Pushes    int    `json:"pushes"`
	TursoPush int64  `json:"turso_push"`
	TursoPull int64  `json:"turso_pull"`
	Error     string `json:"error,omitempty"`
	Attention bool   `json:"attention,omitempty"`
}

// attemptDoc is the recorded attempt as a document, nil when the daemon has not
// recorded one. The error is stored raw and sanitized where it is rendered.
func attemptDoc(a relevosync.Attempt) *dbSyncAttemptDoc {
	if a.Start.IsZero() {
		return nil
	}
	return &dbSyncAttemptDoc{
		Start:     statusStamp(a.Start),
		End:       statusStamp(a.End),
		Exported:  a.Exported,
		Applied:   a.Applied,
		Pulls:     a.Pulls,
		Pushes:    a.Pushes,
		TursoPush: a.TursoPush,
		TursoPull: a.TursoPull,
		Error:     a.Error,
		Attention: a.Attention,
	}
}

// attemptDocs renders the attempt ring oldest first, so the document reads in
// the order the attempts ran.
func attemptDocs(rs []relevosync.Attempt) []dbSyncAttemptDoc {
	out := make([]dbSyncAttemptDoc, 0, len(rs))
	for _, r := range rs {
		if doc := attemptDoc(r); doc != nil {
			out = append(out, *doc)
		}
	}
	return out
}

// dbSyncAttempts reads the attempt ring for the status document. A ring that
// cannot be read is absent rather than a failed status: the ring is a recent
// addition and a machine that has never recorded one is in the ordinary state.
func dbSyncAttempts(local relevosync.Local) []dbSyncAttemptDoc {
	rs, err := relevosync.ReadAttempts(local)
	if err != nil || len(rs) == 0 {
		return nil
	}
	return attemptDocs(rs)
}

// dbSyncTables reads this month's per-table breakdown for the status document,
// absent like the ring when there is nothing to show.
func dbSyncTables(local relevosync.Local) synclog.TableTallies {
	tables, err := relevosync.ReadTables(local, time.Now())
	if err != nil || len(tables) == 0 {
		return nil
	}
	return tables
}

// dbSyncAttemptDetails is the status line's account of the last attempt: how it
// ended and what it moved, so a machine whose attempts keep failing reads as
// failing whatever its older times say.
func dbSyncAttemptDetails(a *dbSyncAttemptDoc) string {
	if a == nil {
		return ""
	}
	var b strings.Builder
	if a.Error != "" {
		fmt.Fprintf(&b, " · last attempt failed at %s: %s", a.End, sanitize.Text(a.Error))
	} else {
		fmt.Fprintf(&b, " · last attempt ok at %s", a.End)
	}
	fmt.Fprintf(&b, " (exported %d, applied %d, pulls %d, pushes %d)", a.Exported, a.Applied, a.Pulls, a.Pushes)
	if a.Attention {
		b.WriteString(" · needs attention")
	}
	return b.String()
}

// statusStamp renders a recorded moment as the UTC RFC 3339 instant a reader
// can compare, or "" when nothing was ever recorded. A zero time is not 1970
// here: it is a moment that never happened, and printing it as one would be a
// true statement about the wrong thing.
func statusStamp(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}
