package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

// paneModel builds a roundPane directly (R2.10): the pane is the same type
// round 1 extracted, only the wrapper Model is gone.
func paneModel(t *testing.T, b view.BindingStatus, active tab) roundPane {
	t.Helper()
	p := roundPane{width: 140, rows: 36, now: func() time.Time { return railNow },
		report: view.Report{Bindings: []view.BindingStatus{b}}}
	p.detail = detailModel{name: b.Key(), round: paneRound(b), rounds: roundsOf(b), live: true, active: active,
		vp: viewport.New(p.contentWidth(), p.viewportHeight())}
	p.fillViewport()
	return p
}

func TestPaneHeadRows(t *testing.T) {
	b := view.BindingStatus{
		Name: "webshop", Round: 4, Display: "NEEDS YOU",
		MasterMindID: "mastermind-9f2", MasterMindName: "architect-1", MasterMindKind: "claude", MasterMindRoute: "channel",
		BuilderKind: "agy", BuilderStatus: "blocked", Consults: 2,
		Branch: "relevo/webshop", Dirty: true,
		LastClose: &view.CloseInfo{Round: 3, Commits: 2, Tree: "clean"},
		Last:      &view.LastEvent{TS: railNow.Add(-2 * time.Minute), Round: 4, Kind: store.KindQuestion},
	}
	p := paneModel(t, b, tabReport)
	if p.headRows() != 6 {
		t.Fatalf("headRows() = %d, want 6", p.headRows())
	}
	tokens := stripANSI(p.tokensLine(&b))
	if !strings.Contains(tokens, "+2 commits") {
		t.Errorf("tokensLine missing commits: %q", tokens)
	}

	oneCommit := view.BindingStatus{
		Name: "ledger", Round: 2, Display: "ACTIVE", Dirty: true,
		LastClose: &view.CloseInfo{Round: 1, Commits: 1, Tree: "dirty"},
	}
	pOne := paneModel(t, oneCommit, tabReport)
	tokensOne := stripANSI(pOne.tokensLine(&oneCommit))
	if !strings.Contains(tokensOne, "+1 commit") {
		t.Errorf("tokensLine missing +1 commit: %q", tokensOne)
	}
	rv := roundView{pane: pOne}
	ctxLeft, _ := rv.Context(testEnv(pOne.src, view.Report{Bindings: []view.BindingStatus{oneCommit}}, pOne.width, pOne.rows))
	if !strings.Contains(stripANSI(ctxLeft), "dirty") {
		t.Errorf("context row missing dirty: %q", ctxLeft)
	}
}

func TestPaneHeadHeadlessAndCwd(t *testing.T) {
	b := view.BindingStatus{
		Name: "api", Round: 2, Display: "ACTIVE", CWD: "/home/x/api",
		BuilderKind: "opencode", BuilderStatus: "working", BuilderCandidate: "opencode-1",
		Headless: &view.HeadlessInfo{PID: 48211, StartedAt: railNow.Add(-21 * time.Minute)},
	}
	p := paneModel(t, b, tabReport)
	tokens := stripANSI(p.tokensLine(&b))
	if !strings.Contains(tokens, "pid 48211 since") {
		t.Errorf("tokensLine missing pid: %q", tokens)
	}
	rv := roundView{pane: p}
	ctxLeft, _ := rv.Context(testEnv(p.src, view.Report{Bindings: []view.BindingStatus{b}}, p.width, p.rows))
	if !strings.Contains(stripANSI(ctxLeft), "/home/x/api") {
		t.Errorf("context row missing cwd: %q", ctxLeft)
	}
}

func TestTabBarWordsAndUnderline(t *testing.T) {
	p := paneModel(t, view.BindingStatus{Name: "a", Round: 1, Display: "ACTIVE"}, tabDiff)
	tabs := p.tabsRow()
	plain := stripANSI(tabs)
	words := plain
	if idx := strings.Index(plain, "round"); idx != -1 {
		words = plain[:idx]
	}
	if strings.ContainsAny(words, "12345") {
		t.Errorf("tabs must not carry numbers: %q", words)
	}
	// A writer round draws today's five tabs; the artifacts tab is a
	// reader round's (round 5b), so it is not among them.
	for _, tb := range writerTabs {
		if title := tabTitles[tb]; !strings.Contains(plain, title) {
			t.Errorf("tabs missing %q: %q", title, plain)
		}
	}
	if strings.Contains(plain, tabTitles[tabArtifacts]) {
		t.Errorf("writer tabs must not carry the artifacts tab: %q", plain)
	}
	if !strings.Contains(tabs, chip(chipAccentStyle, "diff")) {
		t.Errorf("active tab not chipAccentStyle: %q", tabs)
	}
	if !strings.Contains(plain, "round") || !strings.Contains(plain, "[") || !strings.Contains(plain, "]") {
		t.Errorf("tabs missing stepper: %q", plain)
	}
}

