package ui

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/stats"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/ui/dash"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

var updateGolden = flag.Bool("update", false, "update golden files")

const goldenVersion = "v0.13.0-28-gb66c6fc"

// goldenModel is a shell loaded with a full view.Report.
func goldenModel(t *testing.T, width, height int, rep view.Report) Model {
	t.Helper()
	st := store.New(t.TempDir())
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st}}, Options{Interval: time.Second, Version: goldenVersion})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: rep})
	return res.(Model)
}

// allStatesRows covers every display state and the row facts the fleet and
// pane can show today.
func allStatesRows() []view.BindingStatus {
	return []view.BindingStatus{
		{
			Name: "atlas", Round: 4, Display: "ACTIVE",
			MasterMindName: "architect-1", MasterMindKind: "claude", MasterMindRoute: "channel", MasterMindRouteLive: true,
			BuilderKind: "opencode", BuilderStatus: "working", Branch: "relevo/atlas",
			LiveUsage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash", DurationMS: 4 * 60_000,
				Tokens: usage.Tokens{In: 1_800, CacheRead: 91_000, CacheWrite: 3_100, Out: 8_200},
				Cost:   usage.Cost{USD: 0.04, Basis: usage.Measured}, Samples: 3},
			Spend: &usage.Spend{Rounds: 1, Measured: 0.16, Tokens: usage.Tokens{In: 3_500_000}},
		},
		{
			Name: "webshop", Round: 4, Display: "NEEDS YOU",
			MasterMindName: "architect-1", MasterMindKind: "claude", MasterMindRoute: "pull",
			BuilderKind: "agy", BuilderStatus: "blocked", Branch: "relevo/webshop",
			Dirty: true, Consults: 2,
			LastUsage: &usage.Usage{Harness: "claude", Provider: "anthropic", Model: "claude-sonnet-5", DurationMS: 9 * 60_000,
				Tokens: usage.Tokens{In: 100, CacheRead: 15_000_000, CacheWrite: 50_000, Out: 55_000}, Cost: usage.Cost{USD: 4.71, Basis: usage.Measured}, Samples: 1},
			Spend:   &usage.Spend{Rounds: 3, Consults: 2, Measured: 9.40, Unknown: 1},
			Waiting: &view.Waiting{Cause: "blocked", Since: railNow.Add(-2 * time.Minute), Line: "which branch should r4 target?", Hint: "relevo status --name webshop"},
			Last:    &view.LastEvent{TS: railNow.Add(-2 * time.Minute), Round: 4, Kind: store.KindQuestion},
		},
		{
			Name: "ledger", Round: 3, Display: "PAUSED",
			MasterMindName: "architect-1", MasterMindKind: "claude", MasterMindRoute: "pull",
			BuilderKind: "agy", BuilderStatus: "idle", Branch: "relevo/ledger",
			Spend: &usage.Spend{Rounds: 3, Measured: 1.23, Estimated: 0.40, Unknown: 1},
		},
		{
			Name: "api", Round: 2, Display: "ACTIVE",
			MasterMindName: "architect-1", MasterMindKind: "claude", MasterMindRoute: "channel", MasterMindRouteLive: true,
			BuilderKind: "agy", BuilderStatus: "working", Branch: "relevo/api",
		},
		{
			Name: "worker", Round: 2, Display: "ACTIVE",
			MasterMindName: "architect-1", MasterMindKind: "claude", MasterMindRoute: "channel", MasterMindRouteLive: true,
			BuilderKind: "opencode", BuilderStatus: "working", BuilderCandidate: "opencode-1", Branch: "relevo/worker",
			Headless: &view.HeadlessInfo{PID: 48211, StartedAt: railNow.Add(-21 * time.Minute)},
		},
		{
			Name: "docs", Round: 1, Display: "DONE",
			MasterMindName: "architect-1", MasterMindKind: "claude", MasterMindRoute: "pull",
			BuilderKind: "agy", BuilderStatus: "unknown", CWD: "/home/x/docs",
			Last: &view.LastEvent{TS: railNow.Add(-3 * time.Hour)},
		},
	}
}

// reportReadyRows is the fleet fixture for the report-ready golden: every
// state row, plus a binding whose human mastermind is owed a report (§4.5). Its
// display word is ACTIVE, so the header's count can only include it through
// the report-ready rule.
func reportReadyRows() []view.BindingStatus {
	rows := append([]view.BindingStatus(nil), allStatesRows()...)
	return append(rows, view.BindingStatus{
		Name: "inbox", Round: 3, Display: "ACTIVE",
		MasterMindID: "pl_aaaaaaaabbbb", MasterMindName: "you", MasterMindKind: "human", MasterMindRoute: "pull",
		BuilderKind: "opencode", BuilderStatus: "exited", Branch: "relevo/inbox",
		Last:    &view.LastEvent{TS: railNow.Add(-3 * time.Minute), Round: 3, Kind: store.KindReport},
		Pending: &view.PendingInfo{Round: 3, Kind: store.KindReport},
	})
}

// histRows is the archived fixture for the round-archived golden.
func histRows() []relevo.HistoryBinding {
	return []relevo.HistoryBinding{
		{
			Name: "oldapi", ID: "h1", Rounds: 3, Feature: "auth",
			LastActivity: railNow.Add(-49 * 24 * time.Hour),
			Archived:     true, ArchivedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		},
	}
}

func gatedGates() []availability.Gate {
	return []availability.Gate{
		{Token: "codex", Kind: availability.RateLimited, Since: railNow, Until: railNow.Add(88 * time.Minute)},
	}
}

// realFleetReport builds the realistic fleet report specified in §8 / §2.3.
func realFleetReport() view.Report {
	// 27 DONE rows: 3 today (newest done-01 at now-38m), others on previous days.
	doneRows := make([]view.BindingStatus, 27)
	for i := 1; i <= 27; i++ {
		name := fmt.Sprintf("done-%02d", i)
		var ts time.Time
		switch i {
		case 1:
			ts = railNow.Add(-38 * time.Minute)
		case 2:
			ts = railNow.Add(-2 * time.Hour)
		case 3:
			ts = railNow.Add(-5 * time.Hour)
		default:
			ts = railNow.Add(-time.Duration(i-2) * 24 * time.Hour)
		}
		doneRows[i-1] = view.BindingStatus{
			Name:    name,
			Round:   1,
			Display: "DONE",
			Last:    &view.LastEvent{TS: ts},
		}
	}

	bindings := []view.BindingStatus{
		{
			Name:           "fix-433",
			Round:          2,
			Display:        "NEEDS YOU",
			BuilderKind:    "agy",
			BuilderName:    "deepseek-v4.1-flash",
			MasterMindName: "architect-3",
			Branch:         "relevo/fix-433",
			LastClose:      &view.CloseInfo{Commits: 2},
			Spend:          &usage.Spend{Measured: 0.03},
			Waiting: &view.Waiting{
				Cause: "blocked",
				Line:  "“Should the dedupe also cover archived bindings, or only live ones?”",
				Since: railNow.Add(-3 * time.Minute),
			},
			Last: &view.LastEvent{
				TS:    railNow.Add(-3 * time.Minute),
				Kind:  store.KindQuestion,
				Round: 2,
			},
		},
		{
			Name:           "spool-db",
			Round:          1,
			Display:        "ACTIVE",
			BuilderStatus:  "working",
			QuietFor:       "17s",
			RoundStart:     railNow.Add(-5 * time.Minute),
			BuilderName:    "gemini-3.8-flash-high",
			MasterMindName: "architect-2",
			Spend:          &usage.Spend{Plan: 1},
		},
		{
			Name:           "tok-seg",
			Round:          1,
			Display:        "ACTIVE",
			BuilderStatus:  "working",
			QuietFor:       "16s",
			RoundStart:     railNow.Add(-6 * time.Minute),
			BuilderName:    "gemini-3.8-flash-high",
			MasterMindName: "architect-5",
			Spend:          &usage.Spend{Plan: 1},
		},
		{
			Name:          "oc-tui-a",
			Round:         6,
			Display:       "ACTIVE",
			BuilderStatus: "idle",
			LastPayload: &view.LastEvent{
				Kind: store.KindReport,
				TS:   railNow.Add(-14 * time.Minute),
			},
			BuilderName:    "deepseek-v4.1-flash",
			MasterMindName: "architect-3",
			Spend:          &usage.Spend{Measured: 0.20},
			Unread:         true,
		},
		{
			Name:          "rl-tail",
			Round:         2,
			Display:       "ACTIVE",
			BuilderStatus: "idle",
			LastPayload: &view.LastEvent{
				Kind: store.KindReport,
				TS:   railNow.Add(-31 * time.Minute),
			},
			BuilderName:    "gemini-3.8-flash-high",
			MasterMindName: "architect-5",
			Spend:          &usage.Spend{Plan: 1},
		},
		{
			Name:          "oc-tui-probe",
			Round:         4,
			Display:       "ACTIVE",
			BuilderStatus: "idle",
			LastPayload: &view.LastEvent{
				Kind: store.KindReport,
				TS:   railNow.Add(-48 * time.Minute),
			},
			BuilderName:    "gemini-3.8-flash-high",
			MasterMindName: "architect-3",
			Spend:          &usage.Spend{Plan: 1},
		},
		{
			Name:          "serve-status-json",
			Round:         2,
			Display:       "ACTIVE",
			BuilderStatus: "idle",
			LastPayload: &view.LastEvent{
				Kind: store.KindReport,
				TS:   railNow.Add(-1 * time.Hour),
			},
			BuilderName:    "deepseek-v4.1-flash",
			MasterMindName: "architect-13",
			Spend:          &usage.Spend{Measured: 0.03},
		},
	}
	bindings = append(bindings, doneRows...)

	return view.Report{
		Bindings: bindings,
		Gated: []availability.Gate{
			{
				Token: "agy/antigravity/claude-sonnet-4-6",
				Kind:  availability.RateLimited,
				Since: railNow,
				Until: railNow.Add(42 * time.Hour),
			},
			{
				Token: "codex/openai/gpt-5.6-terra:high",
				Kind:  availability.RateLimited,
				Since: railNow,
				Until: railNow.Add(25 * 24 * time.Hour),
			},
		},
	}
}

