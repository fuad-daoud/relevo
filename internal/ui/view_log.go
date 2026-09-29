package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/sanitize"
	"github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/ui/dash"
)

var (
	pickRe       = regexp.MustCompile(`picked (\S+) for builder: order #(\d+)`)
	exitCodeRe   = regexp.MustCompile(`code (\d+)`)
	switchRe     = regexp.MustCompile(`^switched builder \((.*?)\): picked (\S+) for builder: order #(\d+)`)
	switchExitRe = regexp.MustCompile(`exited \(code \d+\) without (?:a report|an output)`)
)

// shapeOf is a binding's shape as the log view reads it: "" when there is no
// lookup or the store does not hold the name, which words as a writer.
func shapeOf(shapes func(string) string, binding string) string {
	if shapes == nil {
		return ""
	}
	return shapes(binding)
}

// outputClause words a sentence about the artifact that was not written:
// "without an output" for a reader, "without a report" for a writer.
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
	Err     bool           // a failed action: Detail in redStyle
}

type logView struct {
	events    []db.EventLogRow
	hist      availability.History
	revs      []db.RevisionRow
	loaded    bool
	err       error
	fetching  bool
	lastFetch time.Time
	cursor    int // index into the rendered entry list (day rules excluded; see §5.3)
	top       int // first body line drawn
	follow    bool
	filter    string
	input     textinput.Model
	editing   bool
	// shapes answers a binding's stored shape, so a row's word can follow it.
	// nil (and "" for an unknown name) words as a writer.
	shapes func(binding string) string
}

type eventLogMsg struct {
	events []db.EventLogRow
	hist   availability.History
	revs   []db.RevisionRow
	err    error
	at     time.Time
	shapes func(binding string) string
}

// bindingShapes is the log view's shape lookup: a closure over the runtime's
// store that answers a binding's shape, or "" for a name the store no longer
// holds -- which words as a writer, exactly as today.
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

func newLogView(env Env) (View, tea.Cmd) {
	in := newTextInput()
	in.Prompt = ""
	v := logView{
		follow: true,
		input:  in,
	}
	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	var base relevo.Runtime
	if env.Src != nil {
		base = env.Src.Base()
	}
	return v, fetchEventLog(env.Ctx, base, now)
}

func fetchEventLog(ctx context.Context, rt relevo.Runtime, now time.Time) tea.Cmd {
	return func() tea.Msg {
		if rt.DB == nil {
			return eventLogMsg{err: relevo.ErrNoDatabase, at: now}
		}
		events, err := rt.DB.RecentEvents(now.Add(-7*24*time.Hour), 3000)
		if err != nil {
			return eventLogMsg{err: err, at: now}
		}
		revs, err := rt.DB.Revisions(0)
		if err != nil {
			return eventLogMsg{err: err, at: now}
		}
		hist, err := availability.LoadHistory(relevo.AvailabilityDeps(rt))
		if err != nil {
			hist = availability.History{}
		}
		return eventLogMsg{
			events: events,
			hist:   hist,
			revs:   revs,
			at:     now,
			shapes: bindingShapes(rt),
		}
	}
}

