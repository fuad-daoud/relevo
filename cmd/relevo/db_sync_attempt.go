package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/sanitize"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// dbSyncAttemptDoc is the daemon's last steady attempt as `db sync status`
// prints it.
type dbSyncAttemptDoc struct {
	Start     string `json:"start"`
	End       string `json:"end"`
	Exported  int    `json:"exported"`
	Applied   int    `json:"applied"`
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
		Error:     a.Error,
		Attention: a.Attention,
	}
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
	fmt.Fprintf(&b, " (exported %d, applied %d)", a.Exported, a.Applied)
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