// dashRows is the dashboard golden's fixed grid.
func dashRows() []db.RoundRow {
	s := func(v string) *string { return &v }
	i := func(v int) *int { return &v }
	i64 := func(v int64) *int64 { return &v }
	f := func(v float64) *float64 { return &v }
	return []db.RoundRow{
		{
			BindingID: "b1", BindingName: "persist",
			Number: 5, StartedAt: railNow.Add(-2 * time.Hour), Outcome: db.OutcomeReported,
			Candidate: s("claude/anthropic/sonnet"), Commits: i(1), Tree: s("clean"),
			GateResult: s("pass"), InTokens: i64(1_000_000), OutTokens: i64(200_000),
			CostUSD: f(0.42), CostBasis: s("measured"), DurationMS: i64(27 * 60_000),
		},
		{
			BindingID: "b1", BindingName: "persist",
			Number: 4, StartedAt: railNow.Add(-26 * time.Hour), Outcome: db.OutcomeHalted,
			Candidate: s("claude/anthropic/sonnet"), Commits: i(0), Tree: s("dirty"),
			GateResult: s("fail"), InTokens: i64(400_000), CacheTokens: i64(100_000),
			CostUSD: f(1.10), CostBasis: s("measured"), DurationMS: i64(12 * 60_000),
		},
		{
			BindingID: "b2", BindingName: "api",
			Number: 2, StartedAt: railNow.Add(-50 * time.Hour), Outcome: db.OutcomeReported,
			Candidate: s("agy/antigravity/claude-sonnet-4-6"), Commits: i(3), Tree: s("clean"),
			GateResult: s("pass"), InTokens: i64(2_000_000),
			CostUSD: f(9.10), CostBasis: s("unknown"),
		},
	}
}

// goldenRoundModel is the fleet with the cursor's row opened, then its
// terminal tab fed so the golden shows content.
func goldenRoundModel(t *testing.T, width, height int) Model {
	t.Helper()
	terminalBody := "$ go test ./...\nok  \tgithub.com/fuad-daoud/relevo/internal/ui\t1.2s\n"
	m := goldenModel(t, width, height, view.Report{Bindings: allStatesRows(), Gated: gatedGates()})
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	m = drain(t, m, cmd)
	rv, ok := m.top().(roundView)
	if !ok {
		t.Fatalf("enter did not push a round view: %T", m.top())
	}
	name, round := rv.pane.detail.name, rv.pane.detail.round
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	m = res.(Model)
	res, _ = m.Update(tabMsg{name: name, round: round, t: tabTerminal, content: tabContent{loaded: true, body: terminalBody, at: railNow}})
	return res.(Model)
}

// goldenArchivedRoundModel pushes an archived round view with its plan tab
// fed.
func goldenArchivedRoundModel(t *testing.T, width, height int) Model {
	t.Helper()
	st := store.New(t.TempDir())
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st}}, Options{Interval: time.Second, Version: goldenVersion})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusLoaded = true
	h := histRows()[0]
	v, _ := newHistRoundView(m.env(), h, 0)
	m.stack = append(m.stack, v)
	res, _ = m.Update(tabMsg{name: h.Name, round: 3, t: tabPrompt, content: tabContent{loaded: true, round: 3, body: "# Round 3 plan\n\nDo the thing.\n"}})
	return res.(Model)
}

// roundReaderFiles is the round-reader-artifacts goldens' artifact directory:
// summary.md and findings.md, sized so the card reads the board's
// "2 artifacts · 12.4k".
func roundReaderFiles() map[string]string {
	summary := "# Review of round 1\n\n" +
		"The diff is **small** and the tests pass: `go test ./...` is green.\n" +
		"Nothing in the scratch worktree is worth flagging.\n\n" +
		"## Notes\n\n" +
		"- the worktree is thrown away at close\n" +
		"- the repository is left as it was\n\n"
	findings := "## findings\n\n" +
		"- nothing to flag\n" +
		"- the artifact directory survives the seal\n\n"
	return map[string]string{
		"summary.md":  padBytes(summary, 1126),
		"findings.md": padBytes(strings.Repeat(findings, 60), 11300),
	}
}

// padBytes pads s with spaces to exactly n bytes, or cuts it there.
func padBytes(s string, n int) string {
	if len(s) >= n {
		return s[:n]
	}
	return s + strings.Repeat(" ", n-len(s))
}

// roundReaderArtifactsModel pushes the round view for a closed reader round
// with its artifacts tab open and the cursor on the reviewer's output file
// (round 5b).
func roundReaderArtifactsModel(t *testing.T, width, height int) Model {
	t.Helper()
	const (
		name  = "review-568"
		actor = "reviewer"
	)
	st := store.New(t.TempDir())
	seedReaderArtifacts(t, st, name, actor, 1, roundReaderFiles(), railNow.Add(-2*time.Minute))

	m := goldenActionModelWithStore(t, width, height, &fakeActions{},
		view.Report{Bindings: []view.BindingStatus{readerRoundRow(name, actor)}}, st)
	m = pointer(t, m, name)
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	// The reader's second tab is its artifacts tab.
	res, cmd = m.Update(key('2'))
	return drain(t, res.(Model), cmd)
}

// seedPlanFixture writes name's round plan file and the plan log entry that
// recorded it, so a fixture's plan tab reads its body and its sent time from
// the store rather than from an injected tabMsg.
func seedPlanFixture(t *testing.T, st *store.Store, name string, round int, ts time.Time, body string) {
	t.Helper()
	b := newTestBinding(name)
	b.Round = round
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	p := st.PromptPath(name, round)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog(name, store.LogEntry{
		TS: ts, Round: round, Direction: store.DirToBuilder, Kind: store.KindPrompt, Path: p,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
}

// realRoundModel pushes the round view for spool-db with realistic data (§8).
func realRoundModel(t *testing.T, width, height int) Model {
	t.Helper()
	rep := realFleetReport()
	for i := range rep.Bindings {
		if rep.Bindings[i].Name == "spool-db" {
			rep.Bindings[i].Display = "ACTIVE"
			rep.Bindings[i].BuilderStatus = "working"
			rep.Bindings[i].Round = 1
			rep.Bindings[i].PlanRound = 1
			rep.Bindings[i].Spend = nil
			rep.Bindings[i].BuilderName = "gemini-3.8-flash-high"
			rep.Bindings[i].MasterMindName = "architect-2"
			rep.Bindings[i].Branch = "relevo/spool-db"
			rep.Bindings[i].Headless = &view.HeadlessInfo{
				PID:       1401366,
				StartedAt: railNow.Add(-5 * time.Minute),
			}
			rep.Bindings[i].RoundStart = railNow.Add(-5 * time.Minute)
			rep.Bindings[i].QuietFor = "17s"
			rep.Bindings[i].LiveUsage = &usage.Usage{
				Model:      "gemini-3.8-flash-high",
				DurationMS: 5 * 60_000,
				Samples:    1,
				Tokens:     usage.Tokens{In: 718_000, CacheRead: 4_200_000, Out: 64_000},
				Cost:       usage.Cost{Basis: usage.Unknown},
				Note:       "no price",
			}
			break
		}
	}
	planBody := "# Round diff and consult findings go straight into round_file\n\nDate: 2026-09-24. Base: origin/main `b66c6fcc` (#441).\nThere is **one round** in this plan. builder.log, NNN-<id>-ask.md and NNN-plan.md\nare out of scope. Do not touch them.\n\n## Scope\n\n- builder.log is out of scope.\n- Do not touch them.\n\n```go\nfunc main() {}\n```\n"
	st := store.New(t.TempDir())
	seedPlanFixture(t, st, "spool-db", 1, railNow.Add(-5*time.Minute), planBody)

	m := goldenActionModelWithStore(t, width, height, &fakeActions{}, rep, st)
	m = pointer(t, m, "spool-db")
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return drain(t, res.(Model), cmd)
}

// realRoundNeedsYouModel pushes the round view for fix-433 in NEEDS YOU state (§8).
func realRoundNeedsYouModel(t *testing.T, width, height int) Model {
	t.Helper()
	rep := realFleetReport()
	for i := range rep.Bindings {
		if rep.Bindings[i].Name == "fix-433" {
			rep.Bindings[i].PlanRound = 2
			break
		}
	}
	planBody := "# Fix 433 Plan\n\nDedupe bindings across stores.\n"
	st := store.New(t.TempDir())
	seedPlanFixture(t, st, "fix-433", 2, railNow.Add(-3*time.Minute), planBody)

	m := goldenActionModelWithStore(t, width, height, &fakeActions{}, rep, st)
	m = pointer(t, m, "fix-433")
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return drain(t, res.(Model), cmd)
}

// goldenRoundsModel hosts the dashboard with its rows fed, reached through
// the shell's start command so the breadcrumb reads relevo › rounds, as the
// real `:rounds` does (A5).
func goldenRoundsModel(t *testing.T, width, height int) Model {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	st := store.New(t.TempDir())
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st, DB: d}},
		Options{Interval: time.Second, Start: "rounds", Version: goldenVersion})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, cmd := m.Update(statusMsg{report: view.Report{}})
	m = drain(t, res.(Model), cmd)
	if _, ok := m.top().(roundsView); !ok {
		t.Fatalf("Start=rounds must replace the stack, top is %T", m.top())
	}
	res, _ = m.Update(dash.RowsMsg{Rows: dashRows(), At: railNow})
	return res.(Model)
}