type foldKey struct {
	binding string
	roundID string
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

func buildLogEntries(events []db.EventLogRow, hist availability.History, revs []db.RevisionRow, actions []actionEntry, since time.Time, name func(token string) string, shapes func(binding string) string) []logEntry {
	picks := make(map[foldKey]string)
	drifts := make(map[foldKey]string)
	diffs := make(map[foldKey]string)
	for _, e := range events {
		k := foldKey{binding: e.BindingName, roundID: derefStr(e.RoundID)}
		switch e.Kind {
		case "pick":
			if _, ok := picks[k]; !ok {
				picks[k] = derefStr(e.Note)
			}
		case "drift":
			if _, ok := drifts[k]; !ok {
				drifts[k] = derefStr(e.Note)
			}
		case "diff":
			if _, ok := diffs[k]; !ok {
				diffs[k] = derefStr(e.Note)
			}
		}
	}

	mapName := func(tok string) string {
		if name != nil {
			return name(tok)
		}
		return tok
	}

	var out []logEntry

	for _, e := range events {
		k := foldKey{binding: e.BindingName, roundID: derefStr(e.RoundID)}
		roundNum := 0
		if e.Round != nil {
			roundNum = *e.Round
		}
		shape := shapeOf(shapes, e.BindingName)

		if store.IsPromptKind(store.Kind(e.Kind)) {
			var parts []string
			if pickNote, ok := picks[k]; ok {
				if m := pickRe.FindStringSubmatch(pickNote); m != nil {
					parts = append(parts, fmt.Sprintf("on %s (#%s)", mapName(m[1]), m[2]))
				}
			}
			var ej struct {
				Tier string `json:"tier"`
			}
			_ = json.Unmarshal([]byte(e.EntryJSON), &ej)
			if ej.Tier != "" {
				parts = append(parts, ej.Tier)
			}
			if driftNote, ok := drifts[k]; ok && roundNum > 1 {
				driftClean := strings.ReplaceAll(driftNote, ",", "")
				parts = append(parts, fmt.Sprintf("edited since r%d: %s", roundNum-1, driftClean))
			}
			out = append(out, logEntry{
				At:      e.TS,
				Binding: e.BindingName,
				Round:   roundNum,
				Word:    "sent",
				Style:   accentStyle,
				Detail:  strings.ReplaceAll(strings.Join(parts, " · "), "\n", " · "),
			})
			continue
		}

		switch e.Kind {
		case "report":
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
				style = greenStyle
			case "halted":
				word = "halted"
				style = warnStyle
			case "blocked":
				word = "blocked"
				style = warnStyle
			default:
				word = "reported"
				if shape == store.ShapeReader {
					word = "artifact"
				}
				style = mutedStyle
			}

			var parts []string
			if ej.Outcome == "halted" && ej.HaltedAt != "" {
				parts = append(parts, "at "+ej.HaltedAt)
			}
			if diffNote, ok := diffs[k]; ok && diffNote != "" {
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
			}
			if e.Tokens != nil {
				parts = append(parts, stats.ShortTokens(*e.Tokens)+" tokens")
			}
			if e.DurationMS != nil {
				parts = append(parts, shortDurationText(*e.DurationMS))
			}
			out = append(out, logEntry{
				At:      e.TS,
				Binding: e.BindingName,
				Round:   roundNum,
				Word:    word,
				Style:   style,
				Detail:  strings.ReplaceAll(strings.Join(parts, " · "), "\n", " · "),
			})

		case "exit":
			note := derefStr(e.Note)
			clause := outputClause(shape)
			detail := clause
			if m := exitCodeRe.FindStringSubmatch(note); m != nil {
				detail = fmt.Sprintf("%s (code %s)", clause, m[1])
			}
			out = append(out, logEntry{
				At:      e.TS,
				Binding: e.BindingName,
				Round:   roundNum,
				Word:    "exited",
				Style:   redStyle,
				Detail:  strings.ReplaceAll(detail, "\n", " · "),
			})

		case "switch":
			note := derefStr(e.Note)
			var detail string
			if m := switchRe.FindStringSubmatch(note); m != nil {
				why := m[1]
				tok := m[2]
				order := m[3]
				if strings.HasPrefix(why, "rate-limited: ") {
					rest := strings.TrimPrefix(why, "rate-limited: ")
					why = statsGateReason(rest)
				} else if switchExitRe.MatchString(why) {
					why = "exited " + outputClause(shape)
				}
				detail = fmt.Sprintf("to %s (#%s) · %s", mapName(tok), order, why)
			} else {
				detail = clip(note, 80)
			}
			out = append(out, logEntry{
				At:      e.TS,
				Binding: e.BindingName,
				Round:   roundNum,
				Word:    "switched",
				Style:   warnStyle,
				Detail:  strings.ReplaceAll(detail, "\n", " · "),
			})

		case "question":
			out = append(out, logEntry{
				At:      e.TS,
				Binding: e.BindingName,
				Round:   roundNum,
				Word:    "needs you",
				Style:   warnStyle,
				Detail:  "blocked at a dialog",
			})

		case "answer":
			out = append(out, logEntry{
				At:      e.TS,
				Binding: e.BindingName,
				Round:   roundNum,
				Word:    "answered",
				Style:   mutedStyle,
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
				Style:   mutedStyle,
				Detail:  strings.ReplaceAll(derefStr(e.Note), "\n", " · "),
			})
		}
	}

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
			style = warnStyle
			detail = h.Provider
			if reason := statsGateReasonText(h.Note); reason != "" {
				detail += " · " + reason
			}
		case availability.Cleared:
			word = "cleared"
			style = greenStyle
			detail = h.Provider
		default:
			word = string(h.Kind)
			style = mutedStyle
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

	for _, rv := range revs {
		if !since.IsZero() && rv.At.Before(since) {
			continue
		}
		out = append(out, logEntry{
			At:      rv.At,
			Binding: "",
			Round:   0,
			Word:    "config",
			Style:   mutedStyle,
			Detail:  strings.ReplaceAll(fmt.Sprintf("rev %d · %s", rv.Rev, rv.Message), "\n", " · "),
		})
	}

	for _, a := range actions {
		if !since.IsZero() && a.At.Before(since) {
			continue
		}
		out = append(out, logEntry{
			At:      a.At,
			Binding: "you",
			Round:   0,
			Word:    a.Verb,
			Style:   textStyle,
			Detail:  strings.ReplaceAll(a.Text, "\n", " · "),
			Err:     a.Err,
		})
	}

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

func (v logView) Crumbs() []string { return []string{"log"} }

func (v logView) Capturing() bool { return v.editing }

func (v logView) Keys() []KeyHelp {
	return []KeyHelp{
		{"↑↓", "move"},
		{"enter", "open round"},
		{"/", "filter"},
		{"f", "follow"},
	}
}

func (v logView) filteredEntries(env Env) []logEntry {
	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	since := now.Add(-7 * 24 * time.Hour)
	var nameOf func(string) string
	if env.Src != nil && env.Src.Base().Candidates != nil {
		nameOf = env.Src.Base().Candidates.NameOf
	}
	all := buildLogEntries(v.events, v.hist, v.revs, env.ActionLog, since, nameOf, v.shapes)
	if v.filter == "" {
		return all
	}
	q := strings.ToLower(v.filter)
	var filtered []logEntry
	for _, e := range all {
		b := strings.ToLower(e.Binding)
		w := strings.ToLower(e.Word)
		d := strings.ToLower(e.Detail)
		if strings.Contains(b, q) || strings.Contains(w, q) || strings.Contains(d, q) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func (v logView) Context(env Env) (string, string) {
	if v.err != nil && !v.loaded {
		return "   " + errorStyle.Render(v.err.Error()), ""
	}
	if !v.loaded {
		return "   " + mutedStyle.Render("loading…"), ""
	}

	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	since := now.Add(-7 * 24 * time.Hour)
	var nameOf func(string) string
	if env.Src != nil && env.Src.Base().Candidates != nil {
		nameOf = env.Src.Base().Candidates.NameOf
	}
	entries := buildLogEntries(v.events, v.hist, v.revs, env.ActionLog, since, nameOf, v.shapes)

	todayCount := 0
	counts := make(map[string]int)
	for _, e := range entries {
		if dash.DayLabel(e.At, now, time.Local) == "today" {
			todayCount++
			counts[e.Word]++
		}
	}

	var items []string
	items = append(items, textStyle.Bold(true).Render(fmt.Sprintf("%d", todayCount))+" "+mutedStyle.Render("events today"))
	for _, w := range []string{"sent", "done", "halted", "exited", "switched", "gated"} {
		if c := counts[w]; c > 0 {
			items = append(items, textStyle.Bold(true).Render(fmt.Sprintf("%d", c))+" "+mutedStyle.Render(w))
		}
	}
	left := "   " + strings.Join(items, "   ")

	var right string
	if v.filter != "" {
		right += mutedStyle.Render("/ "+clip(v.filter, 40)) + "   "
	}
	if v.follow {
		right += accentStyle.Render("following")
	} else {
		right += faintStyle.Render("paused")
	}
	right += "   "

	return left, right
}

func (v logView) Update(msg tea.Msg, env Env) (View, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		now := env.Now
		if now.IsZero() {
			now = time.Now()
		}
		if !v.fetching && (v.lastFetch.IsZero() || now.Sub(v.lastFetch) >= 5*time.Second) {
			v.fetching = true
			var base relevo.Runtime
			if env.Src != nil {
				base = env.Src.Base()
			}
			return v, fetchEventLog(env.Ctx, base, now)
		}
		return v, nil

	case eventLogMsg:
		v.fetching = false
		v.lastFetch = msg.at
		if msg.err != nil {
			v.err = msg.err
		} else {
			v.events = msg.events
			v.hist = msg.hist
			v.revs = msg.revs
			v.shapes = msg.shapes
			v.loaded = true
			v.err = nil
		}
		if v.follow {
			v.cursor = 0
			v.top = 0
		} else {
			entries := v.filteredEntries(env)
			if v.cursor >= len(entries) && len(entries) > 0 {
				v.cursor = len(entries) - 1
			}
		}
		return v, nil

	case roundOpenMsg:
		if msg.hist != nil {
			next, cmd := newHistRoundView(env, *msg.hist, msg.round)
			return v, push(next, cmd)
		}
		next, cmd := newRoundView(env, msg.key, msg.round)
		return v, push(next, cmd)

	case tea.KeyMsg:
		if v.editing {
			switch msg.Type {
			case tea.KeyEnter:
				v.editing = false
				v.filter = strings.TrimSpace(v.input.Value())
				v.cursor = 0
				v.top = 0
				v.input.Blur()
				return v, nil
			case tea.KeyEsc:
				v.editing = false
				v.input.Blur()
				return v, nil
			default:
				var cmd tea.Cmd
				v.input, cmd = v.input.Update(msg)
				return v, cmd
			}
		}

		entries := v.filteredEntries(env)
		pageSize := bodyHeight(env) - 3
		if pageSize < 1 {
			pageSize = 1
		}

		switch msg.String() {
		case "up", "k":
			if v.cursor > 0 {
				v.cursor--
				v.follow = false
			}
			return v, nil
		case "down", "j":
			if v.cursor < len(entries)-1 {
				v.cursor++
			}
			v.follow = false
			return v, nil
		case "pgup":
			if v.cursor > 0 {
				v.cursor -= pageSize
				if v.cursor < 0 {
					v.cursor = 0
				}
				v.follow = false
			}
			return v, nil
		case "pgdown", " ":
			if len(entries) > 0 {
				v.cursor += pageSize
				if v.cursor >= len(entries) {
					v.cursor = len(entries) - 1
				}
			}
			v.follow = false
			return v, nil
		case "home":
			v.cursor = 0
			v.top = 0
			v.follow = true
			return v, nil
		case "end":
			if len(entries) > 0 {
				v.cursor = len(entries) - 1
			}
			v.follow = false
			return v, nil
		case "f":
			v.follow = true
			v.cursor = 0
			v.top = 0
			return v, nil
		case "enter":
			if v.cursor >= 0 && v.cursor < len(entries) {
				e := entries[v.cursor]
				if e.Binding != "" && e.Binding != "you" && e.Round > 0 {
					return v, openRound(env, dash.JumpMsg{BindingName: e.Binding, Round: e.Round})
				}
			}
			return v, nil
		case "/":
			v.editing = true
			in := v.input
			if in.Prompt == "" && in.Placeholder == "" && in.Width == 0 {
				in = newTextInput()
				in.Prompt = ""
			}
			in.SetValue(v.filter)
			cmd := in.Focus()
			v.input = in
			return v, cmd
		}
	}

	return v, nil
}

func pluralEvent(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func (v logView) Body(env Env, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}

	cw := width - 6
	if cw < 0 {
		cw = 0
	}

	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}

	blankRow := "   " + strings.Repeat(" ", cw) + "   "

	var line1 string
	if v.editing {
		line1 = "   " + fit("/ "+v.input.View(), cw) + "   "
	} else {
		detailW := cw - 5 - 2 - 14 - 2 - 4 - 2 - 9 - 2
		if detailW < 0 {
			detailW = 0
		}
		header := fmt.Sprintf("%-5s  %-14s  %-4s  %-9s  %-*s", "TIME", "BINDING", "RND", "EVENT", detailW, "DETAIL")
		line1 = "   " + fit(faintStyle.Bold(true).Render(header), cw) + "   "
	}

	entries := v.filteredEntries(env)

	if v.loaded && len(entries) == 0 {
		var lines []string
		lines = append(lines, blankRow, line1)
		lines = append(lines, "   "+fit(emptyStyle.Render("no events in the last 7 days"), cw)+"   ")
		for len(lines) < height {
			lines = append(lines, blankRow)
		}
		if len(lines) > height {
			lines = lines[:height]
		}
		return strings.Join(lines, "\n")
	}

	var runs [][]logEntry
	var labels []string
	var runIndices [][]int

	i := 0
	for i < len(entries) {
		lbl := dash.DayLabel(entries[i].At, now, time.Local)
		j := i + 1
		for j < len(entries) {
			if dash.DayLabel(entries[j].At, now, time.Local) != lbl {
				break
			}
			j++
		}
		runs = append(runs, entries[i:j])
		labels = append(labels, lbl)
		var idxs []int
		for k := i; k < j; k++ {
			idxs = append(idxs, k)
		}
		runIndices = append(runIndices, idxs)
		i = j
	}

	entryLine := make(map[int]int)
	var bodyLines []string

	detailW := cw - 5 - 2 - 14 - 2 - 4 - 2 - 9 - 2
	if detailW < 0 {
		detailW = 0
	}

	for r := range runs {
		label := labels[r]
		rightText := " " + pluralEvent(len(runs[r]), "event")
		leftWidth := lipgloss.Width(label) + 1
		rightWidth := lipgloss.Width(rightText)
		fill := cw - leftWidth - rightWidth
		if fill < 0 {
			fill = 0
		}
		ruleContent := mutedStyle.Render(label) + " " + gridStyle.Render(strings.Repeat("┈", fill)) + faintStyle.Render(rightText)
		dayRuleLine := "   " + fit(ruleContent, cw) + "   "
		bodyLines = append(bodyLines, dayRuleLine)

		for subIdx, e := range runs[r] {
			entryIdx := runIndices[r][subIdx]
			entryLine[entryIdx] = len(bodyLines)
			isCursor := (entryIdx == v.cursor)

			timeText := e.At.Local().Format("15:04")
			tStyle := mutedStyle
			if isCursor {
				tStyle = tStyle.Background(selBandStyle.GetBackground())
			}
			col1 := tStyle.Render(timeText)

			bindText := e.Binding
			var bStyle lipgloss.Style
			if bindText == "" {
				bindText = "—"
				bStyle = faintStyle
			} else if bindText == "you" {
				bStyle = accentStyle
			} else {
				bStyle = textStyle
			}
			if isCursor {
				bStyle = bStyle.Bold(true).Background(selBandStyle.GetBackground())
			}
			col2 := bStyle.Render(pad(clip(bindText, 14), 14))

			rndText := ""
			if e.Round > 0 {
				rndText = fmt.Sprintf("r%d", e.Round)
			}
			rStyle := mutedStyle
			if isCursor {
				rStyle = rStyle.Background(selBandStyle.GetBackground())
			}
			col3 := rStyle.Render(pad(rndText, 4))

			eStyle := e.Style
			if isCursor {
				eStyle = eStyle.Background(selBandStyle.GetBackground())
			}
			col4 := eStyle.Render(pad(clip(e.Word, 9), 9))

			dStyle := mutedStyle
			if e.Err {
				dStyle = redStyle
			}
			if isCursor {
				dStyle = dStyle.Background(selBandStyle.GetBackground())
			}
			detailText := clip(e.Detail, detailW)
			col5 := dStyle.Render(detailText)

			sep := "  "
			if isCursor {
				sep = selBandStyle.Render("  ")
			}
			rowContent := col1 + sep + col2 + sep + col3 + sep + col4 + sep + col5
			var line string
			if isCursor {
				if lipgloss.Width(rowContent) > cw {
					rowContent = lipgloss.NewStyle().MaxWidth(cw).Render(rowContent)
				}
				if w := lipgloss.Width(rowContent); w < cw {
					rowContent += selBandStyle.Render(strings.Repeat(" ", cw-w))
				}
				line = "   " + rowContent + "   "
			} else {
				line = "   " + fit(rowContent, cw) + "   "
			}
			bodyLines = append(bodyLines, line)
		}
	}

	visibleHeight := height - 2
	if visibleHeight < 0 {
		visibleHeight = 0
	}

	top := v.top
	if len(entries) > 0 && v.cursor >= 0 && v.cursor < len(entries) {
		cursorLine := entryLine[v.cursor]
		if cursorLine < top {
			top = cursorLine
		}
		if cursorLine >= top+visibleHeight {
			top = cursorLine - visibleHeight + 1
		}
		if top > len(bodyLines)-visibleHeight {
			top = len(bodyLines) - visibleHeight
		}
		if top < 0 {
			top = 0
		}
	}

	var displayed []string
	if top < len(bodyLines) {
		end := top + visibleHeight
		if end > len(bodyLines) {
			end = len(bodyLines)
		}
		displayed = bodyLines[top:end]
	}

	allLines := []string{blankRow, line1}
	allLines = append(allLines, displayed...)
	for len(allLines) < height {
		allLines = append(allLines, blankRow)
	}
	if len(allLines) > height {
		allLines = allLines[:height]
	}

	return strings.Join(allLines, "\n")
}
