package view

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// claudeCodeMargin is the number of cells Claude Code's chrome takes from
// COLUMNS: measured at 141 of 146 rendered before its own ellipsis.
const claudeCodeMargin = 4

var (
	ansiDim      = "\x1b[38;5;245m"
	ansiNeedsYou = "\x1b[1;38;5;214m"
	ansiReportIn = "\x1b[38;5;80m"
	ansiReset    = "\x1b[0m"
)

// AgeText humanises a duration into coarse units: truncating; under a minute
// Ns; under an hour Nm; otherwise Nh Mm with M the whole minutes past the
// hour, always present (e.g. 1h 0m). Negative renders 0s.
func AgeText(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d/time.Second))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	hours := int(d / time.Hour)
	mins := int((d % time.Hour) / time.Minute)
	return fmt.Sprintf("%dh %dm", hours, mins)
}

// RenderStatusLine formats a Report into one row per live binding
// for Claude Code's statusLine setting. It renders the same row rule the
// OpenCode sidebar shows: the shown round (report_round when > 0, else round)
// and the row's status and tone come from StatusLineRows. The middle names the
// actor; the status column is padded to the widest status and the clock is
// right-aligned last, so neither moves when another row's status or clock
// changes length.
func RenderStatusLine(r Report, now time.Time, columns int) string {
	if len(r.Bindings) == 0 {
		return ""
	}
	if columns <= 0 {
		columns = 80
	}

	rows := StatusLineRows(r, now)
	nameW, statusW, clockW := statusLineColumns(rows)

	var sb strings.Builder
	for _, row := range rows {
		sb.WriteString(renderedStatusLineRow(row, nameW, statusW, clockW, columns))
	}
	return sb.String()
}

// statusLineColumns is the shared name, status and clock column widths of a row
// set, so the status and the clock never move when another row's text changes.
func statusLineColumns(rows []StatusLineRow) (nameW, statusW, clockW int) {
	for _, row := range rows {
		if w := utf8.RuneCountInString(row.Name); w > nameW {
			nameW = w
		}
		if w := utf8.RuneCountInString(row.Status); w > statusW {
			statusW = w
		}
		if w := utf8.RuneCountInString(row.Clock); w > clockW {
			clockW = w
		}
	}
	return nameW, statusW, clockW
}

// statusLineRowText lays out one StatusLineRow's visible text at the shared
// column widths. Non-empty dotColour and statusColour wrap the dot and the
// status, so padding counts the uncoloured runes; "" for both is the plain path.
func statusLineRowText(row StatusLineRow, nameW, statusW, clockW, columns int, dotColour, statusColour string) string {
	dot := "○"
	if row.NeedsYou {
		dot = "●"
	}
	if dotColour != "" {
		dot = dotColour + dot + ansiReset
	}

	round := row.Round
	if row.ReportRound > 0 {
		round = row.ReportRound
	}

	mid := row.Chain
	if mid == "" {
		mid = "r" + strconv.Itoa(round) + " · " + row.Actor
		if row.On != "" {
			mid += " on " + row.On
		}
		if row.Reason != "" {
			mid += " · " + row.Reason
		}
		if row.Live != nil {
			mid += fmt.Sprintf(" · +%d/-%d in %d", row.Live.Added, row.Live.Removed, row.Live.Files)
			if row.Live.Shared {
				mid += " (shared)"
			}
		}
		if row.Tokens != "" {
			mid += " · " + row.Tokens
		}
	}
	// One sanitizing point, once the middle is whole: it covers both the chain
	// row's own Chain text and the Reason a member row concatenates, and it
	// runs before truncate, so no downstream helper has to know a control byte
	// can be here.
	mid = sanitizeText(mid)

	leftW := 2 + nameW + 2
	midW := columns - leftW - 1 - statusW - 2 - clockW

	if midW < 8 {
		// Unpadded fallback: a row this narrow cannot afford three aligned
		// columns, so the status and the clock follow the middle cell.
		status := row.Status
		if statusColour != "" {
			status = statusColour + status + ansiReset
		}
		return dot + " " + row.Name + "  " + mid + " · " + status + " · " + row.Clock + "\n"
	}
	status := pad(row.Status, statusW)
	if statusColour != "" {
		status = statusColour + status + ansiReset
	}
	return dot + " " + pad(row.Name, nameW) + "  " + pad(truncate(mid, midW), midW) + " " + status + "  " + padLeft(row.Clock, clockW) + "\n"
}

