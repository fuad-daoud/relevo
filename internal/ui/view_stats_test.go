package ui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// statsFixture builds a rich stats.Report by hand, so the view tests and the
// goldens never need a database (§7).
func statsFixture() stats.Report {
	at := func(day int) time.Time {
		return time.Date(2026, time.September, day, 12, 0, 0, 0, time.UTC)
	}
	var counts [24]int
	counts[9], counts[14] = 2, 1
	return stats.Report{
		Since: at(16), Until: at(24),
		Totals: stats.Totals{
			Rounds: 6, Bindings: 2, Candidates: 2, Unrecorded: 1,
			CostUSD: 1.40, PlanRounds: 1, Tokens: 2_500_000, MedianMS: 27 * 60_000,
			TokenKinds: stats.TokenCounts{
				In: 40_000, Cache: 2_400_000, Write: 10_000, Out: 50_000, Measured: 5,
			},
		},
		Scorecard: []stats.ScoreRow{
			{
				Token:  "opencode/cline-pass/deepseek-v4.1-flash",
				Rounds: 5, Closed: 4, Reported: 3, Halted: 1,
				DonePct: 75, HaltPct: 25,
				MedianMS: 27 * 60_000, HasMedian: true,
				TTFTMS: 2500, HasTTFT: true,
				CostPerRound: 0.04, HasCost: true,
				CommitsPerRound: 1.2, HasCommits: true,
				TokenKinds: stats.TokenCounts{
					In: 30_000, Cache: 2_200_000, Write: 8_000, Out: 40_000, Measured: 4,
				},
			},
			{
				Token:  "agy/antigravity/claude-sonnet-4-6",
				Rounds: 2, Closed: 2, Reported: 2, DonePct: 100, HaltPct: 0,
				MedianMS: 9 * 60_000, HasMedian: true, Plan: true, Few: true,
				TokenKinds: stats.TokenCounts{
					In: 10_000, Cache: 200_000, Write: 2_000, Out: 10_000, Measured: 1,
				},
			},
		},
		Spend: stats.Spend{
			Days: []stats.DayCost{
				{Day: "2026-09-16", USD: 0.40, Tokens: 900_000, ByProvider: map[string]float64{"cline-pass": 0.40}},
				{Day: "2026-09-17", USD: 0.20, Tokens: 400_000, ByProvider: map[string]float64{"cline-pass": 0.20}},
				{Day: "2026-09-18", USD: 0.00, ByProvider: map[string]float64{}},
				{Day: "2026-09-19", USD: 0.80, Tokens: 1_200_000, ByProvider: map[string]float64{"google": 0.80}},
			},
			ThisWeek: 1.40, LastWeek: 0.55,
		},
		Reliability: stats.Reliability{
			Switches: 2, RoundsSwitched: 1, SwitchPct: 33, RateLimits: 3, SpawnFailures: 1,
			ByHour: []stats.HourRow{{Provider: "google", Counts: counts}},
			Active: []availability.Gate{{
				Token: "gemini-3.8-flash-high", Kind: availability.RateLimited,
				Since: at(24), Until: at(19).AddDate(0, 1, 0),
			}},
		},
		Repos: []stats.RepoRow{
			{
				GroupRow: stats.GroupRow{Key: "https://github.com/fuad-daoud/relevo", Rounds: 5, Halted: 1, Landed: 1, CostUSD: 1.30, Tokens: 2_400_000, RoundsPerLand: 3.0},
				Features: []stats.FeatureRow{
					{GroupRow: stats.GroupRow{Key: "cockpit", Rounds: 3, CostUSD: 0.90, Tokens: 1_500_000, Landed: 1, RoundsPerLand: 2.0}},
				},
				NoFeature: stats.FeatureRow{GroupRow: stats.GroupRow{Key: "(none)", Rounds: 2, Tokens: 900_000}},
			},
			{
				GroupRow: stats.GroupRow{Key: "(none)", Rounds: 1, CostUSD: 0.10, Tokens: 100_000},
			},
		},
		Outcomes: stats.Outcomes{
			ByRound: map[string]int{
				db.OutcomeReported: 4, db.OutcomeHalted: 1, db.OutcomeOpen: 1,
			},
			ByReport: map[string]int{"done": 3, "halted": 1, "no outcome": 1},
		},
	}
}

// statsFixtureRepoKey is the fixture's one labelled repo, the repo the repos
// tests expand and select.
const statsFixtureRepoKey = "https://github.com/fuad-daoud/relevo"

// statsTestView is a loaded stats view over the fixture.
func statsTestView(window string) statsView {
	return statsView{window: window, loaded: true, rep: statsFixture()}
}

// statsTwentyCandidates is the 20-row scorecard the overview scroll tests use:
// every row a closed, done round, the last five Few (§4.6).
func statsTwentyCandidates() stats.Report {
	rep := statsFixture()
	rep.Scorecard = nil
	for i := 0; i < 20; i++ {
		rep.Scorecard = append(rep.Scorecard, stats.ScoreRow{
			Token:  fmt.Sprintf("cand-%02d", i),
			Rounds: 6, Closed: 6, Reported: 6, DonePct: 100, Few: i >= 15,
		})
	}
	return rep
}

// statsTestEnv is a view Env with a store but no database.
func statsTestEnv(t *testing.T, width, height int) Env {
	t.Helper()
	return testEnv(mastermindSource{relevo.Runtime{Store: store.New(t.TempDir())}}, view.Report{}, width, height)
}

// statsKey builds a rune key.
func statsKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// statsShell builds the shell over a temp DB with the stats view at window
// pushed and its first fetch drained.
func statsShell(t *testing.T, width, height int, window string) Model {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	st := store.New(t.TempDir())
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st, DB: d}},
		Options{Interval: time.Second})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: view.Report{}})
	m = res.(Model)
	m = drain(t, m, execLine("stats "+window, m.env(), m.prefs))
	if _, ok := m.top().(statsView); !ok {
		t.Fatalf(":stats must replace the stack, top is %T", m.top())
	}
	return m
}

// statsReposShell is statsShell at 160x40 with the report fed and the repos
// tab open, already expanded on the given repo keys. It lets a test place the
// cursor on a child row without pressing the expansion keys.
func statsReposShell(t *testing.T, rep stats.Report, expanded ...string) Model {
	t.Helper()
	m := statsShell(t, 160, 40, "30d")
	res, _ := m.Update(statsMsg{window: "30d", rep: rep})
	m = res.(Model)
	res, _ = m.Update(statsKey('5'))
	m = res.(Model)
	v := m.top().(statsView)
	v.expanded = map[string]bool{}
	for _, k := range expanded {
		v.expanded[k] = true
	}
	m.stack[len(m.stack)-1] = v
	return m
}

// TestStatsWindowCycle: `w` cycles 7d → 30d → 90d → all → 7d, each returns a
// fetch, and a statsMsg for another window is dropped (§4.3, §7).
func TestStatsWindowCycle(t *testing.T) {
	env := statsTestEnv(t, 160, 40)
	v := View(statsTestView("30d"))

	for i, want := range []string{"90d", "all", "7d", "30d"} {
		next, cmd := v.Update(statsKey('w'), env)
		v = next
		if got := v.(statsView).window; got != want {
			t.Fatalf("w #%d: window = %q, want %q", i+1, got, want)
		}
		if cmd == nil {
			t.Fatalf("w #%d must return a fetch command", i+1)
		}
	}

	// A stale reply for another window must not touch the report.
	sv := v.(statsView)
	next, _ := sv.Update(statsMsg{window: "7d", rep: stats.Report{Totals: stats.Totals{Rounds: 99}}}, env)
	if got := next.(statsView).rep.Totals.Rounds; got != 6 {
		t.Errorf("stale statsMsg changed rep.Rounds to %d, want the fixture's", got)
	}
}

// TestStatsEnterOpensFilteredRounds: enter pushes `:rounds` filtered to the
// selected row, with the window's since term unless the window is all, and
// the (none) repo row notices (§4.3, §7). The candidates subtests are ported
// to the new table (§4.6): focus 0 indexes overviewCandRows, not the
// scorecard.
func TestStatsEnterOpensFilteredRounds(t *testing.T) {
	t.Run("candidates row", func(t *testing.T) {
		m := statsShell(t, 160, 40, "30d")
		res, _ := m.Update(statsMsg{window: "30d", rep: statsFixture()})
		m = res.(Model)
		res, _ = m.Update(statsKey('2'))
		m = res.(Model)
		rows := m.top().(statsView).overviewCandRows(func(s string) string { return s })
		if len(rows) == 0 {
			t.Fatal("the candidates table has no rows")
		}
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		rv, ok := m.top().(roundsView)
		if !ok {
			t.Fatalf("enter must push a rounds view, got %T", m.top())
		}
		if got, want := rv.dash.QueryText(), "candidate:"+rows[0].Token+" since:30d"; got != want {
			t.Errorf("QueryText = %q, want %q", got, want)
		}
	})

	t.Run("under all there is no since", func(t *testing.T) {
		m := statsShell(t, 160, 40, "all")
		res, _ := m.Update(statsMsg{window: "all", rep: statsFixture()})
		m = res.(Model)
		res, _ = m.Update(statsKey('2'))
		m = res.(Model)
		rows := m.top().(statsView).overviewCandRows(func(s string) string { return s })
		if len(rows) == 0 {
			t.Fatal("the candidates table has no rows")
		}
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		rv, ok := m.top().(roundsView)
		if !ok {
			t.Fatalf("enter must push a rounds view, got %T", m.top())
		}
		if got, want := rv.dash.QueryText(), "candidate:"+rows[0].Token; got != want {
			t.Errorf("QueryText = %q, want no since term", got)
		}
	})

	t.Run("repos row", func(t *testing.T) {
		m := statsShell(t, 160, 40, "30d")
		res, _ := m.Update(statsMsg{window: "30d", rep: statsFixture()})
		m = res.(Model)
		res, _ = m.Update(statsKey('5'))
		m = res.(Model)
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		rv, ok := m.top().(roundsView)
		if !ok {
			t.Fatalf("enter must push a rounds view, got %T", m.top())
		}
		want := `repo:"https://github.com/fuad-daoud/relevo" since:30d`
		if got := rv.dash.QueryText(); got != want {
			t.Errorf("QueryText = %q, want %q", got, want)
		}
	})

	t.Run("none row notices", func(t *testing.T) {
		m := statsShell(t, 160, 40, "30d")
		res, _ := m.Update(statsMsg{window: "30d", rep: statsFixture()})
		m = res.(Model)
		res, _ = m.Update(statsKey('5'))
		m = res.(Model)
		res, _ = m.Update(statsKey('j'))
		m = res.(Model)
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		if _, ok := m.top().(roundsView); ok {
			t.Fatal("the (none) row must not push a rounds view")
		}
		if !strings.Contains(m.notice, "rounds with no repo cannot be filtered") {
			t.Errorf("notice = %q", m.notice)
		}
	})
}

// TestStatsContextNoDollars ports round 1's S3 test to §2.1: the totals are
// the overview's now, and the context row still never shows a dollar amount.
func TestStatsContextNoDollars(t *testing.T) {
	v := statsTestView("30d")
	left, right := v.Context(statsTestEnv(t, 132, 34))

	if plain := stripANSI(left + right); strings.Contains(plain, "$") {
		t.Errorf("context row = %q, want no dollars", plain)
	}
}

// TestStatsContextIsTabs pins §2.1: the context row's left side is the tabs
// row and its right side the window chips; the totals line is gone. The word
// "tokens" is a tab name, so the totals check is on the fact strings the old
// row carried.
func TestStatsContextIsTabs(t *testing.T) {
	v := statsTestView("30d")
	left, right := v.Context(statsTestEnv(t, 132, 34))
	plainLeft := stripANSI(left)

	for _, want := range []string{"overview", "candidates"} {
		if !strings.Contains(plainLeft, want) {
			t.Errorf("context left = %q, want the %s tab", plainLeft, want)
		}
	}
	if strings.Contains(plainLeft, "rounds") {
		t.Errorf("context left = %q, want no totals", plainLeft)
	}
	for _, fact := range []string{"6 rounds", "2.5M tokens", "98% cached", "50k out", "27m median", "1 halted"} {
		if strings.Contains(plainLeft, fact) {
			t.Errorf("context left = %q, want no totals (found %q)", plainLeft, fact)
		}
	}
	if !strings.Contains(stripANSI(right), "30d") {
		t.Errorf("context right = %q, want the 30d window", stripANSI(right))
	}
}

// TestStatsOverviewFillsWidth pins §4.5: at 132 and 180 columns the four tiles
// fill the width (ROUNDS value at column 3, OUTPUT at 3 + 3*((w-6)/4)) and the
// right table's first repo row ends exactly at w-3.
func TestStatsOverviewFillsWidth(t *testing.T) {
	for _, w := range []int{132, 180} {
		v := statsTestView("30d")
		body := stripANSI(v.Body(statsTestEnv(t, w, 34), w, 34))
		lines := strings.Split(body, "\n")

		i := statsLineIndex(lines, "ROUNDS")
		if i < 0 {
			t.Fatalf("w=%d: no ROUNDS tile:\n%s", w, body)
		}
		value := lines[i+1]
		if got := statsFirstCol(value); got != 3 {
			t.Errorf("w=%d: ROUNDS value starts at %d, want 3", w, got)
		}
		want := 3 + 3*((w-4)/4)
		out := stats.ShortTokens(statsFixture().Totals.TokenKinds.Out)
		if got := strings.Index(value, out); got != want {
			t.Errorf("w=%d: OUTPUT value at %d, want %d", w, got, want)
		}

		h := statsLineIndex(lines, "% TOKENS")
		if h < 0 {
			t.Fatalf("w=%d: no %% TOKENS header:\n%s", w, body)
		}
		row := lines[h+1]
		if end := len([]rune(strings.TrimRight(row, " "))); end != w-3 {
			t.Errorf("w=%d: first repo row ends at %d, want %d", w, end, w-3)
		}
	}
}

// TestStatsOverviewColumnsAligned pins §4.7 and §4.8: the column where the
// header's DONE ends is the column where every scorecard row's done % ends,
// and the same holds for TOKENS on the repos table.
func TestStatsOverviewColumnsAligned(t *testing.T) {
	v := statsTestView("30d")
	body := stripANSI(v.Body(statsTestEnv(t, 132, 34), 132, 34))
	lines := strings.Split(body, "\n")
	rep := statsFixture()

	candHdr := statsLineIndex(lines, "IN/RND")
	if candHdr < 0 {
		t.Fatalf("no CANDIDATE header:\n%s", body)
	}
	doneEnd := statsColEnd(lines[candHdr], "DONE")
	if doneEnd < 0 {
		t.Fatalf("no DONE header:\n%s", body)
	}
	if len(rep.Scorecard) == 0 {
		t.Fatal("fixture has no scorecard rows")
	}
	for i, s := range rep.Scorecard {
		row := lines[candHdr+1+i]
		want := stats.PctText(s.DonePct, s.Closed)
		if end := statsColEnd(row, want); end != doneEnd {
			t.Errorf("candidate row %d: done %% ends at %d, want %d\n%q", i, end, doneEnd, row)
		}
	}

	repoHdr := statsLineIndex(lines, "% TOKENS")
	if repoHdr < 0 {
		t.Fatalf("no %% TOKENS header:\n%s", body)
	}
	tokEnd := statsColEnd(lines[repoHdr], "TOKENS")
	if tokEnd < 0 {
		t.Fatalf("no TOKENS header:\n%s", body)
	}
	rows := append([]stats.RepoRow(nil), rep.Repos...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Tokens > rows[j].Tokens })
	for i, g := range rows {
		row := lines[repoHdr+1+i]
		want := stats.ShortTokens(g.Tokens)
		if end := statsColEnd(row, want); end != tokEnd {
			t.Errorf("repo row %d: tokens ends at %d, want %d\n%q", i, end, tokEnd, row)
		}
	}
}

