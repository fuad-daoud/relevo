package ui

import (
	"context"
	"fmt"
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
)

// splitModel builds the shell at width x height with the given live rows
// (R2.10: the split is gone (X1); this is now just "a loaded shell").
func splitModel(t *testing.T, width, height int, rows ...view.BindingStatus) Model {
	t.Helper()
	st := store.New(t.TempDir())
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st}}, Options{Interval: time.Second})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: view.Report{Bindings: rows}})
	return res.(Model)
}

// fleet is the shell's root fleet view.
func fleet(m Model) fleetView { return m.stack[0].(fleetView) }

// testEnv is a view's Env for a test at width x height, clock railNow.
func testEnv(src Source, rep view.Report, width, height int) Env {
	return Env{Ctx: context.Background(), Src: src, Report: rep, Loaded: true,
		StatusAt: railNow, Now: railNow, Width: width, Height: height}
}

// newTestRound builds a round view over key with no live rows beyond ret.
func newTestRound(t *testing.T, rt relevo.Runtime, rep view.Report, key string, round int) roundView {
	t.Helper()
	v, _ := newRoundView(testEnv(mastermindSource{rt}, rep, 140, 40), key, round)
	return v.(roundView)
}

// newTestHistRound builds a hist round view over h.
func newTestHistRound(t *testing.T, rt relevo.Runtime, h relevo.HistoryBinding, round int) roundView {
	t.Helper()
	v, _ := newHistRoundView(testEnv(mastermindSource{rt}, view.Report{}, 140, 40), h, round)
	return v.(roundView)
}

// drain runs cmds through the model, unwrapping tea.BatchMsg as the
// bubbletea loop does, until nothing is left. Commands the model returns
// are drained too, so a save and its prefsSavedMsg do not leak.
func drain(t *testing.T, m Model, cmds ...tea.Cmd) Model {
	t.Helper()
	queue := append([]tea.Cmd(nil), cmds...)
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		if b, ok := msg.(tea.BatchMsg); ok {
			queue = append(append([]tea.Cmd(nil), b...), queue...)
			continue
		}
		res, next := m.Update(msg)
		m = res.(Model)
		if next != nil {
			queue = append(queue, next)
		}
	}
	return m
}

// roundKey sends one key through a round view's Update.
func roundKey(rv roundView, k tea.KeyMsg) roundView {
	next, _ := rv.Update(k, testEnv(mastermindSource{relevo.Runtime{}}, rv.pane.report, rv.pane.width, rv.pane.rows+4))
	return next.(roundView)
}

// roundMsg sends one message through a round view's Update.
func roundMsg(rv roundView, msg tea.Msg) roundView {
	next, _ := rv.Update(msg, testEnv(mastermindSource{relevo.Runtime{}}, rv.pane.report, rv.pane.width, rv.pane.rows+4))
	return next.(roundView)
}

func threeRows() []view.BindingStatus {
	return []view.BindingStatus{
		{Name: "api", Round: 2, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "working", Last: &view.LastEvent{TS: railNow.Add(-6 * time.Minute)}},
		{Name: "docs", Round: 1, Display: "DONE", BuilderKind: "agy", Last: &view.LastEvent{TS: railNow.Add(-time.Hour)}},
		{Name: "webshop", Round: 4, Display: "NEEDS YOU", BuilderKind: "agy", BuilderStatus: "blocked", Last: &view.LastEvent{TS: railNow.Add(-2 * time.Minute)}},
	}
}

// TestStickyFollowsKeyAcrossOwners: the fleet's sticky is the row key, so a
// second client's same-named binding appearing above must not steal the
// cursor -- it stays on the key it was on.
func TestStickyFollowsKeyAcrossOwners(t *testing.T) {
	m := splitModel(t, 140, 40, view.BindingStatus{
		Name: "persist", Owner: "b", OwnerLabel: "b", Round: 1, Display: "ACTIVE", BuilderKind: "agy",
	})
	fv := fleet(m)
	if got := fv.rows(m.env())[fv.cursor].Key(); got != "b/persist" {
		t.Fatalf("cursor on %q", got)
	}
	res, _ := m.Update(statusMsg{report: view.Report{Bindings: []view.BindingStatus{
		{Name: "persist", Owner: "a", OwnerLabel: "a", Round: 1, Display: "ACTIVE", BuilderKind: "agy"},
		{Name: "persist", Owner: "b", OwnerLabel: "b", Round: 1, Display: "ACTIVE", BuilderKind: "agy"},
	}}})
	m = res.(Model)
	fv = fleet(m)
	if got := fv.rows(m.env())[fv.cursor].Key(); got != "b/persist" {
		t.Errorf("cursor moved to %q, want b/persist", got)
	}
}

