package relevo

import (
	"fmt"
	"math"
	"strings"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// logLineIndent is the width of the timestamp column plus its two-space
// gap, so the usage line sits under the "round" column.
const logLineIndent = 21

// LogLine is the one text form of a log entry, shared by `relevo log` and
// the ui's log tab. Line one is the format both have always printed; a
// second line, indented and marked ⎿ like the status log tail, carries the
// round's usage when the entry has one (#142).
func LogLine(e store.LogEntry) string {
	first := fmt.Sprintf("%s  round %-3d %-10s %-9s %s %s",
		e.TS.Local().Format("2006-01-02 15:04:05"), e.Round, e.Direction, e.Kind, e.Path, e.Note)
	if e.Outcome != "" {
		first += " outcome=" + e.Outcome
	}
	if e.Flagged > 0 {
		first += fmt.Sprintf(" flagged=%d", e.Flagged)
	}
	if e.Flagged > 0 && e.FlaggedBy != "" {
		first += " by=" + e.FlaggedBy
	}
	if e.Classify != nil && e.Classify.Note == "" {
		first += fmt.Sprintf(" p=%.2f", e.Classify.Max)
	}
	if e.Classify != nil && e.Classify.Partial {
		first += " partial"
	}
	if e.Late {
		first += " late"
	}
	if store.IsPromptKind(e.Kind) && e.Tier != "" {
		first += " tier=" + e.Tier
	}
	if e.Gate != nil {
		first += " gate=" + e.Gate.Result
	}
	if e.Kind == store.KindReport && e.BuilderSession != nil {
		first += fmt.Sprintf(" session=%s:%s", e.BuilderSession.Kind, short8(e.BuilderSession.ID))
	}
	if e.Kind == store.KindReport && e.Rusage != nil {
		first += fmt.Sprintf(" cpu %s peak %s", shortCPU(e.Rusage.CPUMS), shortBytes(e.Rusage.PeakMemBytes))
	}
	if e.Usage == nil {
		return first
	}
	return first + "\n" + strings.Repeat(" ", logLineIndent) + "⎿ " + usage.Line(*e.Usage)
}

// short8 is the first eight characters of id, for the log line's session=
// suffix: enough to name a session, not so much that it wraps the line.
func short8(id string) string {
	r := []rune(id)
	if len(r) > 8 {
		return string(r[:8])
	}
	return id
}

// shortCPU renders a round's CPU time at seconds resolution: tenths of a
// second below a minute, minutes and seconds below an hour, then hours and
// minutes. usage.ShortDuration answers "<1m" for every one of those, which
// collapses the 8.5s/19.7s rounds this number is compared for (#299); the
// wall-clock formatter is left to its other callers.
func shortCPU(ms int64) string {
	if ms <= 0 {
		return ""
	}
	tenths := (ms + 50) / 100
	if tenths < 600 {
		return fmt.Sprintf("%d.%ds", tenths/10, tenths%10)
	}
	s := tenths / 10
	if s < 3600 {
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
}

// shortBytes: bytes as "640KB", "850MB", "1.2GB" -- KB and MB round to a
// whole number, GB and above keep one decimal. Rounds half up.
func shortBytes(n int64) string {
	switch {
	case n < 1_000:
		return fmt.Sprintf("%dB", n)
	case n < 1_000_000:
		return fmt.Sprintf("%dKB", int64(math.Round(float64(n)/1_000)))
	case n < 1_000_000_000:
		return fmt.Sprintf("%dMB", int64(math.Round(float64(n)/1_000_000)))
	default:
		return fmt.Sprintf("%.1fGB", float64(n)/1_000_000_000)
	}
}