// TestStatsOverviewNarrowStacks pins §2.2 and §2.4 at 90 columns: the tiles
// are 2x2 and the two tables stack.
func TestStatsOverviewNarrowStacks(t *testing.T) {
	const w = 90
	v := statsTestView("30d")
	body := stripANSI(v.Body(statsTestEnv(t, w, 40), w, 40))
	lines := strings.Split(body, "\n")

	i := statsLineIndex(lines, "ROUNDS")
	if i < 0 {
		t.Fatalf("no ROUNDS tile:\n%s", body)
	}
	tileW := (w - 4) / 2
	tokens, out := stats.ShortTokens(statsFixture().Totals.TokenKinds.Total()), "50k"
	if got := strings.Index(lines[i+1], tokens); got != 3+tileW {
		t.Errorf("2x2 tiles: TOKENS value at %d, want %d", got, 3+tileW)
	}
	if strings.Contains(lines[i+1], out) {
		t.Errorf("2x2 tiles: OUTPUT must start the second tile row:\n%q", lines[i+1])
	}
	if got := strings.Index(lines[i+4], out); got != 3+tileW {
		t.Errorf("2x2 tiles: OUTPUT value at %d, want %d", got, 3+tileW)
	}

	candHdr := statsLineIndex(lines, "IN/RND")
	repoHdr := statsLineIndex(lines, "% TOKENS")
	if candHdr < 0 || repoHdr < 0 {
		t.Fatalf("missing table headers:\n%s", body)
	}
	if strings.Contains(lines[candHdr], "% TOKENS") {
		t.Errorf("tables must stack, got one header row: %q", lines[candHdr])
	}
	if repoHdr <= candHdr {
		t.Errorf("repos header at line %d must follow candidates at %d", repoHdr, candHdr)
	}
	if end := len(strings.TrimRight(lines[candHdr], " ")); end > 3+(w-6) {
		t.Errorf("stacked table ends at %d, want within %d", end, 3+(w-6))
	}
}

// statsLineIndex is the first line containing want, or -1.
func statsLineIndex(lines []string, want string) int {
	for i, l := range lines {
		if strings.Contains(l, want) {
			return i
		}
	}
	return -1
}

// statsFirstCol is the column of a line's first non-space cell, or -1.
func statsFirstCol(line string) int {
	for i, r := range line {
		if r != ' ' {
			return i
		}
	}
	return -1
}

// statsColEnd is the cell where word ends in line, or -1. It counts runes, so
// a cell that is one rune ("…", "▇") counts as one column.
func statsColEnd(line, word string) int {
	runes, w := []rune(line), []rune(word)
	for i := 0; i+len(w) <= len(runes); i++ {
		if string(runes[i:i+len(w)]) == word {
			return i + len(w) - 1
		}
	}
	return -1
}

// statsCellAt is the cell of a right-aligned column that ends at end in line:
// the run of non-space runes ending there, or "" when end is off the line.
func statsCellAt(line string, end int) string {
	runes := []rune(line)
	if end < 0 || end >= len(runes) {
		return ""
	}
	i := end
	for i >= 0 && runes[i] != ' ' {
		i--
	}
	return string(runes[i+1 : end+1])
}

// TestStatsOverviewTiles pins the overview's four tiles against the fixture's
// known TokenKinds (§3.3).
func TestStatsOverviewTiles(t *testing.T) {
	v := statsTestView("30d")
	body := stripANSI(v.Body(statsTestEnv(t, 132, 34), 132, 34))

	for _, want := range []string{"ROUNDS", "TOKENS", "CACHE", "OUTPUT", "2.5M", "98%", "5 rounds measured"} {
		if !strings.Contains(body, want) {
			t.Errorf("overview missing %q:\n%s", want, body)
		}
	}
}

// TestStatsOverviewNoTokens pins §3.3's empty chart: a report whose days
// recorded no tokens says so instead of drawing bars.
func TestStatsOverviewNoTokens(t *testing.T) {
	rep := statsFixture()
	rep.Totals.TokenKinds = stats.TokenCounts{}
	rep.Totals.Tokens = 0
	for i := range rep.Spend.Days {
		rep.Spend.Days[i].Tokens = 0
	}
	v := statsView{window: "30d", loaded: true, rep: rep}
	body := stripANSI(v.Body(statsTestEnv(t, 132, 34), 132, 34))

	if !strings.Contains(body, "no token usage recorded") {
		t.Errorf("overview without tokens = %q, want the no-token line", body)
	}
}

// TestStatsNoDatabase: `:stats` against a source with a nil DB is refused
// with the notice and the stack is unchanged (§4.6, §7).
func TestStatsNoDatabase(t *testing.T) {
	m := splitModel(t, 140, 40)
	cmd := execLine("stats", m.env(), m.prefs)
	m = drain(t, m, cmd)
	if len(m.stack) != 1 {
		t.Errorf("stack depth = %d, want 1 (unchanged)", len(m.stack))
	}
	if !strings.Contains(m.notice, "no database") {
		t.Errorf("notice = %q, want the no-database notice", m.notice)
	}

	if _, _, err := newStatsView(m.env(), "30d"); !errors.Is(err, relevo.ErrNoDatabase) {
		t.Errorf("newStatsView err = %v, want ErrNoDatabase", err)
	}
}

// TestStatsRefreshThrottle: a tick within 30s does not refetch; one after
// does (§4.3, §7).
func TestStatsRefreshThrottle(t *testing.T) {
	sv := statsTestView("30d")
	sv.fetchedAt = railNow

	soon := testEnv(mastermindSource{relevo.Runtime{Store: store.New(t.TempDir())}}, view.Report{}, 160, 40)
	soon.Now = railNow.Add(10 * time.Second)
	next, cmd := sv.Update(tickMsg(soon.Now), soon)
	if cmd != nil {
		t.Error("a tick within 30s must not refetch")
	}

	late := soon
	late.Now = railNow.Add(31 * time.Second)
	next, cmd = next.Update(tickMsg(late.Now), late)
	if cmd == nil {
		t.Error("a tick after 30s must refetch")
	}
	if !next.(statsView).fetching {
		t.Error("a refetch must mark the view fetching")
	}
}

// TestStatsFocusAndCursor ports the old two-panel focus test (S2): the tab bar
// decides which panel owns ↑↓, and j/k move that panel's cursor within bounds
// (§4.3, §4.4, §7). The repos cursor walks the visible rows: the fixture's two
// repo rows while every repo is collapsed.
func TestStatsFocusAndCursor(t *testing.T) {
	env := statsTestEnv(t, 100, 30)
	v := View(statsTestView("30d"))

	// The repos tab focuses the repos panel.
	next, _ := v.Update(statsKey('5'), env)
	v = next
	if got := v.(statsView).focus; got != 1 {
		t.Fatalf("repos tab: focus = %d, want 1", got)
	}
	// Two collapsed repos: j once reaches the second, j again stays there.
	next, _ = v.Update(statsKey('j'), env)
	v = next
	if got := v.(statsView).cursor[1]; got != 1 {
		t.Errorf("cursor[1] = %d, want 1 (the second repo row)", got)
	}
	next, _ = v.Update(statsKey('j'), env)
	v = next
	if got := v.(statsView).cursor[1]; got != 1 {
		t.Errorf("cursor[1] = %d, want 1 (clamped at the visible list's end)", got)
	}
	next, _ = v.Update(statsKey('k'), env)
	v = next
	if got := v.(statsView).cursor[1]; got != 0 {
		t.Errorf("cursor[1] = %d, want 0", got)
	}
	// The candidates tab focuses the candidates panel: k clamps at 0.
	next, _ = v.Update(statsKey('2'), env)
	v = next
	if got := v.(statsView).focus; got != 0 {
		t.Fatalf("candidates tab: focus = %d, want 0", got)
	}
	next, _ = v.Update(statsKey('k'), env)
	v = next
	if got := v.(statsView).cursor[0]; got != 0 {
		t.Errorf("cursor[0] = %d, want 0 (clamped)", got)
	}
}

// TestStatsTabsKeys pins §3.1's tab bar and §3.4's per-tab keys: tab cycles
// the five tabs and wraps, shift+tab goes back, a digit jumps to its tab, ↑↓
// move the overview's table cursor or the candidates and repos panel cursor,
// and enter on repos opens the filtered rounds (S2).
func TestStatsTabsKeys(t *testing.T) {
	env := statsTestEnv(t, 100, 30)
	v := View(statsTestView("30d"))

	// tab cycles five tabs and wraps back to overview.
	for i := 1; i <= len(statsTabs); i++ {
		next, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab}, env)
		v = next
		if got := v.(statsView).tab; got != i%len(statsTabs) {
			t.Fatalf("tab #%d: tab = %d, want %d", i, got, i%len(statsTabs))
		}
	}
	// shift+tab goes back.
	next, _ := v.Update(tea.KeyMsg{Type: tea.KeyShiftTab}, env)
	v = next
	if got := v.(statsView).tab; got != statsTabRepos {
		t.Fatalf("shift+tab: tab = %d, want %d (repos)", got, statsTabRepos)
	}
	// 3 jumps to tokens.
	next, _ = v.Update(statsKey('3'), env)
	v = next
	if got := v.(statsView).tab; got != statsTabTokens {
		t.Fatalf("3: tab = %d, want tokens", got)
	}

	// ↑↓ move the overview's own table cursor, not the tabs' panels...
	next, _ = v.Update(statsKey('1'), env)
	v = next
	next, _ = v.Update(statsKey('j'), env)
	v = next
	if got := v.(statsView); got.tab != statsTabOverview || got.ovCursor[0] != 1 ||
		got.cursor[0] != 0 || got.cursor[1] != 0 {
		t.Fatalf("j on overview must move ovCursor[0] to 1: %+v", got)
	}
	// ...and move the cursor on candidates.
	next, _ = v.Update(statsKey('2'), env)
	v = next
	next, _ = v.Update(statsKey('j'), env)
	v = next
	if got := v.(statsView).cursor[0]; got != 1 {
		t.Fatalf("j on candidates: cursor[0] = %d, want 1", got)
	}

	// enter on repos opens :rounds, through the existing shell helper.
	m := statsShell(t, 160, 40, "30d")
	res, _ := m.Update(statsMsg{window: "30d", rep: statsFixture()})
	m = res.(Model)
	res, _ = m.Update(statsKey('5'))
	m = res.(Model)
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	rv, ok := m.top().(roundsView)
	if !ok {
		t.Fatalf("enter on repos must push a rounds view, got %T", m.top())
	}
	want := `repo:"https://github.com/fuad-daoud/relevo" since:30d`
	if got := rv.dash.QueryText(); got != want {
		t.Errorf("QueryText = %q, want %q", got, want)
	}
}

// TestStatsNiceStep pins §4.1: the step is the smallest nice value at or above
// max/4, so ceil(max/step) lands in 1..4 and m*step is the axis maximum.
func TestStatsNiceStep(t *testing.T) {
	cases := []struct {
		max, step, axisMax float64
		m                  int
	}{
		{6_800_000, 2_000_000, 8_000_000, 4},
		{9_100_000, 2_500_000, 10_000_000, 4},
		{5_500_000, 2_000_000, 6_000_000, 3},
		{1000, 250, 1000, 4},
		{3, 1, 3, 3},
	}
	for _, c := range cases {
		got := statsNiceStep(c.max)
		if got != c.step {
			t.Errorf("statsNiceStep(%v) = %v, want %v", c.max, got, c.step)
			continue
		}
		m := int(math.Ceil(c.max / got))
		if m != c.m {
			t.Errorf("ceil(%v/%v) = %d, want %d", c.max, got, m, c.m)
		}
		if axisMax := float64(m) * got; axisMax != c.axisMax {
			t.Errorf("axis max = %v, want %v", axisMax, c.axisMax)
		}
	}
}

