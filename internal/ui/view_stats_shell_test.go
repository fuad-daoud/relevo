package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	datastats "github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/ui/stats"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
	"github.com/fuad-daoud/relevo/internal/view"
)

// This file holds the shell-driven stats tests: every one drives the stats
// view through the shell's Model and the exported stats API, the way the
// cockpit does. The pure unit tests live in internal/ui/stats.

// statsFixture builds a rich stats report by hand, so the view tests and the
// goldens never need a database.
func statsFixture() datastats.Report {
	at := func(day int) time.Time {
		return time.Date(2026, time.September, day, 12, 0, 0, 0, time.UTC)
	}
	var counts [24]int
	counts[9], counts[14] = 2, 1
	return datastats.Report{
		Since: at(16), Until: at(24),
		Totals: datastats.Totals{
			Rounds: 6, Bindings: 2, Candidates: 2, Unrecorded: 1,
			CostUSD: 1.40, PlanRounds: 1, Tokens: 2_500_000, MedianMS: 27 * 60_000,
			TokenKinds: datastats.TokenCounts{
				In: 40_000, Cache: 2_400_000, Write: 10_000, Out: 50_000, Measured: 5,
			},
		},
		Scorecard: []datastats.ScoreRow{
			{
				Token:  "opencode/cline-pass/deepseek-v4.1-flash",
				Rounds: 5, Closed: 4, Reported: 3, Halted: 1,
				DonePct: 75, HaltPct: 25,
				MedianMS: 27 * 60_000, HasMedian: true,
				TTFTMS: 2500, HasTTFT: true,
				CostPerRound: 0.04, HasCost: true,
				CommitsPerRound: 1.2, HasCommits: true,
				TokenKinds: datastats.TokenCounts{
					In: 30_000, Cache: 2_200_000, Write: 8_000, Out: 40_000, Measured: 4,
				},
			},
			{
				Token:  "agy/antigravity/claude-sonnet-4-6",
				Rounds: 2, Closed: 2, Reported: 2, DonePct: 100, HaltPct: 0,
				MedianMS: 9 * 60_000, HasMedian: true, Plan: true, Few: true,
				TokenKinds: datastats.TokenCounts{
					In: 10_000, Cache: 200_000, Write: 2_000, Out: 10_000, Measured: 1,
				},
			},
		},
		Spend: datastats.Spend{
			Days: []datastats.DayCost{
				{Day: "2026-09-16", USD: 0.40, Tokens: 900_000, ByProvider: map[string]float64{"cline-pass": 0.40}},
				{Day: "2026-09-17", USD: 0.20, Tokens: 400_000, ByProvider: map[string]float64{"cline-pass": 0.20}},
				{Day: "2026-09-18", USD: 0.00, ByProvider: map[string]float64{}},
				{Day: "2026-09-19", USD: 0.80, Tokens: 1_200_000, ByProvider: map[string]float64{"google": 0.80}},
			},
			ThisWeek: 1.40, LastWeek: 0.55,
		},
		Reliability: datastats.Reliability{
			Switches: 2, RoundsSwitched: 1, SwitchPct: 33, RateLimits: 3, SpawnFailures: 1,
			ByHour: []datastats.HourRow{{Provider: "google", Counts: counts}},
			Active: []availability.Gate{{
				Token: "gemini-3.8-flash-high", Kind: availability.RateLimited,
				Since: at(24), Until: at(19).AddDate(0, 1, 0),
			}},
		},
		Repos: []datastats.RepoRow{
			{
				GroupRow: datastats.GroupRow{Key: "https://github.com/fuad-daoud/relevo", Rounds: 5, Halted: 1, Landed: 1, CostUSD: 1.30, Tokens: 2_400_000, RoundsPerLand: 3.0},
				Features: []datastats.FeatureRow{
					{GroupRow: datastats.GroupRow{Key: "cockpit", Rounds: 3, CostUSD: 0.90, Tokens: 1_500_000, Landed: 1, RoundsPerLand: 2.0}},
				},
				NoFeature: datastats.FeatureRow{GroupRow: datastats.GroupRow{Key: "(none)", Rounds: 2, Tokens: 900_000}},
			},
			{
				GroupRow: datastats.GroupRow{Key: "(none)", Rounds: 1, CostUSD: 0.10, Tokens: 100_000},
			},
		},
		Outcomes: datastats.Outcomes{
			ByRound: map[string]int{
				db.OutcomeReported: 4, db.OutcomeHalted: 1, db.OutcomeOpen: 1,
			},
			ByReport: map[string]int{"done": 3, "halted": 1, "no outcome": 1},
		},
	}
}

