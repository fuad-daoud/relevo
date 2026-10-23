package availability

import (
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
)

// concurrencyGates is one ConcurrencyLimit gate per candidate whose pool is at
// or over its max_concurrency. A pool is harness+provider, the plan rather than
// the model, because the allowance is the plan's. Nothing is recorded: the
// provider stays ungated and the verdict is recomputed from the live count.
func concurrencyGates(d Deps) []Gate {
	if d.Candidates == nil || d.InFlight == nil {
		return nil
	}
	type entry struct {
		ref  string
		pool [2]string
		cap  int
	}
	var all []entry
	capped := false
	for _, ref := range d.Candidates.Refs() {
		r, err := candidate.ParseRef(ref)
		if err != nil {
			continue
		}
		c, err := d.Candidates.Lookup(r)
		if err != nil {
			continue
		}
		all = append(all, entry{ref: ref, pool: [2]string{r.Harness, r.Provider}, cap: c.MaxConcurrency})
		capped = capped || c.MaxConcurrency > 0
	}
	if !capped {
		return nil
	}
	counts := d.InFlight(d.InFlightExcept)
	use := map[[2]string]int{}
	for _, e := range all {
		use[e.pool] += counts[e.ref]
	}
	var now time.Time
	if d.Now != nil {
		now = d.Now()
	}
	var out []Gate
	for _, e := range all {
		if e.cap <= 0 || use[e.pool] < e.cap {
			continue
		}
		out = append(out, Gate{
			Token:  e.ref,
			Kind:   ConcurrencyLimit,
			Since:  now,
			Note:   fmt.Sprintf("%d/%d", use[e.pool], e.cap),
			Source: "relevo",
		})
	}
	return out
}
