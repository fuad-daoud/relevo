package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/muesli/termenv"
)

func TestFleetGroupsOrderAndOmission(t *testing.T) {
	rows := []view.BindingStatus{
		{Name: "b-idle", Display: "ACTIVE", BuilderStatus: "idle"},
		{Name: "b-need", Display: "NEEDS YOU"},
		{Name: "b-work", Display: "ACTIVE", BuilderStatus: "working"},
		{Name: "b-held", Display: "PAUSED"},
	}
	env := Env{Loaded: true, Report: view.Report{Bindings: rows}, Now: railNow, Width: 140, Height: 40}
	f := newFleetView(true)
	body := f.Body(env, 140, 40)
	plainBody := stripANSI(body)

	// Groups appear in order: needs you, working, idle, on hold
	idxNeed := strings.Index(plainBody, "needs you")
	idxWork := strings.Index(plainBody, "working")
	idxIdle := strings.Index(plainBody, "idle")
	idxHeld := strings.Index(plainBody, "on hold")
	if idxNeed < 0 || idxWork < 0 || idxIdle < 0 || idxHeld < 0 {
		t.Fatalf("expected all groups present: need=%d work=%d idle=%d held=%d", idxNeed, idxWork, idxIdle, idxHeld)
	}
	if !(idxNeed < idxWork && idxWork < idxIdle && idxIdle < idxHeld) {
		t.Errorf("groups not in order: need=%d work=%d idle=%d held=%d", idxNeed, idxWork, idxIdle, idxHeld)
	}

	// Empty group (other) has no section line
	if strings.Contains(plainBody, "other") {
		t.Errorf("empty group 'other' must not have a section line, body:\n%s", plainBody)
	}
}

func TestFleetDoneFold(t *testing.T) {
	rows := []view.BindingStatus{
		{Name: "live-1", Display: "ACTIVE", BuilderStatus: "working"},
		{Name: "done-1", Display: "DONE", Last: &view.LastEvent{TS: railNow.Add(-1 * time.Hour)}},
		{Name: "done-2", Display: "DONE", Last: &view.LastEvent{TS: railNow.Add(-2 * time.Hour)}},
		{Name: "done-3", Display: "DONE", Last: &view.LastEvent{TS: railNow.Add(-3 * time.Hour)}},
	}
	env := Env{Loaded: true, Report: view.Report{Bindings: rows}, Now: railNow, Width: 140, Height: 40}
	f := newFleetView(true)

	// Folded: rows() excludes done rows
	vis := f.rows(env)
	if len(vis) != 1 || vis[0].Name != "live-1" {
		t.Fatalf("folded rows() = %v, want [live-1]", vis)
	}
	body := stripANSI(f.Body(env, 140, 40))
	if !strings.Contains(body, "3 done") {
		t.Errorf("fold line must say '3 done', got:\n%s", body)
	}

	// Pressing '.' includes them
	next, _ := f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'.'}}, env)
	f = next.(fleetView)
	vis = f.rows(env)
	if len(vis) != 4 {
		t.Fatalf("unfolded rows() length = %d, want 4", len(vis))
	}

	// Toggle back to folded
	next, _ = f.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'.'}}, env)
	f = next.(fleetView)
	if len(f.rows(env)) != 1 {
		t.Fatalf("folded rows() length = %d, want 1", len(f.rows(env)))
	}

	// A filter matching one done row shows it while folded
	f.filterText = "done-2"
	vis = f.rows(env)
	if len(vis) != 1 || vis[0].Name != "done-2" {
		t.Fatalf("filtered rows() = %v, want [done-2]", vis)
	}
	filteredBody := stripANSI(f.Body(env, 140, 40))
	if !strings.Contains(filteredBody, "done-2") {
		t.Errorf("filtered body must contain done-2, got:\n%s", filteredBody)
	}
}