func logFixtureEvents() []db.EventLogRow {
	r1, r2, r17, r5 := "r1", "r2", "r17", "r5"
	n1, n2, n17, n5 := 1, 2, 17, 5
	t2, d2 := int64(154000), int64(45000)
	t3, d3 := int64(138000), int64(65000)
	ty1, dy1 := int64(1200000), int64(27*60000)

	yesterday := railNow.AddDate(0, 0, -1)
	at := func(hour, min int) time.Time {
		return time.Date(railNow.Year(), railNow.Month(), railNow.Day(), hour, min, 0, 0, railNow.Location())
	}
	atY := func(hour, min int) time.Time {
		return time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), hour, min, 0, 0, yesterday.Location())
	}

	s := func(str string) *string { return &str }

	return []db.EventLogRow{
		// Today
		{TS: at(13, 45).Add(time.Second), Seq: 2, Kind: "pick", Note: s("picked gemini-3.8-flash-high for builder: order #1"), BindingName: "oc-live", RoundID: &r1, Round: &n1},
		{TS: at(13, 45), Seq: 1, Kind: "plan", EntryJSON: `{"tier":"yolo"}`, BindingName: "oc-live", RoundID: &r1, Round: &n1},

		{TS: at(13, 30).Add(time.Second), Seq: 2, Kind: "diff", Note: s("1 file, +1 -0; 1 commit, clean"), BindingName: "haiku", RoundID: &r2, Round: &n2},
		{TS: at(13, 30), Seq: 1, Kind: "report", EntryJSON: `{"outcome":"done"}`, BindingName: "haiku", RoundID: &r2, Round: &n2, Tokens: &t2, DurationMS: &d2},

		{TS: at(13, 10).Add(time.Second), Seq: 2, Kind: "diff", Note: s("1 file, +1 -0; no commits, dirty"), BindingName: "question", RoundID: &r1, Round: &n1},
		{TS: at(13, 10), Seq: 1, Kind: "report", EntryJSON: `{"outcome":"halted","halted_at":"step 2"}`, BindingName: "question", RoundID: &r1, Round: &n1, Tokens: &t3, DurationMS: &d3},

		{TS: at(12, 40).Add(time.Second), Seq: 2, Kind: "drift", Note: s("36 files, +3187, -45"), BindingName: "ck-d2-stats", RoundID: &r17, Round: &n17},
		{TS: at(12, 40), Seq: 1, Kind: "plan", EntryJSON: `{"tier":"yolo"}`, BindingName: "ck-d2-stats", RoundID: &r17, Round: &n17},

		{TS: at(12, 15), Seq: 1, Kind: "switch", Note: s("switched builder (exited (code 0) without a report): picked glm-5.3-flash for builder: order #6"), BindingName: "oc-496", RoundID: &r2, Round: &n2},

		{TS: at(12, 14), Seq: 1, Kind: "exit", Note: s("without a report (code 0)"), BindingName: "oc-496", RoundID: &r2, Round: &n2},

		// Yesterday
		{TS: atY(16, 30).Add(time.Second), Seq: 2, Kind: "diff", Note: s("4 files, +80 -12; 2 commits, clean"), BindingName: "persist", RoundID: &r5, Round: &n5},
		{TS: atY(16, 30), Seq: 1, Kind: "report", EntryJSON: `{"outcome":"done"}`, BindingName: "persist", RoundID: &r5, Round: &n5, Tokens: &ty1, DurationMS: &dy1},

		{TS: atY(14, 45), Seq: 1, Kind: "plan", BindingName: "persist", RoundID: &r5, Round: &n5},
	}
}

func logFixtureHist() availability.History {
	at := func(hour, min int) time.Time {
		return time.Date(railNow.Year(), railNow.Month(), railNow.Day(), hour, min, 0, 0, railNow.Location())
	}
	return availability.History{
		Events: []availability.Event{
			{
				At:       at(11, 50),
				Kind:     availability.RateLimited,
				Provider: "cline-pass",
				Note:     "weekly Clinepass limit reached, resets in 1d 4h",
			},
		},
	}
}

func logFixtureRevs() []db.RevisionRow {
	yesterday := railNow.AddDate(0, 0, -1)
	atY := func(hour, min int) time.Time {
		return time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), hour, min, 0, 0, yesterday.Location())
	}
	return []db.RevisionRow{
		{
			Rev:     12,
			At:      atY(15, 0),
			Message: "set candidates",
		},
	}
}

// goldenLogModel hosts the persistent event log view, reached through
// the shell's start command so the breadcrumb reads relevo › log.
func goldenLogModel(t *testing.T, width, height int) Model {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	st := store.New(t.TempDir())
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st, DB: d}},
		Options{Interval: time.Second, Start: "log", Version: goldenVersion})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, cmd := m.Update(statusMsg{report: view.Report{}})
	m = drain(t, res.(Model), cmd)
	if _, ok := m.top().(logView); !ok {
		t.Fatalf("Start=log must replace the stack, top is %T", m.top())
	}
	m.actionLog = []actionEntry{
		{
			At:   time.Date(railNow.Year(), railNow.Month(), railNow.Day(), 11, 20, 0, 0, railNow.Location()),
			Verb: "gate",
			Text: "gated google until 22:00",
		},
	}
	res, _ = m.Update(eventLogMsg{
		events: logFixtureEvents(),
		hist:   logFixtureHist(),
		revs:   logFixtureRevs(),
		at:     railNow,
	})
	return res.(Model)
}

// goldenStatsModel hosts the stats view, reached through the shell's start
// command so the breadcrumb reads relevo › stats, and feeds it a report.
func goldenStatsModel(t *testing.T, width, height int, rep stats.Report) Model {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	st := store.New(t.TempDir())
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st, DB: d}},
		Options{Interval: time.Second, Start: "stats", Version: goldenVersion})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, cmd := m.Update(statusMsg{report: view.Report{}})
	m = drain(t, res.(Model), cmd)
	if _, ok := m.top().(statsView); !ok {
		t.Fatalf("Start=stats must replace the stack, top is %T", m.top())
	}
	res, _ = m.Update(statsMsg{window: "30d", rep: rep})
	return res.(Model)
}

// overviewRows is the stats-overview golden's synthetic rounds, shaped like the
// user's data (§5): three candidates, a deepseek-like lane with a 99% cache
// share and a gemini-like one with 88%, tokens on four of the last seven days,
// three repos, and one row with no token field at all.
func overviewRows() []db.RoundRow {
	s := func(v string) *string { return &v }
	i64 := func(v int64) *int64 { return &v }
	at := func(d, h int) time.Time { return time.Date(2026, 9, d, h, 0, 0, 0, time.Local) }

	relevo := s("https://github.com/fuad-daoud/relevo")
	money := s("https://github.com/fuad-daoud/money")
	site := s("https://github.com/fuad-daoud/site")
	deepseek := s("deepseek-v4.1-flash")
	gemini := s("gemini-3.8-flash-high")
	glm := s("glm-5.3-flash")
	kimi := s("kimi-k3")
	qwen := s("qwen-4-coder")

	// One round's four token columns per lane.
	type tokens struct{ in, cache, write, out int64 }
	ds := tokens{in: 20_000, cache: 1_980_000, write: 5_000, out: 60_000}
	gm := tokens{in: 120_000, cache: 880_000, out: 40_000}
	gl := tokens{in: 50_000, cache: 200_000, out: 20_000}
	row := func(binding string, repo *string, started time.Time, cand *string, outcome string, t tokens, nilTokens bool) db.RoundRow {
		r := db.RoundRow{
			BindingID: binding, BindingName: binding, Repo: repo,
			StartedAt: started, Outcome: outcome, Candidate: cand,
		}
		if !nilTokens {
			r.InTokens, r.CacheTokens, r.WriteTokens, r.OutTokens =
				i64(t.in), i64(t.cache), i64(t.write), i64(t.out)
		}
		if outcome != db.OutcomeOpen {
			r.DurationMS = i64(27 * 60_000)
		}
		return r
	}

	rows := []db.RoundRow{
		// deepseek-v4.1-flash: six rounds, one of them unmeasured, 99% cache.
		row("b1", relevo, at(11, 10), deepseek, db.OutcomeReported, ds, false),
		row("b1", money, at(13, 10), deepseek, db.OutcomeReported, ds, false),
		row("b1", relevo, at(15, 10), deepseek, db.OutcomeReported, ds, false),
		row("b1", relevo, at(17, 10), deepseek, db.OutcomeHalted, ds, false),
		row("b1", relevo, at(17, 11), deepseek, db.OutcomeReported, ds, false),
		row("b1", site, at(17, 12), deepseek, db.OutcomeOpen, ds, true),

		// gemini-3.8-flash-high: five measured rounds, 88% cache.
		row("b2", money, at(11, 11), gemini, db.OutcomeReported, gm, false),
		row("b2", money, at(13, 11), gemini, db.OutcomeReported, gm, false),
		row("b2", site, at(15, 11), gemini, db.OutcomeReported, gm, false),
		row("b2", site, at(17, 10), gemini, db.OutcomeReported, gm, false),
		row("b2", money, at(17, 11), gemini, db.OutcomeReported, gm, false),

		// glm-5.3-flash: five measured rounds, 80% cache.
		row("b3", relevo, at(11, 12), glm, db.OutcomeReported, gl, false),
		row("b3", relevo, at(13, 12), glm, db.OutcomeReported, gl, false),
		row("b3", money, at(15, 12), glm, db.OutcomeReported, gl, false),
		row("b3", site, at(17, 12), glm, db.OutcomeReported, gl, false),
		row("b3", relevo, at(17, 13), glm, db.OutcomeReported, gl, false),

		// kimi-k3 and qwen-4-coder: under five rounds, so the overview dims
		// their rows.
		row("b4", relevo, at(12, 10), kimi, db.OutcomeReported, gl, false),
		row("b4", relevo, at(16, 10), kimi, db.OutcomeReported, gl, false),
		row("b5", relevo, at(14, 10), qwen, db.OutcomeReported, gl, false),
	}
	// The halted round carries a report outcome, so the ROUNDS tile and the
	// context row have a halt to count.
	rows[3].ReportOutcome = s("halted")
	return rows
}

