package dash

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/histq"
	"github.com/muesli/termenv"
)

func TestShortRepo(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://github.com/fuad-daoud/relay", "fuad-daoud/relay"}, // name-guard: legacy
		{"git@github.com:x/persist.git", "x/persist"},
		{"/home/fuad/sandbox/relevo-oc-test/.git", "sandbox/relevo-oc-test"},
		{"relay", "relay"}, // name-guard: legacy
	}
	for _, tc := range cases {
		if got := ShortRepo(tc.in); got != tc.want {
			t.Errorf("ShortRepo(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestOutcomeWord(t *testing.T) {
	m := newTestModel(t, "")
	m.Running = func(b string, r int) bool {
		return b == "running-b" && r == 1
	}
	cases := []struct {
		row       db.RoundRow
		wantWord  string
		wantStyle lipgloss.Style
	}{
		{db.RoundRow{Outcome: db.OutcomeOpen, BindingName: "running-b", Number: 1}, "running", m.styles.Accent},
		{db.RoundRow{Outcome: db.OutcomeOpen, BindingName: "other-b", Number: 1}, "open", m.styles.Dim},
		{db.RoundRow{Outcome: db.OutcomeExited}, "exited", m.styles.Danger},
		{db.RoundRow{Outcome: db.OutcomeHalted}, "halted", m.styles.Warn},
		{db.RoundRow{Outcome: db.OutcomeSwitched}, "switched", m.styles.Warn},
		{db.RoundRow{Outcome: db.OutcomeDoneNoReport}, "no output", m.styles.Faint},
		{db.RoundRow{Outcome: db.OutcomeReported, ReportOutcome: p("done")}, "done", m.styles.Ok},
		{db.RoundRow{Outcome: db.OutcomeReported, ReportOutcome: p("halted")}, "halted", m.styles.Warn},
		{db.RoundRow{Outcome: db.OutcomeReported, ReportOutcome: p("blocked")}, "blocked", m.styles.Warn},
		{db.RoundRow{Outcome: db.OutcomeReported, ReportOutcome: p("other")}, "no outcome", m.styles.Faint},
		{db.RoundRow{Outcome: db.OutcomeReported, ReportOutcome: nil}, "no outcome", m.styles.Faint},
	}
	for i, tc := range cases {
		word, style := m.outcomeWord(tc.row)
		if word != tc.wantWord {
			t.Errorf("case %d: word = %q, want %q", i, word, tc.wantWord)
		}
		if style.GetForeground() != tc.wantStyle.GetForeground() {
			t.Errorf("case %d (%s): style mismatch", i, word)
		}
	}
}

func TestDayRules(t *testing.T) {
	m := feed(t, newTestModel(t, ""))
	view := m.View()
	lines := strings.Split(view, "\n")
	var ruleLines []string
	for _, l := range lines {
		if strings.Contains(l, "┈") {
			ruleLines = append(ruleLines, l)
		}
	}
	if len(ruleLines) != 3 {
		t.Fatalf("expected 3 day rule lines, got %d:\n%s", len(ruleLines), view)
	}

	wants := []string{"yesterday", "sat 19 sep", "fri 18 sep"}
	ruleRE := regexp.MustCompile(`1 round · \S+$`)
	for i, rl := range ruleLines {
		stripped := stripANSI(rl)
		if !strings.HasPrefix(stripped, "   "+wants[i]) {
			t.Errorf("rule line %d: got %q, want prefix %q", i, stripped, "   "+wants[i])
		}
		if !strings.HasSuffix(stripped, "   ") {
			t.Errorf("rule line %d does not end with 3 spaces: %q", i, stripped)
		}
		beforeMargin := stripped[:len(stripped)-3]
		if !ruleRE.MatchString(beforeMargin) {
			t.Errorf("rule line %d does not end with '1 round · <tokens>': %q", i, beforeMargin)
		}
	}
}

func TestNoDayRulesUnlessSortedByStarted(t *testing.T) {
	m := feed(t, newTestModel(t, ""))
	res, _ := m.Update(key("s"))
	view := res.View()
	if strings.Contains(view, "┈") {
		t.Errorf("view after one s still contains day rules (┈):\n%s", view)
	}
}

func TestCursorSkipsDayRules(t *testing.T) {
	m := feed(t, newTestModel(t, ""))
	lines := m.visible()
	if lines[m.cursor].kind == lineDay {
		t.Fatalf("initial cursor is on a lineDay: index %d", m.cursor)
	}
	if got := lines[m.cursor].row.Number; got != 5 {
		t.Fatalf("initial cursor row r%d, want r5", got)
	}

	// down -> r4
	res, _ := m.Update(special(tea.KeyDown))
	m = res
	lines = m.visible()
	if lines[m.cursor].kind == lineDay || lines[m.cursor].row.Number != 4 {
		t.Errorf("after down: cursor %d kind %v r%d, want r4", m.cursor, lines[m.cursor].kind, lines[m.cursor].row.Number)
	}

	// down -> r2
	res, _ = m.Update(special(tea.KeyDown))
	m = res
	lines = m.visible()
	if lines[m.cursor].kind == lineDay || lines[m.cursor].row.Number != 2 {
		t.Errorf("after second down: cursor %d kind %v r%d, want r2", m.cursor, lines[m.cursor].kind, lines[m.cursor].row.Number)
	}

	// up -> r4
	res, _ = m.Update(special(tea.KeyUp))
	m = res
	lines = m.visible()
	if lines[m.cursor].kind == lineDay || lines[m.cursor].row.Number != 4 {
		t.Errorf("after up: cursor %d kind %v r%d, want r4", m.cursor, lines[m.cursor].kind, lines[m.cursor].row.Number)
	}

	// home -> r5
	res, _ = m.Update(special(tea.KeyHome))
	m = res
	lines = m.visible()
	if lines[m.cursor].kind == lineDay || lines[m.cursor].row.Number != 5 {
		t.Errorf("after home: cursor %d kind %v r%d, want r5", m.cursor, lines[m.cursor].kind, lines[m.cursor].row.Number)
	}

	// end -> r2
	res, _ = m.Update(special(tea.KeyEnd))
	m = res
	lines = m.visible()
	if lines[m.cursor].kind == lineDay || lines[m.cursor].row.Number != 2 {
		t.Errorf("after end: cursor %d kind %v r%d, want r2", m.cursor, lines[m.cursor].kind, lines[m.cursor].row.Number)
	}
}

func TestNoDollars(t *testing.T) {
	flat := feed(t, newTestModel(t, "")).View()
	if strings.Contains(flat, "$") {
		t.Errorf("flat view contains $:\n%s", flat)
	}

	grouped := feed(t, newTestModel(t, "by:candidate")).View()
	if strings.Contains(grouped, "$") {
		t.Errorf("grouped view contains $:\n%s", grouped)
	}

	m := feed(t, newTestModel(t, "by:candidate"))
	res, _ := m.Update(special(tea.KeyEnter))
	expanded := res.View()
	if strings.Contains(expanded, "$") {
		t.Errorf("expanded view contains $:\n%s", expanded)
	}
}

func TestMarginsAndWidth(t *testing.T) {
	sizes := [][2]int{{132, 34}, {100, 30}}
	queries := []string{"", "by:candidate"}
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		for _, q := range queries {
			m := newTestModel(t, q)
			m.SetSize(w, h)
			m = feed(t, m)
			view := m.View()
			for _, line := range strings.Split(view, "\n") {
				lineWidth := lipgloss.Width(line)
				if lineWidth > w {
					t.Errorf("%dx%d %q: line width %d > %d", w, h, q, lineWidth, w)
				}
				s := stripANSI(line)
				if len(s) > 0 {
					if !strings.HasPrefix(s, "   ") {
						t.Errorf("%dx%d %q: line does not start with 3 spaces: %q", w, h, q, s)
					}
					if lineWidth >= w-3 && !strings.HasSuffix(s, "   ") {
						t.Errorf("%dx%d %q: line does not end with 3 spaces: %q", w, h, q, s)
					}
				}
			}
		}
	}
}

func TestColumnsDropByWidth(t *testing.T) {
	m := newTestModel(t, "")
	m.SetSize(132, 30)
	h132 := stripANSI(m.roundHeader())
	if !strings.Contains(h132, "REPO") {
		t.Errorf("at 132: want REPO in %q", h132)
	}

	m.SetSize(110, 30)
	h110 := stripANSI(m.roundHeader())
	if strings.Contains(h110, "REPO") {
		t.Errorf("at 110: did not expect REPO in %q", h110)
	}
	if !strings.Contains(h110, "TREE") {
		t.Errorf("at 110: want TREE in %q", h110)
	}

	m.SetSize(90, 30)
	h90 := stripANSI(m.roundHeader())
	if strings.Contains(h90, "TREE") || strings.Contains(h90, "COMMITS") {
		t.Errorf("at 90: did not expect TREE or COMMITS in %q", h90)
	}
}

func TestGroupNoneLast(t *testing.T) {
	extra := db.RoundRow{
		BindingID: "b3", BindingName: "none-row",
		Repo: nil, Number: 1, StartedAt: testNow.Add(-time.Hour),
		Outcome:  db.OutcomeReported,
		InTokens: p(int64(10_000_000)),
	}
	rows := append(testRows(), extra)
	for _, desc := range []bool{true, false} {
		m := New(nil, time.UTC, func() time.Time { return testNow }, "by:repo", "")
		m.fetchFn = func(ctx context.Context, q histq.Query) ([]db.RoundRow, error) {
			return q.Apply(rows), nil
		}
		m.SetSize(160, 40)
		m = feed(t, m)
		m.sortDesc = desc
		lines := m.visible()
		if len(lines) == 0 {
			t.Fatal("no visible lines")
		}
		last := lines[len(lines)-1]
		if last.kind != lineGroup {
			t.Fatalf("last line kind = %v, want lineGroup", last.kind)
		}
		rendered := stripANSI(m.groupLine(last.group, false))
		trimmed := strings.TrimSpace(rendered)
		if !strings.HasPrefix(trimmed, "(no repo)") {
			t.Errorf("desc=%v: last group line %q does not start with (no repo)", desc, trimmed)
		}
	}
}

// TestGroupShareHeaderAligned asserts the "% TOKENS" group header is
// right-aligned over its cell: the header's final "S" sits over the "%" of
// every group's percentage, at 132 and 200 columns (round 5, cockpit D2
// share-header fix).
func TestGroupShareHeaderAligned(t *testing.T) {
	for _, w := range []int{132, 200} {
		m := newTestModel(t, "by:binding")
		m.SetSize(w, 40)
		m = feed(t, m)

		header := stripANSI(m.groupHeader())
		hIdx := strings.Index(header, "% TOKENS")
		if hIdx < 0 {
			t.Fatalf("width %d: header has no %% TOKENS: %q", w, header)
		}
		headerCol := utf8.RuneCountInString(header[:hIdx]) + utf8.RuneCountInString("% TOKENS") - 1

		if len(m.groups) == 0 {
			t.Fatalf("width %d: no groups", w)
		}
		for _, g := range m.groups {
			line := stripANSI(m.groupLine(g, false))
			pIdx := strings.LastIndex(line, "%")
			if pIdx < 0 {
				t.Fatalf("width %d: group %q line has no %%: %q", w, g.Key, line)
			}
			pctCol := utf8.RuneCountInString(line[:pIdx])
			if pctCol != headerCol {
				t.Errorf("width %d: group %q: %% at col %d, header S at col %d\nheader: %q\nline:   %q",
					w, g.Key, pctCol, headerCol, header, line)
			}
		}
	}
}

func TestGroupTokensBar(t *testing.T) {
	rows := []db.RoundRow{
		{
			BindingID: "b1", BindingName: "persist",
			Number: 1, StartedAt: testNow,
			InTokens: p(int64(1_000_000)),
		},
	}
	m := New(nil, time.UTC, func() time.Time { return testNow }, "by:binding", "")
	m.fetchFn = func(ctx context.Context, q histq.Query) ([]db.RoundRow, error) {
		return q.Apply(rows), nil
	}
	m.SetSize(160, 40)
	m = feed(t, m)
	view := m.View()
	if !strings.Contains(view, "▇▇▇▇▇▇▇▇▇▇ 100%") {
		t.Errorf("view does not contain 100%% tokens bar:\n%s", view)
	}
}

func TestRunningAndSummary(t *testing.T) {
	rows := []db.RoundRow{
		{
			BindingID: "b1", BindingName: "persist", Number: 5,
			StartedAt: testNow.Add(-2 * time.Minute),
			Outcome:   db.OutcomeOpen,
		},
		{
			BindingID: "b1", BindingName: "persist", Number: 4,
			StartedAt:     testNow.Add(-time.Hour),
			Outcome:       db.OutcomeReported,
			ReportOutcome: p("done"),
			DurationMS:    p(int64(10 * 60_000)),
		},
		{
			BindingID: "b2", BindingName: "api", Number: 2,
			StartedAt:  testNow.Add(-2 * time.Hour),
			Outcome:    db.OutcomeHalted,
			DurationMS: p(int64(5 * 60_000)),
		},
		{
			BindingID: "b2", BindingName: "api", Number: 1,
			StartedAt:     testNow.Add(-3 * time.Hour),
			Outcome:       db.OutcomeReported,
			ReportOutcome: p("done"),
			DurationMS:    p(int64(15 * 60_000)),
		},
	}

	m := New(nil, time.UTC, func() time.Time { return testNow }, "", "")
	m.fetchFn = func(ctx context.Context, q histq.Query) ([]db.RoundRow, error) {
		return q.Apply(rows), nil
	}
	m.Running = func(binding string, round int) bool {
		return binding == "persist" && round == 5
	}
	m.SetSize(160, 40)
	m = feed(t, m)

	// An open row with Running returning true reads running, and its TOOK is now-start ("2m")
	firstRound := m.visible()[1].row // index 0 is dayRule
	word, _ := m.outcomeWord(firstRound)
	if word != "running" {
		t.Errorf("word = %q, want running", word)
	}
	rl := stripANSI(m.roundLine(firstRound, false, false))
	if !strings.Contains(rl, "running") || !strings.Contains(rl, "2m") {
		t.Errorf("roundLine does not have running and 2m: %q", rl)
	}

	// With Running nil it reads open
	mNil := m
	mNil.Running = nil
	wordNil, _ := mNil.outcomeWord(firstRound)
	if wordNil != "open" {
		t.Errorf("word with nil Running = %q, want open", wordNil)
	}

	// Summary().ByWord counts done/halted as §3.5 says
	s := m.Summary()
	if s.ByWord["done"] != 2 {
		t.Errorf("ByWord[done] = %d, want 2", s.ByWord["done"])
	}
	if s.ByWord["halted"] != 1 {
		t.Errorf("ByWord[halted] = %d, want 1", s.ByWord["halted"])
	}

	// SummaryLine (ANSI stripped) starts with 4 rounds
	sl := stripANSI(m.SummaryLine(0))
	if !strings.HasPrefix(sl, "4 rounds") {
		t.Errorf("SummaryLine = %q, want prefix '4 rounds'", sl)
	}
}

// assertBandSpansRow walks the ANSI string and asserts that every printable rune
// at display columns [3, width-3) has an active background, and the 3 margin
// cells on each side do not. SGR 48;... sets background; 0, 49 or bare m clears it.
func assertBandSpansRow(t *testing.T, line string, width int) {
	t.Helper()
	bgActive := false
	col := 0
	inEsc := false
	escBuf := strings.Builder{}

	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if inEsc {
			escBuf.WriteRune(r)
			if r == 'm' {
				inEsc = false
				seq := escBuf.String()
				params := seq[1 : len(seq)-1]
				if params == "" {
					bgActive = false
				} else {
					parts := strings.Split(params, ";")
					for pIdx := 0; pIdx < len(parts); pIdx++ {
						p := parts[pIdx]
						switch p {
						case "", "0", "49":
							bgActive = false
						case "48":
							bgActive = true
							if pIdx+1 < len(parts) && parts[pIdx+1] == "2" {
								pIdx += 4
							} else if pIdx+1 < len(parts) && parts[pIdx+1] == "5" {
								pIdx += 2
							}
						case "38":
							if pIdx+1 < len(parts) && parts[pIdx+1] == "2" {
								pIdx += 4
							} else if pIdx+1 < len(parts) && parts[pIdx+1] == "5" {
								pIdx += 2
							}
						}
					}
				}
				escBuf.Reset()
			} else if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '~' {
				inEsc = false
				escBuf.Reset()
			}
			continue
		}

		if r == 0x1b {
			inEsc = true
			escBuf.Reset()
			continue
		}

		w := lipgloss.Width(string(r))
		if w == 0 {
			continue
		}

		for c := col; c < col+w; c++ {
			if c >= 3 && c < width-3 {
				if !bgActive {
					t.Errorf("column %d (rune %q) missing band background", c, r)
				}
			} else {
				if bgActive {
					t.Errorf("column %d (rune %q) has band background in margin", c, r)
				}
			}
		}
		col += w
	}
	if col != width {
		t.Errorf("line display width = %d, want %d", col, width)
	}
}

