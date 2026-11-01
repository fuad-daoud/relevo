package ingest

import (
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// deriveOutcome derives round n's outcome, first match wins:
//
//	report entry              -> reported
//	done marker, no report    -> done_no_report
//	exit on the current round -> exited
//	needs_you or Halt on n    -> halted
//	stop entry, no report     -> done_no_report
//	switch entry              -> switched
//	otherwise                 -> open
//
// The open arm reads store.RoundOpen, as `stop` and the served view do, so
// history cannot call closed a round they call open. A halt names only the round
// it halted: one recorded against a later round says nothing about an earlier one.
func deriveOutcome(events []store.LogEntry, n int, b store.Binding, members map[string]bool) string {
	var hasReport, hasExit, hasSwitch, hasStop bool
	for _, e := range events {
		if e.Round != n {
			continue
		}
		switch e.Kind {
		case store.KindReport:
			hasReport = true
		case store.KindExit:
			hasExit = true
		case store.KindSwitch:
			hasSwitch = true
		case store.KindStop:
			hasStop = true
		}
	}

	switch {
	case hasReport:
		return db.OutcomeReported
	case members[donePathBase(n)]:
		return db.OutcomeDoneNoReport
	case hasExit && n == b.Round && b.State != store.StateActive:
		return db.OutcomeExited
	case (b.State == store.StateNeedsYou && n == b.Round) || (b.Halt != "" && n == b.Round):
		return db.OutcomeHalted
	case hasStop:
		return db.OutcomeDoneNoReport
	case hasSwitch:
		return db.OutcomeSwitched
	default:
		return db.OutcomeOpen
	}
}

// switchesForRound counts switch entries for round n, skipping a relaunch's note
// prefix -- a relaunch replaces a lost process with the same candidate -- and a
// poll's entry once the log holds the server's own record of the round's switches
// in full.
func switchesForRound(events []store.LogEntry, n int) int {
	count, polled := 0, 0
	for _, e := range events {
		if e.Round != n || e.Kind != store.KindSwitch ||
			strings.HasPrefix(e.Note, "relaunched ") || strings.HasPrefix(e.Note, "resumed session ") {
			continue
		}
		if strings.HasPrefix(e.Note, store.PollSwitchNotePrefix) {
			polled++
			continue
		}
		count++
	}
	if count == 0 {
		return polled
	}
	return count
}

// candidateForRound resolves round n's candidate from the round's own last pick
// or switch entry's note, else the builder earlier rounds left running, else
// b.BuilderCandidate when no round records one. A pick naming another actor is a
// consult's and is skipped. candidateTok is verbatim; ref sheds a trailing
// "#...", since a ":effort" suffix belongs to the model.
func candidateForRound(events []store.LogEntry, n int, b store.Binding) (candidateTok string, ref candidate.Ref, ok bool) {
	tok := lastBuilderToken(events, b.WriterRole(), func(round int) bool { return round == n })
	if tok == "" {
		tok = lastBuilderToken(events, b.WriterRole(), func(round int) bool { return round > 0 && round < n })
	}
	if tok == "" {
		tok = b.BuilderCandidate
	}
	if tok == "" {
		return "", candidate.Ref{}, false
	}

	if r, err := candidate.ParseRef(candidate.StripEffort(tok)); err == nil {
		ref = r
	}
	return tok, ref, true
}

// lastBuilderToken is the token of the last pick or switch entry in rounds
// inRound accepts that names the builder actor plays.
func lastBuilderToken(events []store.LogEntry, actor string, inRound func(int) bool) string {
	tok := ""
	for _, e := range events {
		if !inRound(e.Round) || (e.Kind != store.KindPick && e.Kind != store.KindSwitch) {
			continue
		}
		if e.Kind == store.KindPick && isOtherActorPick(e.Note, actor) {
			continue
		}
		tok = parseBuilderNote(e.Kind, e.Note)
	}
	return tok
}

// isOtherActorPick reports whether note is `ask`'s consult pick --
// "picked <tok> for <role>: ..." with a role other than actor.
func isOtherActorPick(note, actor string) bool {
	const prefix = "picked "
	if !strings.HasPrefix(note, prefix) {
		return false
	}
	rest := note[len(prefix):]
	sp := strings.IndexByte(rest, ' ')
	if sp < 0 {
		return false
	}
	const sep = " for "
	if !strings.HasPrefix(rest[sp:], sep) {
		return false
	}
	rolePart := rest[sp+len(sep):]
	ci := strings.IndexByte(rolePart, ':')
	if ci < 0 {
		return false
	}
	role := rolePart[:ci]
	return role != "" && role != actor && strings.IndexByte(role, ' ') < 0
}

func parseBuilderNote(kind store.Kind, note string) string {
	switch kind {
	case store.KindPick:
		return parsePickNote(note)
	case store.KindSwitch:
		return parseSwitchNote(note)
	}
	return ""
}

// parsePickNote parses the first word after "picked ", minus one trailing ":".
func parsePickNote(note string) string {
	const prefix = "picked "
	if !strings.HasPrefix(note, prefix) {
		return ""
	}
	rest := note[len(prefix):]
	tok := rest
	if i := strings.IndexByte(rest, ' '); i >= 0 {
		tok = rest[:i]
	}
	return strings.TrimSuffix(tok, ":")
}

// parseSwitchNote parses the pick clause when present, else the token after the
// last "->", trimmed of a trailing ")" and whitespace.
func parseSwitchNote(note string) string {
	if i := strings.LastIndex(note, "picked "); i >= 0 {
		return parsePickNote(note[i:])
	}
	i := strings.LastIndex(note, "->")
	if i < 0 {
		return ""
	}
	tok := strings.TrimSpace(note[i+2:])
	tok = strings.TrimSuffix(tok, ")")
	return strings.TrimSpace(tok)
}