// statsTimelineDays30 is the timeline tests' 30-day series: five days carry
// tokens, the largest 6.8M, so statsNiceStep gives a 2M step and m is 4.
func statsTimelineDays30() []stats.DayCost {
	days := make([]stats.DayCost, 30)
	for i := range days {
		days[i].Day = time.Date(2026, time.August, 19, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02")
	}
	days[0].Tokens = 6_800_000
	days[7].Tokens = 2_000_000
	days[15].Tokens = 5_000_000
	days[22].Tokens = 1_000_000
	days[29].Tokens = 3_000_000
	return days
}

// TestStatsTimelineTicks pins §4.2 and §4.3 at 132 columns over 30 days: the
// plot is eight rows, the y ticks sit on rows 0, 2, 4 and 6 with their labels,
// the empty plot cells carry the gridlines, the axis carries one ┴ per x tick,
// and the x labels are five, seven days apart, ending at 09-17.
func TestStatsTimelineTicks(t *testing.T) {
	days := statsTimelineDays30()

	lines := statsTimelineLines(days, 132, 8)
	for i, l := range lines {
		lines[i] = stripANSI(l)
	}

	plot := lines[:len(lines)-2]
	if len(plot) != 8 {
		t.Fatalf("plot rows = %d, want 8:\n%s", len(plot), strings.Join(lines, "\n"))
	}
	for row, wants := range map[int][]string{
		0: {"┤", "8M"},
		2: {"┤", "6M"},
		4: {"┤", "4M"},
		6: {"┤", "2M"},
	} {
		for _, w := range wants {
			if !strings.Contains(plot[row], w) {
				t.Errorf("plot row %d = %q, want %q", row, plot[row], w)
			}
		}
	}
	if !strings.Contains(plot[2], "┈") {
		t.Errorf("plot row 2 = %q, want a ┈ gridline in an empty cell", plot[2])
	}
	axis := lines[len(lines)-2]
	if got := strings.Count(axis, "┴"); got != 5 {
		t.Errorf("axis ┴ count = %d, want 5\n%q", got, axis)
	}
	labelRow := lines[len(lines)-1]
	labels := regexp.MustCompile(`\d\d-\d\d`).FindAllString(labelRow, -1)
	want := "08-20 08-27 09-03 09-10 09-17"
	if got := strings.Join(labels, " "); got != want {
		t.Errorf("x labels = %q, want %q\n%q", got, want, labelRow)
	}
	for i, l := range lines {
		if got := len([]rune(strings.TrimRight(l, " "))); got > 129 {
			t.Errorf("line %d trimmed width = %d, want <= 129\n%q", i, got, l)
		}
	}
}

// TestStatsTimelineDailyTicksOnAWeek pins §4.3's daily step: a seven-day window
// at 132 columns gives every day a label.
func TestStatsTimelineDailyTicksOnAWeek(t *testing.T) {
	days := make([]stats.DayCost, 7)
	for i := range days {
		days[i].Day = time.Date(2026, time.September, 11, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02")
	}
	days[3].Tokens = 6_800_000
	days[6].Tokens = 2_000_000

	lines := statsTimelineLines(days, 132, 0)
	for i, l := range lines {
		lines[i] = stripANSI(l)
	}
	labelRow := lines[len(lines)-1]
	labels := regexp.MustCompile(`\d\d-\d\d`).FindAllString(labelRow, -1)
	want := "09-11 09-12 09-13 09-14 09-15 09-16 09-17"
	if got := strings.Join(labels, " "); got != want {
		t.Errorf("x labels = %q, want every day %q\n%q", got, want, labelRow)
	}
}

// TestStatsTimelineConstantWidth pins §2.3's constant width: whatever the day
// count, the plot fills its room -- the axis row is exactly width-3 cells -- and
// no plot row overruns that. At 90 and 400 days each bar is one cell wide, so
// bars of different columns never touch.
func TestStatsTimelineConstantWidth(t *testing.T) {
	const width = 132
	for _, n := range []int{7, 21, 30, 90, 200, 400} {
		days := make([]stats.DayCost, n)
		for i := range days {
			days[i].Day = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02")
		}
		days[0].Tokens = 6_800_000
		days[n-1].Tokens = 2_000_000

		lines := statsTimelineLines(days, width, 0)
		for i, l := range lines {
			lines[i] = stripANSI(l)
		}
		axis := lines[len(lines)-2]
		if !strings.Contains(axis, "└") {
			t.Fatalf("%d days: no axis row:\n%s", n, strings.Join(lines, "\n"))
		}
		if got := len([]rune(strings.TrimRight(axis, " "))); got != width-3 {
			t.Errorf("%d days: axis row is %d cells, want %d\n%q", n, got, width-3, axis)
		}
		for i, l := range lines {
			if got := len([]rune(strings.TrimRight(l, " "))); got > width-3 {
				t.Errorf("%d days: line %d is %d cells, want <= %d\n%q", n, i, got, width-3, l)
			}
		}

		if n != 90 && n != 400 {
			continue
		}
		// The bottom plot row: its bars are one cell wide, so two adjacent bar
		// cells would mean two columns' bars touch.
		bottom := lines[len(lines)-3]
		plot := bottom[strings.Index(bottom, "│")+len("│"):]
		runes := []rune(plot)
		for x := 1; x < len(runes); x++ {
			if statsBarCell(runes[x-1]) && statsBarCell(runes[x]) {
				t.Errorf("%d days: bars touch at cells %d and %d: %q", n, x-1, x, plot)
			}
		}
	}
}

// statsBarCell reports whether r is one of the chart's bar cells.
func statsBarCell(r rune) bool {
	return strings.ContainsRune("▁▂▃▄▅▆▇█", r)
}

// TestStatsOverviewSidePadding pins §4.5's three-cell right margin: at 100, 132
// and 200 columns no overview line runs past w-3, and at 132 the right table's
// last cell is at w-4.
func TestStatsOverviewSidePadding(t *testing.T) {
	for _, w := range []int{100, 132, 200} {
		v := statsTestView("30d")
		body := stripANSI(v.Body(statsTestEnv(t, w, 34), w, 34))
		for i, line := range strings.Split(body, "\n") {
			if got := len([]rune(strings.TrimRight(line, " "))); got > w-3 {
				t.Errorf("w=%d line %d: trimmed width %d, want <= %d\n%q", w, i, got, w-3, line)
			}
		}
	}

	v := statsTestView("30d")
	body := stripANSI(v.Body(statsTestEnv(t, 132, 34), 132, 34))
	lines := strings.Split(body, "\n")
	h := statsLineIndex(lines, "% TOKENS")
	if h < 0 {
		t.Fatalf("no %% TOKENS header:\n%s", body)
	}
	row := lines[h+1]
	if got := len([]rune(strings.TrimRight(row, " "))) - 1; got != 132-4 {
		t.Errorf("right table's last cell at %d, want %d\n%q", got, 132-4, row)
	}
}

// TestStatsOverviewScrolls pins §4.6 and §4.7: the candidates table is a
// fixed-height viewport over every scorecard row, and the cursor scrolls it.
// §2.1 moved the `a–b of n` count from the dropped section line onto the header
// line, so the count is asserted there.
func TestStatsOverviewScrolls(t *testing.T) {
	env := statsTestEnv(t, 132, 34)
	v := View(statsView{window: "30d", loaded: true, rep: statsTwentyCandidates()})

	body := stripANSI(v.Body(env, 132, 34))
	lines := strings.Split(body, "\n")
	page := regexp.MustCompile(`1–\d+ of 20`).FindString(body)
	if page == "" {
		t.Errorf("first page must show `1–N of 20`:\n%s", body)
	} else {
		if page == "1–20 of 20" {
			t.Errorf("first page shows all 20 rows, want N < 20:\n%s", body)
		}
		hdr := statsLineIndex(lines, "IN/RND")
		if hdr < 0 || !strings.Contains(lines[hdr], page) {
			t.Errorf("the count %q must ride on the header line, hdr=%d:\n%s", page, hdr, body)
		}
	}
	if strings.Contains(body, "cand-19") {
		t.Errorf("first page must not show cand-19:\n%s", body)
	}

	for i := 0; i < 19; i++ {
		next, _ := v.Update(statsKey('j'), env)
		v = next
	}
	body = stripANSI(v.Body(env, 132, 34))
	if !strings.Contains(body, "cand-19") {
		t.Errorf("after 19 j the body must show cand-19:\n%s", body)
	}
	if !strings.Contains(body, "–20 of 20") {
		t.Errorf("after 19 j the body must show `–20 of 20`:\n%s", body)
	}
	if strings.Contains(body, "cand-00") {
		t.Errorf("after 19 j the body must not show cand-00:\n%s", body)
	}
}

// TestStatsOverviewEnter pins §4.10: on the overview, enter opens the selected
// table row's rounds -- the top-tokens repo after `l`, the second candidate
// after `j`.
func TestStatsOverviewEnter(t *testing.T) {
	t.Run("repos row", func(t *testing.T) {
		m := statsShell(t, 160, 40, "30d")
		res, _ := m.Update(statsMsg{window: "30d", rep: statsFixture()})
		m = res.(Model)
		res, _ = m.Update(statsKey('l'))
		m = res.(Model)
		if got := m.top().(statsView).ovFocus; got != 1 {
			t.Fatalf("l: ovFocus = %d, want 1", got)
		}
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		rv, ok := m.top().(roundsView)
		if !ok {
			t.Fatalf("enter on the repos table must push a rounds view, got %T", m.top())
		}
		want := `repo:"https://github.com/fuad-daoud/relevo" since:30d`
		if got := rv.dash.QueryText(); got != want {
			t.Errorf("QueryText = %q, want %q", got, want)
		}
	})

	t.Run("candidate row", func(t *testing.T) {
		m := statsShell(t, 160, 40, "30d")
		res, _ := m.Update(statsMsg{window: "30d", rep: statsFixture()})
		m = res.(Model)
		res, _ = m.Update(statsKey('j'))
		m = res.(Model)
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		rv, ok := m.top().(roundsView)
		if !ok {
			t.Fatalf("enter on the candidates table must push a rounds view, got %T", m.top())
		}
		want := "candidate:agy/antigravity/claude-sonnet-4-6 since:30d"
		if got := rv.dash.QueryText(); got != want {
			t.Errorf("QueryText = %q, want %q", got, want)
		}
	})
}

// TestStatsShareCell pins §4.9's honest-share rule: the bar is round(pct/10)
// cells with no minimum, so under 5% draws none, and the cell is always 17
// cells, two spaces, a ten-cell bar and a four-cell percent.
func TestStatsShareCell(t *testing.T) {
	cases := []struct {
		tokens, total int64
		bars          int
		pct           string
	}{
		{900_000, 1_000_000, 9, " 90%"},
		{540_000, 1_000_000, 5, " 54%"},
		{110_000, 1_000_000, 1, " 11%"},
		{40_000, 1_000_000, 0, "  4%"},
		{0, 1_000_000, 0, "  0%"},
	}
	for _, c := range cases {
		got := stripANSI(statsShareCell(c.tokens, c.total))
		want := "  " + strings.Repeat("▇", c.bars) + strings.Repeat(" ", 10-c.bars) + " " + c.pct
		if got != want {
			t.Errorf("statsShareCell(%d, %d) = %q, want %q", c.tokens, c.total, got, want)
		}
		if n := len([]rune(got)); n != 17 {
			t.Errorf("statsShareCell(%d, %d) is %d cells, want 17", c.tokens, c.total, n)
		}
	}
}

// TestStatsOverviewNoCaptions pins §4.1 and §4.2: each section line keeps only
// its title, plus the `a–b of n` scroll count when the table overflows.
func TestStatsOverviewNoCaptions(t *testing.T) {
	v := statsTestView("30d")
	body := stripANSI(v.Body(statsTestEnv(t, 132, 34), 132, 34))
	for _, gone := range []string{"by rounds", "dimmed", "by tokens"} {
		if strings.Contains(body, gone) {
			t.Errorf("overview still shows %q:\n%s", gone, body)
		}
	}
}

// TestStatsOverviewNoTableTitles pins §1.1 and §2.1: the two overview tables
// start at their header rows -- no CANDIDATES or BUSIEST REPOS section lines --
// the chart heading has no `per day` subtitle, and an overflowing table carries
// its `a–b of n` count in the header's name column.
func TestStatsOverviewNoTableTitles(t *testing.T) {
	env := statsTestEnv(t, 132, 34)
	v := statsTestView("30d")
	body := stripANSI(v.Body(env, 132, 34))
	lines := strings.Split(body, "\n")

	for _, title := range []string{"CANDIDATES", "BUSIEST REPOS"} {
		for i, line := range lines {
			if strings.TrimSpace(line) == title {
				t.Errorf("line %d is the table title %q, want no title:\n%s", i, title, body)
			}
		}
	}
	if strings.Contains(body, "per day") {
		t.Errorf("the chart heading still carries a subtitle:\n%s", body)
	}
	hdr := statsLineIndex(lines, "IN/RND")
	if hdr < 0 {
		t.Fatalf("no candidates header:\n%s", body)
	}
	if !strings.Contains(lines[hdr], "% TOKENS") {
		t.Errorf("header line = %q, want the candidates and repos headers on one line", lines[hdr])
	}

	// A 20-row candidates table overflows, so its count rides on the header.
	v = statsView{window: "30d", loaded: true, rep: statsTwentyCandidates()}
	body = stripANSI(v.Body(env, 132, 34))
	lines = strings.Split(body, "\n")
	hdr = statsLineIndex(lines, "IN/RND")
	if hdr < 0 {
		t.Fatalf("20 candidates: no header:\n%s", body)
	}
	if !strings.Contains(lines[hdr], "of 20") {
		t.Errorf("20 candidates: header = %q, want the `of 20` count", lines[hdr])
	}
}

// TestStatsOverviewConfiguredCandidates pins §3.3 and §4.1: every configured
// candidate the window has no scorecard row for follows the scorecard rows as
// a faint, zero-round row, and enter on such a row notices instead of pushing
// `:rounds`.
func TestStatsOverviewConfiguredCandidates(t *testing.T) {
	const (
		haiku = "claude/anthropic/haiku"
		terra = "codex/openai/gpt-5.6-terra:high"
		// idletail is the idle row's numbers: 0 rounds, then "·" for the four
		// other columns.
		idleTail = "0     ·        ·        ·      ·"
	)

	rep := statsFixture()
	env := statsTestEnv(t, 132, 34)
	v := statsTestView("30d")
	v.configured = []string{rep.Scorecard[0].Token, rep.Scorecard[1].Token, haiku, terra}

	// The order: every scorecard row, then the two idle rows by name.
	rows := v.overviewCandRows(func(s string) string { return s })
	var got []string
	for _, c := range rows {
		got = append(got, c.Token)
	}
	want := []string{rep.Scorecard[0].Token, rep.Scorecard[1].Token, haiku, terra}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("overview rows = %q, want %q", got, want)
	}
	for i, c := range rows {
		if idle := i >= len(rep.Scorecard); c.Idle != idle {
			t.Errorf("row %d (%s): Idle = %v, want %v", i, c.Token, c.Idle, idle)
		}
	}

	// The rendered table: the scorecard rows, then the two idle ones.
	leftW := (132 - 9) / 2
	cells, _ := v.statsOverviewCandidates(env, leftW, 20)
	if len(cells) != len(rep.Scorecard)+3 {
		t.Fatalf("table has %d lines, want %d", len(cells), len(rep.Scorecard)+3)
	}
	idleRows := cells[1+len(rep.Scorecard):]
	if !strings.HasSuffix(stripANSI(idleRows[0]), idleTail) || !strings.HasSuffix(stripANSI(idleRows[1]), idleTail) {
		t.Errorf("idle rows = %q, %q; want each to end %q", stripANSI(idleRows[0]), stripANSI(idleRows[1]), idleTail)
	}
	nameW := statsCandidateNameW(leftW)
	if !strings.HasPrefix(stripANSI(idleRows[0]), stats.FitKey(haiku, nameW, false)) ||
		!strings.HasPrefix(stripANSI(idleRows[1]), stats.FitKey(terra, nameW, false)) {
		t.Errorf("idle rows = %q, %q; want %s then %s", stripANSI(idleRows[0]), stripANSI(idleRows[1]), haiku, terra)
	}
	plain := stats.FitKey(haiku, nameW, false) + "     " + idleTail
	if idleRows[0] != faintStyle.Render(plain) {
		t.Errorf("idle row = %q, want faintStyle over its plain text %q", idleRows[0], plain)
	}

	// j onto the first idle row, then enter: a notice, no push.
	view := View(v)
	for i := 0; i < len(rep.Scorecard); i++ {
		next, _ := view.Update(statsKey('j'), env)
		view = next
	}
	next, cmd := view.Update(tea.KeyMsg{Type: tea.KeyEnter}, env)
	if cmd == nil {
		t.Fatal("enter on an idle row must return the notice command")
	}
	if _, ok := next.(statsView); !ok {
		t.Fatalf("enter on an idle row must not push, top is %T", next)
	}
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf("enter on an idle row produced %T, want noticeMsg", cmd())
	}
	if msg.text != haiku+" has no rounds in this window" {
		t.Errorf("notice = %q, want the no-rounds notice for %s", msg.text, haiku)
	}

	// A scorecard token that is no longer configured keeps its row.
	v.configured = []string{haiku, terra}
	var kept []string
	for _, c := range v.overviewCandRows(func(s string) string { return s }) {
		kept = append(kept, c.Token)
	}
	if strings.Join(kept, "|") != strings.Join(want, "|") {
		t.Errorf("rows with both scorecard tokens unconfigured = %q, want %q", kept, want)
	}
	if body := stripANSI(v.Body(env, 132, 34)); !strings.Contains(body, haiku) {
		t.Errorf("overview must list the idle %s:\n%s", haiku, body)
	}
}

// TestStatsOverviewTilesUncut pins §4.4: the tile pitch leaves two cells of
// every tile for the gap, so a tile's note is never cut at 132 columns.
func TestStatsOverviewTilesUncut(t *testing.T) {
	v := statsTestView("30d")
	body := stripANSI(v.Body(statsTestEnv(t, 132, 34), 132, 34))
	lines := strings.Split(body, "\n")
	i := statsLineIndex(lines, "ROUNDS")
	if i < 0 {
		t.Fatalf("no ROUNDS tile:\n%s", body)
	}
	if want := "1 halted · 1 open · 27m median"; !strings.Contains(lines[i+2], want) {
		t.Errorf("ROUNDS note = %q, want %q whole", lines[i+2], want)
	}
	for r := i; r < i+3; r++ {
		if strings.Contains(lines[r], "…") {
			t.Errorf("tile row %d is cut: %q", r, lines[r])
		}
	}
}