func TestCursorBandSpansRow(t *testing.T) {
	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)

	styles := Styles{
		Selected: lipgloss.NewStyle().Background(lipgloss.Color("#123456")),
		Fg:       lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")),
		Dim:      lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")),
		Ok:       lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00")),
		Faint:    lipgloss.NewStyle().Foreground(lipgloss.Color("#444444")),
		Strong:   lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Bold(true),
		Accent:   lipgloss.NewStyle().Foreground(lipgloss.Color("#0000ff")),
		Warn:     lipgloss.NewStyle().Foreground(lipgloss.Color("#ffff00")),
		Danger:   lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")),
		Grid:     lipgloss.NewStyle().Foreground(lipgloss.Color("#333333")),
	}

	// 1. Flat View at 132x20
	mFlat := newTestModel(t, "")
	mFlat.SetSize(132, 20)
	mFlat.SetStyles(styles)
	mFlat = feed(t, mFlat)
	flatView := mFlat.View()
	flatLines := strings.Split(flatView, "\n")
	cursorFlatLine := flatLines[3+mFlat.cursor-mFlat.windowTop()]
	assertBandSpansRow(t, cursorFlatLine, 132)

	// 2. by:repo View at 132x20
	mRepo := newTestModel(t, "by:repo")
	mRepo.SetSize(132, 20)
	mRepo.SetStyles(styles)
	mRepo = feed(t, mRepo)
	repoView := mRepo.View()
	repoLines := strings.Split(repoView, "\n")
	cursorRepoLine := repoLines[3+mRepo.cursor-mRepo.windowTop()]
	assertBandSpansRow(t, cursorRepoLine, 132)
}

