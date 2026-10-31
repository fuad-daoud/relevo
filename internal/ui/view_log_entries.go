package ui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/sanitize"
	datastats "github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/ui/stats"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

var (
	pickRe       = regexp.MustCompile(`picked (\S+) for builder: order #(\d+)`)
	exitCodeRe   = regexp.MustCompile(`code (\d+)`)
	switchRe     = regexp.MustCompile(`^switched builder \((.*?)\): picked (\S+) for builder: order #(\d+)`)
	switchExitRe = regexp.MustCompile(`exited \(code \d+\) without (?:a report|an output)`)
)

func shapeOf(shapes func(string) string, binding string) string {
	if shapes == nil {
		return ""
	}
	return shapes(binding)
}

func outputClause(shape string) string {
	if shape == store.ShapeReader {
		return "without an output"
	}
	return "without a report"
}

type logEntry struct {
	At      time.Time
	Binding string         // "" draws as "—"; "you" for a cockpit action
	Round   int            // 0 = none
	Word    string         // the EVENT cell
	Style   lipgloss.Style // the EVENT cell's style
	Detail  string         // one line; newlines replaced by " · "
	Err     bool           // a failed action: Detail in widget.RedStyle
}

// bindingShapes is the log view's shape lookup: a closure over the runtime's
// store that answers a binding's shape, or "" for a name the store does not
// hold -- which words as a writer.
func bindingShapes(rt relevo.Runtime) func(string) string {
	if rt.Store == nil {
		return nil
	}
	st := rt.Store
	return func(name string) string {
		b, err := st.Load(name)
		if err != nil {
			return ""
		}
		return b.Shape
	}
}

func derefStr(p *string) string {
	if p != nil {
		return *p
	}
	return ""
}

func shortDurationText(ms int64) string {
	switch {
	case ms < 60_000:
		return "<1m"
	}
	minutes := ms / 60_000
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	return fmt.Sprintf("%dh%02dm", minutes/60, minutes%60)
}

type logFold struct {
	picks  map[foldKey]string
	drifts map[foldKey]string
	diffs  map[foldKey]string
}

func foldNotes(events []db.EventLogRow) logFold {
	f := logFold{
		picks:  make(map[foldKey]string),
		drifts: make(map[foldKey]string),
		diffs:  make(map[foldKey]string),
	}
	for _, e := range events {
		k := foldKey{binding: e.BindingName, roundID: derefStr(e.RoundID)}
		switch e.Kind {
		case "pick":
			if _, ok := f.picks[k]; !ok {
				f.picks[k] = derefStr(e.Note)
			}
		case "drift":
			if _, ok := f.drifts[k]; !ok {
				f.drifts[k] = derefStr(e.Note)
			}
		case "diff":
			if _, ok := f.diffs[k]; !ok {
				f.diffs[k] = derefStr(e.Note)
			}
		}
	}
	return f
}

func mappedName(name func(string) string, tok string) string {
	if name != nil {
		return name(tok)
	}
	return tok
}

func buildLogEntries(events []db.EventLogRow, hist availability.History, revs []db.RevisionRow, actions []widget.ActionEntry, since time.Time, name func(token string) string, shapes func(binding string) string) []logEntry {
	folds := foldNotes(events)
	var out []logEntry
	out = appendEventEntries(out, events, folds, name, shapes)
	out = appendHistoryEntries(out, hist, since)
	out = appendRevisionEntries(out, revs, since)
	out = appendActionEntries(out, actions, since)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].At.After(out[j].At)
	})

	// One pass over every Detail, so the log view draws only sanitised text
	// whatever its source row carried.
	for i := range out {
		out[i].Detail = sanitize.Text(out[i].Detail)
	}

	return out
}