func TestRoundContextByGroup(t *testing.T) {
	cases := []struct {
		name     string
		b        view.BindingStatus
		pillWord string
	}{
		{
			name:     "needs you",
			b:        view.BindingStatus{Name: "b1", Round: 1, Display: "NEEDS YOU"},
			pillWord: "needs you",
		},
		{
			name:     "working",
			b:        view.BindingStatus{Name: "b2", Round: 1, Display: "ACTIVE", BuilderStatus: "working"},
			pillWord: "working",
		},
		{
			name:     "idle",
			b:        view.BindingStatus{Name: "b3", Round: 1, Display: "ACTIVE", BuilderStatus: "idle"},
			pillWord: "idle",
		},
		{
			name:     "on hold",
			b:        view.BindingStatus{Name: "b4", Round: 1, Display: "PAUSED"},
			pillWord: "on hold",
		},
		{
			name:     "other",
			b:        view.BindingStatus{Name: "b5", Round: 1, Display: "CUSTOM"},
			pillWord: "custom",
		},
		{
			name:     "done",
			b:        view.BindingStatus{Name: "b6", Round: 1, Display: "DONE"},
			pillWord: "done",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := paneModel(t, tc.b, tabPrompt)
			rv := roundView{pane: p, actions: true}
			env := testEnv(p.src, view.Report{Bindings: []view.BindingStatus{tc.b}}, p.width, p.rows)
			left, _ := rv.Context(env)
			plain := stripANSI(left)
			if !strings.Contains(plain, tc.pillWord) {
				t.Errorf("context row missing pill word %q: %q", tc.pillWord, plain)
			}

			// The action-key labels move to Keys(): with actions it has x stop, g gate and o shell; without actions none of them.
			var keyStrings []string
			for _, k := range rv.Keys() {
				keyStrings = append(keyStrings, k.Key+" "+k.Help)
			}
			keysJoined := strings.Join(keyStrings, " ")
			for _, wantKey := range []string{"x stop", "g gate", "o shell"} {
				if !strings.Contains(keysJoined, wantKey) {
					t.Errorf("Keys() with actions missing %q: %q", wantKey, keysJoined)
				}
			}

			rvNoActions := roundView{pane: p, actions: false}
			var keyStringsNoActions []string
			for _, k := range rvNoActions.Keys() {
				keyStringsNoActions = append(keyStringsNoActions, k.Key+" "+k.Help)
			}
			noActionsJoined := strings.Join(keyStringsNoActions, " ")
			for _, unwantedKey := range []string{"x stop", "g gate", "o shell"} {
				if strings.Contains(noActionsJoined, unwantedKey) {
					t.Errorf("Keys() without actions must not contain %q: %q", unwantedKey, noActionsJoined)
				}
			}
		})
	}
}

func TestRoundTokensLineHist(t *testing.T) {
	p := paneModel(t, view.BindingStatus{Name: "archived-binding"}, tabPrompt)
	p.detail.live = false
	p.detail.round = 2
	p.detail.rounds = 5
	p.detail.archivedAt = time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	line := stripANSI(p.tokensLine(nil))
	if !strings.Contains(line, "no live facts for a released binding") {
		t.Errorf("hist tokensLine missing 'no live facts for a released binding': %q", line)
	}
}

func TestRoundHeadRowsMatchView(t *testing.T) {
	for _, h := range []int{12, 18, 40} {
		t.Run(fmt.Sprintf("height-%d", h), func(t *testing.T) {
			b := view.BindingStatus{Name: "srv", Round: 1, Display: "ACTIVE", BuilderStatus: "working"}
			p := paneModel(t, b, tabPrompt)
			p.rows = h
			p.detail.cache[tabPrompt] = tabContent{loaded: true, body: "VP_TEST_LINE_1\nVP_TEST_LINE_2\nVP_TEST_LINE_3"}
			p.fillViewport()

			if p.viewportHeight() != p.rows-p.headRows() {
				t.Errorf("viewportHeight = %d, want %d", p.viewportHeight(), p.rows-p.headRows())
			}

			rendered := p.view(p.width)
			lines := strings.Split(rendered, "\n")
			firstVP := -1
			for i, l := range lines {
				if strings.Contains(l, "VP_TEST_LINE_1") {
					firstVP = i
					break
				}
			}
			if firstVP != p.headRows() {
				t.Errorf("first viewport line at %d, want headRows() = %d", firstVP, p.headRows())
			}
		})
	}
}