// statsOverviewReport is the stats-overview-132 golden's report: stats.Build
// over the synthetic rows, 30 days wide so the window matches the chart's
// `30 days` heading (§7), with one active gate on the gemini-like lane so the
// candidates golden's STATUS column has a gated row.
func statsOverviewReport() stats.Report {
	return stats.Build(stats.Inputs{
		Rows: overviewRows(),
		Gates: []availability.Gate{{
			Token: "gemini-3.8-flash-high", Kind: availability.RateLimited,
			Since: railNow, Until: railNow.Add(26 * time.Hour),
		}},
		Since: railNow.AddDate(0, 0, -29),
		Until: railNow,
		Loc:   time.Local,
	})
}

// statsReposExpandedReport is the stats-repos-expanded-132 golden's report: a
// Build over rows where one feature label and one ticket are used by two repos,
// so each repo carries its own scoped numbers and nests the ticket under its
// feature, plus unlabelled rounds, so the (no feature) row shows, and a repo
// with no labels at all.
func statsReposExpandedReport() stats.Report {
	s := func(v string) *string { return &v }
	i64 := func(v int64) *int64 { return &v }
	at := func(d, h int) time.Time { return time.Date(2026, 9, d, h, 0, 0, 0, time.Local) }
	relevo := s("https://github.com/fuad-daoud/relevo")
	money := s("https://github.com/fuad-daoud/money")
	site := s("https://github.com/fuad-daoud/site")
	shared := s("cockpit")
	ticket := s("#665")

	// One round's tokens, so relevo leads the table and its label leads its
	// feature section.
	row := func(binding string, repo, feature, tk *string, day int, in, out int64) db.RoundRow {
		return db.RoundRow{
			BindingID: binding, BindingName: binding, Repo: repo, Feature: feature, Ticket: tk,
			StartedAt: at(day, 9), Outcome: db.OutcomeReported,
			InTokens: i64(in), OutTokens: i64(out),
		}
	}

	rows := []db.RoundRow{
		// The shared label cockpit under relevo, counted on its own.
		row("b1", relevo, shared, ticket, 10, 900_000, 100_000),
		row("b1", relevo, shared, nil, 11, 900_000, 100_000),
		// Relevo's unlabelled round: the (no feature) row.
		row("b2", relevo, nil, nil, 12, 200_000, 20_000),
		// The same label and ticket under money, counted on its own.
		row("b3", money, shared, ticket, 13, 300_000, 30_000),
		row("b4", money, nil, nil, 14, 100_000, 10_000),
		// A repo with no labels at all: expanding it shows nothing.
		row("b5", site, nil, nil, 15, 50_000, 5_000),
	}
	return stats.Build(stats.Inputs{
		Rows:  rows,
		Since: railNow.AddDate(0, 0, -29),
		Until: railNow,
		Loc:   time.Local,
	})
}

// candKeys sends keys through a candidates golden model one at a time,
// draining each key's command as the bubbletea loop would.
func candKeys(t *testing.T, m Model, keys ...tea.KeyMsg) Model {
	t.Helper()
	for _, k := range keys {
		res, cmd := m.Update(k)
		m = drain(t, res.(Model), cmd)
	}
	return m
}

// candDown presses down n times.
func candDown(t *testing.T, m Model, n int) Model {
	t.Helper()
	for range n {
		m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyDown})
	}
	return m
}

// candType types s one rune at a time.
func candType(t *testing.T, m Model, s string) Model {
	t.Helper()
	for _, r := range s {
		m = candKeys(t, m, key(r))
	}
	return m
}

// goldenActorsModel is the actors goldens' builder: a loaded shell, the
// `:actors` command, and its doc load drained.
func goldenActorsModel(t *testing.T, width, height int, fa *fakeActions, rep view.Report) Model {
	t.Helper()
	m := goldenActionModel(t, width, height, fa, rep)
	return drain(t, m, execLine("actors", m.env(), m.prefs))
}

// workflowsFixtureList is the workflow list the goldens render: the shipped
// workflow and two saved ones, so ORIGIN, INPUTS and PARAMS each have a row
// that is not the same as its neighbours.
func workflowsFixtureList() []relevo.WorkflowSummary {
	return []relevo.WorkflowSummary{
		{
			Name: "default", Origin: relevo.WorkflowOriginShipped,
			Description: "plans built, checked and reviewed, then one security scan",
			Inputs:      workflow.Inputs{Plans: workflow.InputRequired},
			Params: []relevo.WorkflowParam{
				{Name: "builder", Kind: workflow.ParamString, Value: "builder"},
				{Name: "reviewer", Kind: workflow.ParamString, Value: "reviewer"},
				{Name: "scan", Kind: workflow.ParamBool, Value: "true"},
			},
		},
		{
			Name: "audit-only", Origin: relevo.WorkflowOriginSaved,
			Description: "one reader round per plan",
			Inputs:      workflow.Inputs{Plans: workflow.InputRequired, Task: workflow.InputOptional},
		},
		{
			Name: "fix-first", Origin: relevo.WorkflowOriginSaved,
			Description: "build once, then check",
			Inputs:      workflow.Inputs{Task: workflow.InputRequired},
			Params:      []relevo.WorkflowParam{{Name: "gate", Kind: workflow.ParamString, Value: "make check"}},
		},
	}
}

// workflowsFixtureSources is the source each fixture name serves, and which of
// them answers with the shipped definition rather than source a user wrote.
func workflowsFixtureSources() map[string]workflowSourceText {
	return map[string]workflowSourceText{
		"default": {text: "{\n  \"name\": \"default\",\n  \"start\": \"plans\"\n}\n", shipped: true},
		"audit-only": {text: "# one reader round per plan\n" +
			"name: audit-only\nstart: audit\nsteps:\n  audit: { run: reviewer, on: { done: done } }\n"},
		"fix-first": {text: "name: fix-first\nstart: build\nsteps:\n" +
			"  build: { run: builder, on: { done: check } }\n" +
			"  check: { check: gate, on: { green: done, red: done } }\n"},
	}
}

// workflowsFixtureGraphs is the step graph each fixture name serves, in the
// order WorkflowGraph walks one.
func workflowsFixtureGraphs() map[string][]relevo.GraphRow {
	return map[string][]relevo.GraphRow{
		"default": {
			{ID: "plans", Kind: "for-each", Edges: []string{"build", "scan-gate"},
				EdgeText: "on empty → scan-gate · on next → build"},
			{ID: "build", Kind: "run", Actors: "builder", Edges: []string{"check"},
				EdgeText: "on done → check"},
			{ID: "check", Kind: "check", Actors: "gate", Edges: []string{"repair", "review"},
				EdgeText: "on green → review · on red → repair"},
			{ID: "repair", Kind: "run", Actors: "builder", Edges: []string{"check"},
				EdgeText: "on done → check · budget → review"},
			{ID: "review", Kind: "run", Actors: "reviewer", Edges: []string{"correct", "plans"},
				EdgeText: "on verdict=changes → correct · on verdict=pass → plans"},
		},
		"audit-only": {
			{ID: "audit", Kind: "run", Actors: "reviewer", EdgeText: "on done → done"},
		},
		"fix-first": {
			{ID: "build", Kind: "run", Actors: "builder", Edges: []string{"check"}, EdgeText: "on done → check"},
			{ID: "check", Kind: "check", Actors: "gate", EdgeText: "on green → done · on red → done"},
		},
	}
}

// goldenWorkflowsModel is the workflows goldens' builder: a loaded shell, the
// `:workflows` command, and its list load drained.
func goldenWorkflowsModel(t *testing.T, width, height int, fa *fakeActions) Model {
	t.Helper()
	m := goldenActionModel(t, width, height, fa, view.Report{})
	return drain(t, m, execLine("workflows", m.env(), m.prefs))
}

// workflowsFake is the scripted Actions the workflows goldens run on.
func workflowsFake() *fakeActions {
	return &fakeActions{
		workflows: workflowsFixtureList(),
		sources:   workflowsFixtureSources(),
		graphs:    workflowsFixtureGraphs(),
		actors:    workflowsFixtureActors(),
		result:    Result{Text: "stored workflow fix-first", Refresh: true},
		saveResult: Result{
			Text:    "workflow edit fix-first (config version 4)",
			Refresh: true,
		},
	}
}

