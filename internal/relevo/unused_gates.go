package relevo

import (
	"sort"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/view"
)

// UnusedProviderGates returns the live rate limits on providers outside the
// configured candidate set. A missing gates store or candidate set is nil:
// with nothing to compare against there are no unused providers. A ledger load
// failure reads as nil too, because Gates reports that error once on stderr
// already and a bookkeeping file must not take status down.
func UnusedProviderGates(rt Runtime) []view.ProviderGate {
	if rt.Gates == nil || rt.Candidates == nil {
		return nil
	}

	l, err := availability.LoadLedger(rt.Gates)
	if err != nil {
		return nil
	}

	return UnusedProviderGatesFrom(rt, l)
}

// UnusedProviderGatesFrom is UnusedProviderGates with the ledger already
// loaded: a status report that also builds the candidate gates reads the kv row
// once and projects both from the one Ledger. A nil gates store or candidate
// set is still nil.
func UnusedProviderGatesFrom(rt Runtime, l availability.Ledger) []view.ProviderGate {
	if rt.Gates == nil || rt.Candidates == nil {
		return nil
	}

	used := make(map[string]bool)
	for _, p := range rt.Candidates.Providers() {
		used[p] = true
	}

	var out []view.ProviderGate
	for _, e := range l.Prune(rt.Now()).Entries {
		if e.Kind != availability.RateLimited || used[e.Subject] {
			continue
		}
		out = append(out, view.ProviderGate{
			Provider: e.Subject,
			Since:    e.At,
			Until:    e.Until,
			Note:     e.Note,
			Source:   e.Source,
			Binding:  e.Binding,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Since.Before(out[j].Since)
	})
	return out
}