func TestRoundTokensLineKeepsOnlyCounts(t *testing.T) {
	b := view.BindingStatus{
		Name: "api", Round: 2, Display: "ACTIVE",
		LiveUsage: &usage.Usage{
			Harness: "opencode", Model: "glm-5.3-flash", DurationMS: 4 * 60_000,
			Tokens:  usage.Tokens{In: 1_800, CacheRead: 91_000, CacheWrite: 3, Out: 8_200},
			Cost:    usage.Cost{USD: 0.04, Basis: usage.Measured},
			Samples: 3,
		},
	}
	p := paneModel(t, b, tabReport)
	line := stripANSI(p.tokensLine(&b))

	for _, want := range []string{"in ", "out ", "write 3", "$0.04"} {
		if !strings.Contains(line, want) {
			t.Errorf("tokensLine missing %q: %q", want, line)
		}
	}
	for _, dontWant := range []string{"glm-5.3-flash", "write 0", "4m"} {
		if strings.Contains(line, dontWant) {
			t.Errorf("tokensLine should not contain %q: %q", dontWant, line)
		}
	}

	bZero := view.BindingStatus{
		Name: "api", Round: 2, Display: "ACTIVE",
		LiveUsage: &usage.Usage{
			Harness: "opencode", Model: "glm-5.3-flash", DurationMS: 4 * 60_000,
			Tokens:  usage.Tokens{In: 1_800, CacheRead: 91_000, CacheWrite: 0, Out: 8_200},
			Cost:    usage.Cost{USD: 0.04, Basis: usage.Measured},
			Samples: 3,
		},
	}
	pZero := paneModel(t, bZero, tabReport)
	lineZero := stripANSI(pZero.tokensLine(&bZero))
	if strings.Contains(lineZero, "write 0") {
		t.Errorf("tokensLine should not contain write 0: %q", lineZero)
	}
	if !strings.Contains(lineZero, "in ") || !strings.Contains(lineZero, "out ") || !strings.Contains(lineZero, "$0.04") {
		t.Errorf("tokensLine missing counts or cost: %q", lineZero)
	}
}

func TestRoundNoRawToken(t *testing.T) {
	b := view.BindingStatus{
		Name:             "api",
		Round:            2,
		Display:          "ACTIVE",
		BuilderName:      "gemini-3.8-flash-high",
		BuilderCandidate: "opencode-1",
		Headless:         &view.HeadlessInfo{PID: 1234, StartedAt: railNow.Add(-5 * time.Minute)},
	}
	p := paneModel(t, b, tabPrompt)
	rv := roundView{pane: p, actions: true}
	env := testEnv(p.src, view.Report{Bindings: []view.BindingStatus{b}}, p.width, p.rows)
	view := rv.Body(env, 140, 40)
	if strings.Contains(view, "`") {
		t.Errorf("view contains backtick: %q", view)
	}
	if strings.Contains(view, "opencode-1") {
		t.Errorf("view contains candidate token: %q", view)
	}
}

func TestSourceLineOmitsTimeWhenUnknown(t *testing.T) {
	b := view.BindingStatus{Name: "a", Round: 3, Display: "ACTIVE"}
	p := paneModel(t, b, tabPrompt)
	p.detail.cache[tabPrompt] = tabContent{loaded: true, body: "x", round: 2}
	if got := stripANSI(p.sourceLine()); got != "prompt r2" {
		t.Errorf("prompt source = %q, want %q", got, "prompt r2")
	}
	p.detail.active = tabReport
	p.detail.cache[tabReport] = tabContent{loaded: true, body: "x", round: 2}
	got := stripANSI(p.sourceLine())
	if got != "report r2" {
		t.Errorf("report source = %q, want %q", got, "report r2")
	}
	if strings.Contains(got, " · ") {
		t.Errorf("a source line with an unknown time must have no separator, got %q", got)
	}
}

func TestDiffStatAndColour(t *testing.T) {
	patch := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,3 @@\n context\n-old\n+new\n+more\ndiff --git a/y.go b/y.go\n@@ -1 +1 @@\n-a\n+b\n"
	files, add, del := diffStat(patch)
	if files != 2 || add != 3 || del != 2 {
		t.Errorf("diffStat = %d files +%d -%d", files, add, del)
	}
	out := strings.Split(colourDiff(patch), "\n")
	if out[0] != diffFileStyle.Render("diff --git a/x.go b/x.go") {
		t.Errorf("file header not styled: %q", out[0])
	}
	if out[3] != diffHunkStyle.Render("@@ -1,2 +1,3 @@") {
		t.Errorf("hunk not styled: %q", out[3])
	}
	if out[5] != diffDelStyle.Render("-old") || out[6] != diffAddStyle.Render("+new") {
		t.Errorf("+/- not styled: %q %q", out[5], out[6])
	}
	if out[1] != diffFileStyle.Render("--- a/x.go") || out[2] != diffFileStyle.Render("+++ b/x.go") {
		t.Errorf("---/+++ must be file headers, not del/add: %q %q", out[1], out[2])
	}
	if out[4] != " context" {
		t.Errorf("context line altered: %q", out[4])
	}
}