// workflowsFixtureActors are the actors the fixture workflows name, so the edit
// loop validates against the same names its sources run.
func workflowsFixtureActors() map[string]workflow.ActorInfo {
	return map[string]workflow.ActorInfo{
		"builder":  {Shape: workflow.ShapeWriter},
		"reviewer": {Shape: workflow.ShapeReader},
	}
}

// workflowsOn points the list's cursor at name and returns the model.
func workflowsOn(t *testing.T, m Model, name string) Model {
	t.Helper()
	wv, ok := m.top().(workflowsView)
	if !ok {
		t.Fatalf(":workflows did not push a workflows view: %T", m.top())
	}
	for i, w := range wv.list {
		if w.Name == name {
			wv.cur = i
			m.stack[len(m.stack)-1] = wv
			return m
		}
	}
	t.Fatalf("no workflow named %q in the list", name)
	return m
}

// workflowsViewModelOn is the list with the cursor on name, over the Actions a
// test scripted, so a key test can inspect what it recorded.
func workflowsViewModelOn(t *testing.T, width, height int, fa *fakeActions, name string) Model {
	t.Helper()
	return workflowsOn(t, goldenWorkflowsModel(t, width, height, fa), name)
}

// workflowsViewModel is workflowsViewModelOn over the shared fixture, for the
// goldens that only render.
func workflowsViewModel(t *testing.T, width, height int, name string) Model {
	t.Helper()
	return workflowsViewModelOn(t, width, height, workflowsFake(), name)
}

// workflowGraphModel is one workflow's step graph, reached by enter.
func workflowGraphModel(t *testing.T, width, height int, name string) Model {
	t.Helper()
	return candKeys(t, workflowsViewModel(t, width, height, name), tea.KeyMsg{Type: tea.KeyEnter})
}

// goldenChainsModel is the chains goldens' builder: a loaded shell, the
// `:chains` command, and its doc load drained.
func goldenChainsModel(t *testing.T, width, height int, fa *fakeActions) Model {
	t.Helper()
	m := goldenActionModel(t, width, height, fa, view.Report{})
	return drain(t, m, execLine("chains", m.env(), m.prefs))
}

func chainsFixtureDoc() relevo.ChainsDoc {
	return relevo.ChainsDoc{
		Chains: []relevo.ChainEntry{
			{
				Name:      "api-worker",
				Status:    "running",
				Step:      "building",
				PlanPos:   1,
				PlanTotal: 3,
				Elapsed:   4 * time.Minute,
				Where:     "local",
				Depth:     0,
			},
			{
				Name:      "db-migrate",
				Status:    "halted",
				Reason:    "migration failed: schema mismatch on table users",
				Step:      "verify",
				PlanPos:   2,
				PlanTotal: 2,
				Elapsed:   12 * time.Minute,
				Where:     "local",
				Depth:     0,
			},
			{
				Name:      "feature-auth",
				Status:    "running",
				Step:      "review",
				PlanPos:   1,
				PlanTotal: 1,
				Elapsed:   25 * time.Minute,
				Where:     "placed prod-1",
				Depth:     0,
				Children:  []string{"feature-auth.oauth", "feature-auth.token"},
				Steps: []relevo.ChainStep{
					{ID: "build", Kind: "run", Actor: "builder", Member: "feature-auth", Visits: 2, Round: 2, LastOutcome: "build r2 · done"},
					{ID: "review", Kind: "run", Actor: "reviewer", Member: "feature-auth-reviewer", Visits: 1, Round: 1, InFlight: true, LastOutcome: "review r1 · verdict=changes"},
					{ID: "check", Kind: "check", Visits: 1, Round: 1, LastOutcome: "check · red → repair"},
					{ID: "repair", Kind: "run", Actor: "builder", Member: "feature-auth", Visits: 0},
				},
				Members: []view.BindingStatus{
					chainMemberRow("feature-auth", 2),
					chainMemberRow("feature-auth-reviewer", 1),
				},
			},
			{
				Name:      "feature-auth.oauth",
				Parent:    "feature-auth",
				Status:    "halted",
				Reason:    "oauth callback endpoint returned 503",
				Step:      "test",
				PlanPos:   1,
				PlanTotal: 1,
				Elapsed:   8 * time.Minute,
				Where:     "local",
				Depth:     1,
			},
			{
				Name:      "feature-auth.token",
				Parent:    "feature-auth",
				Status:    "done",
				Step:      "finish",
				PlanPos:   1,
				PlanTotal: 1,
				Elapsed:   15 * time.Minute,
				Where:     "local",
				Depth:     1,
			},
		},
	}
}

// chainMemberRow is one chain member's status row: a writer mid-round, as the
// chains read model carries it from the status document.
func chainMemberRow(name string, round int) view.BindingStatus {
	return view.BindingStatus{
		Name: name, Round: round, PlanRound: round, Display: "ACTIVE",
		State: "active", BuilderStatus: "working",
		BuilderCandidate: "agy/antigravity/claude-sonnet-4-6",
		BuilderName:      "claude-sonnet-4-6",
		MasterMindName:   "architect-2", Branch: "relevo/" + name,
		RoundStart: railNow.Add(-2 * time.Minute),
	}
}

// chainsStepsModel is the steps golden's builder: the chains list, the cursor
// pointed at the named chain, and enter.
func chainsStepsModel(t *testing.T, width, height int, fa *fakeActions, chain string) Model {
	t.Helper()
	return chainsStepsModelOn(t, goldenChainsModel(t, width, height, fa), chain)
}

// chainsStepsModelOn points a chains list already built at chain and enters
// it, so a golden can seed its own store first.
func chainsStepsModelOn(t *testing.T, m Model, chain string) Model {
	t.Helper()
	cv, ok := m.top().(chainsView)
	if !ok {
		t.Fatalf(":chains did not push a chains view: %T", m.top())
	}
	for i, c := range cv.doc.Chains {
		if c.Name == chain {
			cv.cur = i
			m.stack[len(m.stack)-1] = cv
			break
		}
	}
	return candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
}

// chainStepsHaltedModel is the halted chain's steps: a check that went red
// and the repair step it chose.
func chainStepsHaltedModel(t *testing.T, width, height int) Model {
	t.Helper()
	doc := chainsFixtureDoc()
	for i := range doc.Chains {
		if doc.Chains[i].Name != "feature-auth" {
			continue
		}
		doc.Chains[i].Status = "halted"
		doc.Chains[i].Reason = "check went red twice on the same assertion"
		doc.Chains[i].Steps = []relevo.ChainStep{
			{ID: "build", Kind: "run", Actor: "builder", Member: "feature-auth", Visits: 2, Round: 2, LastOutcome: "build r2 · done"},
			{ID: "review", Kind: "run", Actor: "reviewer", Member: "feature-auth-reviewer", Visits: 1, Round: 1, LastOutcome: "review r1 · verdict=changes"},
			{ID: "check", Kind: "check", Visits: 2, Round: 2, LastOutcome: "check · red → repair"},
			{ID: "repair", Kind: "run", Actor: "builder", Member: "feature-auth", Visits: 1, Round: 3, LastOutcome: "repair r3 · done"},
		}
	}
	fa := &fakeActions{chainsDoc: doc}
	m := chainsStepsModel(t, width, height, fa, "feature-auth")
	return candKeys(t, m, tea.KeyMsg{Type: tea.KeyDown}) // the check step
}

// chainDrillModel is the member round a check step's outcome came from: the
// builder's round 2, opened under the crumbs chains › feature-auth › build.
func chainDrillModel(t *testing.T, width, height int) Model {
	t.Helper()
	st := store.New(t.TempDir())
	seedPlanFixture(t, st, "feature-auth", 2, railNow.Add(-6*time.Minute),
		"# Round 2 plan\n\nAdd the token refresh path and its tests.\n")
	fa := &fakeActions{chainsDoc: chainsFixtureDoc()}
	m := goldenActionModelWithStore(t, width, height, fa, view.Report{}, st)
	m = drain(t, m, execLine("chains", m.env(), m.prefs))
	m = chainsStepsModelOn(t, m, "feature-auth")
	return candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // the build step
}

// chainTraceModel is the chains list with the trace of the cursor's chain
// pushed and its read drained.
func chainTraceModel(t *testing.T, width, height int) Model {
	t.Helper()
	fa := &fakeActions{
		chainsDoc:   chainsFixtureDoc(),
		chainTraces: map[string]relevo.ChainTraceDoc{"feature-auth": chainTraceFixture()},
	}
	m := goldenChainsModel(t, width, height, fa)
	cv := m.top().(chainsView)
	for i, c := range cv.doc.Chains {
		if c.Name == "feature-auth" {
			cv.cur = i
		}
	}
	m.stack[len(m.stack)-1] = cv
	res, cmd := m.Update(key('t'))
	return drain(t, res.(Model), cmd)
}

// chainTraceFromStepsModel is the same trace pushed from the steps view rather
// than from the chains list: the two entry points must draw one breadcrumb, so
// the golden for the steps path exists to pin that.
func chainTraceFromStepsModel(t *testing.T, width, height int) Model {
	t.Helper()
	fa := &fakeActions{
		chainsDoc:   chainsFixtureDoc(),
		chainTraces: map[string]relevo.ChainTraceDoc{"feature-auth": chainTraceFixture()},
	}
	m := chainsStepsModel(t, width, height, fa, "feature-auth")
	res, cmd := m.Update(key('t'))
	return drain(t, res.(Model), cmd)
}