// TestStatsOverviewTablesStackBelow120 pins §4.5: the two tables sit side by
// side from 120 columns and stack below it, and the clipped header labels never
// crowd the numeric columns out of the table.
func TestStatsOverviewTablesStackBelow120(t *testing.T) {
	body := func(w, h int) []string {
		v := statsTestView("30d")
		return strings.Split(stripANSI(v.Body(statsTestEnv(t, w, h), w, h)), "\n")
	}

	lines := body(110, 40)
	candHdr := statsLineIndex(lines, "IN/RND")
	repoHdr := statsLineIndex(lines, "% TOKENS")
	if candHdr < 0 || repoHdr < 0 {
		t.Fatalf("missing table headers at 110:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Contains(lines[candHdr], "% TOKENS") {
		t.Errorf("at 110 the tables must stack, got one header row: %q", lines[candHdr])
	}
	if repoHdr <= candHdr {
		t.Errorf("at 110 the repos header at line %d must follow candidates at %d", repoHdr, candHdr)
	}

	wide := body(120, 40)
	both := statsLineIndex(wide, "IN/RND")
	if both < 0 || !strings.Contains(wide[both], "% TOKENS") {
		t.Errorf("at 120 one line must carry IN/RND and %% TOKENS:\n%s", strings.Join(wide, "\n"))
	}

	narrow := body(100, 40)
	narrowHdr := statsLineIndex(narrow, "IN/RND")
	if narrowHdr < 0 || !strings.Contains(narrow[narrowHdr], "CACHE") {
		t.Errorf("at 100 the candidates header must show CACHE whole:\n%s", strings.Join(narrow, "\n"))
	}
}

// TestStatsTimelineGrows pins §2.1: rows is the plot's row count, H is
// max(rows, m), and tick j sits on row H - round(j*H/m).
func TestStatsTimelineGrows(t *testing.T) {
	days := statsTimelineDays30()

	plot := func(rows int) []string {
		lines := statsTimelineLines(days, 132, rows)
		for i, l := range lines {
			lines[i] = stripANSI(l)
		}
		return lines[:len(lines)-2]
	}

	big := plot(16)
	if len(big) != 16 {
		t.Fatalf("rows 16: plot rows = %d, want 16", len(big))
	}
	for _, r := range []int{0, 4, 8, 12} {
		if !strings.Contains(big[r], "┤") {
			t.Errorf("rows 16: plot row %d = %q, want a ┤ tick", r, big[r])
		}
	}

	mid := plot(12)
	if len(mid) != 12 {
		t.Fatalf("rows 12: plot rows = %d, want 12", len(mid))
	}
	for _, r := range []int{0, 3, 6, 9} {
		if !strings.Contains(mid[r], "┤") {
			t.Errorf("rows 12: plot row %d = %q, want a ┤ tick", r, mid[r])
		}
	}

	small := plot(2)
	if len(small) != 4 {
		t.Fatalf("rows 2: plot rows = %d, want 4 (H = max(rows, m))", len(small))
	}
	for _, r := range []int{0, 1, 2, 3} {
		if !strings.Contains(small[r], "┤") {
			t.Errorf("rows 2: plot row %d = %q, want a ┤ tick", r, small[r])
		}
	}
}

// TestStatsOverviewChartUsesSpareRows pins §2.2: the chart's rows are
// clamp(height-20, 8, 16), so a taller screen draws more plot rows -- 16 at
// 132x60 against 8 at 132x22, the floor -- and the heading is just TOKENS with
// no subtitle.
func TestStatsOverviewChartUsesSpareRows(t *testing.T) {
	rows := func(t *testing.T, h int) int {
		t.Helper()
		v := statsTestView("30d")
		body := stripANSI(v.Body(statsTestEnv(t, 132, h), 132, h))
		lines := strings.Split(body, "\n")
		head := -1
		for i, l := range lines {
			if strings.TrimRight(l, " ") == "   TOKENS" {
				head = i
				break
			}
		}
		if head < 0 {
			t.Fatalf("h=%d: no chart heading:\n%s", h, body)
		}
		for i := head + 1; i < len(lines); i++ {
			if strings.Contains(lines[i], "└") {
				return i - head - 1
			}
		}
		t.Fatalf("h=%d: no axis line:\n%s", h, body)
		return -1
	}

	short, tall := rows(t, 22), rows(t, 60)
	if short != 8 || tall != 16 {
		t.Errorf("the chart at 132x22 and 132x60 has %d and %d plot rows, want 8 and 16", short, tall)
	}
	if tall <= short {
		t.Errorf("the chart at 132x60 has %d plot rows, at 132x22 %d; want more", tall, short)
	}
}

// TestStatsOverviewChartHeightIsFixedAcrossWindows pins §2.2: at 132x50 the
// chart's plot rows are clamp(avail-20, 8, 16) and do not move with the data --
// four reports with different maxima and row counts draw the same height.
func TestStatsOverviewChartHeightIsFixedAcrossWindows(t *testing.T) {
	const width, height = 132, 50
	env := statsTestEnv(t, width, height)

	// scale returns rep with every day's tokens multiplied by num/den, so the
	// chart's maximum and its y intervals change.
	scale := func(rep stats.Report, num, den int64) stats.Report {
		days := append([]stats.DayCost(nil), rep.Spend.Days...)
		for i := range days {
			days[i].Tokens = days[i].Tokens * num / den
		}
		rep.Spend.Days = days
		return rep
	}

	reports := []struct {
		name string
		rep  stats.Report
	}{
		{"fixture", statsFixture()},
		{"tokens ×10", scale(statsFixture(), 10, 1)},
		{"tokens ÷10", scale(statsFixture(), 1, 10)},
		{"20 candidates", statsTwentyCandidates()},
	}

	// avail is the room Body gives the overview: its height less its head line.
	avail := height - 1
	want := clamp(avail-20, 8, 16)

	for _, tc := range reports {
		v := statsView{window: "30d", loaded: true, rep: tc.rep}
		lines := strings.Split(stripANSI(v.Body(env, width, height)), "\n")

		head := -1
		for i, l := range lines {
			if strings.TrimRight(l, " ") == "   TOKENS" {
				head = i
				break
			}
		}
		if head < 0 {
			t.Fatalf("%s: no chart heading:\n%s", tc.name, strings.Join(lines, "\n"))
		}
		plot := -1
		for i := head + 1; i < len(lines); i++ {
			if strings.Contains(lines[i], "└") {
				plot = i - head - 1
				break
			}
		}
		if plot < 0 {
			t.Fatalf("%s: no axis line:\n%s", tc.name, strings.Join(lines, "\n"))
		}
		if plot != want {
			t.Errorf("%s: chart plot rows = %d, want %d", tc.name, plot, want)
		}
	}
}

// TestStatsPageScrollBounds pins §3.4's scroll bounds at a short pane: pgdn
// stops at the max top instead of growing past the body, one pgup lowers the
// page by avail-1 (or to 0), and end and home jump to the max top and 0.
func TestStatsPageScrollBounds(t *testing.T) {
	env := statsTestEnv(t, 132, 12)
	v := View(statsTestView("30d"))
	lines, _, avail := v.(statsView).page(env)
	maxTop := statsMaxTop(len(lines), avail)

	for i := 0; i < 50; i++ {
		next, _ := v.Update(tea.KeyMsg{Type: tea.KeyPgDown}, env)
		v = next
	}
	if got := v.(statsView).top; got != maxTop {
		t.Fatalf("after 50 pgdn top = %d, want maxTop %d", got, maxTop)
	}

	want := maxTop - max(1, avail-1)
	if want < 0 {
		want = 0
	}
	next, _ := v.Update(tea.KeyMsg{Type: tea.KeyPgUp}, env)
	v = next
	if got := v.(statsView).top; got != want {
		t.Errorf("after one pgup top = %d, want %d", got, want)
	}

	next, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnd}, env)
	v = next
	if got := v.(statsView).top; got != maxTop {
		t.Errorf("end: top = %d, want maxTop %d", got, maxTop)
	}
	next, _ = v.Update(tea.KeyMsg{Type: tea.KeyHome}, env)
	v = next
	if got := v.(statsView).top; got != 0 {
		t.Errorf("home: top = %d, want 0", got)
	}
}

// TestStatsPageFollowsCursor pins §3.4's follow: on the overview at a short
// pane `l` scrolls the page down to the repos table's band, and `h` then `k`
// scroll back up to the candidates' first row.
func TestStatsPageFollowsCursor(t *testing.T) {
	env := statsTestEnv(t, 132, 12)
	v := View(statsTestView("30d"))

	next, _ := v.Update(statsKey('l'), env)
	v = next
	if got := v.(statsView).top; got == 0 {
		t.Fatal("l must scroll the page to the repos table, top is still 0")
	}
	body := stripANSI(v.Body(env, 132, bodyHeight(env)))
	if !strings.Contains(body, "fuad-daoud/relevo") {
		t.Errorf("after l the body must show the top-tokens repo:\n%s", body)
	}

	next, _ = v.Update(statsKey('h'), env)
	v = next
	next, _ = v.Update(statsKey('k'), env)
	v = next
	body = stripANSI(v.Body(env, 132, bodyHeight(env)))
	nameW := statsCandidateNameW((132 - 9) / 2)
	want := stats.FitKey(statsFixture().Scorecard[0].Token, nameW, false)
	if !strings.Contains(body, want) {
		t.Errorf("after h and k the body must show the candidates' first row %q:\n%s", want, body)
	}
}

// TestStatsPageHint pins §3.5: the head line names the way off the page while
// the body overflows -- more below at the top, more above at the bottom -- and
// stays blank when everything fits.
func TestStatsPageHint(t *testing.T) {
	env := statsTestEnv(t, 132, 12)
	v := View(statsTestView("30d"))

	first := strings.TrimRight(strings.Split(stripANSI(v.Body(env, 132, bodyHeight(env))), "\n")[0], " ")
	if !strings.HasSuffix(first, "more below · pgdn") {
		t.Errorf("first body line = %q, want the more-below hint", first)
	}
	if got := len([]rune(first)); got != 132-3 {
		t.Errorf("hint ends at %d, want %d", got, 132-3)
	}

	next, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnd}, env)
	v = next
	first = strings.TrimRight(strings.Split(stripANSI(v.Body(env, 132, bodyHeight(env))), "\n")[0], " ")
	if !strings.HasSuffix(first, "more above · pgup") {
		t.Errorf("first body line at the end = %q, want the more-above hint", first)
	}

	tall := statsTestEnv(t, 132, 34)
	fits := View(statsTestView("30d"))
	first = strings.Split(stripANSI(fits.Body(tall, 132, bodyHeight(tall))), "\n")[0]
	if strings.TrimRight(first, " ") != "" {
		t.Errorf("at 132x34 the first body line = %q, want blank", first)
	}
}

// TestStatsTabResetsTop pins §3.4: after a pgdn, `tab` puts the page back at
// its top.
func TestStatsTabResetsTop(t *testing.T) {
	env := statsTestEnv(t, 132, 12)
	v := View(statsTestView("30d"))

	next, _ := v.Update(tea.KeyMsg{Type: tea.KeyPgDown}, env)
	v = next
	if got := v.(statsView).top; got == 0 {
		t.Fatal("pgdn must move the page, top is still 0")
	}
	next, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab}, env)
	v = next
	if got := v.(statsView).top; got != 0 {
		t.Errorf("tab must reset top to 0, got %d", got)
	}
}

// TestStatsArrowsScrollWithoutCursor pins the tokens tab's ↑↓: on a tab with no
// row cursor -- the kind split, whose stacked charts are taller than a 20-line
// pane -- `j` and `k` scroll the page one line and clamp at 0 and statsMaxTop.
func TestStatsArrowsScrollWithoutCursor(t *testing.T) {
	rep := statsFixture()
	rep.Spend.Days = []stats.DayCost{{
		Day: "2026-09-24", Tokens: 7_080_000,
		Kinds: stats.TokenCounts{Cache: 6_000_000, In: 1_000_000, Out: 60_000, Write: 20_000, Measured: 1},
	}}
	rep.Totals.TokenKinds = stats.TokenCounts{Cache: 6_000_000, In: 1_000_000, Out: 60_000, Write: 20_000, Measured: 1}
	rep.Totals.Tokens = 7_080_000
	env := statsTestEnv(t, 132, 20)
	v := View(statsView{window: "30d", loaded: true, rep: rep, tab: statsTabTokens, split: 3})

	lines, _, avail := v.(statsView).page(env)
	maxTop := statsMaxTop(len(lines), avail)
	if maxTop <= 0 {
		t.Fatalf("the kind split must overflow a 20-line pane, maxTop = %d", maxTop)
	}

	next, _ := v.Update(statsKey('j'), env)
	v = next
	if got := v.(statsView).top; got != 1 {
		t.Fatalf("j: top = %d, want 1", got)
	}
	next, _ = v.Update(statsKey('k'), env)
	v = next
	if got := v.(statsView).top; got != 0 {
		t.Fatalf("k: top = %d, want 0", got)
	}
	next, _ = v.Update(statsKey('k'), env)
	v = next
	if got := v.(statsView).top; got != 0 {
		t.Errorf("k at top 0: top = %d, want 0", got)
	}

	for i := 0; i < 200; i++ {
		next, _ = v.Update(statsKey('j'), env)
		v = next
	}
	if got := v.(statsView).top; got != maxTop {
		t.Errorf("after 200 j top = %d, want statsMaxTop %d", got, maxTop)
	}
}

// TestStatsArrowsMoveCursorWithTable: on the overview, which has a table, `j`
// still moves its cursor and leaves top to follow alone.
func TestStatsArrowsMoveCursorWithTable(t *testing.T) {
	env := statsTestEnv(t, 132, 34)
	v := View(statsTestView("30d"))
	before := v.(statsView).top

	next, _ := v.Update(statsKey('j'), env)
	v = next
	if got := v.(statsView).ovCursor[0]; got != 1 {
		t.Errorf("j on the overview: ovCursor[0] = %d, want 1", got)
	}
	if got := v.(statsView).top; got != before {
		t.Errorf("j on the overview: top = %d, want %d (follow alone)", got, before)
	}
}

// TestStatsTokensKeysScroll: the tokens tab's Keys() names ↑↓ as scroll.
func TestStatsTokensKeysScroll(t *testing.T) {
	v := statsTestView("30d")
	v.tab = statsTabTokens
	var found bool
	for _, k := range v.Keys() {
		if k.Key != "↑↓" {
			continue
		}
		found = true
		if k.Help != "scroll" {
			t.Errorf("the ↑↓ key's help = %q, want scroll", k.Help)
		}
	}
	if !found {
		t.Errorf("the tokens tab's Keys() must name ↑↓")
	}
}

// TestStatsCandidatesTabRows pins §4.3's table: at 132 columns the candidates
// tab is a header and one row per overviewCandRows row -- the same order the
// overview's table shows -- with no titled rule and no footnote, an idle
// candidate reading 0 and "·", and nothing past w-3.
func TestStatsCandidatesTabRows(t *testing.T) {
	const (
		haiku = "claude/anthropic/haiku"
		terra = "codex/openai/gpt-5.6-terra:high"
	)
	rep := statsFixture()
	env := statsTestEnv(t, 132, 40)
	v := statsTestView("30d")
	v.tab = statsTabCandidates
	v.configured = []string{rep.Scorecard[0].Token, rep.Scorecard[1].Token, haiku, terra}

	body := stripANSI(v.Body(env, 132, 40))
	lines := strings.Split(body, "\n")
	if strings.Contains(body, "──") {
		t.Errorf("the candidates tab must have no titled rule:\n%s", body)
	}
	if strings.Contains(body, "unrecorded") {
		t.Errorf("the candidates tab must have no footnote:\n%s", body)
	}

	hdr := statsLineIndex(lines, "HALTED")
	if hdr < 0 {
		t.Fatalf("no header row:\n%s", body)
	}
	for _, want := range []string{"HALTED", "STATUS"} {
		if !strings.Contains(lines[hdr], want) {
			t.Errorf("header = %q, want %s", lines[hdr], want)
		}
	}
	if strings.Contains(lines[hdr], "MEASURED") {
		t.Errorf("header = %q, want no MEASURED column", lines[hdr])
	}

	rows := v.overviewCandRows(func(s string) string { return s })
	if want := len(rep.Scorecard) + 2; len(rows) != want {
		t.Fatalf("rows = %d, want %d", len(rows), want)
	}
	visible, nameW := statsCandVisible(132)
	for i, c := range rows {
		line := lines[hdr+1+i]
		if got, want := line[3:3+nameW], stats.FitKey(c.Token, nameW, false); got != want {
			t.Errorf("row %d name = %q, want %q (overviewCandRows order)", i, got, want)
		}
		if !c.Idle {
			continue
		}
		if got := statsCellAt(line, statsColEnd(lines[hdr], "RNDS")); got != "0" {
			t.Errorf("idle row %d RNDS cell = %q, want 0\n%q", i, got, line)
		}
		if got := statsCellAt(line, statsColEnd(lines[hdr], "CACHE")); got != "·" {
			t.Errorf("idle row %d CACHE cell = %q, want ·\n%q", i, got, line)
		}
		if got := strings.Count(line, "·"); got != len(visible)-1 {
			t.Errorf("idle row %d has %d · cells, want %d\n%q", i, got, len(visible)-1, line)
		}
	}
	if !rows[len(rows)-1].Idle {
		t.Fatalf("the last of %d rows must be idle: %+v", len(rows), rows[len(rows)-1])
	}

	for i, line := range lines {
		if got := len([]rune(strings.TrimRight(line, " "))); got > 132-3 {
			t.Errorf("line %d trimmed width = %d, want <= %d\n%q", i, got, 132-3, line)
		}
	}
}

// TestStatsCandidatesTabHaltedIsReportCount pins §4.3's HALTED column: it is
// the report count (§3.1), not the round-outcome HaltPct, so a row with
// ReportHalted 7 and HaltPct 0 shows 7 under the header's HALTED.
func TestStatsCandidatesTabHaltedIsReportCount(t *testing.T) {
	rep := statsFixture()
	rep.Scorecard[0].ReportHalted = 7
	rep.Scorecard[0].HaltPct = 0
	env := statsTestEnv(t, 132, 40)
	v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabCandidates}

	body := stripANSI(v.Body(env, 132, 40))
	lines := strings.Split(body, "\n")
	hdr := statsLineIndex(lines, "HALTED")
	if hdr < 0 {
		t.Fatalf("no HALTED header:\n%s", body)
	}
	haltEnd := statsColEnd(lines[hdr], "HALTED")
	if haltEnd < 0 {
		t.Fatalf("no HALTED column:\n%s", lines[hdr])
	}
	row := lines[hdr+1]
	if got := statsCellAt(row, haltEnd); got != "7" {
		t.Errorf("HALTED cell = %q, want 7\n%q", got, row)
	}
}

// TestStatsCandidatesTabStatus pins §4.1 and §4.2: a gate on a row's token
// shows the time left in its STATUS cell while the others read ready, and
// statsGateLeft rounds the time left down.
func TestStatsCandidatesTabStatus(t *testing.T) {
	rep := statsFixture()
	rep.Reliability.Active = append(rep.Reliability.Active,
		availability.Gate{Token: rep.Scorecard[1].Token, Until: railNow.Add(26 * time.Hour)})
	env := statsTestEnv(t, 132, 40)
	v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabCandidates}

	body := stripANSI(v.Body(env, 132, 40))
	lines := strings.Split(body, "\n")
	hdr := statsLineIndex(lines, "STATUS")
	if hdr < 0 {
		t.Fatalf("no STATUS header:\n%s", body)
	}
	if got := strings.TrimRight(lines[hdr+1], " "); !strings.HasSuffix(got, "ready") {
		t.Errorf("the ungated row = %q, want it to end `ready`", got)
	}
	if got := strings.TrimRight(lines[hdr+2], " "); !strings.HasSuffix(got, "gated 1d") {
		t.Errorf("the gated row = %q, want it to end `gated 1d`", got)
	}

	cases := []struct {
		left time.Duration
		want string
	}{
		{7*time.Minute + 30*time.Second, "gated 7m"},
		{3*time.Hour + 59*time.Minute, "gated 3h"},
		{24*24*time.Hour + 5*time.Hour, "gated 24d"},
	}
	for _, c := range cases {
		if got := statsGateLeft(availability.Gate{Until: railNow.Add(c.left)}, railNow); got != c.want {
			t.Errorf("statsGateLeft(%v) = %q, want %q", c.left, got, c.want)
		}
	}
	if got := statsGateLeft(availability.Gate{}, railNow); got != "gated" {
		t.Errorf("statsGateLeft(zero Until) = %q, want gated", got)
	}
}

