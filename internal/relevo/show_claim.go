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
// consult id of its own. An entry with no path, any other section, or a read
// whose section resolved to Missing claims nothing.
//
// An entry a push route already admitted is excluded upstream, so an admitted
// entry is never claimable by any read.
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
