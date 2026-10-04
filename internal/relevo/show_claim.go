package relevo

import "github.com/fuad-daoud/relevo/internal/store"

// claimPrinted is the match a live read hands delivery.PullMatching: the
// pending mastermind-bound entry the read printed, and nothing else.
//
// A read confirms a payload only when what it printed IS that payload's
// content. A report or output section prints a round's report -- a reader's
// report is its artifact output -- so the payload it may claim is a report-kind
// entry for the very round the section resolved to. A findings section prints
// one consult's findings file, which is the very artifact that consult's
// findings entry points at, so it may claim that entry and no other: every
// consult on a binding queues its own entry, so a match on the kind alone would
// confirm the oldest consult's entry while a later consult's file is the one
// printed -- claiming an entry nobody read and leaving the one that was read to
// be delivered again. The path is the whole match because the entry carries no
// consult id of its own, and its round is the round the consult ran on, which a
// read may have resolved differently from where it wrote the file. An entry
// with no path is a consult that wrote no findings: there is no file for any
// read to print, so no read claims it. Every other section (prompt, diff,
// drift, log, transcript, gate, artifacts) prints something a pending payload
// is not, so it claims nothing; neither does a read whose section resolved to
// Missing, which printed no content to confirm with.
//
// A report read of an earlier round therefore leaves a later round's payload
// pending, and a report read of a round whose pending entry is a diff leaves
// that diff pending for the route that pushes it. An entry a push route already
// admitted is excluded upstream, by the claimable scan this match runs inside,
// so an admitted entry is never claimable by any read. That scan runs over
// every claimable entry and skips the ones this match rejects WITHOUT
// confirming them, so a stale halt, an older findings entry ahead of a report,
// or another consult's findings ahead of this one does not stop the read from
// confirming its own payload.
func claimPrinted(st *store.Store, section ShowSection, res ShowResult, findingsID string) func(store.LogEntry) bool {
	report := !res.Missing && (section == ShowReport || section == ShowOutput)
	findings := !res.Missing && section == ShowFindings
	printed := ""
	if findings {
		printed = st.FindingsPath(res.Name, res.Round, findingsID)
	}
	return func(e store.LogEntry) bool {
		switch {
		case report:
			return e.Kind == store.KindReport && e.Round == res.Round
		case findings:
			return e.Kind == store.KindFindings && e.Path == printed
		default:
			return false
		}
	}
}