// TestStatsCandidatesTabDetail pins §4.4: the selected row's detail block
// counts its rounds, bindings, window share and switches, an idle row says it
// has no rounds, and enter on that idle row notices instead of pushing
// `:rounds`.
func TestStatsCandidatesTabDetail(t *testing.T) {
	const haiku = "claude/anthropic/haiku"

	rep := statsFixture()
	rep.Scorecard[0].Bindings = 2
	env := statsTestEnv(t, 132, 40)
	v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabCandidates}
	v.configured = []string{rep.Scorecard[0].Token, rep.Scorecard[1].Token, haiku}

	body := stripANSI(v.Body(env, 132, 40))
	for _, want := range []string{"rounds on", "bindings", "% of the window", "switches"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail block missing %q:\n%s", want, body)
		}
	}

	view := View(v)
	for i := 0; i < len(rep.Scorecard); i++ {
		next, _ := view.Update(statsKey('j'), env)
		view = next
	}
	body = stripANSI(view.Body(env, 132, 40))
	if !strings.Contains(body, "no rounds in this window") {
		t.Errorf("the idle row's detail must say it has no rounds:\n%s", body)
	}

	next, cmd := view.Update(tea.KeyMsg{Type: tea.KeyEnter}, env)
	if cmd == nil {
		t.Fatal("enter on an idle row must return the notice command")
	}
	if _, ok := next.(statsView); !ok {
		t.Fatalf("enter on an idle row must not push, top is %T", next)
	}
	msg, ok := cmd().(noticeMsg)
	if !ok {
		t.Fatalf("enter on an idle row produced %T, want noticeMsg", cmd())
	}
	if msg.text != haiku+" has no rounds in this window" {
		t.Errorf("notice = %q, want the no-rounds notice for %s", msg.text, haiku)
	}

	// §2.3 with a row whose ttft is unknown: the clause goes whole, so no
	// dangling "· ·" is left behind.
	t.Run("unknown ttft clause", func(t *testing.T) {
		rep := statsFixture()
		rep.Scorecard[0].HasTTFT = false
		env := statsTestEnv(t, 132, 40)
		v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabCandidates}
		line := stripANSI(v.statsCandDetail(env, statsOverviewCand{
			Token: rep.Scorecard[0].Token, Row: rep.Scorecard[0],
		})[2])
		if strings.Contains(line, "ttft") {
			t.Errorf("line 3 = %q, want no ttft clause", line)
		}
		if strings.Contains(line, "· ·") {
			t.Errorf("line 3 = %q, want no empty clause", line)
		}
	})

	// §2.3 with a row whose median is unknown: that clause goes too.
	t.Run("unknown median clause", func(t *testing.T) {
		rep := statsFixture()
		rep.Scorecard[0].HasMedian = false
		env := statsTestEnv(t, 132, 40)
		v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabCandidates}
		line := stripANSI(v.statsCandDetail(env, statsOverviewCand{
			Token: rep.Scorecard[0].Token, Row: rep.Scorecard[0],
		})[2])
		if strings.Contains(line, "median") {
			t.Errorf("line 3 = %q, want no median clause", line)
		}
	})

	// §2.2's fourth line: the rounds that reported token usage, in plain
	// words. The no-round row carries rounds so the row is not itself idle.
	measured := []struct {
		name     string
		measured int
		rounds   int
		want     string
	}{
		{"some measured", 4, 5, "4 of 5 rounds reported token usage"},
		{"every measured", 5, 5, "every round reported token usage"},
		{"none measured", 0, 5, "no round reported token usage"},
	}
	for _, tc := range measured {
		t.Run(tc.name, func(t *testing.T) {
			rep := statsFixture()
			row := rep.Scorecard[0]
			row.Rounds, row.TokenKinds.Measured = tc.rounds, tc.measured
			env := statsTestEnv(t, 132, 40)
			v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabCandidates}
			line := stripANSI(v.statsCandDetail(env, statsOverviewCand{Token: row.Token, Row: row})[3])
			if !strings.Contains(line, tc.want) {
				t.Errorf("Measured %d, Rounds %d: line 4 = %q, want %q",
					tc.measured, tc.rounds, line, tc.want)
			}
		})
	}
}

// statsRolesEnv is statsTestEnv with the runtime carrying a candidate set and
// a registry, so the detail block's roles read from the registry (§2.2).
func statsRolesEnv(t *testing.T, set *candidate.Set, reg *roles.Registry) Env {
	t.Helper()
	return testEnv(mastermindSource{relevo.Runtime{
		Store: store.New(t.TempDir()), Candidates: set, Registry: reg,
	}}, view.Report{}, 132, 40)
}

// TestStatsCandidatesTabRoles pins §2.2: detail line 1 takes its roles from the
// runtime's role registry, which is where an actors-config machine keeps them,
// so a token the candidate set lists builder for shows `· builder` even when
// the code never reads the candidate's own Roles.
func TestStatsCandidatesTabRoles(t *testing.T) {
	rep := statsFixture()
	token := rep.Scorecard[0].Token
	parts := strings.SplitN(token, "/", 3)
	if len(parts) != 3 {
		t.Fatalf("fixture token %q is not harness/provider/model", token)
	}

	body := fmt.Sprintf(`[{"harness":%q,"provider":%q,"model":%q,"roles":["builder"]}]`,
		parts[0], parts[1], parts[2])
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write candidate set fixture: %v", err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}
	reg, err := roles.Build(nil, set, policy.Policy{})
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}

	env := statsRolesEnv(t, set, reg)
	v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabCandidates}
	v.configured = []string{token}

	lines := v.statsCandDetail(env, statsOverviewCand{Token: token, Row: rep.Scorecard[0]})
	first := strings.TrimRight(stripANSI(lines[0]), " ")
	if !strings.HasSuffix(first, "· builder") {
		t.Errorf("detail line 1 = %q, want it to end `· builder`", first)
	}
}

// TestStatsCandidatesTabDropsColumns pins §3.3's dropping: TTFT leaves first,
// then MED, the name column never falls under 16 cells, and no line passes
// w-3.
func TestStatsCandidatesTabDropsColumns(t *testing.T) {
	cases := []struct {
		width int
		gone  []string
		kept  []string
	}{
		{100, nil, []string{"TTFT", "MED"}},
		{80, []string{"TTFT", "MED"}, nil},
	}
	for _, tc := range cases {
		v := statsTestView("30d")
		v.tab = statsTabCandidates
		env := statsTestEnv(t, tc.width, 40)
		body := stripANSI(v.Body(env, tc.width, 40))
		lines := strings.Split(body, "\n")
		hdr := statsLineIndex(lines, "RNDS")
		if hdr < 0 {
			t.Fatalf("w=%d: no header:\n%s", tc.width, body)
		}
		for _, gone := range tc.gone {
			if strings.Contains(lines[hdr], gone) {
				t.Errorf("w=%d: header still shows %s: %q", tc.width, gone, lines[hdr])
			}
		}
		for _, kept := range tc.kept {
			if !strings.Contains(lines[hdr], kept) {
				t.Errorf("w=%d: header must still show %s: %q", tc.width, kept, lines[hdr])
			}
		}
		if _, nameW := statsCandVisible(tc.width); nameW < 16 {
			t.Errorf("w=%d: name column is %d cells, want at least 16", tc.width, nameW)
		}
		for i, line := range lines {
			if got := len([]rune(strings.TrimRight(line, " "))); got > tc.width-3 {
				t.Errorf("w=%d line %d: trimmed width %d, want <= %d\n%q", tc.width, i, got, tc.width-3, line)
			}
		}
	}
}

// TestStatsCandidatesTabFollows pins §4.6: the candidates tab's page scrolls
// with its cursor, so on a short pane the last of 12 rows comes into view.
func TestStatsCandidatesTabFollows(t *testing.T) {
	rep := statsFixture()
	v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabCandidates}
	for i := 0; i < 10; i++ {
		v.configured = append(v.configured, fmt.Sprintf("cand-%02d", i))
	}
	v.configured = append(v.configured, rep.Scorecard[0].Token, rep.Scorecard[1].Token)

	env := statsTestEnv(t, 132, 12)
	view := View(v)
	for i := 0; i < 11; i++ {
		next, _ := view.Update(statsKey('j'), env)
		view = next
	}
	sv := view.(statsView)
	if sv.top == 0 {
		t.Fatal("following the last row must scroll the page, top is still 0")
	}
	body := stripANSI(sv.Body(env, 132, bodyHeight(env)))
	if !strings.Contains(body, "cand-09") {
		t.Errorf("the page must show the last row cand-09:\n%s", body)
	}
}

// The tokens tab's three candidate tokens; the idle one carries no tokens. §6.
const (
	statsTokensBig   = "h/p1/m1"
	statsTokensSmall = "h/p2/m2"
	statsTokensIdle  = "h/p3/m3"
)

// statsTokensReport is the tokens tests' report: two days carrying tokens for
// two candidates and two providers, with the three kinds (§6).
func statsTokensReport() stats.Report {
	rep := statsFixture()
	rep.Spend.Days = []stats.DayCost{
		{
			Day: "2026-09-16", Tokens: 1000,
			ByCandidate:      map[string]int64{statsTokensBig: 900, statsTokensSmall: 100},
			TokensByProvider: map[string]int64{"p1": 900, "p2": 100},
			Kinds:            stats.TokenCounts{Cache: 800, In: 100, Out: 80, Write: 20, Measured: 1},
		},
		{
			Day: "2026-09-17", Tokens: 2000,
			ByCandidate:      map[string]int64{statsTokensBig: 1500, statsTokensSmall: 500},
			TokensByProvider: map[string]int64{"p1": 1500, "p2": 500},
			Kinds:            stats.TokenCounts{Cache: 1600, In: 200, Out: 160, Write: 40, Measured: 1},
		},
	}
	rep.Totals.TokenKinds = stats.TokenCounts{In: 300, Cache: 2400, Out: 240, Write: 60, Measured: 2}
	rep.Totals.Tokens = 3000
	return rep
}

// statsTokensView is the tokens tab at split over the tokens report, with the
// two token-carrying candidates plus one idle candidate configured (§6).
func statsTokensView(split int) statsView {
	return statsView{
		window: "30d", loaded: true, tab: statsTabTokens, split: split,
		rep:        statsTokensReport(),
		configured: []string{statsTokensBig, statsTokensSmall, statsTokensIdle},
	}
}

// statsChartTitleRow is the index of the line whose title names the series, or
// -1. A title starts with the gutter and the name in bold (§2.2).
func statsChartTitleRow(lines []string, name string) int {
	want := "   " + textStyle.Bold(true).Render(name)
	for i, l := range lines {
		if strings.Contains(l, want) {
			return i
		}
	}
	return -1
}

// statsChartCaption is a chart line's y label: the text left of its rule,
// trimmed, or "" when the line carries no rule.
func statsChartCaption(line string) string {
	if i := strings.IndexAny(line, "┤│"); i >= 0 {
		return strings.TrimSpace(line[:i])
	}
	return ""
}

// statsChartBarCol is the column of a chart line's "┤" or "│" rule, or -1.
func statsChartBarCol(line string) int {
	for i, r := range []rune(line) {
		if r == '┤' || r == '│' {
			return i
		}
	}
	return -1
}

// statsChartTicks is the y labels a chart draws on its ┤ rows, from its first
// plot row to its axis row.
func statsChartTicks(lines []string, titleRow int) []string {
	var labels []string
	for _, l := range lines[titleRow+1:] {
		if strings.Contains(l, "└") {
			break
		}
		if strings.Contains(l, "┤") {
			labels = append(labels, statsChartCaption(l))
		}
	}
	return labels
}

// statsBarCells counts the bar cells in a chart's rows.
func statsBarCells(lines []string) int {
	n := 0
	for _, l := range lines {
		for _, r := range l {
			if strings.ContainsRune(string(statsBarRunes[1:]), r) {
				n++
			}
		}
	}
	return n
}

// statsStripBarCol is the column of the strips' old " │" separator: the total
// split's test checks that no strip survives there.
const statsStripBarCol = 3 + 22 + 1

// TestStatsTokensSplitCycle pins §4.6: a new view starts on candidate, `s`
// cycles 1→2→3→0→1 on the tokens tab, `s` on the overview does nothing, and
// the tokens tab's keys name `s` and no `p`.
func TestStatsTokensSplitCycle(t *testing.T) {
	m := statsShell(t, 132, 40, "30d")
	if got := m.top().(statsView).split; got != 1 {
		t.Fatalf("a new view's split = %d, want 1 (candidate)", got)
	}

	res, _ := m.Update(statsKey('3'))
	m = res.(Model)
	for i, want := range []int{2, 3, 0, 1} {
		res, _ := m.Update(statsKey('s'))
		m = res.(Model)
		if got := m.top().(statsView).split; got != want {
			t.Fatalf("s #%d: split = %d, want %d", i+1, got, want)
		}
	}

	// s on the overview tab does nothing.
	res, _ = m.Update(statsKey('1'))
	m = res.(Model)
	before := m.top().(statsView).split
	res, _ = m.Update(statsKey('s'))
	m = res.(Model)
	if got := m.top().(statsView).split; got != before {
		t.Errorf("s on the overview changed the split to %d, want %d", got, before)
	}

	// The tokens tab's keys name s and never p.
	v := statsTestView("30d")
	v.tab = statsTabTokens
	var hasS bool
	for _, k := range v.Keys() {
		if k.Key == "p" {
			t.Errorf("the tokens tab still names the p key")
		}
		if k.Key != "s" {
			continue
		}
		hasS = true
		if k.Help != "split" {
			t.Errorf("the s key's help = %q, want split", k.Help)
		}
	}
	if !hasS {
		t.Errorf("the tokens tab must name the s key")
	}
}

// TestStatsTokensChips pins §4.1 and §4.5: the tokens tab's first line is only
// the four split chips, with no `split` label.
func TestStatsTokensChips(t *testing.T) {
	v := statsTokensView(1)
	lines := v.tokensTabLines(statsTestEnv(t, 132, 40), 132, 39)
	if len(lines) == 0 {
		t.Fatal("the tokens tab has no lines")
	}
	first := strings.TrimSpace(stripANSI(lines[0]))
	if first != "total   candidate   provider   kind" {
		t.Errorf("the chips row = %q, want the four split names", first)
	}
	if strings.Contains(first, "split") {
		t.Errorf("the chips row = %q, want no split label", first)
	}
}