// renderedStatusLineRow lays out one StatusLineRow with its SGR codes: the dot
// keeps its dim or needs colour, and the tone colours only the visible status
// text, so the padding never counts the colour and the column never widens.
func renderedStatusLineRow(row StatusLineRow, nameW, statusW, clockW, columns int) string {
	dotColour := ansiDim
	if row.NeedsYou {
		dotColour = ansiNeedsYou
	}
	statusColour := ""
	switch row.Tone {
	case "needs":
		statusColour = ansiNeedsYou
	case "report":
		statusColour = ansiReportIn
	case "phase":
		statusColour = ansiDim
	}
	return statusLineRowText(row, nameW, statusW, clockW, columns, dotColour, statusColour)
}

// PlainStatusLineRows returns each row's visible text, in order, with no SGR
// codes and no trailing newline: the row exactly as RenderStatusLine renders it
// at the same column widths. columns <= 0 is the renderer's default width (80).
func PlainStatusLineRows(rows []StatusLineRow, columns int) []string {
	if columns <= 0 {
		columns = 80
	}
	nameW, statusW, clockW := statusLineColumns(rows)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, strings.TrimSuffix(statusLineRowText(row, nameW, statusW, clockW, columns, "", ""), "\n"))
	}
	return out
}

// RenderMasterMindLine is the statusline's first line: the mastermind's own name,
// dim, so each terminal shows which mastermind it is. An empty name renders
// nothing -- a session relevo did not identify keeps the statusline it had.
// When columns > 0 and the line would overflow it, the visible text is cut
// with the same truncate the binding rows use, before the colour codes wrap it.
func RenderMasterMindLine(name string, columns int) string {
	if name == "" {
		return ""
	}
	text := "MasterMind " + name
	if columns > 0 && utf8.RuneCountInString(text) > columns {
		text = truncate(text, columns)
	}
	return ansiDim + text + ansiReset + "\n"
}

// rowWord is the word a row uses for a delivered payload that produced an
// artifact: "artifact" for a reader, "report" for a writer. Shape alone picks
// it -- never a role name.
func rowWord(b BindingStatus) string {
	if b.Shape == store.ShapeReader {
		return "artifact"
	}
	return "report"
}

// phase is the bare phase of a binding's last payload: the wording the middle
// segment uses when it has to carry one. It ignores the note and the outcome,
// because a delivered report states those in its status column instead.
func phase(b BindingStatus) string {
	if b.LastPayload == nil {
		return "no prompt yet"
	}
	if store.IsPromptKind(b.LastPayload.Kind) {
		return "prompt sent"
	}
	switch b.LastPayload.Kind {
	case store.KindReport:
		return rowWord(b) + " in"
	case store.KindQuestion:
		return "question in"
	case store.KindAnswer:
		return "answered"
	}
	return ""
}

// ActivityWord is the runner's own activity word a row may show while its round
// is in flight, or "" when the runner's status carries no word a row may claim.
// A working runner reads "working", or "quiet X" once the progress sampler has
// gone silent (QuietFor is set). The definite words -- "stalled X", "exploring
// X", "gating X", "exited N", the bare "exited", "running", "queued (...)" and
// the bare "queued" -- pass through verbatim. Every other word returns "": a
// runner that is idle, unknown or "", and words a row must not claim as its own
// activity (unreachable, a credential word, cert, closed, gone). The caller
// keeps its phase when this is empty.
func ActivityWord(b BindingStatus) string {
	switch s := b.BuilderStatus; {
	case s == "working":
		if b.QuietFor != "" {
			return "quiet " + b.QuietFor
		}
		return "working"
	case strings.HasPrefix(s, "stalled "),
		strings.HasPrefix(s, "exploring "),
		strings.HasPrefix(s, "gating "):
		return s
	case s == "exited", strings.HasPrefix(s, "exited "):
		return s
	case s == "running", s == "queued", strings.HasPrefix(s, "queued "):
		return s
	}
	return ""
}