func TestSourceLinePerTab(t *testing.T) {
	b := view.BindingStatus{Name: "a", Round: 3, Display: "ACTIVE"}
	p := paneModel(t, b, tabReport)
	p.detail.cache[tabReport] = tabContent{loaded: true, body: "x", round: 2, at: railNow.Add(-time.Hour)}
	if got := stripANSI(p.sourceLine()); got != "report r2 · 13:02" {
		t.Errorf("report source = %q", got)
	}
	p.detail.active = tabTerminal
	p.detail.cache[tabTerminal] = tabContent{loaded: true, body: "l1\nl2\nl3", at: railNow.Add(-time.Second)}
	if got := stripANSI(p.sourceLine()); got != "remote · captured 1s ago · 3 lines" {
		t.Errorf("terminal source = %q", got)
	}
	b.Headless = &view.HeadlessInfo{LogPath: "/x/002-builder.log"}
	p.report = view.Report{Bindings: []view.BindingStatus{b}}
	p.detail.headless = true
	p.detail.cache[tabTerminal] = tabContent{loaded: true, body: "l1\nl2\nl3", at: railNow.Add(-time.Second),
		transcript: true, logName: "002-builder.log"}
	p.detail.follow = true
	if got := stripANSI(p.sourceLine()); got != "headless · 002-builder.log · 3 lines · following" {
		t.Errorf("headless following source = %q", got)
	}
	p.detail.follow = false
	if got := stripANSI(p.sourceLine()); got != "headless · 002-builder.log · 3 lines · scrolled" {
		t.Errorf("headless scrolled source = %q", got)
	}
	p.detail.headless = false
	p.detail.cache[tabTerminal] = tabContent{loaded: true, body: "l1\nl2\nl3", at: railNow.Add(-time.Second),
		transcript: true, logName: "002-builder.log"}
	p.detail.follow = true
	if got := stripANSI(p.sourceLine()); got != "pane · 002-builder.log · 3 lines · following" {
		t.Errorf("pane transcript source = %q", got)
	}
	p.detail.follow = false
	p.detail.active = tabDiff
	p.detail.cache[tabDiff] = tabContent{loaded: true, body: "diff --git a/x b/x\n+a\n-b\n"}
	if got := stripANSI(p.sourceLine()); got != "round 2 · 1 file · +1 −1" {
		t.Errorf("diff source = %q", got)
	}
	p.detail.active = tabLog
	p.detail.cache[tabLog] = tabContent{loaded: true, body: "e1\ne2"}
	if got := stripANSI(p.sourceLine()); got != "2 entries" {
		t.Errorf("log source = %q", got)
	}
	p.detail.active = tabReport
	p.detail.cache[tabReport] = tabContent{}
	if got := stripANSI(p.sourceLine()); got != "loading…" {
		t.Errorf("unloaded source = %q", got)
	}
}

func TestHintLineOnlyForBlockedTerminal(t *testing.T) {
	b := view.BindingStatus{Name: "webshop", Round: 4, Display: "NEEDS YOU",
		Waiting: &view.Waiting{Cause: "blocked", Hint: "relevo status --name webshop"}}
	p := paneModel(t, b, tabTerminal)
	line, ok := p.hintLine(&b)
	if !ok || stripANSI(line) != "relevo: relevo status --name webshop" {
		t.Errorf("hint = %q ok=%v", stripANSI(line), ok)
	}
	p.detail.active = tabReport
	if _, ok := p.hintLine(&b); ok {
		t.Error("hint must only show on the terminal tab")
	}
	p.detail.active = tabTerminal
	b.Waiting.Cause = "halted"
	if _, ok := p.hintLine(&b); ok {
		t.Error("hint must only show for a blocked builder")
	}
	b.Waiting = nil
	if _, ok := p.hintLine(&b); ok {
		t.Error("hint must not show without Waiting")
	}
}

