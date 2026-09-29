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
//	switch entry              -> switched
//	otherwise                 -> open
func deriveOutcome(events []store.LogEntry, n int, b store.Binding, members map[string]bool) string {
	var hasReport, hasExit, hasSwitch bool
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
	case hasSwitch:
		return db.OutcomeSwitched
	default:
		return db.OutcomeOpen
	}
}

// switchesForRound counts switch entries for round n. A relaunch replaces a lost
// process with the same candidate, so its note prefix is skipped.
func switchesForRound(events []store.LogEntry, n int) int {
	count := 0
	for _, e := range events {
		if e.Round == n && e.Kind == store.KindSwitch &&
			!strings.HasPrefix(e.Note, "relaunched ") && !strings.HasPrefix(e.Note, "resumed session ") {
			count++
		}
	}
	return count
}

// candidateForRound resolves round n's candidate: the token from the last pick or
// switch entry's note, falling back to b.BuilderCandidate. A pick naming an actor
// other than the binding's own is a consult's pick (or another runner's) and is
// skipped. candidateTok is verbatim; ref has a trailing "#..." stripped, since a
// ":effort" suffix belongs to the model.
func candidateForRound(events []store.LogEntry, n int, b store.Binding) (candidateTok string, ref candidate.Ref, ok bool) {
	actor := actorOf(b)
	var lastKind store.Kind
	var lastNote string
	var found bool
	for _, e := range events {
		if e.Round != n {
			continue
		}
		if e.Kind == store.KindPick && isOtherActorPick(e.Note, actor) {
			continue
		}
		if e.Kind == store.KindPick || e.Kind == store.KindSwitch {
			lastKind, lastNote, found = e.Kind, e.Note, true
		}
	}

	tok := ""
	if found {
		tok = parseBuilderNote(lastKind, lastNote)
	}
	if tok == "" {
		tok = b.BuilderCandidate
	}
	if tok == "" {
		return "", candidate.Ref{}, false
	}

	if r, err := candidate.ParseRef(stripEffortHash(tok)); err == nil {
		ref = r
	}
	return tok, ref, true
}

// isOtherActorPick reports whether note is the pick `ask` writes for a consult:
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

// actorOf is the actor a binding's runner plays: b.Role, or "builder" when it is
// empty (records from before the actor split, and the seeded writer actor).
func actorOf(b store.Binding) string {
	if b.Role == "" {
		return "builder"
	}
	return b.Role
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

// parsePickNote parses the first space-delimited word after "picked ", with one
// trailing ":" stripped.
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

// parseSwitchNote parses the pick clause when present, otherwise the token after
// the last "->", trimmed of a trailing ")" and whitespace.
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

// stripEffortHash removes a trailing "#..." suffix; a ":effort" suffix belongs to
// the model part and is left alone.
func stripEffortHash(tok string) string {
	if i := strings.IndexByte(tok, '#'); i >= 0 {
		return tok[:i]
	}
	return tok
}
