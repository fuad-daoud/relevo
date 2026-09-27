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

// PendingNeedsYouAfter is how long an undelivered report/question may wait on a
// route relevo can push before it counts as NEEDS YOU: a push in flight
// must not flash NEEDS YOU, but one that has waited this long is stalled.
const PendingNeedsYouAfter = 60 * time.Second

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

	nameW, statusW, clockW := 0, 0, 0
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

	var sb strings.Builder
	for _, row := range rows {
		sb.WriteString(renderedStatusLineRow(row, nameW, statusW, clockW, columns))
	}
	return sb.String()
}

// renderedStatusLineRow lays out one StatusLineRow at the shared column
// widths. The tone colours only the visible status text; the padding counts
// the uncoloured runes, so the colour never widens the column.
func renderedStatusLineRow(row StatusLineRow, nameW, statusW, clockW, columns int) string {
	dotColoured := ansiDim + "○" + ansiReset
	if row.NeedsYou {
		dotColoured = ansiNeedsYou + "●" + ansiReset
	}

	round := row.Round
	if row.ReportRound > 0 {
		round = row.ReportRound
	}

	mid := "r" + strconv.Itoa(round) + " · " + row.Actor
	if row.On != "" {
		mid += " on " + row.On
	}
	if row.Reason != "" {
		mid += " · " + row.Reason
	}
	if row.Tokens != "" {
		mid += " · " + row.Tokens
	}

	colour := ""
	switch row.Tone {
	case "needs":
		colour = ansiNeedsYou
	case "report":
		colour = ansiReportIn
	case "phase":
		colour = ansiDim
	}

	leftW := 2 + nameW + 2
	midW := columns - leftW - 1 - statusW - 2 - clockW

	if midW < 8 {
		// Unpadded fallback: a row this narrow cannot afford three aligned
		// columns, so the status and the clock follow the middle cell.
		status := row.Status
		if colour != "" {
			status = colour + status + ansiReset
		}
		return dotColoured + " " + row.Name + "  " + mid + " · " + status + " · " + row.Clock + "\n"
	}

	status := pad(row.Status, statusW)
	if colour != "" {
		status = colour + status + ansiReset
	}
	right := status + "  " + padLeft(row.Clock, clockW)
	return dotColoured + " " + pad(row.Name, nameW) + "  " + pad(truncate(mid, midW), midW) + " " + right + "\n"
}

// RenderPlannerLine is the statusline's first line: the planner's own name,
// dim, so each terminal shows which planner it is. An empty name renders
// nothing -- a session relevo did not identify keeps the statusline it had.
// When columns > 0 and the line would overflow it, the visible text is cut
// with the same truncate the binding rows use, before the colour codes wrap it.
func RenderPlannerLine(name string, columns int) string {
	if name == "" {
		return ""
	}
	text := "planner " + name
	if columns > 0 && utf8.RuneCountInString(text) > columns {
		text = truncate(text, columns)
	}
	return ansiDim + text + ansiReset + "\n"
}

// phase is the bare phase of a binding's last payload: the wording the middle
// segment uses when it has to carry one. It ignores the note and the outcome,
// because a delivered report states those in its status column instead.
func phase(b BindingStatus) string {
	if b.LastPayload == nil {
		return "no plan yet"
	}
	switch b.LastPayload.Kind {
	case store.KindPlan:
		return "plan sent"
	case store.KindReport:
		return "report in"
	case store.KindQuestion:
		return "question in"
	case store.KindAnswer:
		return "answered"
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
			base = fmt.Sprintf("report in (%s)", b.LastPayload.Note)
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
		if b.Display == "HELD" {
			return b.Display, "held"
		}
		return b.Display, "quiet"
	}
	if reportIn && b.LastPayload != nil {
		switch b.LastPayload.Kind {
		case store.KindReport:
			status = "REPORT IN"
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

// StatusLinePlanner is the planner identification in StatusLineDoc.
type StatusLinePlanner struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// StatusLineRow is one binding row in StatusLineDoc.
type StatusLineRow struct {
	Name     string `json:"name"`
	Round    int    `json:"round"`
	Display  string `json:"display"`
	NeedsYou bool   `json:"needs_you"`
	// ReportIn is true when the newest to-planner report/question has been
	// delivered: it is a to-planner payload and nothing is pending on the
	// planner. A delivered report is handled, so the consumer shows REPORT IN
	// rather than NEEDS YOU.
	ReportIn    bool   `json:"report_in"`
	ReportRound int    `json:"report_round,omitempty"`
	Harness     string `json:"harness"`
	Candidate   string `json:"candidate"`
	// On is what the row's actor runs on, as "actor on X" names it: the
	// candidate's short name, else its harness when the set no longer
	// holds the token, with "@server" for a remote runner.
	On       string `json:"on"`
	Waiting  string `json:"waiting"`
	Clock    string `json:"clock"`
	Tokens   string `json:"tokens"`
	LastKind string `json:"last_kind"`
	LastTS   string `json:"last_ts"`
	Route    string `json:"route"`
	// Actor is who runs the binding: b.Role when it is set, else "builder",
	// because a builder binding stores an empty role (normRole).
	Actor string `json:"actor"`
	// Status is the row's one status text (rowStatus), so no surface derives
	// it again; Tone is the colour it takes.
	Status string `json:"status"`
	Tone   string `json:"tone"`
	// Reason explains the status in the middle segment; empty unless there is
	// something to explain.
	Reason string `json:"reason"`
}

// StatusLineDoc is the top-level document emitted by relevo status --line --json.
type StatusLineDoc struct {
	Planner *StatusLinePlanner `json:"planner"`
	Now     time.Time          `json:"now"`
	Rows    []StatusLineRow    `json:"rows"`
}

// StatusLineRows produces one StatusLineRow per r.Bindings entry, in order.
func StatusLineRows(r Report, now time.Time) []StatusLineRow {
	rows := make([]StatusLineRow, 0, len(r.Bindings))
	for _, b := range r.Bindings {
		rows = append(rows, statusLineRowOf(b, now))
	}
	return rows
}

// statusLineRowOf builds the statusline row for one binding.
func statusLineRowOf(b BindingStatus, now time.Time) StatusLineRow {
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
	toPlannerPayload := b.LastPayload != nil &&
		b.LastPayload.Direction == store.DirToPlanner &&
		(b.LastPayload.Kind == store.KindReport || b.LastPayload.Kind == store.KindQuestion)

	// A payload still waiting on the planner is only a fault when relevo
	// cannot push it (pull, or no live route) or it has waited longer than
	// PendingNeedsYouAfter; a push in flight must not flash NEEDS YOU.
	pending := b.Pending != nil
	stalled := pending && (b.PlannerRoute == "pull" || !b.PlannerRouteLive ||
		(b.LastPayload != nil && now.Sub(b.LastPayload.TS) > PendingNeedsYouAfter))

	needsYou := b.Display == "NEEDS YOU" || stalled
	reportRound := 0
	if toPlannerPayload {
		reportRound = b.LastPayload.Round
	}
	reportIn := toPlannerPayload && !pending
	actor := b.Role
	if actor == "" {
		actor = "builder"
	}
	status, tone := rowStatus(b, needsYou, reportIn)
	reason := ""
	if needsYou {
		reason = waiting(b)
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
		LastKind:    lastKind,
		LastTS:      lastTS,
		Route:       b.PlannerRoute,
		Actor:       actor,
		Status:      status,
		Tone:        tone,
		Reason:      reason,
	}
}