func TestEmbeddedBlankRowAboveHeader(t *testing.T) {
	m := newTestModel(t, "")
	m.Embedded = true
	m.SetSize(132, 20)
	m = feed(t, m)
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) != 20 {
		t.Fatalf("expected 20 lines, got %d", len(lines))
	}
	if strings.TrimSpace(lines[0]) != "" {
		t.Errorf("line 0 is not blank: %q", lines[0])
	}
	if !strings.Contains(lines[1], "STARTED") {
		t.Errorf("line 1 does not contain STARTED: %q", lines[1])
	}
}

func TestSummaryLineFits(t *testing.T) {
	rows := []db.RoundRow{
		{BindingID: "b1", BindingName: "r-done", Number: 1, StartedAt: testNow, Outcome: db.OutcomeReported, ReportOutcome: p("done"), DurationMS: p(int64(60_000)), InTokens: p(int64(1000))},
		{BindingID: "b1", BindingName: "r-halted", Number: 2, StartedAt: testNow, Outcome: db.OutcomeHalted, DurationMS: p(int64(120_000)), InTokens: p(int64(1000))},
		{BindingID: "b1", BindingName: "r-blocked", Number: 3, StartedAt: testNow, Outcome: db.OutcomeReported, ReportOutcome: p("blocked"), DurationMS: p(int64(180_000)), InTokens: p(int64(1000))},
		{BindingID: "b1", BindingName: "r-exited", Number: 4, StartedAt: testNow, Outcome: db.OutcomeExited, DurationMS: p(int64(240_000)), InTokens: p(int64(1000))},
		{BindingID: "b1", BindingName: "r-switched", Number: 5, StartedAt: testNow, Outcome: db.OutcomeSwitched, DurationMS: p(int64(300_000)), InTokens: p(int64(1000))},
		{BindingID: "b1", BindingName: "r-running", Number: 6, StartedAt: testNow, Outcome: db.OutcomeOpen, InTokens: p(int64(1000))},
		{BindingID: "b1", BindingName: "r-open", Number: 7, StartedAt: testNow, Outcome: db.OutcomeOpen, InTokens: p(int64(1000))},
	}
	m := New(nil, time.UTC, func() time.Time { return testNow }, "", "")
	m.fetchFn = func(ctx context.Context, q histq.Query) ([]db.RoundRow, error) {
		return q.Apply(rows), nil
	}
	m.Running = func(b string, r int) bool {
		return b == "r-running" && r == 6
	}
	m.SetSize(160, 40)
	m = feed(t, m)

	s0 := m.SummaryLine(0)
	s0Plain := stripANSI(s0)
	if !strings.Contains(s0Plain, "median") {
		t.Errorf("SummaryLine(0) does not contain median: %q", s0Plain)
	}

	s60 := m.SummaryLine(60)
	w60 := lipgloss.Width(s60)
	if w60 > 60 {
		t.Errorf("SummaryLine(60) width = %d > 60: %q", w60, s60)
	}
	s60Plain := stripANSI(s60)
	if !strings.HasPrefix(s60Plain, "7 rounds") {
		t.Errorf("SummaryLine(60) = %q, want prefix '7 rounds'", s60Plain)
	}
	if strings.Contains(s60Plain, "median") {
		t.Errorf("SummaryLine(60) contains median: %q", s60Plain)
	}
}

func TestGroupZeroTokensDot(t *testing.T) {
	rows := []db.RoundRow{
		{
			BindingID: "b1", BindingName: "zero-tokens",
			Number: 1, StartedAt: testNow,
			Outcome: db.OutcomeReported, ReportOutcome: p("done"),
		},
	}
	m := New(nil, time.UTC, func() time.Time { return testNow }, "by:binding", "")
	m.fetchFn = func(ctx context.Context, q histq.Query) ([]db.RoundRow, error) {
		return q.Apply(rows), nil
	}
	m.SetSize(160, 40)
	m = feed(t, m)
	if len(m.groups) == 0 {
		t.Fatal("no groups found")
	}
	gl := stripANSI(m.groupLine(m.groups[0], false))
	if !strings.Contains(gl, "·") {
		t.Errorf("groupLine does not contain '·' in TOKENS column: %q", gl)
	}
}