func TestPaneViewRowsAndWidth(t *testing.T) {
	b := view.BindingStatus{Name: "webshop", Round: 4, Display: "NEEDS YOU",
		Waiting: &view.Waiting{Cause: "blocked", Hint: "relevo status --name webshop"}}
	p := paneModel(t, b, tabTerminal)
	p.detail.cache[tabTerminal] = tabContent{loaded: true, body: strings.Repeat("screen line\n", 50)}
	p.detail.vp.SetContent(bodyOf(tabTerminal, p.detail.cache[tabTerminal], false))
	view := p.view(p.width)
	lines := strings.Split(view, "\n")
	if len(lines) != p.rows {
		t.Fatalf("%d pane rows, want rows %d", len(lines), p.rows)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > p.width {
			t.Errorf("row %d is %d wide, pane is %d: %q", i, w, p.width, stripANSI(l))
		}
	}
	if got := stripANSI(lines[len(lines)-1]); !strings.HasPrefix(got, "relevo: relevo status") {
		t.Errorf("last pane row must be the hint, got %q", got)
	}
}

func TestWrapBodyMakesEveryLineReachable(t *testing.T) {
	long := "alpha " + strings.Repeat("word ", 40) + "omega"
	body := long + "\nsecond\nthird"
	wrapped := wrapBody(body, 50)
	for i, l := range strings.Split(wrapped, "\n") {
		if w := lipgloss.Width(l); w > 50 {
			t.Errorf("line %d is %d wide: %q", i, w, l)
		}
	}
	if !strings.Contains(wrapped, "omega") || !strings.Contains(wrapped, "third") {
		t.Errorf("wrapping lost text:\n%s", wrapped)
	}
	// A path with no spaces still breaks rather than overflowing.
	path := strings.Repeat("/abcdefghij", 12)
	for i, l := range strings.Split(wrapBody(path, 50), "\n") {
		if w := lipgloss.Width(l); w > 50 {
			t.Errorf("path line %d is %d wide", i, w)
		}
	}
	// Styled input keeps its styling and its width.
	styled := colourDiff("+" + strings.Repeat("x", 120))
	for i, l := range strings.Split(wrapBody(styled, 50), "\n") {
		if w := lipgloss.Width(l); w > 50 {
			t.Errorf("styled line %d is %d wide", i, w)
		}
	}
}

func TestColourTranscript(t *testing.T) {
	body := "Now running the tests.\n● Bash go test ./...\n  ⎿ ok: ok  github.com/x 0.4s\n● Read\n  ⎿ error: no such file\n[system]"
	out := strings.Split(colourTranscript(body), "\n")
	if out[0] != "Now running the tests." {
		t.Errorf("prose must be untouched: %q", out[0])
	}
	if p := stripANSI(out[1]); p != "● Bash(go test ./...)" {
		t.Errorf("call = %q", p)
	}
	if !strings.Contains(out[1], stateActiveStyle.Render("●")) || !strings.Contains(out[1], lipgloss.NewStyle().Bold(true).Render("Bash")) {
		t.Errorf("call not styled: %q", out[1])
	}
	if p := stripANSI(out[2]); p != "  ⎿ ok: ok  github.com/x 0.4s" {
		t.Errorf("ok result text changed: %q", p)
	}
	if out[2] != dimStyle.Render("  ⎿ ok: ok  github.com/x 0.4s") {
		t.Errorf("ok result not dim: %q", out[2])
	}
	if p := stripANSI(out[3]); p != "● Read" {
		t.Errorf("call without argument = %q (no empty parens)", p)
	}
	if out[4] != errorStyle.Render("  ⎿ error: no such file") {
		t.Errorf("error result not styled: %q", out[4])
	}
	if out[5] != "[system]" {
		t.Errorf("unknown-event line must be untouched: %q", out[5])
	}
}

func TestColourTranscriptDimsThinking(t *testing.T) {
	body := "∴ considering\n● Bash ls"
	out := strings.Split(colourTranscript(body), "\n")
	if p := stripANSI(out[0]); p != "∴ considering" {
		t.Errorf("thinking text changed: %q", p)
	}
	if out[0] != dimStyle.Italic(true).Render("∴ considering") {
		t.Errorf("thinking line not dim italic: %q", out[0])
	}
	if p := stripANSI(out[1]); p != "● Bash(ls)" {
		t.Errorf("call = %q", p)
	}
	if !strings.Contains(out[1], stateActiveStyle.Render("●")) || !strings.Contains(out[1], lipgloss.NewStyle().Bold(true).Render("Bash")) {
		t.Errorf("call not styled: %q", out[1])
	}
}