func waiting(b BindingStatus) string {
	if b.Detail != "" {
		return b.Detail
	}
	base := phase(b)
	if b.LastPayload != nil && b.LastPayload.Kind == store.KindReport {
		if b.LastPayload.Note != "" {
			base = fmt.Sprintf("%s in (%s)", rowWord(b), b.LastPayload.Note)
		}
		if b.LastPayload.Outcome != "" && b.LastPayload.Outcome != reporttail.OutcomeDone {
			base += " · " + b.LastPayload.Outcome
		}
	}
	return base
}

// rowStatus is a row's one status text and the tone that colours it. The status
// column is the only place a row says where it is; the middle keeps identity
// and, when there is a reason, that reason. The first rule that matches wins,
// so a stalled report reads NEEDS YOU rather than REPORT IN.
func rowStatus(b BindingStatus, needsYou, reportIn bool) (status, tone string) {
	if needsYou {
		return "NEEDS YOU", "needs"
	}
	if b.Display != "" && b.Display != "ACTIVE" {
		return b.Display, "quiet"
	}
	if reportIn && b.LastPayload != nil {
		switch b.LastPayload.Kind {
		case store.KindReport:
			status = strings.ToUpper(rowWord(b)) + " IN"
			if b.LastPayload.Note != "" {
				status += " · " + b.LastPayload.Note
			}
			if b.LastPayload.Outcome != "" && b.LastPayload.Outcome != reporttail.OutcomeDone {
				status += " · " + b.LastPayload.Outcome
			}
			return status, "report"
		case store.KindQuestion:
			return "QUESTION IN", "report"
		}
	}
	// While a round is in flight the runner's own word takes the phase slot: a
	// quiet or stalled runner says more than the payload phase, which for a
	// working round is the bare "prompt sent". A closed round, a row with no
	// round and a runner with no definite word still read the phase.
	if !b.RoundStart.IsZero() && b.RoundEnd.IsZero() {
		if word := ActivityWord(b); word != "" {
			return word, "phase"
		}
	}
	status = phase(b)
	if status == "" {
		status = "--"
	}
	return status, "phase"
}

func roundClock(b BindingStatus, now time.Time) string {
	if b.RoundStart.IsZero() {
		return "--"
	}
	if !b.RoundEnd.IsZero() {
		return AgeText(b.RoundEnd.Sub(b.RoundStart))
	}
	return AgeText(now.Sub(b.RoundStart))
}

func roundTokens(b BindingStatus) string {
	var base int64
	if b.RoundEnd.IsZero() {
		if b.LiveUsage != nil && b.LiveUsage.Samples > 0 {
			base = b.LiveUsage.Tokens.Total()
		}
	} else if b.RoundUsage != nil {
		base = b.RoundUsage.Tokens.Total()
	}
	total := base + b.RoundPriorTokens.Total()
	if total > 0 {
		return usage.ShortTokens(total) + " tok"
	}
	return ""
}

// harnessSegment is the harness segment of a candidate token: the part
// before its first "/" (agy, claude, opencode). A token with no "/" is
// returned whole.
func harnessSegment(token string) string {
	if i := strings.IndexByte(token, '/'); i >= 0 {
		return token[:i]
	}
	return token
}

func truncate(s string, w int) string {
	runes := []rune(s)
	if len(runes) <= w {
		return s
	}
	if w <= 0 {
		return ""
	}
	return string(runes[:w-1]) + "…"
}