// statsKey builds a rune key.
func statsKey(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// statsShell builds the shell over a temp DB with the stats view at window
// pushed and its first fetch drained. The top must be the stats view.
func statsShell(t *testing.T, width, height int, window string) Model {
	t.Helper()
	return statsShellOn(t, width, height, window, relevo.Runtime{})
}

// statsShellOn is statsShell over a caller-supplied runtime, so a test can
// carry configured candidates.
func statsShellOn(t *testing.T, width, height int, window string, rt relevo.Runtime) Model {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	rt.Store = store.New(t.TempDir())
	rt.DB = d
	m := newModel(context.Background(), mastermindSource{rt},
		Options{Interval: time.Second})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: view.Report{}})
	m = res.(Model)
	m = drain(t, m, execLine("stats "+window, m.env(), m.prefs))
	if got := m.top().Crumbs(); len(got) != 1 || got[0] != "stats" {
		t.Fatalf(":stats must replace the stack, top crumbs are %q", got)
	}
	return m
}

// feedStats delivers a stats fetch reply for window to the shell.
func feedStats(t *testing.T, m Model, window string, rep datastats.Report) Model {
	t.Helper()
	res, _ := m.Update(stats.StatsMsg{Window: window, Rep: rep})
	return res.(Model)
}

// pressKeys sends keys through the shell one at a time.
func pressKeys(t *testing.T, m Model, keys ...tea.KeyMsg) Model {
	t.Helper()
	for _, k := range keys {
		res, _ := m.Update(k)
		m = res.(Model)
	}
	return m
}

// statsCandidateSet loads a one-candidate set from JSON, the way the roles
// test builds its registry fixture.
func statsCandidateSet(t *testing.T, harness, provider, model string) *candidate.Set {
	t.Helper()
	body := fmt.Sprintf(`[{"harness":%q,"provider":%q,"model":%q}]`, harness, provider, model)
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write candidate set fixture: %v", err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}
	return set
}