func appendEventEntries(out []logEntry, events []db.EventLogRow, folds logFold, name func(string) string, shapes func(string) string) []logEntry {
	for _, e := range events {
		k := foldKey{binding: e.BindingName, roundID: derefStr(e.RoundID)}
		roundNum := 0
		if e.Round != nil {
			roundNum = *e.Round
		}
		shape := shapeOf(shapes, e.BindingName)

		if store.IsPromptKind(store.Kind(e.Kind)) {
			out = append(out, promptEntry(e, k, roundNum, folds, name))
			continue
		}

		switch e.Kind {
		case "report":
			out = append(out, reportEntry(e, k, roundNum, folds, shape))
		case "exit":
			out = append(out, exitEntry(e, roundNum, shape))
		case "switch":
			out = append(out, switchEntry(e, roundNum, shape, name))
		case "question":
			out = append(out, logEntry{
				At:      e.TS,
				Binding: e.BindingName,
				Round:   roundNum,
				Word:    "needs you",
				Style:   widget.WarnStyle,
				Detail:  "blocked at a dialog",
			})
		case "answer":
			out = append(out, logEntry{
				At:      e.TS,
				Binding: e.BindingName,
				Round:   roundNum,
				Word:    "answered",
				Style:   widget.MutedStyle,
				Detail:  strings.ReplaceAll(derefStr(e.Note), "\n", " · "),
			})
		case "pick", "drift", "diff":
			// no entry of their own (folded above)
		default:
			out = append(out, logEntry{
				At:      e.TS,
				Binding: e.BindingName,
				Round:   roundNum,
				Word:    e.Kind,
				Style:   widget.MutedStyle,
				Detail:  strings.ReplaceAll(derefStr(e.Note), "\n", " · "),
			})
		}
	}
	return out
}

func promptEntry(e db.EventLogRow, k foldKey, roundNum int, folds logFold, name func(string) string) logEntry {
	var parts []string
	if pickNote, ok := folds.picks[k]; ok {
		if m := pickRe.FindStringSubmatch(pickNote); m != nil {
			parts = append(parts, fmt.Sprintf("on %s (#%s)", mappedName(name, m[1]), m[2]))
		}
	}
	var ej struct {
		Tier string `json:"tier"`
	}
	_ = json.Unmarshal([]byte(e.EntryJSON), &ej)
	if ej.Tier != "" {
		parts = append(parts, ej.Tier)
	}
	if driftNote, ok := folds.drifts[k]; ok && roundNum > 1 {
		driftClean := strings.ReplaceAll(driftNote, ",", "")
		parts = append(parts, fmt.Sprintf("edited since r%d: %s", roundNum-1, driftClean))
	}
	return logEntry{
		At:      e.TS,
		Binding: e.BindingName,
		Round:   roundNum,
		Word:    "sent",
		Style:   widget.AccentStyle,
		Detail:  strings.ReplaceAll(strings.Join(parts, " · "), "\n", " · "),
	}
}

func reportEntry(e db.EventLogRow, k foldKey, roundNum int, folds logFold, shape string) logEntry {
	var ej struct {
		Outcome  string `json:"outcome"`
		HaltedAt string `json:"halted_at"`
	}
	_ = json.Unmarshal([]byte(e.EntryJSON), &ej)
	var word string
	var style lipgloss.Style
	switch ej.Outcome {
	case "done":
		word = "done"
		style = widget.GreenStyle
	case "halted":
		word = "halted"
		style = widget.WarnStyle
	case "blocked":
		word = "blocked"
		style = widget.WarnStyle
	default:
		word = "reported"
		if shape == store.ShapeReader {
			word = "artifact"
		}
		style = widget.MutedStyle
	}

	var parts []string
	if ej.Outcome == "halted" && ej.HaltedAt != "" {
		parts = append(parts, "at "+ej.HaltedAt)
	}
	if diffNote, ok := folds.diffs[k]; ok && diffNote != "" {
		parts = append(parts, diffParts(diffNote)...)
	}
	if e.Tokens != nil {
		parts = append(parts, datastats.ShortTokens(*e.Tokens)+" tokens")
	}
	if e.DurationMS != nil {
		parts = append(parts, shortDurationText(*e.DurationMS))
	}
	return logEntry{
		At:      e.TS,
		Binding: e.BindingName,
		Round:   roundNum,
		Word:    word,
		Style:   style,
		Detail:  strings.ReplaceAll(strings.Join(parts, " · "), "\n", " · "),
	}
}