// chainTraceFixture is the trace the trace golden renders: two closed rounds
// and a check that went red.
func chainTraceFixture() relevo.ChainTraceDoc {
	seq := []relevo.ChainTraceEvent{
		{
			Seq: 1, TS: railNow.Add(-9 * time.Minute), Step: "build", Member: "feature-auth", Round: 1,
			Flow:       &workflow.Event{Kind: workflow.EventStepClosed, Step: "build"},
			FlowAction: &workflow.Action{Kind: workflow.ActionSend, Step: "review"},
		},
		{
			Seq: 2, TS: railNow.Add(-6 * time.Minute), Step: "review", Member: "feature-auth-reviewer", Round: 1,
			Flow:       &workflow.Event{Kind: workflow.EventStepClosed, Step: "review", Outcomes: map[string]string{"verdict": "changes"}},
			FlowAction: &workflow.Action{Kind: workflow.ActionRunCheck, Step: "check"},
		},
		{
			Seq: 3, TS: railNow.Add(-4 * time.Minute), Step: "check",
			Flow:       &workflow.Event{Kind: workflow.EventCheckClosed, Step: "check", Run: 1, Result: "red"},
			FlowAction: &workflow.Action{Kind: workflow.ActionSend, Step: "repair"},
		},
	}
	return relevo.ChainTraceDoc{
		Name: "feature-auth", Status: "running", Step: "repair",
		Plan: 1, Plans: 1, Events: seq,
	}
}

// goldenActorViewModel pushes the builder detail via enter.
func goldenActorViewModel(t *testing.T, width, height int) Model {
	t.Helper()
	m := goldenActorsModel(t, width, height, &fakeActions{doc: candFixtureDoc(t)}, candGatedReport())
	return candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
}

// agentFileFixtures is the fake Actions' per-agent file states for the agents
// goldens (§8): every shipped agent installed and up to date on all four
// kinds, with the pins the plan fixes. The paths are built under the real home
// so the views' `~` form is what the golden shows; the test only reads $HOME,
// it never writes there, and the substitution keeps the golden identical on
// every machine.
func agentFileFixtures(t *testing.T) map[string][]harness.AgentFile {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	pins := map[string]map[string]string{
		"researcher": {
			"agy": "inherit", "claude": "haiku",
			"codex": "gpt-5.6-luna", "opencode": "openrouter/z-ai/glm-5.3-flash",
		},
		"reviewer":      {"claude": "opus"},
		"plan-executor": {"agy": "inherit"},
	}
	files := make(map[string][]harness.AgentFile)
	for _, s := range roles.ShippedAgents() {
		for _, h := range harness.All() {
			rel, ok := harness.DefinitionPath(h.Kind, s.Name)
			if !ok {
				t.Fatalf("%s has no %s definition path", h.Kind, s.Name)
			}
			files[s.Name] = append(files[s.Name], harness.AgentFile{
				Kind:  h.Kind,
				Path:  filepath.Join(home, rel),
				State: harness.FileUpToDate,
				Model: pins[s.Name][h.Kind],
			})
		}
	}
	return files
}

// goldenAgentsModel is the agents goldens' builder: a loaded shell, the
// `:agents` command, and its load drained.
func goldenAgentsModel(t *testing.T, width, height int, fa *fakeActions) Model {
	t.Helper()
	m := goldenActionModel(t, width, height, fa, view.Report{})
	return drain(t, m, execLine("agents", m.env(), m.prefs))
}

// goldenSettingsModel is the settings goldens' builder: a loaded shell, the
// `:settings` command, and its doc load drained. It pins numCPU to 22 so the
// serve.max_builders default is deterministic, restoring it on cleanup.
func goldenSettingsModel(t *testing.T, width, height int, fa *fakeActions) Model {
	t.Helper()
	orig := numCPU
	numCPU = func() int { return 22 }
	t.Cleanup(func() { numCPU = orig })
	m := goldenActionModel(t, width, height, fa, view.Report{})
	return drain(t, m, execLine("settings", m.env(), m.prefs))
}

// auditFixtureRevs is this machine's five revisions (§8): the newest today, the
// one before it yesterday, and the first three together on an earlier day, with
// the stored change counts 1, 1, 2, 7 and 0.
func auditFixtureRevs() []db.RevisionRow {
	today := func(hour, min int) time.Time {
		return time.Date(railNow.Year(), railNow.Month(), railNow.Day(), hour, min, 0, 0, railNow.Location())
	}
	back := func(days, hour, min int) time.Time {
		d := railNow.AddDate(0, 0, -days)
		return time.Date(d.Year(), d.Month(), d.Day(), hour, min, 0, 0, railNow.Location())
	}
	return []db.RevisionRow{
		{
			Rev: 5, At: today(1, 36), Source: "ui", Message: "add actor planner", Version: 16,
			Changes: []byte(`[{"path":"actors.planner","op":"add","after":{"agent":"architect"}}]`),
		},
		{
			Rev: 4, At: back(1, 9, 59), Source: "cli", Message: "config set candidates", Version: 15,
			Changes: []byte(`[{"path":"candidates[1].provider","op":"change","before":"antigravity","after":"agy-extra"}]`),
		},
		{
			Rev: 3, At: back(3, 23, 18), Source: "migration", Message: "roles → actors", Version: 14,
			Changes: auditChangesJSON(2),
		},
		{
			Rev: 2, At: back(3, 21, 52), Source: "migration", Message: "candidate names derived", Version: 13,
			Changes: auditChangesJSON(7),
		},
		{
			Rev: 1, At: back(3, 21, 52), Source: "baseline", Message: "config before revisions", Version: 12,
			Changes: []byte(`[]`),
		},
	}
}