func TestFleetCardKeysByGroup(t *testing.T) {
	rows := []view.BindingStatus{
		{Name: "b-need", Display: "NEEDS YOU"},
		{Name: "b-work", Display: "ACTIVE", BuilderStatus: "working"},
		{Name: "b-idle", Display: "ACTIVE", BuilderStatus: "idle"},
		{Name: "b-held", Display: "PAUSED"},
		{Name: "b-done", Display: "DONE"},
	}
	env := Env{
		Loaded:  true,
		Report:  view.Report{Bindings: rows},
		Now:     railNow,
		Width:   140,
		Height:  40,
		Actions: &fakeActions{},
	}
	f := newFleetView(true).withActions(true)
	f.showDone = true // include done in rows

	vis := f.rows(env)
	expectedKeys := map[string][]string{
		"b-need": {"open round", "send the next plan", "shell", "stop"},
		"b-work": {"open round", "stop", "gate", "shell"},
		"b-idle": {"send the next plan", "open round", "done", "shell"},
		"b-held": {"open round", "send", "done"},
		"b-done": {"open round", "unbind"},
	}

	for i, b := range vis {
		f.cursor = i
		card := f.cardLines(env, b, 140)
		cardText := stripANSI(strings.Join(card, "\n"))
		wants := expectedKeys[b.Name]
		for _, want := range wants {
			if !strings.Contains(cardText, want) {
				t.Errorf("%s card missing key %q:\n%s", b.Name, want, cardText)
			}
		}
	}

	// With Actions == nil, only open round
	envNoActions := env
	envNoActions.Actions = nil
	for i, b := range vis {
		f.cursor = i
		card := f.cardLines(envNoActions, b, 140)
		cardText := stripANSI(strings.Join(card, "\n"))
		if !strings.Contains(cardText, "open round") {
			t.Errorf("%s card without actions missing 'open round':\n%s", b.Name, cardText)
		}
		for _, notWant := range []string{"send", "shell", "stop", "gate", "unbind"} {
			if strings.Contains(cardText, notWant) {
				t.Errorf("%s card without actions has %q:\n%s", b.Name, notWant, cardText)
			}
		}
	}
}

func TestFleetRowDropOrder(t *testing.T) {
	cases := []struct {
		width     int
		wantCand  bool
		wantSpend bool
		wantPlan  bool
	}{
		{132, true, true, true},
		{90, true, true, false},
		{70, true, false, false},
		{50, false, false, false},
	}
	for _, tc := range cases {
		cand, spend, plan := fleetRowPlan(tc.width)
		if cand != tc.wantCand || spend != tc.wantSpend || plan != tc.wantPlan {
			t.Errorf("fleetRowPlan(%d) = (cand:%v, spend:%v, plan:%v), want (%v, %v, %v)",
				tc.width, cand, spend, plan, tc.wantCand, tc.wantSpend, tc.wantPlan)
		}
	}

	// Verify name and now always present in rendered row
	b := view.BindingStatus{
		Name:             "webshop",
		Round:            2,
		Display:          "ACTIVE",
		BuilderStatus:    "working",
		BuilderCandidate: "cline/deepseek",
		MasterMindName:   "architect-1",
		Spend:            &usage.Spend{Measured: 0.05},
	}
	for _, w := range []int{132, 90, 70, 50} {
		line := stripANSI(fleetRowLine(b, groupWorking, false, railNow, w))
		if !strings.Contains(line, "webshop") {
			t.Errorf("width %d: name must be present in line: %q", w, line)
		}
		if !strings.Contains(line, "working") {
			t.Errorf("width %d: now must be present in line: %q", w, line)
		}
	}
}

func TestHeaderShowsVersionNotGates(t *testing.T) {
	st := store.New(t.TempDir())
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st}}, Options{
		Interval: time.Second,
		Version:  "v0.13.0-28-gb66c6fc",
	})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = res.(Model)
	m.statusInFlight = false
	rep := view.Report{
		Bindings: []view.BindingStatus{{Name: "web", Display: "ACTIVE"}},
		Gated: []availability.Gate{
			{Token: "agy/antigravity/claude-sonnet-4-6", Kind: availability.RateLimited, Since: railNow, Until: railNow.Add(time.Hour)},
		},
	}
	res, _ = m.Update(statusMsg{report: rep})
	m = res.(Model)

	header := stripANSI(m.headerView(m.env()))
	if !strings.Contains(header, "v0.13.0-28") {
		t.Errorf("header must contain shortVersion 'v0.13.0-28', got: %q", header)
	}
	if strings.Contains(header, "gated") {
		t.Errorf("header must NOT contain 'gated', got: %q", header)
	}

	body := stripANSI(m.body(m.env()))
	if !strings.Contains(body, "◌ gated  antigravity") {
		t.Errorf("fleet body must contain '◌ gated  antigravity', got:\n%s", body)
	}
}

func TestGatedLineOneProviderLatestUntil(t *testing.T) {
	env := Env{
		Now:   railNow,
		Width: 140,
		Report: view.Report{
			Gated: []availability.Gate{
				{Token: "codex/openai/gpt-4o", Kind: availability.RateLimited, Since: railNow, Until: railNow.Add(10 * time.Minute)},
				{Token: "codex/openai/gpt-5", Kind: availability.RateLimited, Since: railNow, Until: railNow.Add(25 * 24 * time.Hour)},
			},
		},
	}
	gl := stripANSI(gatedLine(env, 140))
	if count := strings.Count(gl, "openai"); count != 1 {
		t.Errorf("gatedLine provider 'openai' must appear once, got %d times in: %q", count, gl)
	}
	if !strings.Contains(gl, "openai 25d") {
		t.Errorf("gatedLine must contain 'openai 25d', got: %q", gl)
	}
}