// TestStatsEnterOpensFilteredRounds: enter pushes rounds filtered to the
// selected row, with the window's since term unless the window is all, and
// the (none) repo row notices.
func TestStatsEnterOpensFilteredRounds(t *testing.T) {
	rep := statsFixture()

	t.Run("candidates row", func(t *testing.T) {
		m := feedStats(t, statsShell(t, 160, 40, "30d"), "30d", rep)
		m = pressKeys(t, m, statsKey('2'))
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		rv, ok := m.top().(roundsView)
		if !ok {
			t.Fatalf("enter must push a rounds view, got %T", m.top())
		}
		if got, want := rv.dash.QueryText(), "candidate:"+rep.Scorecard[0].Token+" since:30d"; got != want {
			t.Errorf("QueryText = %q, want %q", got, want)
		}
	})

	t.Run("under all there is no since", func(t *testing.T) {
		m := feedStats(t, statsShell(t, 160, 40, "all"), "all", rep)
		m = pressKeys(t, m, statsKey('2'))
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		rv, ok := m.top().(roundsView)
		if !ok {
			t.Fatalf("enter must push a rounds view, got %T", m.top())
		}
		if got, want := rv.dash.QueryText(), "candidate:"+rep.Scorecard[0].Token; got != want {
			t.Errorf("QueryText = %q, want no since term", got)
		}
	})

	t.Run("repos row", func(t *testing.T) {
		m := feedStats(t, statsShell(t, 160, 40, "30d"), "30d", rep)
		m = pressKeys(t, m, statsKey('5'))
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
		m := feedStats(t, statsShell(t, 160, 40, "30d"), "30d", rep)
		m = pressKeys(t, m, statsKey('5'), statsKey('j'))
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

// TestStatsOverviewEnter: on the overview, enter opens the selected table
// row's rounds -- the top-tokens repo after `l`, the second candidate after
// `j`.
func TestStatsOverviewEnter(t *testing.T) {
	rep := statsFixture()

	t.Run("repos row", func(t *testing.T) {
		m := feedStats(t, statsShell(t, 160, 40, "30d"), "30d", rep)
		m = pressKeys(t, m, statsKey('l'))
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
		m := feedStats(t, statsShell(t, 160, 40, "30d"), "30d", rep)
		m = pressKeys(t, m, statsKey('j'))
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

// crumbsOf is the top view's breadcrumb, which names the view without
// exposing its type.
func crumbsOf(m Model) []string { return m.top().Crumbs() }

// TestStatsTabsKeys: tab cycles the five tabs and wraps, shift+tab goes
// back, a digit jumps to its tab, and enter on repos opens the filtered
// rounds. Each tab shows its own body: the candidates header, the tokens
// chips, the reliability tiles, the repos table.
func TestStatsTabsKeys(t *testing.T) {
	rep := statsFixture()

	t.Run("tab cycles five tabs and wraps", func(t *testing.T) {
		m := feedStats(t, statsShell(t, 100, 30, "30d"), "30d", rep)
		for i, want := range []string{"STATUS", "total   candidate   provider   kind", "SWITCHES", "BINDINGS", "ROUNDS"} {
			m = pressKeys(t, m, tea.KeyMsg{Type: tea.KeyTab})
			if body := stripANSI(m.View()); !strings.Contains(body, want) {
				t.Errorf("tab #%d: body missing %q", i+1, want)
			}
		}
		if got := crumbsOf(m); len(got) != 1 || got[0] != "stats" {
			t.Errorf("after five tabs the top must still be stats, crumbs are %q", got)
		}
	})

	t.Run("shift+tab goes back", func(t *testing.T) {
		m := feedStats(t, statsShell(t, 100, 30, "30d"), "30d", rep)
		m = pressKeys(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
		if body := stripANSI(m.View()); !strings.Contains(body, "BINDINGS") {
			t.Errorf("shift+tab from overview must reach repos, body missing BINDINGS")
		}
	})

	t.Run("digit jumps to its tab", func(t *testing.T) {
		m := feedStats(t, statsShell(t, 100, 30, "30d"), "30d", rep)
		m = pressKeys(t, m, statsKey('3'))
		if body := stripANSI(m.View()); !strings.Contains(body, "total   candidate   provider   kind") {
			t.Errorf("3 must jump to tokens, body missing the chips row")
		}
	})

	t.Run("j moves the overview cursor", func(t *testing.T) {
		m := feedStats(t, statsShell(t, 100, 30, "30d"), "30d", rep)
		m = pressKeys(t, m, statsKey('1'), statsKey('j'))
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		rv, ok := m.top().(roundsView)
		if !ok {
			t.Fatalf("enter must push a rounds view, got %T", m.top())
		}
		want := "candidate:agy/antigravity/claude-sonnet-4-6 since:30d"
		if got := rv.dash.QueryText(); got != want {
			t.Errorf("j on overview must move to the second candidate: QueryText = %q, want %q", got, want)
		}
	})

	t.Run("j moves the candidates cursor and k clamps", func(t *testing.T) {
		m := feedStats(t, statsShell(t, 100, 30, "30d"), "30d", rep)
		m = pressKeys(t, m, statsKey('2'), statsKey('j'))
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		rv, ok := m.top().(roundsView)
		if !ok {
			t.Fatalf("enter must push a rounds view, got %T", m.top())
		}
		want := "candidate:agy/antigravity/claude-sonnet-4-6 since:30d"
		if got := rv.dash.QueryText(); got != want {
			t.Errorf("j on candidates must move to the second row: QueryText = %q, want %q", got, want)
		}

		m = feedStats(t, statsShell(t, 100, 30, "30d"), "30d", rep)
		m = pressKeys(t, m, statsKey('2'), statsKey('k'))
		res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		rv, ok = m.top().(roundsView)
		if !ok {
			t.Fatalf("enter must push a rounds view, got %T", m.top())
		}
		want = "candidate:" + rep.Scorecard[0].Token + " since:30d"
		if got := rv.dash.QueryText(); got != want {
			t.Errorf("k at the top must clamp to the first row: QueryText = %q, want %q", got, want)
		}
	})

	t.Run("enter on repos opens rounds", func(t *testing.T) {
		m := feedStats(t, statsShell(t, 160, 40, "30d"), "30d", rep)
		m = pressKeys(t, m, statsKey('5'))
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
	})
}

// TestStatsRefreshThrottle: a tick within 30s does not refetch -- the
// context shows no refreshing state; one after does, and the returned
// command is the refetch.
func TestStatsRefreshThrottle(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	env := widget.Env{
		Ctx:    context.Background(),
		Src:    mastermindSource{relevo.Runtime{Store: store.New(t.TempDir()), DB: d}},
		Report: view.Report{}, Loaded: true,
		StatusAt: railNow, Now: railNow, Width: 160, Height: 40,
	}
	stub := func(widget.Env, string) (widget.View, tea.Cmd, error) {
		return nil, nil, errors.New("no rounds here")
	}
	v, _, err := stats.NewStatsView(env, "30d", stub)
	if err != nil {
		t.Fatalf("NewStatsView: %v", err)
	}
	next, _ := v.Update(stats.StatsMsg{Window: "30d", Rep: statsFixture()}, env)
	if _, right := next.Context(env); strings.Contains(right, "refreshing") {
		t.Fatal("a fed report must clear the fetching state")
	}

	soon := env
	soon.Now = railNow.Add(10 * time.Second)
	next, cmd := next.Update(widget.TickMsg(soon.Now), soon)
	if cmd != nil {
		t.Error("a tick within 30s must not refetch")
	}
	if _, right := next.Context(soon); strings.Contains(right, "refreshing") {
		t.Error("a tick within 30s must not mark the view fetching")
	}

	late := env
	late.Now = railNow.Add(31 * time.Second)
	next, cmd = next.Update(widget.TickMsg(late.Now), late)
	if cmd == nil {
		t.Fatal("a tick after 30s must refetch")
	}
	if _, right := next.Context(late); !strings.Contains(right, "refreshing") {
		t.Error("a refetch must mark the view fetching")
	}
}

// TestStatsNoDatabase: `:stats` against a source with a nil DB is refused
// with the notice and the stack is unchanged.
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

	stub := func(env widget.Env, q string) (widget.View, tea.Cmd, error) {
		return nil, nil, nil
	}
	if _, _, err := stats.NewStatsView(m.env(), "30d", stub); !errors.Is(err, relevo.ErrNoDatabase) {
		t.Errorf("NewStatsView err = %v, want ErrNoDatabase", err)
	}
}

// The tokens tab's three candidate tokens; the idle one carries no tokens.
const (
	statsTokensBig   = "h/p1/m1"
	statsTokensSmall = "h/p2/m2"
)

// statsTokensReport is the tokens tests' report: two days carrying tokens for
// two candidates and two providers, with the three kinds.
func statsTokensReport() datastats.Report {
	rep := statsFixture()
	rep.Spend.Days = []datastats.DayCost{
		{
			Day: "2026-09-16", Tokens: 1000,
			ByCandidate:      map[string]int64{statsTokensBig: 900, statsTokensSmall: 100},
			TokensByProvider: map[string]int64{"p1": 900, "p2": 100},
			Kinds:            datastats.TokenCounts{Cache: 800, In: 100, Out: 80, Write: 20, Measured: 1},
		},
		{
			Day: "2026-09-17", Tokens: 2000,
			ByCandidate:      map[string]int64{statsTokensBig: 1500, statsTokensSmall: 500},
			TokensByProvider: map[string]int64{"p1": 1500, "p2": 500},
			Kinds:            datastats.TokenCounts{Cache: 1600, In: 200, Out: 160, Write: 40, Measured: 1},
		},
	}
	rep.Totals.TokenKinds = datastats.TokenCounts{In: 300, Cache: 2400, Out: 240, Write: 60, Measured: 2}
	rep.Totals.Tokens = 3000
	return rep
}

// splitBody renders the shell's current body text.
func splitBody(m Model) string { return stripANSI(m.View()) }

// TestStatsTokensSplitCycle: a new view starts on candidate, `s` cycles
// 1→2→3→0→1 on the tokens tab, each split showing its own series, and `s`
// on the overview does nothing.
func TestStatsTokensSplitCycle(t *testing.T) {
	rep := statsTokensReport()

	m := feedStats(t, statsShell(t, 132, 40, "30d"), "30d", rep)
	m = pressKeys(t, m, statsKey('3'))
	if body := splitBody(m); !strings.Contains(body, statsTokensBig) {
		t.Fatalf("a new view must start on the candidate split, body missing %q", statsTokensBig)
	}

	// The provider split names providers, not candidates.
	m = pressKeys(t, m, statsKey('s'))
	body := splitBody(m)
	if !strings.Contains(body, "p1") || strings.Contains(body, statsTokensBig) || strings.Contains(body, statsTokensSmall) {
		t.Errorf("s #1 must reach the provider split, body names a candidate:\n%s", body)
	}

	// The kind split names the four kinds.
	m = pressKeys(t, m, statsKey('s'))
	if body := splitBody(m); !strings.Contains(body, "cache read") {
		t.Errorf("s #2 must reach the kind split, body missing the kind chart:\n%s", body)
	}

	// The total split draws the timeline, not strips.
	m = pressKeys(t, m, statsKey('s'))
	body = splitBody(m)
	if !strings.Contains(body, "TOKENS") || strings.Contains(body, statsTokensBig) || strings.Contains(body, "cache read") {
		t.Errorf("s #3 must reach the total split, want the timeline:\n%s", body)
	}

	// And back to candidate.
	m = pressKeys(t, m, statsKey('s'))
	if body := splitBody(m); !strings.Contains(body, statsTokensBig) {
		t.Errorf("s #4 must cycle back to the candidate split, body missing %q", statsTokensBig)
	}

	// s on the overview does nothing.
	m = pressKeys(t, m, statsKey('1'))
	before := splitBody(m)
	m = pressKeys(t, m, statsKey('s'))
	if got := splitBody(m); got != before {
		t.Errorf("s on the overview changed the body")
	}
}

// TestStatsCandidatesTabDetail: enter on an idle candidates row notices
// instead of pushing rounds.
func TestStatsCandidatesTabDetail(t *testing.T) {
	const haiku = "claude/anthropic/haiku"
	set := statsCandidateSet(t, "claude", "anthropic", "haiku")

	rep := statsFixture()
	m := feedStats(t, statsShellOn(t, 132, 40, "30d", relevo.Runtime{Candidates: set}), "30d", rep)
	// Past the two scorecard rows onto the idle one.
	m = pressKeys(t, m, statsKey('2'), statsKey('j'), statsKey('j'))
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	if _, ok := m.top().(roundsView); ok {
		t.Fatal("enter on an idle row must not push a rounds view")
	}
	if got := crumbsOf(m); len(got) != 1 || got[0] != "stats" {
		t.Errorf("enter on an idle row must stay on stats, crumbs are %q", got)
	}
	if want := set.NameOf(haiku) + " has no rounds in this window"; !strings.Contains(m.notice, want) {
		t.Errorf("notice = %q, want %q", m.notice, want)
	}
}

// statsTicketFeature and statsTicketNoFeature are the tickets the child
// cases hang under the fixture's feature and its (no feature) row. They live
// in constants so no query line holds a URL and a ticket number together.
const (
	statsTicketFeature   = "o/r#607"
	statsTicketNoFeature = "o/r#608"
)

// TestStatsReposChildEnter: enter on a feature child opens rounds scoped to
// its repo and its label; a ticket under a feature adds the feature term; a
// ticket under (no feature) scopes to the repo and the ticket; the
// (no feature) row keeps its notice; and a child of the (none) repo notices
// because no query term means "no repo".
func TestStatsReposChildEnter(t *testing.T) {
	cases := []struct {
		name         string
		tickets      bool
		labelledNone bool
		keys         []tea.KeyMsg
		want         string
		wantNotice   string
	}{
		{name: "feature child", keys: []tea.KeyMsg{statsKey('5'), tea.KeyMsg{Type: tea.KeySpace}, statsKey('j')},
			want: `repo:"https://github.com/fuad-daoud/relevo" feature:"cockpit" since:30d`},
		{name: "ticket under a feature", tickets: true, keys: []tea.KeyMsg{statsKey('5'), tea.KeyMsg{Type: tea.KeySpace}, statsKey('j'), statsKey('j')},
			want: `repo:"https://github.com/fuad-daoud/relevo" feature:"cockpit" ticket:"` + statsTicketFeature + `" since:30d`},
		{name: "ticket under (no feature)", tickets: true, keys: []tea.KeyMsg{statsKey('5'), tea.KeyMsg{Type: tea.KeySpace}, statsKey('j'), statsKey('j'), statsKey('j'), statsKey('j')},
			want: `repo:"https://github.com/fuad-daoud/relevo" ticket:"` + statsTicketNoFeature + `" since:30d`},
		{name: "no feature child", keys: []tea.KeyMsg{statsKey('5'), tea.KeyMsg{Type: tea.KeySpace}, statsKey('j'), statsKey('j')},
			wantNotice: "rounds with no feature cannot be filtered"},
		{name: "child of the (none) repo", labelledNone: true, keys: []tea.KeyMsg{statsKey('5'), statsKey('j'), tea.KeyMsg{Type: tea.KeySpace}, statsKey('j')},
			wantNotice: "rounds with no repo cannot be filtered"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rep := statsFixture()
			if c.tickets {
				rep.Repos[0].Features[0].Tickets = []datastats.GroupRow{{Key: statsTicketFeature, Rounds: 2, Tokens: 700_000}}
				rep.Repos[0].NoFeature.Tickets = []datastats.GroupRow{{Key: statsTicketNoFeature, Rounds: 1, Tokens: 400_000}}
			}
			if c.labelledNone {
				// A label on the (none) repo, so it has a child to select.
				rep.Repos[1].Features = []datastats.FeatureRow{{GroupRow: datastats.GroupRow{Key: "loose", Rounds: 1, Tokens: 50_000}}}
				rep.Repos[1].NoFeature = datastats.FeatureRow{GroupRow: datastats.GroupRow{Key: "(none)", Rounds: 1, Tokens: 50_000}}
			}
			m := feedStats(t, statsShell(t, 160, 40, "30d"), "30d", rep)
			m = pressKeys(t, m, c.keys...)
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

// TestStatsReposNoFeatureRow: enter on the (no feature) row notices instead
// of pushing rounds.
func TestStatsReposNoFeatureRow(t *testing.T) {
	m := feedStats(t, statsShell(t, 160, 40, "30d"), "30d", statsFixture())
	// Expand the fixture's repo, then onto its feature child and its
	// (no feature) row.
	m = pressKeys(t, m, statsKey('5'), tea.KeyMsg{Type: tea.KeySpace}, statsKey('j'), statsKey('j'))
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	if _, ok := m.top().(roundsView); ok {
		t.Fatal("the (no feature) row must not push a rounds view")
	}
	if !strings.Contains(m.notice, "rounds with no feature cannot be filtered") {
		t.Errorf("notice = %q", m.notice)
	}
}

// TestStatsReposExpansionSurvivesRefresh: expansion is view state keyed by
// repo key, so a fresh fetch reply leaves it untouched and the children stay
// shown.
func TestStatsReposExpansionSurvivesRefresh(t *testing.T) {
	rep := statsFixture()
	m := feedStats(t, statsShell(t, 160, 40, "30d"), "30d", rep)
	m = pressKeys(t, m, statsKey('5'), tea.KeyMsg{Type: tea.KeyRight})

	m = feedStats(t, m, "30d", statsFixture())
	if body := stripANSI(m.View()); !strings.Contains(body, "cockpit") {
		t.Errorf("the expanded repo's child is gone after a refresh:\n%s", body)
	}
}

// statsScratchOne and statsScratchTwo are the two scratch repos the scratch
// tests use: absolute paths, which is how a repo with no origin_url keys
// itself.
var (
	statsScratchOne = "/tmp/relevo-scratch/alpha"
	statsScratchTwo = "/tmp/relevo-scratch/beta/.git"
)

// statsScratchReport builds the scratch fixture through Build over
// absolute-path Repo keys, so the fold is exercised end to end. It carries
// one remote repo and one repo-less round beside the scratch pair, so the
// fold never sits alone in the table.
func statsScratchReport() datastats.Report {
	s := func(v string) *string { return &v }
	i64 := func(v int64) *int64 { return &v }
	at := func(d int) time.Time { return time.Date(2026, 9, d, 12, 0, 0, 0, time.UTC) }
	row := func(binding string, repo, feature *string, day int, in, out int64) db.RoundRow {
		return db.RoundRow{
			BindingID: binding, BindingName: binding, Repo: repo, Feature: feature,
			StartedAt: at(day), Outcome: db.OutcomeReported,
			InTokens: i64(in), OutTokens: i64(out),
		}
	}
	one, two := s(statsScratchOne), s(statsScratchTwo)
	remote := s("https://github.com/fuad-daoud/relevo")

	rows := []db.RoundRow{
		// alpha leads, so the children sort tokens desc puts it first.
		row("b1", one, s("cockpit"), 20, 900_000, 100_000),
		row("b1", one, nil, 21, 300_000, 30_000),
		// beta: two rounds sharing one binding, so the fold's Bindings
		// counts it once while each child counts its own.
		row("b2", two, nil, 22, 200_000, 20_000),
		row("b2", two, nil, 23, 150_000, 15_000),
		// A remote repo and a repo-less round keep their own rows.
		row("b3", remote, nil, 24, 50_000, 5_000),
		row("b4", nil, nil, 24, 10_000, 1_000),
	}
	return datastats.Build(datastats.Inputs{
		Rows: rows, Until: stScratchNow, Since: stScratchNow.AddDate(0, 0, -29),
		Loc: time.UTC,
	})
}

// stScratchNow is the scratch fixture's "now".
var stScratchNow = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)

// TestStatsScratchChildIsALeaf: on a scratch child, space collapses the
// fold and the child names leave the body -- a child has nothing to expand.
func TestStatsScratchChildIsALeaf(t *testing.T) {
	m := feedStats(t, statsShell(t, 160, 40, "30d"), "30d", statsScratchReport())
	// Onto the repos tab, expand the fold, onto its first child.
	m = pressKeys(t, m, statsKey('5'), tea.KeyMsg{Type: tea.KeySpace}, statsKey('j'))
	m = pressKeys(t, m, tea.KeyMsg{Type: tea.KeySpace})
	body := stripANSI(m.View())
	for _, path := range []string{"alpha", "beta"} {
		if strings.Contains(body, path) {
			t.Errorf("collapsed fold still shows %s:\n%s", path, body)
		}
	}
	if !strings.Contains(body, "scratch") {
		t.Errorf("the fold row is gone after collapsing:\n%s", body)
	}
}

// TestStatsScratchRowEnterNotices: the fold key notices on both tables,
// while a child falls to the default branch and filters on its own key.
func TestStatsScratchRowEnterNotices(t *testing.T) {
	t.Run("fold on the repos tab", func(t *testing.T) {
		m := feedStats(t, statsShell(t, 132, 40, "30d"), "30d", statsScratchReport())
		m = pressKeys(t, m, statsKey('5'))
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		if _, ok := m.top().(roundsView); ok {
			t.Fatal("the fold row must not push a rounds view")
		}
		if !strings.Contains(m.notice, "scratch repos cannot be filtered as one") {
			t.Errorf("notice = %q", m.notice)
		}
	})

	t.Run("fold on the overview", func(t *testing.T) {
		m := feedStats(t, statsShell(t, 132, 40, "30d"), "30d", statsScratchReport())
		m = pressKeys(t, m, statsKey('l'))
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		if _, ok := m.top().(roundsView); ok {
			t.Fatal("the overview's fold row must not push a rounds view")
		}
		if !strings.Contains(m.notice, "scratch repos cannot be filtered as one") {
			t.Errorf("notice = %q", m.notice)
		}
	})

	t.Run("child filters its own path", func(t *testing.T) {
		m := feedStats(t, statsShell(t, 132, 40, "30d"), "30d", statsScratchReport())
		// Onto the repos tab, expand the fold, onto its first child.
		m = pressKeys(t, m, statsKey('5'), tea.KeyMsg{Type: tea.KeySpace}, statsKey('j'))
		res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = drain(t, res.(Model), cmd)
		rv, ok := m.top().(roundsView)
		if !ok {
			t.Fatalf("enter on a child must push a rounds view, got %T", m.top())
		}
		want := `repo:"` + statsScratchOne + `" since:30d`
		if got := rv.dash.QueryText(); got != want {
			t.Errorf("QueryText = %q, want %q", got, want)
		}
	})
}

// TestStatsEnterOpenerError: when the rounds opener fails, enter notices
// instead of pushing. The failing opener is the mutation check: if the
// error were swallowed, a rounds view would push and no notice would show.
func TestStatsEnterOpenerError(t *testing.T) {
	m := statsShell(t, 132, 40, "30d")
	failing := func(widget.Env, string) (widget.View, tea.Cmd, error) {
		return nil, nil, errors.New("boom")
	}
	v, init, err := stats.NewStatsView(m.env(), "30d", failing)
	if err != nil {
		t.Fatalf("NewStatsView: %v", err)
	}
	m = drain(t, m, init)
	m.stack[len(m.stack)-1] = v
	m = feedStats(t, m, "30d", statsFixture())

	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	if _, ok := m.top().(roundsView); ok {
		t.Fatal("a failing opener must not push a rounds view")
	}
	if m.top() == nil {
		t.Fatal("a failing opener must not push a nil view")
	}
	if got := crumbsOf(m); len(got) != 1 || got[0] != "stats" {
		t.Errorf("a failing opener must stay on stats, crumbs are %q", got)
	}
	if !strings.Contains(m.notice, "no database: boom") {
		t.Errorf("notice = %q, want the opener error", m.notice)
	}
}