// TestStatsTokensCandidateCharts pins §2.2: the candidate split draws one full
// chart per non-idle candidate on one shared scale and one ┤ column, and names
// the idle one only on a final faint line.
func TestStatsTokensCandidateCharts(t *testing.T) {
	const width, height = 132, 60
	v := statsTokensView(1)
	env := statsTestEnv(t, width, height)
	raw := v.Body(env, width, height)
	lines := strings.Split(stripANSI(raw), "\n")

	big := statsChartTitleRow(lines, statsTokensBig)
	small := statsChartTitleRow(lines, statsTokensSmall)
	if big < 0 || small < 0 {
		t.Fatalf("missing a chart title (big %d, small %d):\n%s", big, small, strings.Join(lines, "\n"))
	}
	if big >= small {
		t.Errorf("the charts are at %d and %d; want the large then the small", big, small)
	}

	// Each title ends with its total and its share at column width-3.
	if want := stats.ShortTokens(2400) + " · 80%"; !strings.HasSuffix(strings.TrimRight(lines[big], " "), want) {
		t.Errorf("the large title = %q, want it to end %q", lines[big], want)
	}
	if want := stats.ShortTokens(600) + " · 20%"; !strings.HasSuffix(strings.TrimRight(lines[small], " "), want) {
		t.Errorf("the small title = %q, want it to end %q", lines[small], want)
	}
	for _, i := range []int{big, small} {
		if got := len([]rune(strings.TrimRight(lines[i], " "))); got != width-3 {
			t.Errorf("title %d width = %d, want %d", i, got, width-3)
		}
	}

	// Each chart carries its own axis row, so exactly two └ lines.
	if got := strings.Count(raw, "└"); got != 2 {
		t.Errorf("axis rows = %d, want exactly two", got)
	}

	// One shared scale: the same y labels on both charts' ┤ rows.
	bigTicks, smallTicks := statsChartTicks(lines, big), statsChartTicks(lines, small)
	if len(bigTicks) == 0 {
		t.Fatalf("the large chart has no ┤ rows:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Join(bigTicks, ",") != strings.Join(smallTicks, ",") {
		t.Errorf("the charts' y labels are %v and %v; want one shared scale", bigTicks, smallTicks)
	}

	// One ┤ column: the plots start together.
	bigCol, smallCol := -1, -1
	for _, l := range lines[big+1 : small] {
		if strings.Contains(l, "┤") {
			bigCol = statsChartBarCol(l)
			break
		}
	}
	for _, l := range lines[small+1:] {
		if strings.Contains(l, "┤") {
			smallCol = statsChartBarCol(l)
			break
		}
	}
	if bigCol < 0 || smallCol < 0 || bigCol != smallCol {
		t.Errorf("the ┤ columns are %d and %d, want the same", bigCol, smallCol)
	}

	// The idle series appears only on the final faint line.
	if !strings.Contains(raw, faintStyle.Render("no tokens in this window: "+statsTokensIdle)) {
		t.Errorf("the idle series must be named on the faint line:\n%s", raw)
	}
	if got := strings.Count(raw, statsTokensIdle); got != 1 {
		t.Errorf("the idle series appears %d times, want only the faint line", got)
	}

	for i, l := range lines {
		if got := len([]rune(strings.TrimRight(l, " "))); got > width-3 {
			t.Errorf("line %d trimmed width = %d, want <= %d", i, got, width-3)
		}
	}
}

// TestStatsTokensKindOwnScale pins §2.2's kind split: the four kinds always in
// order, each on its own scale so their top y labels differ, yet all aligned on
// one ┤ column.
func TestStatsTokensKindOwnScale(t *testing.T) {
	rep := statsFixture()
	rep.Spend.Days = []stats.DayCost{{
		Day: "2026-09-24", Tokens: 7_080_000,
		Kinds: stats.TokenCounts{Cache: 6_000_000, In: 1_000_000, Out: 60_000, Write: 20_000, Measured: 1},
	}}
	rep.Totals.TokenKinds = stats.TokenCounts{Cache: 6_000_000, In: 1_000_000, Out: 60_000, Write: 20_000, Measured: 1}
	rep.Totals.Tokens = 7_080_000
	v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabTokens, split: 3}
	lines := strings.Split(stripANSI(v.Body(statsTestEnv(t, 132, 60), 132, 60)), "\n")

	names := []string{"cache read", "fresh input", "output", "cache write"}
	rows := make([]int, len(names))
	tops := make([]string, len(names))
	col := -1
	for i, name := range names {
		rows[i] = statsChartTitleRow(lines, name)
		if rows[i] < 0 {
			t.Fatalf("the kind split has no %s chart:\n%s", name, strings.Join(lines, "\n"))
		}
		if i > 0 && rows[i] <= rows[i-1] {
			t.Errorf("%s at line %d must follow %s at %d", name, rows[i], names[i-1], rows[i-1])
		}
		ticks := statsChartTicks(lines, rows[i])
		if len(ticks) == 0 {
			t.Fatalf("the %s chart has no ┤ rows", name)
		}
		tops[i] = ticks[0]
		for _, l := range lines[rows[i]+1:] {
			if strings.Contains(l, "┤") {
				if c := statsChartBarCol(l); c >= 0 {
					if col < 0 {
						col = c
					} else if c != col {
						t.Errorf("the %s chart's ┤ column = %d, want %d", name, c, col)
					}
				}
				break
			}
		}
	}
	if strings.Contains(strings.Join(lines, "\n"), "no tokens in this window") {
		t.Errorf("the kind split must have no idle line")
	}

	// Each chart scales itself, so the top y labels differ.
	seen := map[string]bool{}
	for _, top := range tops {
		if seen[top] {
			t.Errorf("top y label %q repeats; want each chart its own scale", top)
		}
		seen[top] = true
	}
}

