package store

import (
	"github.com/fuad-daoud/relevo/internal/usage"
)

// PriorUsageOf returns the sum of what a round's earlier segments consumed:
// outgoing switch entries that recorded usage, plus the PriorTokens the
// newest report entry carries (for closed remote rounds) -- as tokens and,
// where the segments carry bills, cost. A segment that moved tokens without
// a bill -- a remote round's tokens-only PriorTokens, an unreadable read --
// leaves the cost Unknown with USD 0, never the priced remainder; a segment
// that moved none adds nothing. Pure; no I/O.
func PriorUsageOf(entries []LogEntry, round int) usage.Usage {
	var total usage.Usage
	var reportPrior *usage.Tokens
	for _, e := range entries {
		if e.Round != round {
			continue
		}
		if e.Kind == KindSwitch && e.Usage != nil {
			total = usage.Add(total, *e.Usage)
		}
		if e.Kind == KindReport && e.Direction == DirToMasterMind && e.PriorTokens != nil {
			reportPrior = e.PriorTokens
		}
	}
	if reportPrior != nil {
		total = usage.Add(total, usage.Usage{Tokens: *reportPrior})
	}
	return total
}

// PriorTokensOf returns the sum of tokens from earlier segments in the given
// round: outgoing switch entries that recorded usage, plus any PriorTokens
// carried on the newest report entry (for closed remote rounds). Pure; no I/O.
func PriorTokensOf(entries []LogEntry, round int) usage.Tokens {
	return PriorUsageOf(entries, round).Tokens
}