// TestFleetNameColumnShowsOwnerName is the owner-grouping port (R2.10): a
// server row's NAME cell is its Key(), owner/name, so two owners' same-named
// bindings read apart.
func TestFleetNameColumnShowsOwnerName(t *testing.T) {
	m := splitModel(t, 140, 40,
		view.BindingStatus{Name: "api", Owner: "a", OwnerLabel: "a", Round: 1, Display: "ACTIVE", BuilderKind: "agy"},
		view.BindingStatus{Name: "api", Owner: "b", OwnerLabel: "b", Round: 2, Display: "ACTIVE", BuilderKind: "agy"},
	)
	view := plain(m.View())
	if !strings.Contains(view, "a/api") || !strings.Contains(view, "b/api") {
		t.Errorf("NAME column must show owner/name for both rows:\n%s", view)
	}
}

// TestSortToggleKeepsSelection pins §5.4's `a`: it flips the order, keeps
// the selection, and returns the sort pref.
func TestSortToggleKeepsSelection(t *testing.T) {
	b1 := view.BindingStatus{Name: "api", Round: 2, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "working", Last: &view.LastEvent{TS: railNow.Add(-6 * time.Minute)}}
	b2 := view.BindingStatus{Name: "docs", Round: 1, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "working", Last: &view.LastEvent{TS: railNow.Add(-time.Hour)}}
	b3 := view.BindingStatus{Name: "webshop", Round: 4, Display: "ACTIVE", BuilderKind: "agy", BuilderStatus: "working", Last: &view.LastEvent{TS: railNow.Add(-2 * time.Minute)}}
	m := splitModel(t, 140, 40, b1, b2, b3)
	res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = res.(Model)
	fv := fleet(m)
	if got := fv.rows(m.env())[fv.cursor].Name; got != "api" {
		t.Fatalf("cursor on %q", got)
	}
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = res.(Model)
	if cmd == nil {
		t.Fatal("a must return a command (the sort pref)")
	}
	fv = fleet(m)
	if fv.attention {
		t.Error("a must switch to name order")
	}
	if got := fv.rows(m.env())[fv.cursor].Name; got != "api" {
		t.Errorf("cursor moved to %q on re-sort", got)
	}
	rows := fv.rows(m.env())
	if rows[0].Name != "api" || rows[2].Name != "webshop" {
		t.Errorf("name order = %v", rows)
	}
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if !fleet(res.(Model)).attention {
		t.Error("a twice is identity")
	}
}

// TestSendKeyDoesNotSortWithoutActions pins W2: with no Actions seam, `s` is
// neither send nor sort. It does nothing: no command, and the sort order is
// untouched.
func TestSendKeyDoesNotSortWithoutActions(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	if !fleet(m).attention {
		t.Fatal("the fleet must start in attention order")
	}
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if cmd != nil {
		t.Errorf("s without Actions must return no command, got %v", cmd)
	}
	if !fleet(res.(Model)).attention {
		t.Error("s without Actions must not sort")
	}
}

// TestFooterNoticesAndRefreshAge pins §5.3's keys row: the top view's keys
// and the globals on the left, the notice and the refresh age on the right,
// the right winning on overlap. The old "<other> NEEDS YOU" note is gone
// (X6: the header's `● N need you` replaces it).
func TestFooterNoticesAndRefreshAge(t *testing.T) {
	m := splitModel(t, 140, 40, threeRows()...)
	m.now = func() time.Time { return railNow.Add(2 * time.Second) }

	// The refresh age is the fleet's context-row right side (§5.4).
	if got := stripANSI(m.contextView(m.env())); !strings.Contains(got, "refreshed 2s ago") {
		t.Errorf("context row must carry the refresh age: %q", got)
	}

	// A notice is the keys row's right side (§5.3).
	res, _ := m.Update(noticeMsg{text: "hello"})
	m = res.(Model)
	if !strings.Contains(stripANSI(m.keysView(m.env())), "hello") {
		t.Errorf("keys row must carry the notice: %q", stripANSI(m.keysView(m.env())))
	}
	if !strings.Contains(stripANSI(m.keysView(m.env())), "command") || !strings.Contains(stripANSI(m.keysView(m.env())), "all keys") {
		t.Errorf("the globals must be in the keys row: %q", stripANSI(m.keysView(m.env())))
	}

	// A round view on top contributes its own keys.
	v, _ := newRoundView(m.env(), "api", 0)
	m.stack = append(m.stack, v)
	if !strings.Contains(stripANSI(m.keysView(m.env())), "[ ]") {
		t.Errorf("the round view's [ ] key must be in the keys row: %q", stripANSI(m.keysView(m.env())))
	}

	// The right side wins on overlap: at a narrow width the notice survives
	// and the view's keys give way -- but never half a key, and the global
	// tail stays (§2.3b).
	m.width = 60
	f := stripANSI(m.keysView(m.env()))
	if lipgloss.Width(f) > 60 {
		t.Errorf("keys row wider than the terminal: %q", f)
	}
	if !strings.Contains(f, "hello") {
		t.Errorf("the notice must survive the squeeze: %q", f)
	}
	if strings.Contains(f, "[ ] round") {
		t.Errorf("the view's keys must be the side that gives way: %q", f)
	}
	if !strings.Contains(f, "all keys") || !strings.Contains(f, "back") {
		t.Errorf("the global tail must survive the squeeze: %q", f)
	}
}