// TestStatsTimelineValsMatchesLines pins §2.1's wrapper: the values form draws
// the same chart, line for line, as the day-series form.
func TestStatsTimelineValsMatchesLines(t *testing.T) {
	days := statsTimelineDays30()
	got := statsTimelineVals(tokensOf(days), days, 132, statsChartOpts{Rows: 8})
	want := statsTimelineLines(days, 132, 8)
	if len(got) != len(want) {
		t.Fatalf("line counts differ: %d and %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestStatsTimelineFixedMax pins §2.1's FixedMax: a scale fixed at ten times
// the data's max takes statsNiceStep's top tick and shortens the bars.
func TestStatsTimelineFixedMax(t *testing.T) {
	days := statsTimelineDays30()
	vals := tokensOf(days)

	max := 0.0
	for _, v := range vals {
		if v > max {
			max = v
		}
	}
	fixed := 10 * max

	step := statsNiceStep(fixed)
	m := clamp(int(math.Ceil(fixed/step)), 1, 4)
	top := stats.ShortTokens(int64(math.Round(float64(m) * step)))

	lines := statsTimelineVals(vals, days, 132, statsChartOpts{Rows: 8, FixedMax: fixed})
	for i, l := range lines {
		lines[i] = stripANSI(l)
	}
	if !strings.Contains(lines[0], top) {
		t.Errorf("the top y label row = %q, want %q", lines[0], top)
	}
	if strings.Contains(lines[0], "8M") {
		t.Errorf("the top y label row = %q, want the data's own 8M gone", lines[0])
	}

	// The bars are shorter than on the data's own scale.
	if got, want := statsBarCells(lines), statsBarCells(statsTimelineLines(days, 132, 8)); got >= want {
		t.Errorf("bar cells with FixedMax = %d, want fewer than %d", got, want)
	}
}

// TestStatsTokensTotalIsTimeline pins §4.5: split 0 draws the overview's
// timeline -- TOKENS, ┤ and └, no strip names -- at a height that depends on
// the screen, not on the data.
func TestStatsTokensTotalIsTimeline(t *testing.T) {
	const width, height = 132, 60
	env := statsTestEnv(t, width, height)

	// scale returns rep with every day's tokens multiplied by num/den.
	scale := func(rep stats.Report, num, den int64) stats.Report {
		days := append([]stats.DayCost(nil), rep.Spend.Days...)
		for i := range days {
			days[i].Tokens = days[i].Tokens * num / den
		}
		rep.Spend.Days = days
		return rep
	}
	body := func(rep stats.Report) []string {
		v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabTokens, split: 0}
		return strings.Split(stripANSI(v.Body(env, width, height)), "\n")
	}
	plotRows := func(t *testing.T, rep stats.Report) int {
		t.Helper()
		lines := body(rep)
		head, axis := -1, -1
		for i, l := range lines {
			if head < 0 && strings.TrimSpace(l) == "TOKENS" {
				head = i
				continue
			}
			if head >= 0 && axis < 0 && strings.Contains(l, "└") {
				axis = i
			}
		}
		if head < 0 || axis < 0 {
			t.Fatalf("no timeline chart: head %d, axis %d\n%s", head, axis, strings.Join(lines, "\n"))
		}
		return axis - head - 1
	}

	base := statsFixture()
	lines := body(base)
	for _, want := range []string{"TOKENS", "┤", "└"} {
		if !strings.Contains(strings.Join(lines, "\n"), want) {
			t.Errorf("the total split is missing %q", want)
		}
	}
	// The total split is the timeline, not strips: no │ in the strips' label
	// column.
	for i, l := range lines {
		if r := []rune(l); len(r) > statsStripBarCol && r[statsStripBarCol] == '│' {
			t.Errorf("line %d carries a strip's │ at column %d: %q", i, statsStripBarCol, l)
		}
	}

	short, wide := plotRows(t, base), plotRows(t, scale(base, 10, 1))
	if short != wide {
		t.Errorf("the chart is %d plot rows for one report and %d for a 10× one; want the same", short, wide)
	}
}

// TestStatsWeekLine pins §4.4: the last seven days, the seven before and the
// busiest day, with the busiest clause gone on an all-zero window.
func TestStatsWeekLine(t *testing.T) {
	days := make([]stats.DayCost, 14)
	for i := range days {
		days[i].Day = time.Date(2026, time.September, 10+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		days[i].Tokens = 1000
	}
	for i := 7; i < 14; i++ {
		days[i].Tokens = 2000
	}
	days[11].Tokens = 5000

	want := "   this week 17k   last week 7k   busiest day 09-21 5k"
	if got := stripANSI(statsWeekLine(days)); got != want {
		t.Errorf("statsWeekLine = %q, want %q", got, want)
	}

	zero := make([]stats.DayCost, len(days))
	for i := range zero {
		zero[i].Day = days[i].Day
	}
	plain := stripANSI(statsWeekLine(zero))
	if strings.Contains(plain, "busiest day") {
		t.Errorf("an all-zero window = %q, want no busiest-day clause", plain)
	}
	if !strings.Contains(plain, "this week 0   last week 0") {
		t.Errorf("an all-zero window = %q, want both weeks at 0", plain)
	}
	if got := stripANSI(statsWeekLine(days[:7])); !strings.Contains(got, "last week 0") {
		t.Errorf("a seven-day window = %q, want last week 0", got)
	}
}

// TestStatsTokensEmpty pins §4.5: a window with no tokens shows the no-token
// line under the chips instead of a chart.
func TestStatsTokensEmpty(t *testing.T) {
	rep := statsTokensReport()
	rep.Totals.TokenKinds = stats.TokenCounts{}
	rep.Totals.Tokens = 0
	for i := range rep.Spend.Days {
		rep.Spend.Days[i].Tokens = 0
	}
	v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabTokens, split: 1}

	lines := v.tokensTabLines(statsTestEnv(t, 132, 40), 132, 39)
	if len(lines) < 3 {
		t.Fatalf("the tokens tab has %d lines:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if got := strings.TrimSpace(stripANSI(lines[0])); got != "total   candidate   provider   kind" {
		t.Errorf("the chips row = %q", got)
	}
	if got := stripANSI(lines[2]); !strings.Contains(got, "no token usage recorded in this window") {
		t.Errorf("the empty tab's third line = %q, want the no-token line under the chips", got)
	}
}

// TestStatsReposTabTables pins §4.1's repos tab at 132 columns: one REPO table
// with one cursor, no FEATURE or TICKET header, no titled rule, no outcomes
// block, no dollars and no RNDS/LAND column; the header names BINDINGS,
// COMMITS and % TOKENS; an expanded repo's children sit under it, indented two
// cells inside the name column, their numbers and share on the repo rows'
// columns; and every line stays within w-3.
func TestStatsReposTabTables(t *testing.T) {
	env := statsTestEnv(t, 132, 40)
	v := statsTestView("30d")
	v.tab = statsTabRepos
	v.expanded = map[string]bool{statsFixtureRepoKey: true}

	body := stripANSI(v.Body(env, 132, 40))
	for _, gone := range []string{"──", "outcomes", "RNDS/LAND", "$"} {
		if strings.Contains(body, gone) {
			t.Errorf("the repos tab still shows %q:\n%s", gone, body)
		}
	}
	lines := strings.Split(body, "\n")

	repoHdr := statsLineIndex(lines, "BINDINGS")
	if repoHdr < 0 {
		t.Fatalf("no repos header:\n%s", body)
	}
	for _, want := range []string{"BINDINGS", "COMMITS", "% TOKENS"} {
		if !strings.Contains(lines[repoHdr], want) {
			t.Errorf("repos header = %q, want %s", lines[repoHdr], want)
		}
	}
	for _, gone := range []string{"FEATURE", "TICKET"} {
		if strings.Contains(body, gone) {
			t.Errorf("the repos tab still shows a %s header:\n%s", gone, body)
		}
	}

	// The table is four visible rows: the repo, its feature, its (no feature)
	// row and the second repo. The child names are indented two cells inside
	// the name column; the numbers and the share stay on the repo rows'
	// columns, and the header's own "S" sits over every row's "%".
	repoRow := repoHdr + 1
	childRow := repoRow + 1
	if got, want := statsFirstCol(lines[childRow]), statsFirstCol(lines[repoRow])+2; got != want {
		t.Errorf("the child name starts at col %d, want %d (two cells inside the name column)\n%q\n%q",
			got, want, lines[repoRow], lines[childRow])
	}
	repoEnd := statsColEnd(lines[repoHdr], "TOKENS")
	if end := statsColEnd(lines[childRow], "1.5M"); end != repoEnd {
		t.Errorf("the child's TOKENS ends at %d, want %d (the repo rows' column)\n%q", end, repoEnd, lines[childRow])
	}
	statsCheckShareAligned(t, lines, repoHdr, 4)

	for i, line := range lines {
		if got := len([]rune(strings.TrimRight(line, " "))); got > 132-3 {
			t.Errorf("line %d trimmed width = %d, want <= %d\n%q", i, got, 132-3, line)
		}
	}
}

// TestStatsRepoChildren pins an expanded repo's children (§4.1): each labelled
// feature by tokens desc, its labelled tickets nested under it by tokens desc,
// then the (no feature) row with its tickets under the same rule. The (no
// feature) row shows when the repo has featureless rounds and either a labelled
// feature or a labelled ticket; a repo with no labels has no children.
func TestStatsRepoChildren(t *testing.T) {
	repo := stats.RepoRow{GroupRow: stats.GroupRow{Key: "r"}}

	t.Run("labelled only", func(t *testing.T) {
		labelled := repo
		labelled.Features = []stats.FeatureRow{
			{GroupRow: stats.GroupRow{Key: "small", Tokens: 10}},
			{GroupRow: stats.GroupRow{Key: "big", Tokens: 100}, Tickets: []stats.GroupRow{
				{Key: "t1", Tokens: 5}, {Key: "t2", Tokens: 50},
			}},
		}
		// Zero featureless rounds: no (no feature) row even though the bucket
		// carries tokens.
		labelled.NoFeature = stats.FeatureRow{GroupRow: stats.GroupRow{Key: "(none)", Tokens: 5}}
		got := repoChildren(labelled)
		want := []string{"big", "t2", "t1", "small"}
		if len(got) != len(want) {
			t.Fatalf("children = %+v, want %v", got, want)
		}
		for i, key := range want {
			if got[i].group.Key != key {
				t.Errorf("child %d = %q, want %q", i, got[i].group.Key, key)
			}
		}
		if got[0].kind != kindFeature || got[1].kind != kindTicket || got[2].kind != kindTicket ||
			got[3].kind != kindFeature {
			t.Errorf("kinds = %v, %v, %v, %v; want feature, ticket, ticket, feature",
				got[0].kind, got[1].kind, got[2].kind, got[3].kind)
		}
		for _, r := range got {
			if r.parent != "r" {
				t.Errorf("child = %+v, want a child of repo r", r)
			}
		}
		for _, r := range got[1:3] {
			if r.feature != "big" {
				t.Errorf("ticket row = %+v, want it owned by feature big", r)
			}
		}
	})

	t.Run("labelled and unlabelled", func(t *testing.T) {
		mixed := repo
		mixed.Features = []stats.FeatureRow{{GroupRow: stats.GroupRow{Key: "f1", Tokens: 100}}}
		mixed.NoFeature = stats.FeatureRow{
			GroupRow: stats.GroupRow{Key: "(none)", Rounds: 2, Tokens: 50},
			Tickets:  []stats.GroupRow{{Key: "t1", Tokens: 70}},
		}
		got := repoChildren(mixed)
		want := []string{"f1", "(none)", "t1"}
		if len(got) != len(want) {
			t.Fatalf("children = %+v, want %v", got, want)
		}
		for i, key := range want {
			if got[i].group.Key != key {
				t.Errorf("child %d = %q, want %q", i, got[i].group.Key, key)
			}
		}
		if got[1].kind != kindFeature || got[2].kind != kindTicket {
			t.Errorf("kinds = %v, %v; want a (no feature) row then its ticket", got[1].kind, got[2].kind)
		}
		if got[2].feature != "(none)" {
			t.Errorf("the (no feature) ticket's feature = %q, want %q", got[2].feature, "(none)")
		}
	})

	t.Run("tickets only", func(t *testing.T) {
		// No labelled feature, but featureless rounds carrying tickets: the
		// (no feature) row shows so those tickets stay reachable.
		tickets := repo
		tickets.NoFeature = stats.FeatureRow{
			GroupRow: stats.GroupRow{Key: "(none)", Rounds: 1, Tokens: 30},
			Tickets:  []stats.GroupRow{{Key: "t9", Tokens: 30}},
		}
		got := repoChildren(tickets)
		want := []string{"(none)", "t9"}
		if len(got) != len(want) {
			t.Fatalf("children = %+v, want %v", got, want)
		}
		for i, key := range want {
			if got[i].group.Key != key {
				t.Errorf("child %d = %q, want %q", i, got[i].group.Key, key)
			}
		}
		if got[0].kind != kindFeature || got[1].kind != kindTicket {
			t.Errorf("kinds = %v, %v; want a (no feature) row then its ticket", got[0].kind, got[1].kind)
		}
	})

	t.Run("unlabelled only", func(t *testing.T) {
		plain := repo
		plain.NoFeature = stats.FeatureRow{GroupRow: stats.GroupRow{Key: "(none)", Rounds: 3, Tokens: 30}}
		if got := repoChildren(plain); len(got) != 0 {
			t.Errorf("children = %+v, want none: no labelled child and no ticket to nest", got)
		}
	})
}

// statsCheckShareAligned asserts that on the line at lines[hdr], the "% TOKENS"
// header's final "S" sits in the same column as the "%" of the percentage on
// each of the n rows that follow it.
func statsCheckShareAligned(t *testing.T, lines []string, hdr, n int) {
	t.Helper()
	headerCol := statsColEnd(lines[hdr], "% TOKENS")
	if headerCol < 0 {
		t.Fatalf("no %% TOKENS in header:\n%q", lines[hdr])
	}
	for i := 0; i < n; i++ {
		row := lines[hdr+1+i]
		runes := []rune(row)
		pct := -1
		for j, r := range runes {
			if r == '%' {
				pct = j
			}
		}
		if pct < 0 {
			t.Fatalf("row %d has no %%:\n%q", i, row)
		}
		if pct != headerCol {
			t.Errorf("row %d: %% at col %d, header S at col %d\nheader: %q\nrow:    %q",
				i, pct, headerCol, lines[hdr], row)
		}
	}
}

// TestStatsShareHeaderAligned pins round 5's fix: the "% TOKENS" header is
// right-aligned over its cell, so its final "S" sits over the "%" of every
// row's percentage. Checked on the repos tab's one table (§4.2) expanded so
// the children are rows too, and on the overview's BUSIEST REPOS table (§4.8),
// both at 132 columns.
func TestStatsShareHeaderAligned(t *testing.T) {
	rep := statsFixture()
	repos := append([]stats.RepoRow(nil), rep.Repos...)
	sort.SliceStable(repos, func(i, j int) bool { return repos[i].Tokens > repos[j].Tokens })

	t.Run("repos tab", func(t *testing.T) {
		env := statsTestEnv(t, 132, 40)
		v := statsTestView("30d")
		v.tab = statsTabRepos
		v.expanded = map[string]bool{statsFixtureRepoKey: true}
		body := stripANSI(v.Body(env, 132, 40))
		lines := strings.Split(body, "\n")

		repoHdr := statsLineIndex(lines, "BINDINGS")
		if repoHdr < 0 {
			t.Fatalf("no repos header:\n%s", body)
		}
		// Two repo rows plus the expanded repo's feature and (no feature).
		statsCheckShareAligned(t, lines, repoHdr, len(repos)+2)
	})

	t.Run("overview", func(t *testing.T) {
		env := statsTestEnv(t, 132, 34)
		v := statsTestView("30d")
		body := stripANSI(v.Body(env, 132, 34))
		lines := strings.Split(body, "\n")

		repoHdr := statsLineIndex(lines, "% TOKENS")
		if repoHdr < 0 {
			t.Fatalf("no %% TOKENS header:\n%s", body)
		}
		statsCheckShareAligned(t, lines, repoHdr, len(repos))
	})
}

// TestStatsReposTabDetail pins §4.3: the selected row's detail block names the
// row and its kind, its round and binding totals with the window share, its
// outcomes and its top candidates -- and a feature child's line 1 ends
// `feature`.
func TestStatsReposTabDetail(t *testing.T) {
	env := statsTestEnv(t, 132, 40)
	rep := statsFixture()
	rep.Repos[0].GroupRow.ByCandidate = map[string]int{"deepseek-v4.1-flash": 3}
	v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabRepos,
		expanded: map[string]bool{statsFixtureRepoKey: true}}

	body := stripANSI(v.Body(env, 132, 40))
	for _, want := range []string{"rounds on", "% of the window", "by candidate:", "deepseek-v4.1-flash"} {
		if !strings.Contains(body, want) {
			t.Errorf("repos detail missing %q:\n%s", want, body)
		}
	}

	// The repo is row 0 and its feature child row 1: move onto the child and
	// check its detail's first line.
	v.cursor[1] = 1
	lines, sel := v.reposTabLines(env, 132)
	if sel < 0 || sel >= len(lines) {
		t.Fatalf("sel = %d, want a line in the feature's block", sel)
	}
	first := lines[len(lines)-3]
	if got := strings.TrimRight(stripANSI(first), " "); !strings.HasSuffix(got, "feature") {
		t.Errorf("feature detail line 1 = %q, want it to end `feature`\n%s", got, strings.Join(lines, "\n"))
	}
}

// TestStatsReposNoFeatureRow pins §2.3 at repo scope: a repo with both a
// labelled feature and a featureless round ends its feature section with a
// (no feature) row, enter on it notices instead of pushing rounds, and a repo
// whose featureless rounds carry no ticket and whose features are gone shows
// nothing.
func TestStatsReposNoFeatureRow(t *testing.T) {
	t.Run("row and detail", func(t *testing.T) {
		v := statsView{window: "30d", loaded: true, rep: statsFixture(), tab: statsTabRepos,
			expanded: map[string]bool{statsFixtureRepoKey: true}}
		children := repoChildren(v.rep.Repos[0])
		if len(children) == 0 || children[len(children)-1].group.Key != "(none)" ||
			children[len(children)-1].kind != kindFeature {
			t.Fatalf("children = %+v, want a last (none) feature row", children)
		}

		body := stripANSI(v.Body(statsTestEnv(t, 132, 40), 132, 40))
		if !strings.Contains(body, "(no feature)") {
			t.Errorf("repos tab missing the (no feature) row:\n%s", body)
		}
	})

	t.Run("enter notices", func(t *testing.T) {
		// The fixture's repo relevo: its feature child then its (no feature).
		m := statsReposShell(t, statsFixture(), statsFixtureRepoKey)
		for i := 0; i < 2; i++ {
			res, _ := m.Update(statsKey('j'))
			m = res.(Model)
		}
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		if _, ok := m.top().(roundsView); ok {
			t.Fatal("the (no feature) row must not push a rounds view")
		}
		if !strings.Contains(m.notice, "rounds with no feature cannot be filtered") {
			t.Errorf("notice = %q", m.notice)
		}
	})

	t.Run("unlabelled rounds alone show nothing", func(t *testing.T) {
		rep := statsFixture()
		// No labelled feature and no ticket on the featureless rounds: the
		// visibility rule keeps (no feature) hidden.
		rep.Repos[0].Features = nil
		rep.Repos[0].NoFeature.Tickets = nil
		v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabRepos,
			expanded: map[string]bool{statsFixtureRepoKey: true}}
		if got := repoChildren(rep.Repos[0]); len(got) != 0 {
			t.Fatalf("children = %+v, want none without a labelled feature or ticket", got)
		}
		body := stripANSI(v.Body(statsTestEnv(t, 132, 40), 132, 40))
		if strings.Contains(body, "(no feature)") {
			t.Errorf("repos tab shows (no feature) with no labelled child to reach:\n%s", body)
		}
		if strings.Contains(body, "FEATURE") {
			t.Errorf("repos tab shows a FEATURE header:\n%s", body)
		}
	})
}

// TestStatsReposTabDropsColumns pins §4.2's dropping: at 80 columns COMMITS
// leaves first, the name column stays at least 16 cells, and no line passes
// w-3.
func TestStatsReposTabDropsColumns(t *testing.T) {
	const w = 80
	v := statsTestView("30d")
	v.tab = statsTabRepos
	env := statsTestEnv(t, w, 40)
	body := stripANSI(v.Body(env, w, 40))
	lines := strings.Split(body, "\n")

	hdr := statsLineIndex(lines, "RNDS")
	if hdr < 0 {
		t.Fatalf("no repos header:\n%s", body)
	}
	if strings.Contains(lines[hdr], "COMMITS") {
		t.Errorf("at %d the header still shows COMMITS: %q", w, lines[hdr])
	}
	if _, nameW := statsRepoVisible(w); nameW < 16 {
		t.Errorf("at %d the name column is %d cells, want at least 16", w, nameW)
	}
	for i, line := range lines {
		if got := len([]rune(strings.TrimRight(line, " "))); got > w-3 {
			t.Errorf("line %d trimmed width = %d, want <= %d\n%q", i, got, w-3, line)
		}
	}
}

// TestStatsReliabilityTab pins §5.1 at 132 columns: the four tiles, the gates
// table with a reviewer-scoped gate left out of both the count and the table,
// the UNTIL and LEFT cells, and the limits-by-hour table's red bold three.
func TestStatsReliabilityTab(t *testing.T) {
	rep := statsFixture()
	rep.Reliability.Switches = 5
	rep.Reliability.RoundsSwitched = 3
	rep.Reliability.Active = []availability.Gate{
		{Token: "opencode/google/gemini", Kind: availability.RateLimited,
			Until: railNow.Add(3 * time.Hour), Note: "Individual quota reached"},
		{Token: "claude/anthropic/haiku", Kind: availability.RateLimited,
			Role: "reviewer", Until: railNow.Add(3 * time.Hour), Note: "not a builder"},
	}
	hours := [24]int{}
	hours[4] = 3
	rep.Reliability.ByHour = []stats.HourRow{
		{Provider: "google", Counts: hours},
		{Provider: "openai", Counts: [24]int{6: 1}},
	}
	v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabReliability}
	env := statsTestEnv(t, 132, 40)
	raw := v.Body(env, 132, 40)
	body := stripANSI(raw)

	for _, want := range []string{"SWITCHES", "RATE LIMITS", "SPAWN FAILURES", "GATED NOW"} {
		if !strings.Contains(body, want) {
			t.Errorf("reliability tab missing the %s tile:\n%s", want, body)
		}
	}

	// GATED NOW is the one builder-scoped gate: its value sits on the row
	// below the labels, in its own 32-cell tile column.
	lines := strings.Split(body, "\n")
	i := statsLineIndex(lines, "GATED NOW")
	if i < 0 {
		t.Fatalf("no GATED NOW tile:\n%s", body)
	}
	pitch := (132 - 4) / 4
	value := string([]rune(lines[i+1])[3+3*pitch : 3+4*pitch])
	if got := strings.TrimSpace(value); got != "1" {
		t.Errorf("GATED NOW = %q, want 1", got)
	}

	// The counted gate's row: name, provider, UNTIL and LEFT.
	for _, want := range []string{"opencode/google/gemini", "google", "17:02", "3h", "Individual quota reached"} {
		if !strings.Contains(body, want) {
			t.Errorf("the gates table is missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "haiku") || strings.Contains(body, "not a builder") {
		t.Errorf("the reviewer-scoped gate must not show:\n%s", body)
	}

	// The hours row's three is red bold.
	if !strings.Contains(raw, redStyle.Bold(true).Render("3")) {
		t.Errorf("the hours row's 3 is not redStyle bold:\n%q", raw)
	}
	if strings.Contains(body, "──") {
		t.Errorf("the reliability tab must have no titled rule:\n%s", body)
	}
}

// TestStatsReliabilityEmpty pins §5.1's empty states: with no gates and no
// ByHour the tab says so on one faint line each.
func TestStatsReliabilityEmpty(t *testing.T) {
	rep := statsFixture()
	rep.Reliability.Active = nil
	rep.Reliability.ByHour = nil
	v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabReliability}

	body := stripANSI(v.Body(statsTestEnv(t, 132, 40), 132, 40))
	for _, want := range []string{"no candidate is gated", "no rate limits in this window"} {
		if !strings.Contains(body, want) {
			t.Errorf("empty reliability tab missing %q:\n%s", want, body)
		}
	}
}

// TestStatsReliabilityFillsWidth pins §2's responsive columns at 132 and 200:
// the gates table's REASON cell is padded to reasonW so a row (margin
// included) is exactly w-3 runes wide, and the hours table's per-hour cells
// share one cellW so the header's hour labels and each row's counts land in
// the columns the formula predicts.
func TestStatsReliabilityFillsWidth(t *testing.T) {
	rep := statsFixture()
	rep.Reliability.Active = []availability.Gate{
		{Token: "opencode/google/gemini", Kind: availability.RateLimited,
			Until: railNow.Add(3 * time.Hour), Note: "Individual quota reached"},
	}
	hours := [24]int{}
	hours[9] = 7
	rep.Reliability.ByHour = []stats.HourRow{{Provider: "google", Counts: hours}}
	v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabReliability}

	for _, w := range []int{132, 200} {
		env := statsTestEnv(t, w, 40)

		// The gates table has one header line and one data row; the data
		// row's REASON cell is padded to reasonW, which is what makes the
		// row -- margin included -- exactly w-3 runes wide (§2.1).
		gateLines := v.statsGatesLines(env, w)
		if len(gateLines) != 2 {
			t.Fatalf("w=%d: gates table has %d lines, want 2 (header + one row)", w, len(gateLines))
		}
		if got := len([]rune(stripANSI(gateLines[1]))); got != w-3 {
			t.Errorf("w=%d: gate row width = %d, want %d\n%q", w, got, w-3, stripANSI(gateLines[1]))
		}

		wantCellW := 4
		if w == 200 {
			wantCellW = 7
		}
		hourLines := v.statsHourLines(w)
		if len(hourLines) != 2 {
			t.Fatalf("w=%d: hours table has %d lines, want 2 (header + one row)", w, len(hourLines))
		}
		head := []rune(stripANSI(hourLines[0]))
		row := []rune(stripANSI(hourLines[1]))

		wantEnd := 3 + 16 + 24*wantCellW - 1
		if got := len([]rune(strings.TrimRight(string(head), " "))) - 1; got != wantEnd {
			t.Errorf("w=%d: hour header ends at %d, want %d\n%q", w, got, wantEnd, string(head))
		}
		if wantEnd >= len(head) || head[wantEnd] != '3' {
			t.Errorf("w=%d: hour header's last char at %d is not the tail of \"23\": %q", w, wantEnd, string(head))
		}

		hour9End := 3 + 16 + 10*wantCellW - 1
		if hour9End >= len(head) || head[hour9End] != '9' {
			t.Errorf("w=%d: hour-9 label does not end at column %d: %q", w, hour9End, string(head))
		}
		if hour9End >= len(row) || row[hour9End] != '7' {
			t.Errorf("w=%d: hour-9 count does not end at column %d: %q", w, hour9End, string(row))
		}
	}
}

// TestStatsGateReason pins §2.4: the reliability tab's REASON cell keeps only
// the readable lead clause of the provider's raw note.
func TestStatsGateReason(t *testing.T) {
	cases := []struct {
		note, want string
	}{
		{
			note: "RESOURCE_EXHAUSTED (code 429): Individual quota reached (re-gated after provider rename antigravity -> agy-extra)",
			want: "Individual quota reached",
		},
		{
			note: "error: Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h37m3s.",
			want: "Individual quota reached",
		},
		{
			note: "Codex usage limit: try again at Oct 19th, 2026 10:14 PM (seen by relevo candidates --probe)",
			want: "Codex usage limit",
		},
		{
			note: `AGY_ERROR: {"short_error":"RESOURCE_EXHAUSTED (code 429): Individual quota reached. Please upgrade…","status":"RESOURCE_EXHAUSTED"}`,
			want: "Individual quota reached",
		},
		{note: "", want: "·"},
	}
	for _, c := range cases {
		if got := statsGateReason(c.note); got != c.want {
			t.Errorf("statsGateReason(%q) = %q, want %q", c.note, got, c.want)
		}
	}
}

func TestStatsGateReasonHTTPStatus(t *testing.T) {
	cases := []struct {
		note, want string
	}{
		{
			note: "Error 429: weekly Clinepass limit reached, resets in 1d 4h",
			want: "weekly Clinepass limit reached, resets in 1d 4h",
		},
		{
			note: "error: Individual quota reached. Please upgrade",
			want: "Individual quota reached",
		},
	}
	for _, c := range cases {
		if got := statsGateReason(c.note); got != c.want {
			t.Errorf("statsGateReason(%q) = %q, want %q", c.note, got, c.want)
		}
	}
}

