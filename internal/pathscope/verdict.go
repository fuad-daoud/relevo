package pathscope

import "fmt"

// Verdict is one close's scope judgement. Its zero value is "the actor has
// no scope", which changes nothing. Scoped is set for an actor that declared
// a scope; Refused is set when a changed path (or an unjudgeable round)
// refuses.
type Verdict struct {
	Scoped  bool
	Refused bool
	Path    string
	Reason  string
	More    int
}

// Note is the note a scoped close adds: "scope=ok" on a pass and
// "scope=refused" on a refusal. An unscoped actor adds nothing.
func Note(v Verdict) string {
	switch {
	case !v.Scoped:
		return ""
	case v.Refused:
		return "scope=refused"
	default:
		return "scope=ok"
	}
}

// HaltedAt is the report entry's HaltedAt for a refusal:
// "scope: <first path>".
func HaltedAt(v Verdict) string {
	if v.Path == "" {
		return "scope"
	}
	return "scope: " + v.Path
}

// PayloadLine is the payload's refusal line, followed by the `relevo show …
// --diff` hint the caller computed.
func PayloadLine(diffHint string, v Verdict) string {
	var line string
	if v.Path == "" {
		line = "Scope: refused -- " + v.Reason
	} else {
		line = fmt.Sprintf("Scope: refused -- %s: %s", v.Path, v.Reason)
		if v.More > 0 {
			line += fmt.Sprintf(" (+%d more)", v.More)
		}
	}
	return line + ". Diff: " + diffHint
}

// HaltText is the NEEDS YOU message a refusal sets after the round has
// advanced: it names the offending file, or the reason the round could not be
// judged.
func HaltText(bindingName, actor string, round int, v Verdict) string {
	if v.Path == "" {
		return fmt.Sprintf("%s: round %d cannot be judged against actor %s's scope (%s)",
			bindingName, round, actor, v.Reason)
	}
	msg := fmt.Sprintf("%s: round %d changed %s outside actor %s's scope (%s)",
		bindingName, round, v.Path, actor, v.Reason)
	if v.More > 0 {
		msg += fmt.Sprintf("; %d more", v.More)
	}
	return msg
}
