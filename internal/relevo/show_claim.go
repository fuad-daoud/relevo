package relevo

import "github.com/fuad-daoud/relevo/internal/store"

// claimPrinted is the match a live read hands delivery.PullMatching: the
// pending mastermind-bound entry the read printed, and nothing else.
//
// A read confirms a payload only when what it printed IS that payload's
// content. A report or output section prints a round's report -- a reader's
// report is its artifact output -- so the payload it may claim is a report-kind
// entry for the very round the section resolved to. Every other section
// (prompt, diff, drift, log, transcript, gate, findings, artifacts) prints
// something a pending payload is not, so it claims nothing; neither does a read
// whose section resolved to Missing, which printed no content to confirm with.
//
// The kind and the round are both required, so a report read of an earlier
// round leaves a later round's payload pending, and a report read of a round
// whose pending entry is a diff leaves that diff pending for the route that
// pushes it. An entry a push route already admitted is excluded upstream, by
// the claimable scan this match runs inside, so an admitted entry is never
// claimable by any read.
func claimPrinted(section ShowSection, res ShowResult) func(store.LogEntry) bool {
	report := !res.Missing && (section == ShowReport || section == ShowOutput)
	return func(e store.LogEntry) bool {
		return report && e.Kind == store.KindReport && e.Round == res.Round
	}
}