// TestStatsReposTicketRows pins the nested tickets at repo scope: a labelled
// ticket sits under its feature four cells in against the feature's two, the
// (no feature) bucket nests its own tickets, a ticket row's detail names it a
// ticket, and no (no ticket) row exists anywhere.
func TestStatsReposTicketRows(t *testing.T) {
	rep := statsFixture()
	rep.Repos[0].Features[0].Tickets = []stats.GroupRow{{Key: "o/r#607", Rounds: 2, Tokens: 700_000}}
	rep.Repos[0].NoFeature.Tickets = []stats.GroupRow{{Key: "o/r#608", Rounds: 1, Tokens: 400_000}}

	t.Run("rows, indent and detail", func(t *testing.T) {
		v := statsView{window: "30d", loaded: true, rep: rep, tab: statsTabRepos,
			expanded: map[string]bool{statsFixtureRepoKey: true}}
		children := repoChildren(rep.Repos[0])
		if len(children) != 4 || children[0].group.Key != "cockpit" || children[1].group.Key != "o/r#607" ||
			children[2].group.Key != "(none)" || children[3].group.Key != "o/r#608" {
			t.Fatalf("children = %+v, want cockpit, its ticket, (no feature) and its ticket", children)
		}
		if children[1].feature != "cockpit" || children[3].feature != "(none)" {
			t.Errorf("ticket owners = %q, %q; want cockpit and (none)", children[1].feature, children[3].feature)
		}
		v.focus = 1
		// The repo, its four children, then the second repo.
		if got := v.panelRows(); got != 6 {
			t.Fatalf("panelRows = %d, want 6 visible rows", got)
		}

		body := stripANSI(v.Body(statsTestEnv(t, 132, 60), 132, 60))
		lines := strings.Split(body, "\n")
		if strings.Contains(body, "TICKET") {
			t.Errorf("repos tab still shows a TICKET header:\n%s", body)
		}
		if strings.Contains(body, "(no ticket)") {
			t.Errorf("repos tab shows a (no ticket) row:\n%s", body)
		}
		repoLine := statsLineIndex(lines, "fuad-daoud/relevo")
		featureLine := statsLineIndex(lines, "cockpit")
		ticketLine := statsLineIndex(lines, "o/r#607")
		if repoLine < 0 || featureLine < 0 || ticketLine < 0 {
			t.Fatalf("missing a row: repo %d, feature %d, ticket %d\n%s", repoLine, featureLine, ticketLine, body)
		}
		repoCol := statsFirstCol(lines[repoLine])
		if got, want := statsFirstCol(lines[featureLine]), repoCol+2; got != want {
			t.Errorf("the feature name starts at col %d, want %d (two cells inside)\n%q",
				got, want, lines[featureLine])
		}
		if got, want := statsFirstCol(lines[ticketLine]), repoCol+4; got != want {
			t.Errorf("the ticket name starts at col %d, want %d (four cells inside)\n%q\n%q",
				got, want, lines[featureLine], lines[ticketLine])
		}

		// The ticket's detail names its kind.
		v.cursor[1] = 2
		lines, sel := v.reposTabLines(statsTestEnv(t, 132, 60), 132)
		if sel < 0 || sel >= len(lines) {
			t.Fatalf("sel = %d, want a line in the ticket's block", sel)
		}
		first := stripANSI(lines[len(lines)-3])
		if !strings.Contains(first, "o/r#607") || !strings.Contains(first, "ticket") {
			t.Errorf("ticket detail line 1 = %q, want it to name the ticket\n%s", first, strings.Join(lines, "\n"))
		}
	})

	t.Run("no labelled tickets means no ticket rows", func(t *testing.T) {
		v := statsView{window: "30d", loaded: true, rep: statsFixture(), tab: statsTabRepos,
			expanded: map[string]bool{statsFixtureRepoKey: true}}
		if got := repoChildren(v.rep.Repos[0]); len(got) != 2 {
			t.Fatalf("children = %+v, want only the feature rows", got)
		}
		body := stripANSI(v.Body(statsTestEnv(t, 132, 60), 132, 60))
		if strings.Contains(body, "TICKET") {
			t.Errorf("repos tab shows a TICKET header with no labelled tickets:\n%s", body)
		}
		if strings.Contains(body, "(no ticket)") {
			t.Errorf("repos tab shows (no ticket) with no labelled tickets:\n%s", body)
		}
	})
}

// TestStatsReposExpandKeys pins §4.1's expansion keys: space toggles the repo
// owning the selected row, → expands it, ← collapses it, several repos may be
// expanded at once, expanding never moves the cursor, and ← on a child lands
// the cursor on that repo's row.
func TestStatsReposExpandKeys(t *testing.T) {
	env := statsTestEnv(t, 100, 30)
	// A second labelled repo, so both repos have children to expand.
	rep := statsFixture()
	rep.Repos[1].Features = []stats.FeatureRow{{GroupRow: stats.GroupRow{Key: "loose", Tokens: 50_000}}}
	rep.Repos[1].NoFeature = stats.FeatureRow{GroupRow: stats.GroupRow{Key: "(none)", Rounds: 1, Tokens: 50_000}}
	v := View(statsView{window: "30d", loaded: true, rep: rep})

	next, _ := v.Update(statsKey('5'), env)
	v = next

	// space expands the selected repo in place: the cursor does not move.
	next, _ = v.Update(tea.KeyMsg{Type: tea.KeySpace}, env)
	v = next
	got := v.(statsView)
	if !got.expanded[statsFixtureRepoKey] {
		t.Fatalf("space must expand the selected repo: expanded = %v", got.expanded)
	}
	if got.cursor[1] != 0 {
		t.Errorf("expanding must not move the cursor: cursor[1] = %d, want 0", got.cursor[1])
	}
	if n := got.panelRows(); n != 4 {
		t.Errorf("panelRows = %d, want 4 (two repos plus the first repo's two children)", n)
	}

	// space again collapses it.
	next, _ = v.Update(tea.KeyMsg{Type: tea.KeySpace}, env)
	v = next
	if got := v.(statsView); got.expanded[statsFixtureRepoKey] {
		t.Errorf("space must collapse the expanded repo: expanded = %v", got.expanded)
	}

	// → expands, and two repos may be expanded at once.
	next, _ = v.Update(statsKey('j'), env) // onto the second repo
	v = next
	next, _ = v.Update(tea.KeyMsg{Type: tea.KeyRight}, env)
	v = next
	next, _ = v.Update(statsKey('k'), env) // back onto the first repo
	v = next
	next, _ = v.Update(tea.KeyMsg{Type: tea.KeyRight}, env)
	v = next
	got = v.(statsView)
	if !got.expanded[statsFixtureRepoKey] || !got.expanded["(none)"] {
		t.Fatalf("→ must expand both repos: expanded = %v", got.expanded)
	}
	if n := got.panelRows(); n != 6 {
		t.Errorf("panelRows = %d, want 6 (two repos, each with two children)", n)
	}

	// ← collapses the selected repo only.
	next, _ = v.Update(tea.KeyMsg{Type: tea.KeyLeft}, env)
	v = next
	got = v.(statsView)
	if got.expanded[statsFixtureRepoKey] || !got.expanded["(none)"] {
		t.Errorf("← must collapse only the selected repo: expanded = %v", got.expanded)
	}

	// ← on a child lands the cursor on its repo's row.
	next, _ = v.Update(statsKey('j'), env) // onto the (none) repo
	v = next
	next, _ = v.Update(statsKey('j'), env) // onto that repo's first child
	v = next
	got = v.(statsView)
	if got.cursor[1] != 2 {
		t.Fatalf("cursor[1] = %d, want 2 (the child row)", got.cursor[1])
	}
	next, _ = v.Update(tea.KeyMsg{Type: tea.KeyLeft}, env)
	v = next
	got = v.(statsView)
	if got.expanded["(none)"] {
		t.Errorf("← on a child must collapse its repo: expanded = %v", got.expanded)
	}
	if got.cursor[1] != 1 {
		t.Errorf("← on a child must land the cursor on the repo's row: cursor[1] = %d, want 1", got.cursor[1])
	}
}

// TestStatsReposExpandEmptyRepo pins #669: a repo with no features or tickets
// stays collapsed and answers the expand keys with a notice instead of silence,
// while a featureless repo whose rounds carry tickets still expands to its
// (no feature) row.
func TestStatsReposExpandEmptyRepo(t *testing.T) {
	env := statsTestEnv(t, 100, 30)
	rep := statsFixture()
	rep.Repos[0].Features = nil
	rep.Repos[0].NoFeature = stats.FeatureRow{}
	v := View(statsView{window: "30d", loaded: true, rep: rep})
	next, _ := v.Update(statsKey('5'), env)
	v = next

	for _, key := range []tea.KeyMsg{{Type: tea.KeySpace}, {Type: tea.KeyRight}} {
		next, cmd := v.Update(key, env)
		if cmd == nil {
			t.Fatalf("%v on a repo with no children must notice", key)
		}
		msg, ok := cmd().(noticeMsg)
		if !ok {
			t.Fatalf("%v: msg = %T, want a notice", key, msg)
		}
		if !strings.Contains(msg.text, "has no features or tickets") {
			t.Errorf("%v: notice = %q, want it to name what is missing", key, msg.text)
		}
		got := next.(statsView)
		if got.expanded[statsFixtureRepoKey] {
			t.Errorf("%v expanded a repo with no children", key)
		}
		if n := got.panelRows(); n != 2 {
			t.Errorf("%v: panelRows = %d, want the two repo rows", key, n)
		}
	}

	// The reachability case keeps working: featureless rounds with tickets
	// expand to (no feature) and its tickets, with no notice.
	rep.Repos[0].NoFeature = stats.FeatureRow{
		GroupRow: stats.GroupRow{Key: "(none)", Rounds: 1, Tokens: 30},
		Tickets:  []stats.GroupRow{{Key: "t9", Tokens: 30}},
	}
	v = View(statsView{window: "30d", loaded: true, rep: rep})
	next, _ = v.Update(statsKey('5'), env)
	next, cmd := next.Update(tea.KeyMsg{Type: tea.KeySpace}, env)
	if cmd != nil {
		t.Errorf("a repo with a ticket under (no feature) must expand, not notice")
	}
	if !next.(statsView).expanded[statsFixtureRepoKey] {
		t.Errorf("a repo with a ticket under (no feature) must expand")
	}
}

// TestStatsReposVisibleCursor pins §4.1's cursor: ↑↓ walk only the visible rows
// and stop at the list's ends, and a collapsed repo's children are unreachable.
func TestStatsReposVisibleCursor(t *testing.T) {
	env := statsTestEnv(t, 100, 30)
	v := View(statsTestView("30d"))
	next, _ := v.Update(statsKey('5'), env)
	v = next

	// Collapsed: two repo rows, so ↓ twice stops at 1.
	for i := 0; i < 2; i++ {
		next, _ = v.Update(statsKey('j'), env)
		v = next
	}
	if got := v.(statsView).cursor[1]; got != 1 {
		t.Errorf("collapsed: cursor[1] = %d, want 1", got)
	}

	// Expanding the first repo makes its two children reachable.
	next, _ = v.Update(statsKey('k'), env)
	v = next
	next, _ = v.Update(tea.KeyMsg{Type: tea.KeyRight}, env)
	v = next
	if got := v.(statsView).panelRows(); got != 4 {
		t.Fatalf("panelRows = %d, want 4", got)
	}
	for i := 0; i < 3; i++ {
		next, _ = v.Update(statsKey('j'), env)
		v = next
	}
	if got := v.(statsView).cursor[1]; got != 3 {
		t.Errorf("expanded: cursor[1] = %d, want 3 (the last visible row)", got)
	}
	for i := 0; i < 5; i++ {
		next, _ = v.Update(statsKey('j'), env)
		v = next
	}
	if got := v.(statsView).cursor[1]; got != 3 {
		t.Errorf("expanded: cursor[1] = %d, want 3 (clamped at the visible list's end)", got)
	}

	// ← on a child collapses its repo, lands the cursor on the repo row, and
	// the hidden children are unreachable again.
	next, _ = v.Update(statsKey('k'), env)
	v = next
	next, _ = v.Update(statsKey('k'), env)
	v = next
	if got := v.(statsView).cursor[1]; got != 1 {
		t.Fatalf("cursor[1] = %d, want 1 (the feature child)", got)
	}
	next, _ = v.Update(tea.KeyMsg{Type: tea.KeyLeft}, env)
	v = next
	got := v.(statsView)
	if got.cursor[1] != 0 || len(got.repoTabRows()) != 2 {
		t.Errorf("after collapsing: cursor[1] = %d over %d rows, want 0 over 2", got.cursor[1], len(got.repoTabRows()))
	}
}

// TestStatsReposExpansionSurvivesRefresh pins §4.1: expansion is view state
// keyed by repo key, so a fresh statsMsg (a tick, r or w reply) leaves it
// untouched and the children stay shown.
func TestStatsReposExpansionSurvivesRefresh(t *testing.T) {
	m := statsShell(t, 160, 40, "30d")
	res, _ := m.Update(statsMsg{window: "30d", rep: statsFixture()})
	m = res.(Model)
	res, _ = m.Update(statsKey('5'))
	m = res.(Model)
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = res.(Model)

	res, _ = m.Update(statsMsg{window: "30d", rep: statsFixture()})
	m = res.(Model)
	v := m.top().(statsView)
	if !v.expanded[statsFixtureRepoKey] {
		t.Fatalf("a fresh statsMsg dropped the expansion: expanded = %v", v.expanded)
	}
	if body := stripANSI(m.View()); !strings.Contains(body, "cockpit") {
		t.Errorf("the expanded repo's child is gone after a refresh:\n%s", body)
	}
}

// TestStatsReposChildEnter pins §4.4: enter on a feature child opens `:rounds`
// scoped to its repo and its label; a ticket under a feature adds the feature
// term; a ticket under (no feature) scopes to the repo and the ticket; the
// (no feature) row keeps its notice; and a child of the (none) repo notices
// because no query term means "no repo".
func TestStatsReposChildEnter(t *testing.T) {
	cases := []struct {
		name         string
		expanded     []string
		tickets      bool
		downs        int
		want         string
		wantNotice   string
		labelledNone bool
	}{
		{name: "feature child", expanded: []string{statsFixtureRepoKey}, downs: 1,
			want: `repo:"https://github.com/fuad-daoud/relevo" feature:"cockpit" since:30d`},
		{name: "ticket under a feature", expanded: []string{statsFixtureRepoKey}, tickets: true, downs: 2,
			want: `repo:"https://github.com/fuad-daoud/relevo" feature:"cockpit" ticket:"o/r#607" since:30d`},
		{name: "ticket under (no feature)", expanded: []string{statsFixtureRepoKey}, tickets: true, downs: 4,
			want: `repo:"https://github.com/fuad-daoud/relevo" ticket:"o/r#608" since:30d`},
		{name: "no feature child", expanded: []string{statsFixtureRepoKey}, downs: 2,
			wantNotice: "rounds with no feature cannot be filtered"},
		{name: "child of the (none) repo", expanded: []string{"(none)"}, downs: 2,
			wantNotice: "rounds with no repo cannot be filtered", labelledNone: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := statsFixture()
			if c.tickets {
				rep.Repos[0].Features[0].Tickets = []stats.GroupRow{{Key: "o/r#607", Rounds: 2, Tokens: 700_000}}
				rep.Repos[0].NoFeature.Tickets = []stats.GroupRow{{Key: "o/r#608", Rounds: 1, Tokens: 400_000}}
			}
			if c.labelledNone {
				// A label on the (none) repo, so it has a child to select.
				rep.Repos[1].Features = []stats.FeatureRow{{GroupRow: stats.GroupRow{Key: "loose", Rounds: 1, Tokens: 50_000}}}
				rep.Repos[1].NoFeature = stats.FeatureRow{GroupRow: stats.GroupRow{Key: "(none)", Rounds: 1, Tokens: 50_000}}
			}
			m := statsReposShell(t, rep, c.expanded...)
			for i := 0; i < c.downs; i++ {
				res, _ := m.Update(statsKey('j'))
				m = res.(Model)
			}
			res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = drain(t, res.(Model), cmd)
			if c.wantNotice != "" {
				if _, ok := m.top().(roundsView); ok {
					t.Fatal("this row must not push a rounds view")
				}
				if !strings.Contains(m.notice, c.wantNotice) {
					t.Errorf("notice = %q, want %q", m.notice, c.wantNotice)
				}
				return
			}
			rv, ok := m.top().(roundsView)
			if !ok {
				t.Fatalf("enter on a child must push a rounds view, got %T", m.top())
			}
			if got := rv.dash.QueryText(); got != c.want {
				t.Errorf("QueryText = %q, want %q", got, c.want)
			}
		})
	}
}