func TestBodyOfStylesOnlyHeadlessTerminal(t *testing.T) {
	c := tabContent{loaded: true, body: "● Bash ls"}
	if got := bodyOf(tabTerminal, c, false); got != "● Bash ls" {
		t.Errorf("a pane capture must never be restyled: %q", got)
	}
	if got := bodyOf(tabTerminal, c, true); stripANSI(got) != "● Bash(ls)" {
		t.Errorf("headless terminal = %q", stripANSI(got))
	}
	if got := bodyOf(tabLog, c, true); got != "● Bash ls" {
		t.Errorf("only the terminal tab styles transcript lines: %q", got)
	}
	pc := tabContent{loaded: true, body: "● Bash ls", transcript: true}
	if got := bodyOf(tabTerminal, pc, false); stripANSI(got) != "● Bash(ls)" {
		t.Errorf("pane transcript terminal = %q", stripANSI(got))
	}
}

func TestViewportReachesBottomOfLongLines(t *testing.T) {
	b := view.BindingStatus{Name: "a", Round: 3, Display: "ACTIVE"}
	p := paneModel(t, b, tabLog)
	p.detail.vp.Width = 40
	p.detail.vp.Height = 3
	var lines []string
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf("entry %d %s END%d", i, strings.Repeat("w ", 30), i))
	}
	p.detail.cache[tabLog] = tabContent{loaded: true, body: strings.Join(lines, "\n")}
	p.fillViewport()
	p.detail.vp.GotoBottom()
	if v := stripANSI(p.detail.vp.View()); !strings.Contains(v, "END4") {
		t.Errorf("the last line must be reachable at the bottom, got:\n%s", v)
	}
	if p.detail.vp.TotalLineCount() <= 5 {
		t.Errorf("wrapped content must have more logical lines than raw (%d)", p.detail.vp.TotalLineCount())
	}
}

func TestRoundTokensLineCostWord(t *testing.T) {
	// A LiveUsage whose cost part is unknown: no price for x/y; stream still open; timed out
	// renders "no price" and not "stream".
	bUnknown := view.BindingStatus{
		Name:    "worker",
		Round:   1,
		Display: "ACTIVE",
		LiveUsage: &usage.Usage{
			Model:    "gemini-3.8-flash-high",
			Provider: "google",
			Tokens:   usage.Tokens{In: 100, Out: 50},
			Cost:     usage.Cost{Basis: usage.Unknown},
			Note:     "no price for google/gemini-3.8-flash-high; stream still open; timed out",
		},
	}
	pUnknown := paneModel(t, bUnknown, tabPrompt)
	tlUnknown := stripANSI(pUnknown.tokensLine(&bUnknown))
	if !strings.Contains(tlUnknown, "no price") {
		t.Errorf("tokensLine missing 'no price': %q", tlUnknown)
	}
	if strings.Contains(tlUnknown, "stream") {
		t.Errorf("tokensLine should not contain 'stream': %q", tlUnknown)
	}

	// A measured cost word stays as it is.
	bMeasured := view.BindingStatus{
		Name:    "worker",
		Round:   1,
		Display: "ACTIVE",
		LiveUsage: &usage.Usage{
			Model:    "gemini-3.8-flash-high",
			Provider: "google",
			Tokens:   usage.Tokens{In: 100, Out: 50},
			Cost:     usage.Cost{Basis: usage.Measured, USD: 0.12},
		},
	}
	pMeasured := paneModel(t, bMeasured, tabPrompt)
	tlMeasured := stripANSI(pMeasured.tokensLine(&bMeasured))
	if !strings.Contains(tlMeasured, "$0.12") {
		t.Errorf("tokensLine missing '$0.12': %q", tlMeasured)
	}
}

func TestRoundsOfIdleAfterReport(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	end := railNow.Add(-10 * time.Minute)
	b := view.BindingStatus{
		Name:          "idle-b",
		Round:         6,
		PlanRound:     5,
		BuilderStatus: "idle",
		Display:       "IDLE",
		RoundEnd:      end,
	}
	if err := st.Save(store.Binding{Name: b.Name, CWD: "/repo/" + b.Name, Round: 6, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rep := view.Report{Bindings: []view.BindingStatus{b}}
	env := testEnv(mastermindSource{rt}, rep, 140, 40)

	v, _ := newRoundView(env, b.Name, 0)
	rv := v.(roundView)

	if rv.pane.detail.rounds != 5 {
		t.Errorf("opening gives detail.rounds = %d, want 5", rv.pane.detail.rounds)
	}
	if rv.pane.detail.round != 5 {
		t.Errorf("opening gives detail.round = %d, want 5", rv.pane.detail.round)
	}

	_, right := rv.Context(env)
	plainRight := stripANSI(right)
	if !strings.Contains(plainRight, "round 5 of 5") {
		t.Errorf("Context right missing 'round 5 of 5': %q", plainRight)
	}
	if strings.Contains(plainRight, "live") {
		t.Errorf("Context right must not contain 'live': %q", plainRight)
	}

	// ] stays on 5
	vNext, _ := rv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}}, env)
	rvNext := vNext.(roundView)
	if rvNext.pane.detail.round != 5 {
		t.Errorf("expected round to stay on 5 after ']', got %d", rvNext.pane.detail.round)
	}
}