func TestHelpListsHelpKeys(t *testing.T) {
	st := store.New(t.TempDir())
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st}}, Options{
		Interval: time.Second,
		Actions:  &fakeActions{},
	})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: view.Report{Bindings: []view.BindingStatus{{Name: "web", Display: "ACTIVE"}}}})
	m = res.(Model)

	footer := stripANSI(m.keysView(m.env()))
	if !strings.Contains(footer, "?  all keys") {
		t.Errorf("footer keysView must contain '?  all keys', got: %q", footer)
	}

	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = res.(Model)
	help := stripANSI(m.helpBody(m.env(), 40))
	if !strings.Contains(help, "done rows") {
		t.Errorf("helpBody must list 'done rows', got:\n%s", help)
	}
	if !strings.Contains(help, "retry on…") {
		t.Errorf("helpBody must list 'retry on…', got:\n%s", help)
	}
}

func TestFleetCardAboveList(t *testing.T) {
	m := goldenActionModel(t, 132, 34, &fakeActions{}, realFleetReport())
	body := stripANSI(m.body(m.env()))
	lines := strings.Split(body, "\n")

	cardIdx := -1
	needsYouIdx := -1
	var lastNonBlankIdx int = -1
	var lastNonBlankLine string

	for i, l := range lines {
		if strings.Contains(l, "╭─ fix-433") && cardIdx < 0 {
			cardIdx = i
		}
		if strings.Contains(l, "needs you") && needsYouIdx < 0 {
			needsYouIdx = i
		}
		if strings.TrimSpace(l) != "" {
			lastNonBlankIdx = i
			lastNonBlankLine = l
		}
	}

	if cardIdx < 0 {
		t.Fatalf("card '╭─ fix-433' not found in body:\n%s", body)
	}
	if needsYouIdx < 0 {
		t.Fatalf("section 'needs you' not found in body:\n%s", body)
	}
	if cardIdx >= needsYouIdx {
		t.Errorf("card line index (%d) must be less than 'needs you' section line index (%d)", cardIdx, needsYouIdx)
	}
	if lastNonBlankIdx < 0 || !strings.Contains(lastNonBlankLine, "gated") {
		t.Errorf("last non-blank body line must be the gated line, got line %d: %q", lastNonBlankIdx, lastNonBlankLine)
	}
}

func TestFleetCardCommitPlural(t *testing.T) {
	env := Env{Now: railNow, Width: 132, Height: 34}
	f := newFleetView(true)

	b1 := view.BindingStatus{
		Name:      "b1",
		Display:   "ACTIVE",
		LastClose: &view.CloseInfo{Commits: 1},
	}
	card1 := strings.Join(f.cardLines(env, b1, 132), "\n")
	plain1 := stripANSI(card1)
	if !strings.Contains(plain1, "+1 commit") {
		t.Errorf("Commits=1 must render '+1 commit', got:\n%s", plain1)
	}
	if strings.Contains(plain1, "+1 commits") {
		t.Errorf("Commits=1 must NOT render '+1 commits', got:\n%s", plain1)
	}

	b2 := view.BindingStatus{
		Name:      "b2",
		Display:   "ACTIVE",
		LastClose: &view.CloseInfo{Commits: 2},
	}
	card2 := strings.Join(f.cardLines(env, b2, 132), "\n")
	plain2 := stripANSI(card2)
	if !strings.Contains(plain2, "+2 commits") {
		t.Errorf("Commits=2 must render '+2 commits', got:\n%s", plain2)
	}
}

func TestFleetUnreadTintOnlyOnIdle(t *testing.T) {
	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)

	bIdle := view.BindingStatus{
		Name:          "b-idle",
		Display:       "ACTIVE",
		BuilderStatus: "idle",
		Unread:        true,
	}
	bDone := view.BindingStatus{
		Name:    "b-done",
		Display: "DONE",
		Unread:  true,
	}

	idleLine := fleetRowLine(bIdle, groupIdle, false, railNow, 120)
	doneLine := fleetRowLine(bDone, groupDone, false, railNow, 120)

	accentEscape := strings.Split(accentStyle.Render("X"), "X")[0]
	if accentEscape == "" {
		t.Fatalf("accent colour escape sequence must not be empty in TrueColor")
	}
	if !strings.Contains(idleLine, accentEscape) {
		t.Errorf("unread idle row line must contain accent colour escape %q, got: %q", accentEscape, idleLine)
	}
	if strings.Contains(doneLine, accentEscape) {
		t.Errorf("unread done row line must NOT contain accent colour escape %q, got: %q", accentEscape, doneLine)
	}
}