func diffParts(diffNote string) []string {
	var parts []string
	if idx := strings.Index(diffNote, " paths:"); idx >= 0 {
		diffNote = diffNote[:idx]
	}
	files, rest, found := strings.Cut(diffNote, "; ")
	files = strings.TrimSpace(strings.ReplaceAll(files, ",", ""))
	if files != "" {
		parts = append(parts, files)
	}
	if found && rest != "" {
		for _, item := range strings.Split(rest, ", ") {
			item = strings.TrimSpace(item)
			if item == "no commits" {
				item = "0 commits"
			}
			if item != "" {
				parts = append(parts, item)
			}
		}
	}
	return parts
}

func exitEntry(e db.EventLogRow, roundNum int, shape string) logEntry {
	note := derefStr(e.Note)
	clause := outputClause(shape)
	detail := clause
	if m := exitCodeRe.FindStringSubmatch(note); m != nil {
		detail = fmt.Sprintf("%s (code %s)", clause, m[1])
	}
	return logEntry{
		At:      e.TS,
		Binding: e.BindingName,
		Round:   roundNum,
		Word:    "exited",
		Style:   widget.RedStyle,
		Detail:  strings.ReplaceAll(detail, "\n", " · "),
	}
}

func switchEntry(e db.EventLogRow, roundNum int, shape string, name func(string) string) logEntry {
	note := derefStr(e.Note)
	var detail string
	if m := switchRe.FindStringSubmatch(note); m != nil {
		why := m[1]
		tok := m[2]
		order := m[3]
		if strings.HasPrefix(why, "rate-limited: ") {
			rest := strings.TrimPrefix(why, "rate-limited: ")
			why = stats.GateReason(rest)
		} else if switchExitRe.MatchString(why) {
			why = "exited " + outputClause(shape)
		}
		detail = fmt.Sprintf("to %s (#%s) · %s", mappedName(name, tok), order, why)
	} else {
		detail = clip(note, 80)
	}
	return logEntry{
		At:      e.TS,
		Binding: e.BindingName,
		Round:   roundNum,
		Word:    "switched",
		Style:   widget.WarnStyle,
		Detail:  strings.ReplaceAll(detail, "\n", " · "),
	}
}

func appendHistoryEntries(out []logEntry, hist availability.History, since time.Time) []logEntry {
	for _, h := range hist.Events {
		if !since.IsZero() && h.At.Before(since) {
			continue
		}
		var word string
		var style lipgloss.Style
		var detail string

		switch h.Kind {
		case availability.RateLimited, availability.SpawnFailed:
			word = "gated"
			style = widget.WarnStyle
			detail = h.Provider
			if reason := stats.GateReasonText(h.Note); reason != "" {
				detail += " · " + reason
			}
		case availability.Cleared:
			word = "cleared"
			style = widget.GreenStyle
			detail = h.Provider
		default:
			word = string(h.Kind)
			style = widget.MutedStyle
			detail = h.Provider
		}

		out = append(out, logEntry{
			At:      h.At,
			Binding: h.Binding,
			Round:   0,
			Word:    word,
			Style:   style,
			Detail:  strings.ReplaceAll(detail, "\n", " · "),
		})
	}
	return out
}

func appendRevisionEntries(out []logEntry, revs []db.RevisionRow, since time.Time) []logEntry {
	for _, rv := range revs {
		if !since.IsZero() && rv.At.Before(since) {
			continue
		}
		out = append(out, logEntry{
			At:      rv.At,
			Binding: "",
			Round:   0,
			Word:    "config",
			Style:   widget.MutedStyle,
			Detail:  strings.ReplaceAll(fmt.Sprintf("rev %d · %s", rv.Rev, rv.Message), "\n", " · "),
		})
	}
	return out
}

func appendActionEntries(out []logEntry, actions []widget.ActionEntry, since time.Time) []logEntry {
	for _, a := range actions {
		if !since.IsZero() && a.At.Before(since) {
			continue
		}
		out = append(out, logEntry{
			At:      a.At,
			Binding: "you",
			Round:   0,
			Word:    a.Verb,
			Style:   widget.TextStyle,
			Detail:  strings.ReplaceAll(a.Text, "\n", " · "),
			Err:     a.Err,
		})
	}
	return out
}
