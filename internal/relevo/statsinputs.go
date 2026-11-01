package relevo

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/stats"
)

// StatsInputs assembles stats.Inputs for the window starting at since (zero =
// all) ending at rt.Now(). rt.DB must be open. Read failures of latency and
// history are returned as warnings, never as errors.
func StatsInputs(rt Runtime, since time.Time) (in stats.Inputs, warnings []string, err error) {
	rows, err := rt.DB.Query(db.Filter{Since: since})
	if err != nil {
		return stats.Inputs{}, nil, err
	}
	landed := map[string]bool{}
	done, err := rt.DB.Bindings(db.Filter{State: "done"})
	if err != nil {
		return stats.Inputs{}, nil, err
	}
	for _, b := range done {
		landed[b.ID] = true
	}

	// A latency read failure warns and leaves the report without ttft values.
	// Latency and the history below both read the two history tables, so
	// every origin's rows inside the window are combined.
	deps := AvailabilityDeps(rt)
	var lat availability.LatencyHistory
	if loaded, lerr := availability.LoadLatency(deps); lerr != nil {
		warnings = append(warnings, "could not read latency: "+lerr.Error())
	} else {
		lat = loaded
	}
	// The shell's clock is read once, so the prune and the report's open end
	// agree; the guard keeps a zero Runtime (a test) usable.
	now := time.Now()
	if rt.Now != nil {
		now = rt.Now()
	}
	lat = lat.Prune(now)

	hist, herr := availability.LoadHistory(deps)
	if herr != nil {
		warnings = append(warnings, "could not read history: "+herr.Error())
	}

	return stats.Inputs{
		Rows:    rows,
		Landed:  landed,
		History: hist,
		Gates:   availability.Gates(deps),
		TTFT: func(token string) (int64, bool) {
			s := lat.Summary(token)
			return s.TTFTP50MS, s.N > 0
		},
		IsPlan: func(token string) bool {
			if rt.Candidates == nil {
				return false
			}
			ref, rerr := candidate.ParseRef(token)
			if rerr != nil {
				return false
			}
			c, lerr := rt.Candidates.Lookup(ref)
			if lerr != nil {
				return false
			}
			return c.Plan
		},
		Since: since,
		Until: now,
		Loc:   time.Local,
	}, warnings, nil
}