func pad(s string, w int) string {
	n := utf8.RuneCountInString(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

// padLeft pads s on the left to w runes, so the clock column is right-aligned
// and its last rune sits at the same cell on every row.
func padLeft(s string, w int) string {
	n := utf8.RuneCountInString(s)
	if n >= w {
		return s
	}
	return strings.Repeat(" ", w-n) + s
}

// StatusLineWidth is the row width the verb lays out to: columns, Claude
// Code's own COLUMNS, minus its chrome. columns <= 0 means "not under Claude
// Code" and returns 0, the renderer's own default-to-80 case. The margin is
// override parsed as a non-negative integer when it parses as one, else
// claudeCodeMargin. The result is never less than 1.
func StatusLineWidth(columns int, override string) int {
	if columns <= 0 {
		return 0
	}
	margin := claudeCodeMargin
	if n, err := strconv.Atoi(override); err == nil && n >= 0 {
		margin = n
	}
	if w := columns - margin; w > 1 {
		return w
	}
	return 1
}

// ShouldDrainStdin reports whether the verb should drain stdin before
// rendering: true for anything that is not a character device (a pipe,
// Claude Code's common case, or a regular file), false for a tty, so a
// human running the verb by hand gets it back at once instead of blocking
// on EOF that will never come.
func ShouldDrainStdin(mode os.FileMode) bool {
	return mode&os.ModeCharDevice == 0
}

// StatusLineMasterMind is the mastermind identification in StatusLineDoc.
type StatusLineMasterMind struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// StatusLineRow is one binding row in StatusLineDoc.
type StatusLineRow struct {
	Name     string `json:"name"`
	Round    int    `json:"round"`
	Display  string `json:"display"`
	NeedsYou bool   `json:"needs_you"`
	// ReportIn is true when the newest to-mastermind report/question has been
	// delivered and not consumed by a chain: it is a to-mastermind payload and
	// nothing is pending on the mastermind. A delivered report is handled, so
	// the consumer shows REPORT IN rather than NEEDS YOU.
	ReportIn    bool   `json:"report_in"`
	ReportRound int    `json:"report_round,omitempty"`
	Harness     string `json:"harness"`
	Candidate   string `json:"candidate"`
	// On is what the row's actor runs on, as "actor on X" names it: the
	// candidate's short name, else its harness when the set no longer
	// holds the token, with "@server" for a remote runner.
	On      string `json:"on"`
	Waiting string `json:"waiting"`
	Clock   string `json:"clock"`
	Tokens  string `json:"tokens"`
	// Live is the open round's live diff against its baseline tree, copied
	// from BindingStatus.Live, rendered as the middle's "· +A/-R in F"
	// segment. Nil when no round is open, the baseline was never recorded or
	// the git read failed, so the row stays byte-identical to before.
	Live     *LiveDiff `json:"live,omitempty"`
	LastKind string    `json:"last_kind"`
	LastTS   string    `json:"last_ts"`
	Route    string    `json:"route"`
	// Actor is who runs the binding: b.Role when it is set, else "builder",
	// because a builder binding stores an empty role (normRole).
	Actor string `json:"actor"`
	// Shape is the actor's shape, carried through from the status row:
	// store.ShapeReader for a reader, empty for a writer.
	Shape string `json:"shape,omitempty"`
	// Status is the row's one status text (rowStatus), so no surface derives
	// it again; Tone is the colour it takes.
	Status string `json:"status"`
	Tone   string `json:"tone"`
	// Reason explains the status in the middle segment; empty unless there is
	// something to explain.
	Reason string `json:"reason"`
	// Chain is the whole middle segment for the row that stands in for a
	// chain: "chain x · plan 2/4 · reviewing · 1 correction". Empty on every
	// ordinary row, which then keeps the round-and-actor middle.
	Chain string `json:"chain,omitempty"`
	// Text is the row exactly as relevo status --line renders it, without SGR
	// codes and without the trailing newline, laid out at the renderer's
	// default width. Only the --line --json path fills it; it is the OpenCode
	// sidebar's row, so no consumer composes one.
	Text string `json:"text,omitempty"`
}

// StatusLineDoc is the top-level document emitted by relevo status --line --json.
type StatusLineDoc struct {
	MasterMind *StatusLineMasterMind `json:"mastermind"`
	Board      *StatusLineBoard      `json:"board"` // the live board block, null when absent
	Now        time.Time             `json:"now"`
	Rows       []StatusLineRow       `json:"rows"`
}

// StatusLineRows produces one StatusLineRow per r.Bindings entry, in order.
//
// This is the one projection both the Go statusline and the status --line --json
// document read, so a fork's children are collapsed here rather than by either
// consumer: a parent row carries "split 1/2" and gains a row only for a child
// that needs a human. A report with no fork produces exactly the rows it
// produced before.
func StatusLineRows(r Report, now time.Time) []StatusLineRow {
	rows := make([]StatusLineRow, 0, len(r.Bindings))
	for _, b := range r.Bindings {
		// A fork child contributes a row only when a human must act on it; a
		// running or done child is counted inside its parent's split and adds
		// no row of its own.
		if b.Chain != nil && b.Chain.Parent != "" {
			if child, ok := ChainChildRow(b); ok {
				rows = append(rows, child)
			}
			continue
		}
		rows = append(rows, statusLineRowOf(b, now))
	}
	return rows
}

// actorOf is the actor a row names: b.Role when it is set, else fallback --
// "builder" for a binding, whose stored empty role is the builder's
// (normRole), and "chain" for the synthetic chain row.
func actorOf(b BindingStatus, fallback string) string {
	if b.Role != "" {
		return b.Role
	}
	return fallback
}

// statusLineRowOf builds the statusline row for one binding.
func statusLineRowOf(b BindingStatus, now time.Time) StatusLineRow {
	// The synthetic chain row carries no round and no candidate: its whole
	// middle is the chain's own text and its status column is the chain's.
	if b.Chain != nil {
		return statusLineRowOfChain(b)
	}
	harness := harnessSegment(b.BuilderCandidate)
	if b.Server != "" {
		harness += "@" + b.Server
	}
	var on string
	if b.BuilderCandidate != "" {
		on = b.BuilderName
		if on == "" {
			on = harnessSegment(b.BuilderCandidate)
		}
		if b.Server != "" {
			on += "@" + b.Server
		}
	}
	var lastKind, lastTS string
	if b.LastPayload != nil {
		lastKind = string(b.LastPayload.Kind)
		if !b.LastPayload.TS.IsZero() {
			lastTS = b.LastPayload.TS.UTC().Format(time.RFC3339)
		}
	}
	toMasterMindPayload := b.LastPayload != nil &&
		b.LastPayload.Direction == store.DirToMasterMind &&
		(b.LastPayload.Kind == store.KindReport || b.LastPayload.Kind == store.KindQuestion)

	// A payload still waiting on the mastermind is only a fault when relevo
	// cannot push it; which route that is lives in pendingStalled.
	pending := b.Pending != nil
	stalled := pendingStalled(b, pending, now)

	needsYou := b.Display == "NEEDS YOU" || stalled
	reportRound := 0
	if toMasterMindPayload {
		reportRound = b.LastPayload.Round
	}
	reportIn := toMasterMindPayload && !pending && !isConsumedPayload(b.LastPayload)
	status, tone := rowStatus(b, needsYou, reportIn)
	reason := ""
	if needsYou {
		reason = waiting(b)
	} else if isConsumedPayload(b.LastPayload) {
		reason = b.LastPayload.Note
	} else if b.Detail != "" {
		reason = b.Detail
	}
	return StatusLineRow{
		Name:        b.Name,
		Round:       b.Round,
		Display:     b.Display,
		NeedsYou:    needsYou,
		ReportIn:    reportIn,
		ReportRound: reportRound,
		Harness:     harness,
		Candidate:   b.BuilderCandidate,
		On:          on,
		Waiting:     waiting(b),
		Clock:       roundClock(b, now),
		Tokens:      roundTokens(b),
		Live:        b.Live,
		LastKind:    lastKind,
		LastTS:      lastTS,
		Route:       b.MasterMindRoute,
		Actor:       actorOf(b, "builder"),
		Shape:       b.Shape,
		Status:      status,
		Tone:        tone,
		Reason:      reason,
	}
}