// auditChangesJSON is a stored change array of n entries: the goldens' CHANGES
// column counts them, and nothing else reads them.
func auditChangesJSON(n int) []byte {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"path":"policy.x%d","op":"change","before":%d,"after":%d}`, i, i, i+1)
	}
	b.WriteByte(']')
	return []byte(b.String())
}

// auditFixtureChanges is what ConfigChanges answers for those revisions (§8):
// #4 is the claude-sonnet-4-6 provider line, #5 the added planner actor, #3 a
// roles-to-actors migration, #2 the seven derived names, and #1 the baseline
// with none.
func auditFixtureChanges() map[int64][]relevo.ChangeLine {
	return map[int64][]relevo.ChangeLine{
		5: {{Op: "+", Subject: "actor planner", After: "agent architect"}},
		4: {{Op: "~", Subject: "claude-sonnet-4-6", Field: "provider", Before: "antigravity", After: "agy-extra"}},
		3: {
			{Op: "-", Subject: "roles", Before: "plan-executor"},
			{Op: "+", Subject: "actors", After: "builder"},
		},
		2: {
			{Op: "+", Subject: "gemini-3.8-flash-high", Field: "name", After: "gemini-3.8-flash-high"},
			{Op: "+", Subject: "claude-sonnet-4-6", Field: "name", After: "claude-sonnet-4-6"},
			{Op: "+", Subject: "sonnet", Field: "name", After: "sonnet"},
			{Op: "+", Subject: "haiku", Field: "name", After: "haiku"},
			{Op: "+", Subject: "glm-5.3-flash", Field: "name", After: "glm-5.3-flash"},
			{Op: "+", Subject: "deepseek-v4.1-flash", Field: "name", After: "deepseek-v4.1-flash"},
			{Op: "+", Subject: "gpt-5.6-terra", Field: "name", After: "gpt-5.6-terra"},
		},
	}
}

// auditFixtureFake is the fake the audit goldens run on: the five revisions and
// their changes.
func auditFixtureFake() *fakeActions {
	return &fakeActions{revs: auditFixtureRevs(), changes: auditFixtureChanges()}
}

// goldenAuditModel is the audit goldens' builder: a loaded shell, the `:audit`
// command, and its log load drained.
func goldenAuditModel(t *testing.T, width, height int, fa *fakeActions) Model {
	t.Helper()
	m := goldenActionModel(t, width, height, fa, view.Report{})
	return drain(t, m, execLine("audit", m.env(), m.prefs))
}

// goldenAgentResearcherModel is `:agents` with the cursor on researcher and
// its detail pushed.
func goldenAgentResearcherModel(t *testing.T, width, height int, fa *fakeActions) Model {
	t.Helper()
	m := candDown(t, goldenAgentsModel(t, width, height, fa), 4) // researcher
	return candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
}

func TestGoldenViews(t *testing.T) {
	t.Cleanup(availability.SetGateClock(func() time.Time { return railNow }))

	cases := []struct {
		name          string
		width, height int
		build         func(t *testing.T) Model
	}{
		{
			name: "stats-overview-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return goldenStatsModel(t, 132, 34, statsOverviewReport())
			},
		},
		{
			name: "stats-candidates-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenStatsModel(t, 132, 34, statsOverviewReport())
				res, _ := m.Update(statsKey('2'))
				return res.(Model)
			},
		},
		{
			name: "stats-tokens-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenStatsModel(t, 132, 34, statsOverviewReport())
				res, _ := m.Update(statsKey('3'))
				return res.(Model)
			},
		},
		{
			name: "stats-reliability-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenStatsModel(t, 132, 34, statsOverviewReport())
				res, _ := m.Update(statsKey('4'))
				return res.(Model)
			},
		},
		{
			name: "stats-repos-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenStatsModel(t, 132, 34, statsOverviewReport())
				res, _ := m.Update(statsKey('5'))
				return res.(Model)
			},
		},
		{
			name: "stats-repos-expanded-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenStatsModel(t, 132, 34, statsReposExpandedReport())
				res, _ := m.Update(statsKey('5'))
				m = res.(Model)
				// space expands the first repo; j lands on its first child, so
				// the band and the child detail block show.
				res, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
				m = res.(Model)
				res, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
				return res.(Model)
			},
		},
		{
			name: "stats-wide", width: 160, height: 40,
			build: func(t *testing.T) Model { return goldenStatsModel(t, 160, 40, statsFixture()) },
		},
		{
			name: "stats-narrow", width: 100, height: 30,
			build: func(t *testing.T) Model { return goldenStatsModel(t, 100, 30, statsFixture()) },
		},
		{
			name: "stats-empty", width: 160, height: 40,
			build: func(t *testing.T) Model { return goldenStatsModel(t, 160, 40, stats.Report{}) },
		},
		{
			name: "fleet", width: 140, height: 40,
			build: func(t *testing.T) Model {
				return goldenModel(t, 140, 40, view.Report{Bindings: allStatesRows(), Gated: gatedGates()})
			},
		},
		{
			name: "fleet-narrow-80", width: 80, height: 30,
			build: func(t *testing.T) Model {
				return goldenModel(t, 80, 30, view.Report{Bindings: allStatesRows(), Gated: gatedGates()})
			},
		},
		{
			name: "fleet-real-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return goldenActionModel(t, 132, 34, &fakeActions{}, realFleetReport())
			},
		},
		{
			name: "fleet-real-100", width: 100, height: 30,
			build: func(t *testing.T) Model {
				return goldenActionModel(t, 100, 30, &fakeActions{}, realFleetReport())
			},
		},
		{
			name: "fleet-real-done", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenActionModel(t, 132, 34, &fakeActions{}, realFleetReport())
				res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'.'}})
				return res.(Model)
			},
		},
		{
			name: "fleet-filtered", width: 140, height: 40,
			build: func(t *testing.T) Model {
				m := goldenModel(t, 140, 40, view.Report{Bindings: allStatesRows(), Gated: gatedGates()})
				key := func(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }
				res, _ := m.Update(key('/'))
				m = res.(Model)
				for _, r := range "web" {
					res, _ = m.Update(key(r))
					m = res.(Model)
				}
				res, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
				return res.(Model)
			},
		},
		{
			name: "fleet-empty", width: 140, height: 40,
			build: func(t *testing.T) Model {
				return goldenModel(t, 140, 40, view.Report{})
			},
		},
		{
			name: "fleet-error-before-load", width: 140, height: 40,
			build: func(t *testing.T) Model {
				st := store.New(t.TempDir())
				m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st}}, Options{Interval: time.Second, Version: goldenVersion})
				m.now = func() time.Time { return railNow }
				res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
				m = res.(Model)
				m.statusInFlight = false
				res, _ = m.Update(statusMsg{err: errors.New("status unavailable")})
				return res.(Model)
			},
		},
		{
			name: "round", width: 140, height: 40,
			build: func(t *testing.T) Model { return goldenRoundModel(t, 140, 40) },
		},
		{
			name: "round-archived", width: 140, height: 40,
			build: func(t *testing.T) Model { return goldenArchivedRoundModel(t, 140, 40) },
		},
		{
			name: "round-real-132", width: 132, height: 34,
			build: func(t *testing.T) Model { return realRoundModel(t, 132, 34) },
		},
		{
			name: "round-real-100", width: 100, height: 30,
			build: func(t *testing.T) Model { return realRoundModel(t, 100, 30) },
		},
		{
			name: "round-needs-you", width: 132, height: 34,
			build: func(t *testing.T) Model { return realRoundNeedsYouModel(t, 132, 34) },
		},
		{
			name: "round-reader-artifacts-132", width: 132, height: 34,
			build: func(t *testing.T) Model { return roundReaderArtifactsModel(t, 132, 34) },
		},
		{
			name: "round-reader-artifacts-100", width: 100, height: 30,
			build: func(t *testing.T) Model { return roundReaderArtifactsModel(t, 100, 30) },
		},
		{
			name: "rounds", width: 160, height: 40,
			build: func(t *testing.T) Model { return goldenRoundsModel(t, 160, 40) },
		},
		{
			name: "log-132", width: 132, height: 34,
			build: func(t *testing.T) Model { return goldenLogModel(t, 132, 34) },
		},
		{
			name: "cmdline-open", width: 140, height: 40,
			build: func(t *testing.T) Model {
				m := goldenModel(t, 140, 40, view.Report{Bindings: allStatesRows(), Gated: gatedGates()})
				res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
				m = res.(Model)
				res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
				return res.(Model)
			},
		},
		{
			name: "help", width: 140, height: 40,
			build: func(t *testing.T) Model {
				m := goldenModel(t, 140, 40, view.Report{Bindings: allStatesRows(), Gated: gatedGates()})
				res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
				return res.(Model)
			},
		},
		{
			name: "confirm-stop", width: 140, height: 40,
			build: func(t *testing.T) Model {
				m := goldenActionModel(t, 140, 40, &fakeActions{},
					view.Report{Bindings: allStatesRows(), Gated: gatedGates()})
				m = pointer(t, m, "atlas")
				res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
				return drain(t, res.(Model), cmd)
			},
		},
		{
			name: "confirm-stop-real-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenActionModel(t, 132, 34, &fakeActions{}, realFleetReport())
				m = pointer(t, m, "spool-db")
				res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
				return drain(t, res.(Model), cmd)
			},
		},
		{
			name: "prompt-gate", width: 140, height: 40,
			build: func(t *testing.T) Model {
				m := goldenActionModel(t, 140, 40, &fakeActions{},
					view.Report{Bindings: allStatesRows(), Gated: gatedGates()})
				m = pointer(t, m, "worker")
				res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
				return drain(t, res.(Model), cmd)
			},
		},
		{
			name: "command-real-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenActionModel(t, 132, 34, &fakeActions{}, realFleetReport())
				res, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
				m = res.(Model)
				res, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
				return res.(Model)
			},
		},
		{
			name: "retry-list-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				a := &fakeActions{candidates: []string{"gemini-3.8-flash-high", "deepseek-v4.1-flash", "claude-sonnet-5"}}
				m := goldenActionModel(t, 132, 34, a, realFleetReport())
				m = pointer(t, m, "spool-db")
				res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
				return drain(t, res.(Model), cmd)
			},
		},
		{
			name: "prompt-send", width: 140, height: 40,
			build: func(t *testing.T) Model {
				m := goldenActionModel(t, 140, 40, &fakeActions{},
					view.Report{Bindings: allStatesRows(), Gated: gatedGates()})
				m = pointer(t, m, "atlas")
				res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
				return drain(t, res.(Model), cmd)
			},
		},
		{
			name: "report-ready", width: 140, height: 40,
			build: func(t *testing.T) Model {
				m := goldenActionModel(t, 140, 40, &fakeActions{},
					view.Report{Bindings: reportReadyRows(), Gated: gatedGates()})
				return pointer(t, m, "inbox")
			},
		},
		{
			name: "candidates-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return goldenCandidatesModel(t, 132, 34,
					&fakeActions{doc: candFixtureDoc(t)}, candGatedReport())
			},
		},
		{
			name: "candidates-100", width: 100, height: 30,
			build: func(t *testing.T) Model {
				return goldenCandidatesModel(t, 100, 30,
					&fakeActions{doc: candFixtureDoc(t)}, candGatedReport())
			},
		},
		{
			name: "candidates-unused-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenCandidatesModel(t, 132, 34,
					&fakeActions{doc: candFixtureDoc(t)}, candUnusedReport())
				return candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnd})
			},
		},
		{
			name: "candidates-delete-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenCandidatesModel(t, 132, 34,
					&fakeActions{doc: candFixtureDoc(t)}, candGatedReport())
				for range 3 {
					res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
					m = drain(t, res.(Model), cmd)
				}
				res, cmd := m.Update(key('d'))
				return drain(t, res.(Model), cmd)
			},
		},
		{
			name: "cand-add-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenCandidatesModel(t, 132, 34,
					&fakeActions{doc: candFixtureDoc(t)}, candGatedReport())
				m = candKeys(t, m, key('a'))
				m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyTab})
				return candType(t, m, "cline-pass")
			},
		},
		{
			name: "cand-edit-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenCandidatesModel(t, 132, 34,
					&fakeActions{doc: candFixtureDoc(t)}, candGatedReport())
				m = candDown(t, m, 2) // deepseek-v4.1-flash
				m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
				m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
				return candType(t, m, "cline-pass/deepseek-v4.2-flash#high")
			},
		},
		{
			name: "cand-edit-invalid-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenCandidatesModel(t, 132, 34,
					&fakeActions{doc: candFixtureDoc(t)}, candGatedReport())
				m = candDown(t, m, 5) // glm-5.3-flash
				m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
				m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyTab}, tea.KeyMsg{Type: tea.KeyTab})
				m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
				return candType(t, m, "openrouter/z-ai")
			},
		},
		{
			name: "cand-edit-agy-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenCandidatesModel(t, 132, 34,
					&fakeActions{doc: candFixtureDoc(t)}, candGatedReport())
				m = candDown(t, m, 5) // glm-5.3-flash
				m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
				m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyTab}) // model -> harness
				for range 3 {
					m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyLeft}) // opencode -> agy
				}
				return m
			},
		},
		{
			name: "actors-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return goldenActorsModel(t, 132, 34,
					&fakeActions{doc: candFixtureDoc(t)}, candGatedReport())
			},
		},
		{
			name: "actor-builder-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return candDown(t, goldenActorViewModel(t, 132, 34), 4) // sonnet
			},
		},
		{
			name: "actor-edit-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return candKeys(t, goldenActorViewModel(t, 132, 34), key('e'))
			},
		},
		{
			name: "actor-pick-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return candKeys(t, goldenActorViewModel(t, 132, 34), key('a'))
			},
		},
		{
			name: "actor-placement-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return candKeys(t, goldenActorViewModel(t, 132, 34), tea.KeyMsg{Type: tea.KeyTab})
			},
		},
		{
			name: "actor-librarian-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenActorsModel(t, 132, 34,
					&fakeActions{doc: candFixtureDocWithLibrarian(t)}, candGatedReport())
				m = candDown(t, m, 3) // librarian, after builder, reviewer, researcher
				return candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			},
		},
		{
			name: "agents-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				fa := &fakeActions{doc: candFixtureDoc(t), files: agentFileFixtures(t)}
				return candDown(t, goldenAgentsModel(t, 132, 34, fa), 4) // researcher
			},
		},
		{
			name: "agents-custom-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return candDown(t, goldenAgentsModel(t, 132, 34, sourceAgentFixture(t)), 6) // the custom security-reviewer
			},
		},
		{
			name: "agent-researcher-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				fa := &fakeActions{doc: candFixtureDoc(t), files: agentFileFixtures(t)}
				return candDown(t, goldenAgentResearcherModel(t, 132, 34, fa), 1) // claude
			},
		},
		{
			name: "agent-reset-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				files := agentFileFixtures(t)
				for i := range files["researcher"] {
					if files["researcher"][i].Kind == "claude" {
						files["researcher"][i].State = harness.FileEdited
					}
				}
				fa := &fakeActions{doc: candFixtureDoc(t), files: files}
				m := candDown(t, goldenAgentResearcherModel(t, 132, 34, fa), 1) // claude, yours
				return candKeys(t, m, key('r'))
			},
		},
		{
			name: "agent-edited-newer-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				files := agentFileFixtures(t)
				for i := range files["researcher"] {
					if files["researcher"][i].Kind == "claude" {
						files["researcher"][i].State = harness.FileEditedNewer
					}
				}
				fa := &fakeActions{doc: candFixtureDoc(t), files: files}
				return candDown(t, goldenAgentResearcherModel(t, 132, 34, fa), 1) // claude, edit + newer
			},
		},
		{
			name: "settings-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenSettingsModel(t, 132, 34, &fakeActions{doc: settingsFixtureDoc(t)})
				return candDown(t, m, 4) // gate.default
			},
		},
		{
			name: "settings-100", width: 100, height: 34,
			build: func(t *testing.T) Model {
				return goldenSettingsModel(t, 100, 34, &fakeActions{doc: settingsFixtureDoc(t)})
			},
		},
		{
			name: "settings-check-form-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenSettingsModel(t, 132, 34, &fakeActions{doc: settingsFixtureDoc(t)})
				m = candDown(t, m, 4) // gate.default
				return candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			},
		},
		{
			name: "settings-scope-form-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenSettingsModel(t, 132, 34, &fakeActions{doc: settingsFixtureDoc(t)})
				m = candDown(t, m, 14) // serve.scope
				return candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			},
		},
		{
			name: "settings-classify-form-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenSettingsModel(t, 132, 34, &fakeActions{doc: settingsFixtureDoc(t)})
				m = candDown(t, m, 16) // classify
				return candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			},
		},
		{
			name: "settings-reset-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				// No actor's tier is above edit here, so the reset behind r
				// succeeds and the confirm opens (unlike settingsFixtureDoc,
				// whose yolo-tier actors would refuse this reset before any
				// confirm shows).
				m := goldenSettingsModel(t, 132, 34, &fakeActions{doc: settingsFixtureDocNoTiers(t)})
				m = candDown(t, m, 1) // max_tier
				return candKeys(t, m, key('r'))
			},
		},
		{
			name: "settings-webhooks-empty-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenSettingsModel(t, 132, 34, &fakeActions{doc: settingsFixtureDoc(t)})
				m = candDown(t, m, 17) // notify.webhooks
				return candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			},
		},
		{
			name: "settings-webhooks-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenSettingsModel(t, 132, 34, &fakeActions{doc: webhooksDocFrom(t, webhooksFixtureJSON)})
				m = candDown(t, m, 17) // notify.webhooks
				m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
				return candDown(t, m, 1) // the cursor on the second hook
			},
		},
		{
			name: "settings-webhook-add-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := goldenSettingsModel(t, 132, 34, &fakeActions{doc: settingsFixtureDoc(t)})
				m = candDown(t, m, 17) // notify.webhooks
				m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
				return candKeys(t, m, key('a'))
			},
		},
		{
			name: "audit-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				// The cursor on #4: the list opens on #5, so one down.
				return candKeys(t, goldenAuditModel(t, 132, 34, auditFixtureFake()), tea.KeyMsg{Type: tea.KeyDown})
			},
		},
		{
			name: "audit-100", width: 100, height: 30,
			build: func(t *testing.T) Model {
				return candKeys(t, goldenAuditModel(t, 100, 30, auditFixtureFake()), tea.KeyMsg{Type: tea.KeyDown})
			},
		},
		{
			name: "audit-rollback-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				fa := auditFixtureFake()
				fa.preview = []relevo.ChangeLine{{Op: "-", Subject: "actor planner", Before: "agent architect"}}
				m := candKeys(t, goldenAuditModel(t, 132, 34, fa), tea.KeyMsg{Type: tea.KeyDown})
				return candKeys(t, m, key('R'))
			},
		},
		{
			name: "audit-rev-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := candDown(t, goldenAuditModel(t, 132, 34, auditFixtureFake()), 3) // #5, #4, #3, #2
				return candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			},
		},
		{
			name: "chains-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				fa := &fakeActions{chainsDoc: chainsFixtureDoc()}
				return goldenChainsModel(t, 132, 34, fa)
			},
		},
		{
			name: "chains-100", width: 100, height: 30,
			build: func(t *testing.T) Model {
				fa := &fakeActions{chainsDoc: chainsFixtureDoc()}
				return goldenChainsModel(t, 100, 30, fa)
			},
		},
		{
			name: "chain-steps-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				fa := &fakeActions{chainsDoc: chainsFixtureDoc()}
				return chainsStepsModel(t, 132, 34, fa, "feature-auth")
			},
		},
		{
			name: "chain-steps-100", width: 100, height: 30,
			build: func(t *testing.T) Model {
				fa := &fakeActions{chainsDoc: chainsFixtureDoc()}
				return chainsStepsModel(t, 100, 30, fa, "feature-auth")
			},
		},
		{
			name: "chain-steps-halted-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return chainStepsHaltedModel(t, 132, 34)
			},
		},
		{
			name: "chain-drill-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return chainDrillModel(t, 132, 34)
			},
		},
		{
			name: "chain-trace-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return chainTraceModel(t, 132, 34)
			},
		},
		{
			name: "chain-trace-steps-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return chainTraceFromStepsModel(t, 132, 34)
			},
		},
		{
			name: "chain-trace-steps-100", width: 100, height: 30,
			build: func(t *testing.T) Model {
				return chainTraceFromStepsModel(t, 100, 30)
			},
		},
		{
			name: "workflows-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return goldenWorkflowsModel(t, 132, 34, workflowsFake())
			},
		},
		{
			name: "workflows-100", width: 100, height: 30,
			build: func(t *testing.T) Model {
				return goldenWorkflowsModel(t, 100, 30, workflowsFake())
			},
		},
		{
			name: "workflow-view-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return workflowGraphModel(t, 132, 34, "default")
			},
		},
		{
			name: "workflow-source-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				m := workflowGraphModel(t, 132, 34, "fix-first")
				return candKeys(t, m, key('s'))
			},
		},
		{
			name: "workflow-add-invalid-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				fa := workflowsFake()
				fa.addResults = map[string]Result{
					"~/workflows/broken.yaml": {Err: workflowInvalidErr(t)},
				}
				m := goldenWorkflowsModel(t, 132, 34, fa)
				m = candKeys(t, m, key('a'))
				m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
				m = candType(t, m, "~/workflows/broken.yaml")
				return candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			},
		},
		{
			name: "workflow-replace-confirm-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				fa := workflowsFake()
				fa.addResults = map[string]Result{
					"~/workflows/fix-first.yaml": {Err: workflowSavedErr(t, workflowsFixtureSources()["fix-first"].text)},
				}
				m := goldenWorkflowsModel(t, 132, 34, fa)
				m = candKeys(t, m, key('a'))
				m = candKeys(t, m, tea.KeyMsg{Type: tea.KeyCtrlU})
				m = candType(t, m, "~/workflows/fix-first.yaml")
				return candKeys(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			},
		},
		{
			name: "workflow-remove-confirm-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return candKeys(t, workflowsViewModel(t, 132, 34, "audit-only"), key('d'))
			},
		},
		{
			name: "workflow-edit-problems-132", width: 132, height: 34,
			build: func(t *testing.T) Model {
				return workflowEditReopenModel(t, 132, 34)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.build(t)
			got := stripANSI(m.View())

			path := filepath.Join("testdata", tc.name+".golden")
			if *updateGolden {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			if got != string(want) {
				t.Errorf("%s: golden mismatch\ngot:\n%s\nwant:\n%s", tc.name, got, string(want))
			}

			lines := strings.Split(got, "\n")
			if len(lines) != tc.height {
				t.Errorf("%s: %d lines, want %d (height)", tc.name, len(lines), tc.height)
			}
			for i, l := range lines {
				if w := lipgloss.Width(l); w > tc.width {
					t.Errorf("%s: line %d is %d wide, want <= %d: %q", tc.name, i, w, tc.width, l)
				}
			}
		})
	}
}