// TestHeaderGatesAndClock pins the header's right side (§5.3): the clock
// and needs-you count, and the gated line in the fleet body (D2).
func TestHeaderGatesAndClock(t *testing.T) {
	t.Cleanup(availability.SetGateClock(func() time.Time { return railNow }))
	m := splitModel(t, 140, 40, threeRows()...)
	m.report.Gated = []availability.Gate{{Token: "codex", Kind: availability.RateLimited, Since: railNow, Until: railNow.Add(88 * time.Minute)}}
	h := stripANSI(m.headerView(m.env()))
	if strings.Contains(h, "gated") {
		t.Errorf("header must not contain gates (D2): %q", h)
	}
	if !strings.Contains(h, "14:02") {
		t.Errorf("header must carry clock: %q", h)
	}
	// The needs-you count is on the header too.
	if !strings.Contains(h, "● 1 needs you") {
		t.Errorf("header must carry the needs-you count: %q", h)
	}
	// Gates are rendered in the fleet body instead (§2.3, D2).
	body := stripANSI(fleet(m).Body(m.env(), 140, 40))
	if !strings.Contains(body, "gated  codex") {
		t.Errorf("fleet body must contain gated line: %q", body)
	}
}

// TestTerminalFollowsTailUntilScrolledUp is the surviving tail rule
// (plan §3), ported to the round view (R2.10).
func TestTerminalFollowsTailUntilScrolledUp(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(newTestBinding("api")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt := relevo.Runtime{Store: st}
	rows := threeRows()
	rows[0].Headless = &view.HeadlessInfo{PID: 1, LogPath: "/x/002-builder.log"}

	rv := newTestRound(t, rt, view.Report{Bindings: rows}, "api", 0)
	rv = roundKey(rv, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	rv.pane.tabInFlight = false
	if !rv.pane.detail.follow {
		t.Fatal("a fresh terminal tab must follow")
	}
	body := func(n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "line %d\n", i)
		}
		return strings.TrimRight(b.String(), "\n")
	}
	rv = roundMsg(rv, tabMsg{name: "api", round: rv.pane.detail.round, t: tabTerminal, content: tabContent{loaded: true, body: body(100)}})
	if !rv.pane.detail.vp.AtBottom() {
		t.Error("following: a refresh must land at the bottom")
	}
	// Scroll up: follow clears.
	rv = roundKey(rv, tea.KeyMsg{Type: tea.KeyUp})
	if rv.pane.detail.follow {
		t.Error("scrolling up must stop following")
	}
	y := rv.pane.detail.vp.YOffset
	rv = roundMsg(rv, tabMsg{name: "api", round: rv.pane.detail.round, t: tabTerminal, content: tabContent{loaded: true, body: body(120)}})
	if rv.pane.detail.vp.YOffset != y {
		t.Errorf("not following: a refresh must hold the offset (%d -> %d)", y, rv.pane.detail.vp.YOffset)
	}
	// Back to the bottom: follow resumes.
	for i := 0; i < 200 && !rv.pane.detail.vp.AtBottom(); i++ {
		rv = roundKey(rv, tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if !rv.pane.detail.follow || !rv.pane.detail.vp.AtBottom() {
		t.Error("scrolling to the bottom must resume following")
	}
}

// TestPaneHeadShowsClientLine: an owner-labelled row's mastermind line is
// TestContextShowsClientLine: an owner-labelled row's mastermind line is
// replaced by the client line; a mastermind row keeps the mastermind line.
func TestContextShowsClientLine(t *testing.T) {
	id := "SHA256:VLERFMZnvN5HSw/GCBr6FXPEgs4QeAfdU95BUhMMqI0"

	client := view.BindingStatus{
		Name: "webshop", Owner: id, OwnerLabel: "zen", Round: 4, Display: "NEEDS YOU",
		BuilderKind: "agy", BuilderStatus: "blocked",
	}
	p := paneModel(t, client, tabReport)
	rv := roundView{pane: p}
	env := testEnv(p.src, view.Report{Bindings: []view.BindingStatus{client}}, p.width, p.rows)
	ctxLeft, _ := rv.Context(env)
	ctx := stripANSI(ctxLeft)
	if !strings.Contains(ctx, "client zen") {
		t.Errorf("no client line:\n%s", ctx)
	}
	if strings.Contains(ctx, "mastermind") {
		t.Errorf("the mastermind line must be replaced:\n%s", ctx)
	}

	mastermind := view.BindingStatus{
		Name: "webshop", Round: 4, Display: "NEEDS YOU",
		BuilderKind: "agy", BuilderStatus: "blocked",
		MasterMindID: "mastermind-9f2", MasterMindName: "architect-1", MasterMindKind: "claude", MasterMindRoute: "channel",
	}
	p = paneModel(t, mastermind, tabReport)
	rv = roundView{pane: p}
	env = testEnv(p.src, view.Report{Bindings: []view.BindingStatus{mastermind}}, p.width, p.rows)
	ctxLeft, _ = rv.Context(env)
	ctx = stripANSI(ctxLeft)
	if !strings.Contains(ctx, "architect-1") {
		t.Errorf("mastermind row must keep its mastermind line:\n%s", ctx)
	}
	if strings.Contains(ctx, "client") {
		t.Errorf("mastermind row must not show a client line:\n%s", ctx)
	}
}

func TestPaneHeadUsageAndSpendRows(t *testing.T) {
	var b view.BindingStatus
	for _, r := range threeRows() {
		if r.Name == "webshop" {
			b = r
		}
	}
	p := paneModel(t, b, tabReport)
	b.LastUsage = &usage.Usage{Harness: "claude", Provider: "anthropic", Model: "claude-sonnet-5", DurationMS: 9 * 60_000,
		Tokens: usage.Tokens{In: 100, CacheRead: 15_000_000, Out: 55_000}, Cost: usage.Cost{USD: 4.71, Basis: usage.Measured}, Samples: 1}
	b.Spend = &usage.Spend{Rounds: 2, Measured: 4.71, Unknown: 1}
	tokens := stripANSI(p.tokensLine(&b))
	if !strings.Contains(tokens, "tokens in 100 · cache 15.0M (100%) · out 55k · $4.71") {
		t.Errorf("no usage row in the block's own idiom:\n%s", tokens)
	}
	if !strings.Contains(tokens, "spend $4.71") {
		t.Errorf("no spend row:\n%s", tokens)
	}
}

// TestPaneHeadLiveUsageRow pins the live figure's place in the card
// (#234): a running round's `tokens` row is the live one, exactly one, and
// the closed round's row does not appear beside it; spend keeps its place.
func TestPaneHeadLiveUsageRow(t *testing.T) {
	var b view.BindingStatus
	for _, r := range threeRows() {
		if r.Name == "webshop" {
			b = r
		}
	}
	p := paneModel(t, b, tabReport)
	b.LastUsage = &usage.Usage{Harness: "agy", Provider: "google", Model: "gemini-3-pro", DurationMS: 6 * 60_000,
		Cost: usage.Cost{Basis: usage.Unknown}, Note: "agy keeps no usage record"}
	b.LiveUsage = &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash", DurationMS: 4 * 60_000,
		Tokens: usage.Tokens{In: 1_800, CacheRead: 91_000, CacheWrite: 3_100, Out: 8_200},
		Cost:   usage.Cost{USD: 0.04, Basis: usage.Measured}, Samples: 3}
	b.Spend = &usage.Spend{Rounds: 2, Measured: 0.16}
	tokens := stripANSI(p.tokensLine(&b))
	if n := strings.Count(tokens, "tokens "); n != 1 {
		t.Errorf("%d usage rows, want exactly one:\n%s", n, tokens)
	}
	if !strings.Contains(tokens, "tokens in 2k · cache 91k (95%) · write 3k · out 8k · $0.04") {
		t.Errorf("the usage row must be the live one:\n%s", tokens)
	}
	if strings.Contains(tokens, "gemini-3-pro") || strings.Contains(tokens, "6m") {
		t.Errorf("the closed round's row must yield to the live one:\n%s", tokens)
	}
	if !strings.Contains(tokens, "spend $0.16") {
		t.Errorf("no spend row:\n%s", tokens)
	}
}