func TestFleetCardNoEmptyPart(t *testing.T) {
	b := view.BindingStatus{
		Name:    "bare-row",
		Display: "ACTIVE",
	}
	f := fleetView{}
	env := Env{}
	card := f.cardLines(env, b, 140)
	plain := stripANSI(strings.Join(card, "\n"))
	if strings.Contains(plain, "·    ·") {
		t.Errorf("card contains empty part '·    ·':\n%s", plain)
	}
}

func TestFleetReportedRowShowsTheRoundThatReported(t *testing.T) {
	now := railNow
	b := view.BindingStatus{
		Name:          "b-idle",
		Round:         3,
		Display:       "ACTIVE",
		BuilderStatus: "idle",
		LastPayload: &view.LastEvent{
			Round:     2,
			Direction: store.DirToMasterMind,
			Kind:      store.KindReport,
			TS:        now.Add(-2 * time.Minute),
		},
	}

	if got := stripANSI(rowNow(b, now)); !strings.HasPrefix(got, "r2 · reported") {
		t.Errorf("rowNow = %q, want it to start with %q", got, "r2 · reported")
	}

	f := fleetView{}
	env := Env{Now: now, Width: 140, Height: 40}
	card := stripANSI(strings.Join(f.cardLines(env, b, 140), "\n"))
	if !strings.Contains(card, "round 2 · reported") {
		t.Errorf("card must say %q, got:\n%s", "round 2 · reported", card)
	}
	if strings.Contains(card, "round 3") {
		t.Errorf("card must not name the next round, got:\n%s", card)
	}
}

func TestFleetWorkingRowShowsTheRoundInFlight(t *testing.T) {
	b := view.BindingStatus{
		Name:          "b-work",
		Round:         3,
		Display:       "ACTIVE",
		BuilderStatus: "working",
		LastPayload: &view.LastEvent{
			Round:     3,
			Direction: store.DirToBuilder,
			Kind:      store.KindPrompt,
		},
	}
	if got := stripANSI(rowNow(b, railNow)); !strings.HasPrefix(got, "r3 · ") {
		t.Errorf("rowNow = %q, want it to start with %q", got, "r3 · ")
	}
}

// TestFleetRowNowWords pins the cockpit's working word: it comes from the one
// activity rule (quiet while the sampler is silent, the definite words
// verbatim) and falls back to the runner's own status for a word the rule does
// not claim, so an unknown row still says unknown.
func TestFleetRowNowWords(t *testing.T) {
	quiet := view.BindingStatus{Name: "b-quiet", Round: 3, Display: "ACTIVE", BuilderStatus: "working", QuietFor: "3m"}
	if got := stripANSI(rowNow(quiet, railNow)); got != "r3 · quiet 3m" {
		t.Errorf("rowNow(quiet) = %q, want %q", got, "r3 · quiet 3m")
	}

	unknown := view.BindingStatus{Name: "b-unknown", Round: 2, Display: "ACTIVE", BuilderStatus: "unknown"}
	if got := stripANSI(rowNow(unknown, railNow)); got != "r2 · unknown" {
		t.Errorf("rowNow(unknown) = %q, want %q", got, "r2 · unknown")
	}
}

func TestGatedLineEndsInEllipsisWhenCut(t *testing.T) {
	env := Env{
		Now: railNow,
		Report: view.Report{Gated: []availability.Gate{
			{Token: "agy/antigravity-community-build/claude", Kind: availability.RateLimited, Since: railNow, Until: railNow.Add(10 * time.Minute)},
			{Token: "codex/openrouter-free-tier/gpt-5", Kind: availability.RateLimited, Since: railNow, Until: railNow.Add(20 * time.Minute)},
			{Token: "claude/anthropic-claude-enterprise/sonnet", Kind: availability.RateLimited, Since: railNow, Until: railNow.Add(30 * time.Minute)},
		}},
	}

	full := stripANSI(gatedLine(env, 200))
	if !strings.Contains(full, "anthropic-claude-enterprise") || strings.Contains(full, "…") {
		t.Fatalf("a line that fits must be intact and unellipsised, got %q", full)
	}

	cut := gatedLine(env, 80)
	if got := lipgloss.Width(cut); got != 80 {
		t.Errorf("gatedLine(env, 80) is %d cells wide, want 80", got)
	}
	if plain := strings.TrimRight(stripANSI(cut), " "); !strings.HasSuffix(plain, "…") {
		t.Errorf("a cut gated line must end in an ellipsis, got %q", plain)
	}
}
