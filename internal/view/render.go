package view

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/chatlabel"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// mastermindNameOrID is the mastermind a status row names: the record name when it
// has one, else the id, else "".
func mastermindNameOrID(b BindingStatus) string {
	if b.MasterMindName != "" {
		return b.MasterMindName
	}
	return b.MasterMindID
}

// RenderStatus formats a Report for a terminal.
func RenderStatus(r Report) string {
	var sb strings.Builder

	switch {
	case len(r.Bindings) == 0 && r.DoneHidden > 0:
		// The footer says "clear" and not "free": gc frees disk only for
		// relevo-created worktrees, and the footer must not overpromise.
		fmt.Fprintf(&sb, "%d done · relevo unbind --done to clear\n", r.DoneHidden)
		if len(r.Gated) > 0 {
			writeGatedBlock(&sb, r.Gated, false)
		}
		return sb.String()

	case len(r.Bindings) == 0 && len(r.Gated) == 0:
		sb.WriteString("no bindings\n")
		return sb.String()

	case len(r.Bindings) == 0:
		sb.WriteString("no bindings\n")
		writeGatedBlock(&sb, r.Gated, false)
		return sb.String()
	}

	for _, b := range r.Bindings {
		if b.Chain != nil {
			writeChainRow(&sb, b)
			continue
		}
		writeBindingRow(&sb, b)
	}

	if len(r.Gated) > 0 {
		writeGatedBlock(&sb, r.Gated, r.DoneHidden > 0)
	}

	if r.DoneHidden > 0 {
		// The footer says "clear" and not "free": gc frees disk only for
		// relevo-created worktrees, and the footer must not overpromise.
		fmt.Fprintf(&sb, "%d done · relevo unbind --done to clear\n", r.DoneHidden)
	}

	return sb.String()
}

// writeBindingRow renders one binding's block: the round line, the mastermind
// line, the runner line and the trailer lines.
func writeBindingRow(sb *strings.Builder, b BindingStatus) {
	writeRoundLine(sb, b)
	writeMasterMindLine(sb, b)
	writeBuilderLine(sb, b)
	writeRowTrailer(sb, b)
}

// writeChainRow renders one chain's block in place of its members' rows: the
// chain's name, the tree it works in, the chain's status and its plan segment,
// then the halt reason when it has one. A member's own block still renders
// through writeBindingRow, underneath, in the status <chain> view.
func writeChainRow(sb *strings.Builder, b BindingStatus) {
	cwd := b.CWD
	if cwd == "" {
		cwd = "-"
	}
	fmt.Fprintf(sb, "%-8s %-40s chain  %s  %s", b.Name, cwd, b.Chain.Status, ChainSegment(*b.Chain))
	if b.Detail != "" {
		fmt.Fprintf(sb, "\n  reason   %s", b.Detail)
	}
	fmt.Fprint(sb, "\n")
}

// writeRoundLine renders the binding's name, cwd, round and state, plus the
// land state, the stale age, the dirty marker, the consult count, the
// verdict, the live diff, the quiet age and the unread marker.
func writeRoundLine(sb *strings.Builder, b BindingStatus) {
	fmt.Fprintf(sb, "%-8s %-40s round %-3d %s", b.Name, b.CWD, b.Round, b.Display)
	// The land state sits on the round line, where the human reads what
	// happened to the branch.
	if b.Landed != "" {
		fmt.Fprintf(sb, "  %s", b.Landed)
	}
	// The stale age sits right after the state word, where a human scanning
	// for what has been waiting the longest looks.
	if b.Stale != "" {
		fmt.Fprintf(sb, "  %s", b.Stale)
	}
	if b.Dirty {
		fmt.Fprint(sb, " dirty")
	}
	// Zero stays out of the row entirely: +0c on every healthy binding
	// would be noise, not information.
	if b.Consults > 0 {
		fmt.Fprintf(sb, " +%dc", b.Consults)
	}
	// The newest reviewer verdict, while it judged the round just closed:
	// "verdict: rejected (2 reasons)".
	if b.Verdict != "" {
		fmt.Fprintf(sb, " %s", b.Verdict)
	}
	// The round's live diff against its baseline, how long an ACTIVE row has
	// been quiet, and whether its newest report is unread.
	if b.Live != nil {
		fmt.Fprintf(sb, "  +%d/-%d in %d", b.Live.Added, b.Live.Removed, b.Live.Files)
		if b.Live.Shared {
			fmt.Fprint(sb, " (shared tree)")
		}
	}
	if b.QuietFor != "" {
		fmt.Fprintf(sb, "  quiet %s", b.QuietFor)
	}
	if b.Unread {
		fmt.Fprint(sb, "  ●new")
	}
	fmt.Fprint(sb, "\n")
}