func TestRoundsOfWorking(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	b := view.BindingStatus{
		Name:          "work-b",
		Round:         3,
		PlanRound:     3,
		RoundStart:    railNow.Add(-2 * time.Minute),
		BuilderStatus: "working",
		Display:       "ACTIVE",
	}
	if err := st.Save(store.Binding{Name: b.Name, CWD: "/repo/" + b.Name, Round: 3, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rep := view.Report{Bindings: []view.BindingStatus{b}}
	env := testEnv(mastermindSource{rt}, rep, 140, 40)

	v, _ := newRoundView(env, b.Name, 0)
	rv := v.(roundView)

	_, right := rv.Context(env)
	plainRight := stripANSI(right)
	if !strings.Contains(plainRight, "round 3 of 3 · live") {
		t.Errorf("Context right = %q, want 'round 3 of 3 · live'", plainRight)
	}
}

func TestRoundContextNarrowDropsWholeParts(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	b := view.BindingStatus{
		Name:           "narrow-b",
		Round:          1,
		PlanRound:      1,
		Display:        "ACTIVE",
		BuilderStatus:  "working",
		BuilderName:    "gemini-3.8-flash-high",
		MasterMindName: "architect-2",
		Branch:         "relevo/spool-db",
		RoundStart:     railNow.Add(-5 * time.Minute),
		Dirty:          true,
	}
	if err := st.Save(store.Binding{Name: b.Name, CWD: "/repo/" + b.Name, Round: 1, State: store.StateActive}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rep := view.Report{Bindings: []view.BindingStatus{b}}

	widths := []int{132, 100, 80, 60}
	for _, w := range widths {
		env := testEnv(mastermindSource{rt}, rep, w, 30)
		v, _ := newRoundView(env, b.Name, 0)
		rv := v.(roundView)
		left, _ := rv.Context(env)
		plain := stripANSI(left)

		// The pill and age are always present.
		if !strings.Contains(plain, "working") {
			t.Errorf("width %d: missing pill 'working': %q", w, plain)
		}
		if !strings.Contains(plain, "5m") {
			t.Errorf("width %d: missing age '5m': %q", w, plain)
		}

		// The stripped left never ends inside a word of the branch or mastermind.
		// Each part is either whole or absent.
		if strings.Contains(plain, "relevo/spool-db") {
			// whole
		} else if strings.Contains(plain, "relevo") || strings.Contains(plain, "spool") {
			t.Errorf("width %d: branch partially present in %q", w, plain)
		}

		if strings.Contains(plain, "architect-2") {
			// whole
		} else if strings.Contains(plain, "architect") {
			t.Errorf("width %d: mastermind partially present in %q", w, plain)
		}

		if strings.Contains(plain, "gemini-3.8-flash-high") {
			// whole
		} else if strings.Contains(plain, "gemini") {
			t.Errorf("width %d: candidate partially present in %q", w, plain)
		}

		if strings.Contains(plain, "dirty") {
			// whole
		} else if strings.Contains(plain, "dirt") {
			t.Errorf("width %d: dirty partially present in %q", w, plain)
		}

		// Never ends with a dangling separator
		if strings.HasSuffix(plain, "·") || strings.HasSuffix(plain, "· ") {
			t.Errorf("width %d: dangling separator at end: %q", w, plain)
		}
	}
}

// TestColourTranscriptKeepsTheStampFaint: a stamped thinking/call/result line
// keeps today's styling, with the stamp rendered faint ahead of it.
func TestColourTranscriptKeepsTheStampFaint(t *testing.T) {
	body := "12:41:03 +4.2s ● Bash go test ./...\n" +
		"12:41:03   ⎿ ok: ok  github.com/x 0.4s\n" +
		"12:41:03 ∴ considering\n" +
		"+0.9s   ⎿ error: no such file\n" +
		"12:41:03 ● Read"
	out := strings.Split(colourTranscript(body), "\n")

	cases := []struct {
		i     int
		stamp string
		text  string
	}{
		{0, "12:41:03 +4.2s ", "● Bash(go test ./...)"},
		{1, "12:41:03 ", "  ⎿ ok: ok  github.com/x 0.4s"},
		{2, "12:41:03 ", "∴ considering"},
		{3, "+0.9s ", "  ⎿ error: no such file"},
		{4, "12:41:03 ", "● Read"},
	}
	for _, c := range cases {
		if got := stripANSI(out[c.i]); got != c.stamp+c.text {
			t.Errorf("line %d = %q, want %q", c.i, got, c.stamp+c.text)
		}
		if !strings.HasPrefix(out[c.i], faintStyle.Render(c.stamp)) {
			t.Errorf("line %d stamp is not faint: %q", c.i, out[c.i])
		}
	}
	if !strings.Contains(out[0], stateActiveStyle.Render("●")) ||
		!strings.Contains(out[0], lipgloss.NewStyle().Bold(true).Render("Bash")) {
		t.Errorf("call not styled: %q", out[0])
	}
	if want := faintStyle.Render("12:41:03 ") + dimStyle.Render("  ⎿ ok: ok  github.com/x 0.4s"); out[1] != want {
		t.Errorf("ok result not dim: %q", out[1])
	}
	if want := faintStyle.Render("12:41:03 ") + dimStyle.Italic(true).Render("∴ considering"); out[2] != want {
		t.Errorf("thinking line not dim italic: %q", out[2])
	}
	if want := faintStyle.Render("+0.9s ") + errorStyle.Render("  ⎿ error: no such file"); out[3] != want {
		t.Errorf("error result not red: %q", out[3])
	}
}

// TestScrollbarGeometry: pure geometry from the viewport's own numbers.
func TestScrollbarGeometry(t *testing.T) {
	for _, c := range []struct{ total, height, offset int }{
		{3, 10, 0}, {10, 10, 0}, {0, 0, 0}, {10, 0, 0}, {0, 10, 0},
	} {
		if got := barCells(c.total, c.height, c.offset); got != nil {
			t.Errorf("barCells(%d, %d, %d) = %v, want hidden", c.total, c.height, c.offset, got)
		}
	}

	thumbRow := func(offset int) int {
		for i, c := range barCells(100, 10, offset) {
			if c == mutedStyle.Render("█") {
				return i
			}
		}
		return -1
	}
	for _, c := range []struct{ offset, row int }{
		{0, 0},  // top
		{45, 4}, // middle
		{90, 9}, // bottom (total - height)
	} {
		if got := thumbRow(c.offset); got != c.row {
			t.Errorf("thumb at offset %d = row %d, want %d", c.offset, got, c.row)
		}
	}
	for i, c := range barCells(100, 10, 45) {
		want := borderStyle.Render("│")
		if i == 4 {
			want = mutedStyle.Render("█")
		}
		if c != want {
			t.Errorf("cell %d = %q, want %q", i, c, want)
		}
	}

	// The thumb is at least one cell, however long the content.
	cells := barCells(1000, 10, 0)
	if len(cells) != 10 {
		t.Fatalf("%d cells, want one per viewport row (10)", len(cells))
	}
	thumbs := 0
	for _, c := range cells {
		if c == mutedStyle.Render("█") {
			thumbs++
		}
	}
	if thumbs != 1 {
		t.Errorf("%d thumb cells, want 1", thumbs)
	}
}

// TestScrollbarInPaneView: the bar is drawn on every viewport row and in the
// last column only -- the pane's width and the viewport's content width do not
// move.
func TestScrollbarInPaneView(t *testing.T) {
	b := view.BindingStatus{Name: "webshop", Round: 4, Display: "ACTIVE"}
	p := paneModel(t, b, tabTerminal)
	p.detail.cache[tabTerminal] = tabContent{loaded: true, body: strings.Repeat("screen line\n", 200)}
	p.detail.vp.SetContent(bodyOf(tabTerminal, p.detail.cache[tabTerminal], false))

	got := p.view(p.width)
	lines := strings.Split(got, "\n")
	if len(lines) != p.rows {
		t.Fatalf("%d pane rows, want %d", len(lines), p.rows)
	}
	if p.detail.vp.Width != p.contentWidth() {
		t.Errorf("vp.Width = %d, want contentWidth %d", p.detail.vp.Width, p.contentWidth())
	}
	bar := 0
	for i, l := range lines {
		if w := lipgloss.Width(l); w != p.width {
			t.Errorf("row %d is %d wide, pane is %d", i, w, p.width)
		}
		if strings.Contains(stripANSI(l), "█") || strings.Contains(stripANSI(l), "│") {
			bar++
			if !strings.HasSuffix(stripANSI(l), "█") && !strings.HasSuffix(stripANSI(l), "│") {
				t.Errorf("row %d draws the bar outside the last column: %q", i, stripANSI(l))
			}
		}
	}
	if bar != p.detail.vp.Height {
		t.Errorf("bar drawn on %d rows, want every viewport row (%d)", bar, p.detail.vp.Height)
	}
}
