package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

func newTestBinding(name string) store.Binding {
	return store.Binding{
		Name:             name,
		CWD:              "/tmp/test",
		Planner:          store.Endpoint{PaneID: "w1:p1", SessionID: "planner-session", Kind: "claude"},
		Builder:          store.Endpoint{AgentName: name + "-builder", PaneID: "w1:p2", Kind: "opencode"},
		BuilderCandidate: "agy",
		Round:            2,
		State:            store.StateActive,
	}
}

func TestTabOrderStartsWithPlan(t *testing.T) {
	if tabPlan != 0 {
		t.Fatalf("tabPlan = %d, want 0 (first in the tab order)", tabPlan)
	}
	wantOrder := [tabCount]string{"plan", "report", "transcript", "diff", "log", "artifacts"}
	if tabTitles != wantOrder {
		t.Fatalf("tabTitles = %v, want %v", tabTitles, wantOrder)
	}
}

func TestFetchPlanLive(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	planPath := st.PlanPath(name, 1)
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, []byte("# Round 1 plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := fetchPlan(context.Background(), plannerSource{rt}, name, 1)
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	if tMsg.content.body != "# Round 1 plan\n" {
		t.Fatalf("body = %q, want the plan file's content", tMsg.content.body)
	}
	if tMsg.t != tabPlan {
		t.Errorf("t = %v, want tabPlan", tMsg.t)
	}
	if tMsg.round != 1 {
		t.Errorf("round = %d, want 1", tMsg.round)
	}
}

// TestFetchPlanTimeIsWhenThePlanWasSent pins the plan tab's time to the plan
// log entry's TS, not to the moment the tab was read.
func TestFetchPlanTimeIsWhenThePlanWasSent(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	planPath := st.PlanPath(name, 2)
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, []byte("# Round 2 plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sent := time.Date(2026, 9, 8, 12, 0, 0, 0, time.Local)
	if err := st.AppendLog(name, store.LogEntry{
		TS: sent, Round: 2, Direction: store.DirToBuilder, Kind: store.KindPlan, Path: planPath,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	tMsg := fetchPlan(context.Background(), plannerSource{rt}, name, 2)().(tabMsg)
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	if !tMsg.content.at.Equal(sent) {
		t.Errorf("content.at = %v, want the plan entry's TS %v", tMsg.content.at, sent)
	}
}

// TestFetchReportTimeIsWhenTheReportArrived pins the report tab's time to the
// report log entry's TS.
func TestFetchReportTimeIsWhenTheReportArrived(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	arrived := time.Date(2026, 9, 8, 12, 30, 0, 0, time.Local)
	if err := st.AppendLog(name, store.LogEntry{
		TS: arrived, Round: 2, Direction: store.DirToPlanner, Kind: store.KindReport, Payload: "round 2 report",
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	tMsg := fetchReport(context.Background(), plannerSource{rt}, name, 2)().(tabMsg)
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	if !tMsg.content.at.Equal(arrived) {
		t.Errorf("content.at = %v, want the report entry's TS %v", tMsg.content.at, arrived)
	}
}

func TestFetchStatusReturnsExactlyOneMessage(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{
		Store: st,
	}

	cmd := fetchStatus(context.Background(), plannerSource{rt})
	if cmd == nil {
		t.Fatal("fetchStatus returned nil command")
	}
	msg := cmd()
	sMsg, ok := msg.(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T", msg)
	}
	if sMsg.err != nil {
		t.Fatalf("unexpected error: %v", sMsg.err)
	}
}

func TestFetchReportScrapedPayloadDoesNotTouchPath(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entry := store.LogEntry{
		TS:        time.Now(),
		Round:     2,
		Direction: store.DirToPlanner,
		Kind:      store.KindReport,
		Path:      "/nonexistent/directory/that/does/not/exist/report.md",
		Payload:   "scraped report payload content",
		Note:      "scraped",
	}
	if err := st.AppendLog(name, entry); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	cmd := fetchReport(context.Background(), plannerSource{rt}, name, 2)
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	if tMsg.content.body != "scraped report payload content" {
		t.Fatalf("expected payload, got %q", tMsg.content.body)
	}
	if tMsg.round != 2 {
		t.Fatalf("expected round 2, got %d", tMsg.round)
	}
	if tMsg.t != tabReport {
		t.Fatalf("expected tabReport, got %v", tMsg.t)
	}
}

func TestFetchReportEmptyLogReturnsRoundOneInFlight(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "emptybinding"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cmd := fetchReport(context.Background(), plannerSource{rt}, name, 1)
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	wantEmpty := "round 1 in flight; no report yet"
	if tMsg.content.empty != wantEmpty {
		t.Fatalf("expected empty %q, got %q", wantEmpty, tMsg.content.empty)
	}
}

// TestFetchReportTakesRound pins #183: fetchReport now reads round's own
// report entry, not the newest one logged -- stepping back to an earlier
// round must show that round's report, not a later round's.
func TestFetchReportTakesRound(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries := []store.LogEntry{
		{TS: time.Now(), Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Payload: "round 1 report"},
		{TS: time.Now(), Round: 2, Direction: store.DirToPlanner, Kind: store.KindReport, Payload: "round 2 report"},
	}
	for _, e := range entries {
		if err := st.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	msg := fetchReport(context.Background(), plannerSource{rt}, name, 1)().(tabMsg)
	if msg.content.body != "round 1 report" {
		t.Errorf("round 1: body = %q, want %q", msg.content.body, "round 1 report")
	}
	if msg.round != 1 {
		t.Errorf("round 1: tabMsg.round = %d, want 1", msg.round)
	}

	msg = fetchReport(context.Background(), plannerSource{rt}, name, 2)().(tabMsg)
	if msg.content.body != "round 2 report" {
		t.Errorf("round 2: body = %q, want %q (not round 1's, even though it is the newest logged)", msg.content.body, "round 2 report")
	}
}

// TestFetchTerminalNonCurrentPaneRoundIsEmptyProse pins #183: a plain pane
// builder's terminal only ever shows the binding's live screen, which
// only ever belongs to its current round; a past round it never captured
// a log for reads as prose, not as an error, and must never reach a pane.
func TestFetchDiffRoundZero(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}

	cmd := fetchDiff(context.Background(), plannerSource{rt}, "webshop", 0)
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	wantEmpty := "no completed round yet"
	if tMsg.content.empty != wantEmpty {
		t.Fatalf("expected empty %q, got %q", wantEmpty, tMsg.content.empty)
	}
}

func TestFetchDiffRoundNoStoredPatch(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cmd := fetchDiff(context.Background(), plannerSource{rt}, name, 1)
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	wantEmpty := "no diff recorded for round 1 — no baseline captured"
	if tMsg.content.empty != wantEmpty {
		t.Fatalf("expected empty %q, got %q", wantEmpty, tMsg.content.empty)
	}
}

func TestFetchLogTwoEntriesByteIdentical(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.Local)
	e1 := store.LogEntry{
		TS:        now,
		Round:     1,
		Direction: store.DirToBuilder,
		Kind:      store.KindPlan,
		Path:      "/path/plan1.md",
		Note:      "started",
	}
	e2 := store.LogEntry{
		TS:        now.Add(2 * time.Minute),
		Round:     1,
		Direction: store.DirToPlanner,
		Kind:      store.KindReport,
		Path:      "/path/report1.md",
		Note:      "finished",
	}

	if err := st.AppendLog(name, e1); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if err := st.AppendLog(name, e2); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	want := "2026-09-08 12:00:00  round 1   to_runner  plan      /path/plan1.md started\n" +
		"2026-09-08 12:02:00  round 1   to_planner report    /path/report1.md finished\n"

	cmd := fetchLog(context.Background(), plannerSource{rt}, name, 1)
	msg := cmd()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	if tMsg.content.body != want {
		t.Fatalf("log mismatch:\ngot:\n%q\nwant:\n%q", tMsg.content.body, want)
	}
}

// TestFetchLogFiltersRound pins #183: fetchLog now scopes to round rather
// than dumping the whole binding log.
func TestFetchLogFiltersRound(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.Local)
	entries := []store.LogEntry{
		{TS: now, Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Path: "/p1.md"},
		{TS: now.Add(time.Minute), Round: 2, Direction: store.DirToBuilder, Kind: store.KindPlan, Path: "/p2.md"},
	}
	for _, e := range entries {
		if err := st.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	msg := fetchLog(context.Background(), plannerSource{rt}, name, 1)().(tabMsg)
	if msg.content.err != nil {
		t.Fatalf("unexpected error: %v", msg.content.err)
	}
	if strings.Count(strings.TrimRight(msg.content.body, "\n"), "\n")+1 != 1 {
		t.Fatalf("body = %q, want exactly one line (round 1 only)", msg.content.body)
	}
	if !strings.Contains(msg.content.body, "round 1") || strings.Contains(msg.content.body, "round 2") {
		t.Errorf("body = %q, want only round 1's entry", msg.content.body)
	}
}

func TestFetchForRouting(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"

	b := newTestBinding(name)
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	for _, tab := range []tab{tabPlan, tabReport, tabTerminal, tabDiff, tabLog, tabArtifacts} {
		cmd := fetchFor(context.Background(), plannerSource{rt}, tab, name, 1, 24, 0, true)
		if cmd == nil {
			t.Fatalf("fetchFor returned nil for tab %v", tab)
		}
		msg := cmd()
		tMsg, ok := msg.(tabMsg)
		if !ok {
			t.Fatalf("expected tabMsg for tab %v, got %T", tab, msg)
		}
		if tMsg.t != tab {
			t.Fatalf("expected tab %v, got %v", tab, tMsg.t)
		}
	}
}

// A spawned builder records an AgentName that a pane could forget across a
// server restart. fetchTerminal has already located the live agent, so it must
// address that agent, not replay a name that may no longer resolve.
func TestFetchTerminalHeadlessReadsTheLogNotThePane(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}

	logPath := filepath.Join(t.TempDir(), "002-builder.log")
	if err := os.WriteFile(logPath, []byte("a\nb\nc\nd\ne\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := newTestBinding("webshop")
	b.Builder = store.Endpoint{AgentName: "webshop-builder", Kind: "agy", Mode: store.ModeHeadless, PID: 4242, StartedAt: 1_700_000_000, LogPath: logPath}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// The lines argument is a pane-builder concern (#180's Task 2): the
	// headless branch now always returns the whole log, capped only by
	// headlessLogLines, so a request for 3 lines still gets all of it.
	msg := fetchTerminal(context.Background(), plannerSource{rt}, "webshop", 2, 3)()
	tMsg, ok := msg.(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", msg)
	}
	if tMsg.content.err != nil || tMsg.content.empty != "" {
		t.Fatalf("content = %+v, want a body", tMsg.content)
	}
	if tMsg.content.body != "a\nb\nc\nd\ne" {
		t.Errorf("body = %q, want the whole log regardless of the lines argument", tMsg.content.body)
	}
}

func TestFetchTerminalHeadlessReturnsWholeLog(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}
	name := "webshop"

	logPath := filepath.Join(t.TempDir(), "002-builder.log")
	var lines40 []string
	for i := 1; i <= 40; i++ {
		lines40 = append(lines40, fmt.Sprintf("line %d", i))
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines40, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := newTestBinding(name)
	b.Builder = store.Endpoint{AgentName: name + "-builder", Kind: "agy", Mode: store.ModeHeadless, LogPath: logPath}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	msg := fetchTerminal(context.Background(), plannerSource{rt}, name, 2, 5)().(tabMsg)
	if got := strings.Count(msg.content.body, "\n") + 1; got != 40 {
		t.Errorf("headless terminal body has %d lines, want all 40 regardless of the lines argument", got)
	}

	// A log longer than the cap keeps only its tail.
	var linesOver []string
	for i := 1; i <= headlessLogLines+10; i++ {
		linesOver = append(linesOver, fmt.Sprintf("line %d", i))
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(linesOver, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	msg = fetchTerminal(context.Background(), plannerSource{rt}, name, 2, 5)().(tabMsg)
	lines := strings.Split(msg.content.body, "\n")
	if len(lines) != headlessLogLines || !strings.HasSuffix(lines[len(lines)-1], fmt.Sprint(headlessLogLines+10)) {
		t.Errorf("capped body: %d lines, last %q", len(lines), lines[len(lines)-1])
	}
}

func TestFetchTerminalHeadlessIdleAndMissingLog(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}

	b := newTestBinding("webshop")
	b.Builder = store.Endpoint{AgentName: "webshop-builder", Kind: "agy", Mode: store.ModeHeadless}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	tMsg := fetchTerminal(context.Background(), plannerSource{rt}, "webshop", 2, 24)().(tabMsg)
	if tMsg.content.empty != "headless builder; no round has run yet, so there is no log" {
		t.Errorf("idle: empty = %q", tMsg.content.empty)
	}

	b.Builder.PID = 4242
	b.Builder.LogPath = filepath.Join(t.TempDir(), "absent.log")
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	tMsg = fetchTerminal(context.Background(), plannerSource{rt}, "webshop", 2, 24)().(tabMsg)
	if !strings.HasPrefix(tMsg.content.empty, "log not written yet: ") || !strings.Contains(tMsg.content.empty, b.Builder.LogPath) {
		t.Errorf("missing log: empty = %q", tMsg.content.empty)
	}
}

func TestFetchTerminalHeadlessBetweenRoundsShowsTheLastRoundsLog(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}

	b := newTestBinding("webshop")
	b.Round = 3 // round 2 closed; nothing sent yet
	// Between rounds: no process, LogPath cleared, but the cursor still
	// names round 2 (transcript spec §3.4).
	b.Builder = store.Endpoint{AgentName: "webshop-builder", Kind: "agy", Mode: store.ModeHeadless, StreamRound: 2, StreamOffset: 100}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	logPath := st.BuilderLogPath("webshop", 2)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("Bash go test ./...\n  -> ok: ok\nrelevo-exit:0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tMsg := fetchTerminal(context.Background(), plannerSource{rt}, "webshop", 2, 24)().(tabMsg)
	if tMsg.content.empty != "" || tMsg.content.err != nil {
		t.Fatalf("between rounds the last log must show: %+v", tMsg.content)
	}
	if tMsg.content.body != "Bash go test ./...\n  -> ok: ok\nrelevo-exit:0" {
		t.Errorf("body = %q", tMsg.content.body)
	}
}

// TestFetchTerminalRemoteBuilder pins the #303 remote branch: a remote
// builder shows the round's local builder log when relevo has one, and
// otherwise a single line naming the server.
func TestFetchTerminalRemoteBuilder(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}

	logPath := st.BuilderLogPath("webshop", 2)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("remote work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := newTestBinding("webshop")
	b.Builder = store.Endpoint{Kind: "claude", Mode: store.ModeRemote, Server: "contabo"}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	msg := fetchTerminal(context.Background(), plannerSource{rt}, "webshop", 2, 24)().(tabMsg)
	if msg.content.err != nil || msg.content.empty != "" {
		t.Fatalf("content = %+v, want a body", msg.content)
	}
	if msg.content.body != "remote work" {
		t.Errorf("body = %q, want the local builder log", msg.content.body)
	}
	if !msg.content.transcript {
		t.Error("transcript = false, want true for a rendered round log")
	}

	// No local log for the round: the single line naming the server.
	msg = fetchTerminal(context.Background(), plannerSource{rt}, "webshop", 1, 24)().(tabMsg)
	if want := "remote builder on contabo: relevo show --log webshop"; msg.content.empty != want {
		t.Errorf("empty = %q, want %q", msg.content.empty, want)
	}
}

// N10: a live headless binding whose round has a stream and no log renders the
// stream; an endpoint whose LogPath is its round's stream path (the 2b shape)
// renders the stream too; and an endpoint naming its own log reads that file
// even when a stream exists.
func TestFetchTerminalHeadlessRendersTheStream(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}

	stream := "stream line one\nstream line two\n"
	b := newTestBinding("webshop") // Round 2
	b.Builder = store.Endpoint{
		AgentName:   "webshop-builder",
		Kind:        "agy",
		Mode:        store.ModeHeadless,
		PID:         4242,
		StreamRound: 2,
	}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	streamPath := st.RunnerStreamPath("webshop", 2)
	if err := os.WriteFile(streamPath, []byte(stream), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}

	msg := fetchTerminal(context.Background(), plannerSource{rt}, "webshop", 2, 24)().(tabMsg)
	if msg.content.err != nil || msg.content.empty != "" {
		t.Fatalf("content = %+v, want the rendered stream", msg.content)
	}
	if msg.content.body != "stream line one\nstream line two" {
		t.Errorf("body = %q, want the rendered stream", msg.content.body)
	}
	if !msg.content.transcript {
		t.Error("transcript = false, want true for a rendered stream")
	}
	if msg.content.logName != "002-runner.jsonl (rendered)" {
		t.Errorf("logName = %q, want 002-runner.jsonl (rendered)", msg.content.logName)
	}

	// LogPath equal to the round's stream path (2b): still the stream.
	b.Builder.LogPath = streamPath
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	msg = fetchTerminal(context.Background(), plannerSource{rt}, "webshop", 2, 24)().(tabMsg)
	if msg.content.err != nil || msg.content.empty != "" {
		t.Fatalf("2b shape: content = %+v, want the rendered stream", msg.content)
	}
	if msg.content.body != "stream line one\nstream line two" || msg.content.logName != "002-runner.jsonl (rendered)" {
		t.Errorf("2b shape: body = %q, logName = %q, want the rendered stream", msg.content.body, msg.content.logName)
	}

	// Rule 1: a log that is not the round's stream is read even when a stream
	// exists.
	logPath := filepath.Join(t.TempDir(), "002-builder.log")
	if err := os.WriteFile(logPath, []byte("endpoint log line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.Builder.LogPath = logPath
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	msg = fetchTerminal(context.Background(), plannerSource{rt}, "webshop", 2, 24)().(tabMsg)
	if msg.content.err != nil || msg.content.empty != "" {
		t.Fatalf("rule 1: content = %+v, want the endpoint's log", msg.content)
	}
	if msg.content.body != "endpoint log line" {
		t.Errorf("rule 1: body = %q, want the endpoint's log", msg.content.body)
	}
	if msg.content.logName != "002-builder.log" {
		t.Errorf("rule 1: logName = %q, want 002-builder.log", msg.content.logName)
	}
}

// A round whose stream is only NNN-builder.jsonl still renders, and the tab
// labels it under the name it was read from.
func TestFetchTerminalRendersThePreRenameStream(t *testing.T) {
	st := store.New(t.TempDir())
	rt := relevo.Runtime{Store: st}

	stream := "stream line one\nstream line two\n"
	b := newTestBinding("webshop") // Round 2
	b.Builder = store.Endpoint{
		AgentName:   "webshop-builder",
		Kind:        "agy",
		Mode:        store.ModeHeadless,
		PID:         4242,
		StreamRound: 2,
	}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.WriteFile(st.BuilderStreamPath("webshop", 2), []byte(stream), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}

	msg := fetchTerminal(context.Background(), plannerSource{rt}, "webshop", 2, 24)().(tabMsg)
	if msg.content.err != nil || msg.content.empty != "" {
		t.Fatalf("content = %+v, want the rendered stream", msg.content)
	}
	if msg.content.body != "stream line one\nstream line two" {
		t.Errorf("body = %q, want the rendered stream", msg.content.body)
	}
	if msg.content.logName != "002-builder.jsonl (rendered)" {
		t.Errorf("logName = %q, want 002-builder.jsonl (rendered)", msg.content.logName)
	}
}