// writeMasterMindLine renders the mastermind line, including the mastermind's chat
// label when cmd/relevo filled it. An empty label leaves the line
// byte-identical to the line that existed before these fields.
func writeMasterMindLine(sb *strings.Builder, b BindingStatus) {
	fmt.Fprintf(sb, "  MasterMind  %-14s %-8s route %s",
		mastermindNameOrID(b), b.MasterMindKind, b.MasterMindRoute)
	if b.MasterMindChatLabel != "" || b.MasterMindChatLink != "" {
		lbl := chatlabel.Label{Text: b.MasterMindChatLabel, Link: b.MasterMindChatLink}
		fmt.Fprintf(sb, " · %s", lbl.String())
	}
	fmt.Fprint(sb, "\n")
}

// writeBuilderLine renders the runner line: the process word, the kind and
// status, and the runner's short name when it has one, its token otherwise.
func writeBuilderLine(sb *strings.Builder, b BindingStatus) {
	builderLabel := b.BuilderName
	if builderLabel == "" {
		builderLabel = b.BuilderCandidate
	}
	if b.Headless != nil {
		fmt.Fprintf(sb, "  runner  %-14s %-8s %-9s", b.ProcessWord(), b.BuilderKind, b.BuilderStatus)
		if b.Headless.PID != 0 {
			fmt.Fprintf(sb, " pid %d since %s ", b.Headless.PID, b.Headless.StartedAt.Local().Format("15:04"))
		} else {
			fmt.Fprint(sb, " ")
		}
		fmt.Fprintf(sb, "`%s`", builderLabel)
	} else {
		fmt.Fprintf(sb, "  runner  %-14s %-8s %-9s `%s`",
			"remote", b.BuilderKind, b.BuilderStatus, builderLabel)
	}
	// A builder row is the common case: the actor is always set now, so
	// printing it would end every row in "actor builder". Only a row whose
	// runner plays some other actor names it here.
	if b.Role != "builder" {
		fmt.Fprintf(sb, "   actor %s", b.Role)
	}
	if b.Switches > 0 {
		fmt.Fprintf(sb, "   switched %dx", b.Switches)
	}
	fmt.Fprint(sb, "\n")
}

// writeRowTrailer renders the lines below the runner line: the headless log
// tail, the detail, the last event, the usage and spend, and the pending
// payload.
func writeRowTrailer(sb *strings.Builder, b BindingStatus) {
	if b.Headless != nil {
		for _, line := range b.Headless.Tail {
			fmt.Fprintf(sb, "  log      %s\n", line)
		}
	}
	if b.Detail != "" {
		fmt.Fprintf(sb, "  detail   %s\n", b.Detail)
	}
	if b.Last != nil {
		fmt.Fprintf(sb, "  last     %s %s %s round %d",
			b.Last.TS.Local().Format("15:04:05"), b.Last.Kind, b.Last.Direction, b.Last.Round)
		if b.Last.Note != "" {
			fmt.Fprintf(sb, " (%s)", b.Last.Note)
		}
		if b.Last.Outcome != "" && b.Last.Outcome != reporttail.OutcomeDone {
			fmt.Fprintf(sb, " %s", b.Last.Outcome)
		}
		fmt.Fprint(sb, "\n")
	}
	if b.LiveUsage != nil {
		fmt.Fprintf(sb, "  usage    %s\n", strings.Join(usage.LiveParts(*b.LiveUsage), "  "))
	} else if b.LastUsage != nil {
		fmt.Fprintf(sb, "  usage    %s\n", usage.Line(*b.LastUsage))
	}
	if b.Spend != nil {
		fmt.Fprintf(sb, "  spend    %s\n", usage.SpendLine(*b.Spend))
	}
	if b.Pending != nil {
		fmt.Fprintf(sb, "  pending  %s round %d -> mastermind\n\n", b.Pending.Kind, b.Pending.Round)
	} else {
		fmt.Fprint(sb, "  pending  --\n\n")
	}
}

// writeGatedBlock renders the "candidates" block RenderStatus, `relevo
// candidates` and `relevo doctor` all draw from the same ledger.Gate slice
// for: one row per live gate, sharing GateKindText/GateUntilText so the
// wording never drifts between renderers. trailingBlank adds one blank
// line after the block, only when a footer follows it in the output.
func writeGatedBlock(sb *strings.Builder, gates []availability.Gate, trailingBlank bool) {
	sb.WriteString("candidates\n")

	// A gate prints the candidate's short name when it has one, its token
	// otherwise; the column fits whatever is printed.
	width := 0
	for _, g := range gates {
		if len(gateLabel(g)) > width {
			width = len(gateLabel(g))
		}
	}

	for _, g := range gates {
		fmt.Fprintf(sb, "  %-*s  %-12s  %s  %s",
			width, gateLabel(g), availability.GateKindText(g.Kind), availability.GateTimeText(g.Since), availability.GateUntilText(g.Until))
		if g.Note != "" {
			fmt.Fprintf(sb, "  %s", g.Note)
		}
		if g.Binding != "" {
			fmt.Fprintf(sb, "  (%s)", g.Binding)
		}
		sb.WriteString("\n")
	}

	if trailingBlank {
		sb.WriteString("\n")
	}
}

// gateLabel is what a gate row prints: the candidate's short name when it has
// one, its token otherwise.
func gateLabel(g availability.Gate) string {
	if g.Name != "" {
		return g.Name
	}
	return g.Token
}
