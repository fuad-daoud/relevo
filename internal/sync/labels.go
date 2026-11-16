package sync

import (
	"fmt"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// stampLayout is the text shape every installation stamp is stored in. It
// repeats the database's own layout because the directory reader only parses
// this one: anything else would read back as a corrupt directory.
const stampLayout = "2006-01-02T15:04:05.000Z"

// RecordMachineLabels writes the peer labels the log names into the
// installation directory, so the machine list on a server that pulls no
// history still names its peers. Each write is the importer's own row write,
// so the directory row carries the peer as its owner and a later export
// passes over it rather than sending it back as this machine's.
func RecordMachineLabels(shared *db.DB, transport synclog.LogTransport) error {
	marks, err := transport.ReadMarks()
	if err != nil {
		return fmt.Errorf("sync: record machine labels: %w", err)
	}
	listed, err := shared.InstallationList()
	if err != nil {
		return fmt.Errorf("sync: record machine labels: %w", err)
	}
	current := make(map[string]db.Installation, len(listed))
	for _, in := range listed {
		current[in.ID] = in
	}
	pending := labelWrites(marks.Labels, current, shared.Origin(), time.Now())
	if len(pending) == 0 {
		return nil
	}
	return shared.Tx(func(tx *db.Tx) error {
		for _, w := range pending {
			if err := tx.ExchangeUpsert("installation", w.origin, w.values); err != nil {
				return fmt.Errorf("sync: record machine labels %s: %w", w.origin, err)
			}
		}
		return nil
	})
}

// labelWrite is one directory row to upsert: an insert names the stamps an
// update leaves alone.
type labelWrite struct {
	origin string
	values map[string]any
}

// labelWrites is the rows the log's labels add or change: peers only, with an
// empty label never overwriting a good one and an unchanged one writing
// nothing at all. An update rewrites first_seen with the stamp it holds: the
// column is NOT NULL without a default, which the engine refuses even when
// the row already exists, so only the held value keeps it.
func labelWrites(labels map[string]string, current map[string]db.Installation, self string, now time.Time) []labelWrite {
	origins := make([]string, 0, len(labels))
	for origin := range labels {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	stamp := now.UTC().Format(stampLayout)
	var out []labelWrite
	for _, origin := range origins {
		label := labels[origin]
		if origin == self || label == "" {
			continue
		}
		values := map[string]any{"id": origin, "label": label, "last_seen": stamp}
		if held, ok := current[origin]; ok {
			if held.Label == label {
				continue
			}
			values["first_seen"] = held.FirstSeen.UTC().Format(stampLayout)
		} else {
			values["first_seen"] = stamp
		}
		out = append(out, labelWrite{origin: origin, values: values})
	}
	return out
}
