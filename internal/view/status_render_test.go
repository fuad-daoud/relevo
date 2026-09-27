package view

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

func TestRenderStatusShowsDetailLine(t *testing.T) {
	t.Parallel()

	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "doctor", CWD: "/repo", Round: 3,
		Display:          "NEEDS YOU",
		BuilderCandidate: testAgyRef,
		MasterMindKind:   "claude", BuilderKind: "agy", BuilderStatus: "gone",
		Detail: "round 2 report delivered; nothing outstanding -- unless you want another round",
	}}})

	if !strings.Contains(out, "  detail   round 2 report delivered") {
		t.Errorf("detail line missing from:\n%s", out)
	}
	// It must sit between the builder line and pending, where a human about to
	// rebind is already looking.
	builderAt := strings.Index(out, "  runner ")
	detailAt := strings.Index(out, "  detail ")
	pendingAt := strings.Index(out, "  pending ")
	if builderAt >= detailAt || detailAt >= pendingAt {
		t.Errorf("detail must follow builder and precede pending, got:\n%s", out)
	}
}

// TestRenderStatusMasterMindChat is the status surface: a row whose mastermind
// carries a chat label and a link shows them after its route, and a row that
// carries neither is byte-identical to the line before these fields existed.
func TestRenderStatusMasterMindChat(t *testing.T) {
	t.Parallel()

	out := RenderStatus(Report{Bindings: []BindingStatus{
		{
			Name: "one", CWD: "/a", Round: 1, Display: "ACTIVE",
			MasterMindName: "architect-1", MasterMindKind: "claude", MasterMindRoute: "pull",
			MasterMindChatLabel: `"fix the flake"`, MasterMindChatLink: "https://claude.ai/code/session_01TEST",
		},
		{
			Name: "two", CWD: "/b", Round: 1, Display: "ACTIVE",
			MasterMindName: "architect-2", MasterMindKind: "claude", MasterMindRoute: "pull",
		},
	}})

	var mastermindLines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  MasterMind  ") {
			mastermindLines = append(mastermindLines, line)
		}
	}
	if len(mastermindLines) != 2 {
		t.Fatalf("got %d mastermind lines, want 2:\n%s", len(mastermindLines), out)
	}

	wantFirst := `route pull · "fix the flake" · https://claude.ai/code/session_01TEST`
	if !strings.HasSuffix(mastermindLines[0], wantFirst) {
		t.Errorf("mastermind line %q does not end with %q", mastermindLines[0], wantFirst)
	}
	// Row two has neither field set, so nothing follows its route.
	if !strings.HasSuffix(mastermindLines[1], "route pull") {
		t.Errorf("mastermind line %q does not end with %q", mastermindLines[1], "route pull")
	}
}
func TestRenderStatusOmitsEmptyDetail(t *testing.T) {
	t.Parallel()

	out := RenderStatus(Report{Bindings: []BindingStatus{{
		Name: "ok", CWD: "/repo", Round: 1, Display: "ACTIVE",
		MasterMindKind: "claude", BuilderKind: "agy", BuilderStatus: "working",
		BuilderCandidate: testAgyRef,
	}}})

	if strings.Contains(out, "detail") {
		t.Errorf("no detail line may appear for a healthy binding:\n%s", out)
	}
}
func TestRenderStatusOmitsTheConsultCountWhenZero(t *testing.T) {
	t.Parallel()

	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Round: 3, Consults: 0,
	}}}

	if out := RenderStatus(r); strings.Contains(out, "+0c") {
		t.Errorf("rendered a zero consult count:\n%s", out)
	}
}
func TestRenderStatusShowsSwitches(t *testing.T) {
	t.Parallel()

	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Round: 3, Switches: 1,
	}}}

	if out := RenderStatus(r); !strings.Contains(out, "switched 1x") {
		t.Errorf("RenderStatus output missing switched 1x:\n%s", out)
	}
}
func TestRenderStatusOmitsSwitchedWhenZero(t *testing.T) {
	t.Parallel()

	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Round: 3, Switches: 0,
	}}}

	if out := RenderStatus(r); strings.Contains(out, "switched") {
		t.Errorf("rendered a zero switch count:\n%s", out)
	}
}
func TestHideDoneRemovesOnlyDoneRows(t *testing.T) {
	t.Parallel()

	in := Report{
		Bindings: []BindingStatus{
			{Name: "first", State: string(store.StateActive)},
			{Name: "second", State: string(store.StateDone)},
			{Name: "third", State: string(store.StateBroken)},
		},
	}

	got := HideDone(in)

	if len(in.Bindings) != 3 {
		t.Fatalf("HideDone modified input report: len = %d, want 3", len(in.Bindings))
	}
	if got.DoneHidden != 1 {
		t.Errorf("DoneHidden = %d, want 1", got.DoneHidden)
	}
	if len(got.Bindings) != 2 {
		t.Fatalf("got %d bindings, want 2", len(got.Bindings))
	}
	if got.Bindings[0].Name != "first" || got.Bindings[1].Name != "third" {
		t.Errorf("bindings = %+v, want first and third in original order", got.Bindings)
	}
}
func TestRenderStatusFooterCountsHidden(t *testing.T) {
	t.Parallel()

	cases := []struct {
		hidden    int
		wantSub   string
		wantNoSub string
	}{
		{hidden: 3, wantSub: "3 done · relevo unbind --done to clear"},
		{hidden: 1, wantSub: "1 done · relevo unbind --done to clear"},
		{hidden: 0, wantNoSub: "done ·"},
	}

	for _, tc := range cases {
		r := Report{
			Bindings: []BindingStatus{
				{Name: "live", CWD: "/repo", Round: 1, Display: "ACTIVE"},
			},
			DoneHidden: tc.hidden,
		}
		out := RenderStatus(r)
		if tc.wantSub != "" && !strings.Contains(out, tc.wantSub) {
			t.Errorf("hidden=%d: RenderStatus output missing %q:\n%s", tc.hidden, tc.wantSub, out)
		}
		if tc.wantNoSub != "" && strings.Contains(out, tc.wantNoSub) {
			t.Errorf("hidden=%d: RenderStatus output should not contain %q:\n%s", tc.hidden, tc.wantNoSub, out)
		}
	}
}
func TestRenderStatusFooterOnlyWhenEverythingIsDone(t *testing.T) {
	t.Parallel()

	r := Report{
		Bindings:   nil,
		DoneHidden: 2,
	}
	out := RenderStatus(r)
	if !strings.Contains(out, "2 done · relevo unbind --done to clear") {
		t.Errorf("RenderStatus output missing footer:\n%s", out)
	}
	if strings.Contains(out, "no bindings") {
		t.Errorf("RenderStatus output should not contain 'no bindings':\n%s", out)
	}
}
func TestRenderStatusGatedBlock(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	t.Cleanup(availability.SetGateClock(func() time.Time { return now }))
	r := Report{
		Bindings: []BindingStatus{{
			Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
			MasterMindKind: "claude", BuilderKind: "agy", BuilderStatus: "working",
			BuilderCandidate: testAgyRef,
		}},
		Gated: []availability.Gate{
			{Token: testAgyRef, Name: "agy-m", Kind: availability.SpawnFailed, Since: now, Until: now.Add(10 * time.Minute)},
			{Token: testClaudeRef, Name: "claude-m", Kind: availability.RateLimited, Since: now, Until: time.Time{}},
		},
	}

	out := RenderStatus(r)
	if !strings.Contains(out, "candidates\n") {
		t.Fatalf("missing candidates block:\n%s", out)
	}
	rest := out[strings.Index(out, "candidates\n")+len("candidates\n"):]
	lines := strings.SplitN(rest, "\n", 3)
	if len(lines) < 2 {
		t.Fatalf("expected two gate rows, got:\n%s", rest)
	}
	wantUntil := "until " + now.Add(10*time.Minute).Local().Format("15:04")
	if !strings.Contains(lines[0], "agy-m") || !strings.Contains(lines[0], "spawn failed") || !strings.Contains(lines[0], wantUntil) {
		t.Errorf("row 0 = %q", lines[0])
	}
	if strings.Contains(lines[0], testAgyRef) {
		t.Errorf("row 0 must print the gate's name, not its token: %q", lines[0])
	}
	if !strings.Contains(lines[1], "claude-m") || !strings.Contains(lines[1], "rate-limited") || !strings.Contains(lines[1], "until cleared") {
		t.Errorf("row 1 = %q", lines[1])
	}
}

// TestRenderStatusGatedBlockFallsBackToToken pins the other half:
// a gate with no name -- one built without a candidate set -- prints its
// token.
func TestRenderStatusGatedBlockFallsBackToToken(t *testing.T) {
	t.Parallel()

	r := Report{
		Gated: []availability.Gate{{Token: testAgyRef, Kind: availability.RateLimited, Until: time.Time{}}},
	}

	out := RenderStatus(r)
	if !strings.Contains(out, testAgyRef) {
		t.Errorf("RenderStatus =\n%s\nwant it naming %q", out, testAgyRef)
	}
}
func TestRenderStatusNoGatesIsUnchanged(t *testing.T) {
	t.Parallel()

	r := Report{
		Bindings: []BindingStatus{{
			Name: "webshop", CWD: "/repo", Round: 1, Display: "ACTIVE",
			MasterMindKind: "claude", BuilderKind: "agy", BuilderStatus: "working",
			BuilderCandidate: testAgyRef,
		}},
		Gated: nil,
	}

	out := RenderStatus(r)
	if strings.Contains(out, "candidates") {
		t.Errorf("no gates must render no candidates block:\n%s", out)
	}
}
func TestRenderStatusGatesWithNoBindings(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	r := Report{
		Gated: []availability.Gate{
			{Token: testAgyRef, Kind: availability.SpawnFailed, Since: now, Until: now.Add(10 * time.Minute)},
		},
	}

	out := RenderStatus(r)
	if !strings.HasPrefix(out, "no bindings\ncandidates\n") {
		t.Errorf("out = %q, want it to start with %q", out, "no bindings\ncandidates\n")
	}
}
func TestBindingStatusKey(t *testing.T) {
	t.Parallel()

	if got := (BindingStatus{Name: "api"}).Key(); got != "api" {
		t.Errorf("mastermind key = %q, want api", got)
	}
	if got := (BindingStatus{Name: "api", Owner: "SHA256:abc"}).Key(); got != "SHA256:abc/api" {
		t.Errorf("server key = %q, want SHA256:abc/api", got)
	}
}
func TestShortOwner(t *testing.T) {
	t.Parallel()

	if got := ShortOwner("SHA256:VLERFMZnvN5HSw/GCBr6FXPEgs4QeAfdU95BUhMMqI0"); got != "SHA256:VLERFMZnvN5H…" {
		t.Errorf("ShortOwner(fingerprint) = %q", got)
	}
	if got := ShortOwner("SHA256:short"); got != "SHA256:short" {
		t.Errorf("ShortOwner(short) = %q", got)
	}
	if got := ShortOwner("notanid"); got != "notanid" {
		t.Errorf("ShortOwner(prefix-less) = %q", got)
	}
}
func TestHideDoneKeepsGated(t *testing.T) {
	t.Parallel()

	in := Report{
		Bindings: []BindingStatus{{Name: "old", State: string(store.StateDone)}},
		Gated:    []availability.Gate{{Token: "agy/test/m", Kind: availability.RateLimited}},
	}
	out := HideDone(in)
	if out.DoneHidden != 1 || len(out.Bindings) != 0 {
		t.Fatalf("HideDone = %+v, want one hidden and no rows", out)
	}
	if len(out.Gated) != 1 || out.Gated[0].Token != "agy/test/m" {
		t.Fatalf("Gated = %+v, want the gate carried through", out.Gated)
	}
}
func TestRenderStatusShowsDirty(t *testing.T) {
	t.Parallel()

	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Display: "ACTIVE", Round: 2, Dirty: true, Consults: 1,
	}}}
	out := RenderStatus(r)
	if !strings.Contains(out, "round 2   ACTIVE dirty +1c") {
		t.Errorf("RenderStatus output missing 'dirty' after the display word:\n%s", out)
	}
}
func TestRenderStatusOmitsDirtyWhenClean(t *testing.T) {
	t.Parallel()

	r := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Display: "ACTIVE", Round: 2,
		LastClose: &CloseInfo{Round: 1, Tree: "dirty"}, // the raw fact, without the rule applied
	}}}
	if out := RenderStatus(r); strings.Contains(out, "dirty") {
		t.Errorf("rendered dirty from LastClose instead of Dirty:\n%s", out)
	}
}
func TestStatusTextUsageRowPrefersLive(t *testing.T) {
	t.Parallel()

	rep := Report{Bindings: []BindingStatus{{
		Name: "webshop", State: "active", Display: "ACTIVE", Round: 2,
		LastUsage: &usage.Usage{Harness: "agy", Provider: "google", Model: "gemini-3-pro", DurationMS: 6 * 60_000,
			Cost: usage.Cost{Basis: usage.Unknown}, Note: "agy keeps no usage record"},
		LiveUsage: &usage.Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash", DurationMS: 4 * 60_000,
			Tokens: usage.Tokens{In: 1_800, CacheRead: 91_000, CacheWrite: 3_100, Out: 8_200},
			Cost:   usage.Cost{USD: 0.04, Basis: usage.Measured}, Samples: 3},
	}}}
	text := RenderStatus(rep)
	lines := strings.Split(text, "\n")
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "  usage") {
			n++
			if !strings.HasPrefix(l, "  usage    live  ") {
				t.Errorf("usage row must be the live figure:\n%s", l)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d usage rows, want exactly one (the live one):\n%s", n, text)
	}
}
func TestStatusLiveUsageJSON(t *testing.T) {
	t.Parallel()

	with := BindingStatus{LiveUsage: &usage.Usage{Harness: "agy", Cost: usage.Cost{USD: 0.04, Basis: usage.Measured}}}
	raw, err := json.Marshal(with)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"live_usage"`) {
		t.Errorf("json = %s, want the live_usage key", raw)
	}
	raw, _ = json.Marshal(BindingStatus{})
	if strings.Contains(string(raw), "live_usage") {
		t.Errorf("json = %s, live_usage must be absent when nil", raw)
	}
}
func TestRenderStatusOutcome(t *testing.T) {
	t.Parallel()

	t.Run("prints outcome when halted", func(t *testing.T) {
		ts := time.Date(2026, 9, 18, 15, 4, 5, 0, time.UTC)
		rep := Report{
			Bindings: []BindingStatus{
				{
					Name: "b1", Round: 1, State: "active", Display: "ACTIVE",
					Last: &LastEvent{
						TS: ts, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
						Note: "noreport", Outcome: "halted",
					},
				},
			},
		}
		text := RenderStatus(rep)
		want := "last     " + ts.Local().Format("15:04:05") + " report to_planner round 1 (noreport) halted\n"
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in status text:\n%s", want, text)
		}
	})

	t.Run("prints nothing for done outcome", func(t *testing.T) {
		ts := time.Date(2026, 9, 18, 15, 4, 5, 0, time.UTC)
		rep := Report{
			Bindings: []BindingStatus{
				{
					Name: "b1", Round: 1, State: "active", Display: "ACTIVE",
					Last: &LastEvent{
						TS: ts, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
						Outcome: "done",
					},
				},
			},
		}
		text := RenderStatus(rep)
		want := "last     " + ts.Local().Format("15:04:05") + " report to_planner round 1\n"
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in status text:\n%s", want, text)
		}
		if strings.Contains(text, "done") {
			t.Errorf("status text should not print 'done' outcome:\n%s", text)
		}
	})
}

// TestRoundFacts fixtures.
var rfT0 = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
var rfT1 = rfT0.Add(1 * time.Minute)
var rfT2 = rfT0.Add(2 * time.Minute)
var rfT3 = rfT0.Add(3 * time.Minute)
var rfU1 = &usage.Usage{Tokens: usage.Tokens{In: 1000}}

var roundFactsCases = []struct {
	name      string
	entries   []store.LogEntry
	wantStart time.Time
	wantEnd   time.Time
	wantUsage *usage.Usage
}{
	{
		name:      "empty log",
		entries:   nil,
		wantStart: time.Time{},
		wantEnd:   time.Time{},
		wantUsage: nil,
	},
	{
		name: "plan r1",
		entries: []store.LogEntry{
			{TS: rfT0, Round: 1, Kind: store.KindPlan, Direction: store.DirToBuilder},
		},
		wantStart: rfT0,
		wantEnd:   time.Time{},
		wantUsage: nil,
	},
	{
		name: "plan r1, report r1 with usage",
		entries: []store.LogEntry{
			{TS: rfT0, Round: 1, Kind: store.KindPlan, Direction: store.DirToBuilder},
			{TS: rfT1, Round: 1, Kind: store.KindReport, Direction: store.DirToMasterMind, Usage: rfU1},
		},
		wantStart: rfT0,
		wantEnd:   rfT1,
		wantUsage: rfU1,
	},
	{
		name: "plan r1, report r1 (usage U1), plan r2",
		entries: []store.LogEntry{
			{TS: rfT0, Round: 1, Kind: store.KindPlan, Direction: store.DirToBuilder},
			{TS: rfT1, Round: 1, Kind: store.KindReport, Direction: store.DirToMasterMind, Usage: rfU1},
			{TS: rfT2, Round: 2, Kind: store.KindPlan, Direction: store.DirToBuilder},
		},
		wantStart: rfT2,
		wantEnd:   time.Time{},
		wantUsage: nil,
	},
	{
		name: "plan r2, later plan r2 (nudge or switch), report r2",
		entries: []store.LogEntry{
			{TS: rfT0, Round: 2, Kind: store.KindPlan, Direction: store.DirToBuilder},
			{TS: rfT1, Round: 2, Kind: store.KindPlan, Direction: store.DirToBuilder},
			{TS: rfT2, Round: 2, Kind: store.KindReport, Direction: store.DirToMasterMind, Usage: rfU1},
		},
		wantStart: rfT0,
		wantEnd:   rfT2,
		wantUsage: rfU1,
	},
	{
		name: "plan r1, report r1 (U1), plan r2, report r2 with nil usage",
		entries: []store.LogEntry{
			{TS: rfT0, Round: 1, Kind: store.KindPlan, Direction: store.DirToBuilder},
			{TS: rfT1, Round: 1, Kind: store.KindReport, Direction: store.DirToMasterMind, Usage: rfU1},
			{TS: rfT2, Round: 2, Kind: store.KindPlan, Direction: store.DirToBuilder},
			{TS: rfT3, Round: 2, Kind: store.KindReport, Direction: store.DirToMasterMind, Usage: nil},
		},
		wantStart: rfT2,
		wantEnd:   rfT3,
		wantUsage: nil,
	},
	{
		name: "plan r1, findings entry with usage, question and answer entries",
		entries: []store.LogEntry{
			{TS: rfT0, Round: 1, Kind: store.KindPlan, Direction: store.DirToBuilder},
			{TS: rfT1, Round: 1, Kind: store.KindFindings, Direction: store.DirToMasterMind, Usage: rfU1},
			{TS: rfT2, Round: 1, Kind: store.KindQuestion, Direction: store.DirToMasterMind},
			{TS: rfT3, Round: 1, Kind: store.KindAnswer, Direction: store.DirToBuilder},
		},
		wantStart: rfT0,
		wantEnd:   time.Time{},
		wantUsage: nil,
	},
	{
		name: "plan r1, plan r2, late report r1 with usage",
		entries: []store.LogEntry{
			{TS: rfT0, Round: 1, Kind: store.KindPlan, Direction: store.DirToBuilder},
			{TS: rfT1, Round: 2, Kind: store.KindPlan, Direction: store.DirToBuilder},
			{TS: rfT2, Round: 1, Kind: store.KindReport, Direction: store.DirToMasterMind, Usage: rfU1},
		},
		wantStart: rfT1,
		wantEnd:   time.Time{},
		wantUsage: nil,
	},
}

func TestRoundFacts(t *testing.T) {
	t.Parallel()

	for _, tt := range roundFactsCases {
		t.Run(tt.name, func(t *testing.T) {
			gotStart, gotEnd, gotUsage := RoundFacts(tt.entries)
			if !gotStart.Equal(tt.wantStart) {
				t.Errorf("start = %v, want %v", gotStart, tt.wantStart)
			}
			if !gotEnd.Equal(tt.wantEnd) {
				t.Errorf("end = %v, want %v", gotEnd, tt.wantEnd)
			}
			if tt.wantUsage == nil {
				if gotUsage != nil {
					t.Errorf("usage = %+v, want nil", gotUsage)
				}
			} else {
				if gotUsage == nil {
					t.Fatalf("usage is nil, want %+v", tt.wantUsage)
				}
				if *gotUsage != *tt.wantUsage {
					t.Errorf("usage = %+v, want %+v", gotUsage, tt.wantUsage)
				}
				for _, e := range tt.entries {
					if e.Usage != nil && gotUsage == e.Usage {
						t.Errorf("gotUsage must be a fresh copy, but shares pointer with log entry")
					}
				}
			}
		})
	}
}

// TestApplyRemoteLive fixtures.
var arlNow = time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
var arlLogPath = "/tmp/test.log"

var applyRemoteLiveCases = []struct {
	name          string
	lf            *store.LiveFacts
	stalled       bool
	wantWord      string
	wantExploring string
}{
	{
		name: "working",
		lf: &store.LiveFacts{
			PID:   1001,
			Usage: &usage.Usage{Tokens: usage.Tokens{In: 1000, Out: 500}},
			Diff:  &store.DiffFacts{Files: 1, Added: 5, Removed: 2},
		},
		stalled:       false,
		wantWord:      "working",
		wantExploring: "",
	},
	{
		name: "exploring",
		lf: &store.LiveFacts{
			PID:            1002,
			Usage:          &usage.Usage{Tokens: usage.Tokens{In: 2000, Out: 600}},
			Diff:           &store.DiffFacts{Files: 2, Added: 10, Removed: 4},
			ExploringSince: arlNow.Add(-30 * time.Second),
		},
		stalled:       false,
		wantWord:      "exploring 30s",
		wantExploring: "exploring 30s",
	},
	{
		name: "exploring_stalled",
		lf: &store.LiveFacts{
			PID:            1003,
			Usage:          &usage.Usage{Tokens: usage.Tokens{In: 3000, Out: 700}},
			Diff:           &store.DiffFacts{Files: 3, Added: 15, Removed: 6},
			ExploringSince: arlNow.Add(-45 * time.Second),
		},
		stalled:       true,
		wantWord:      "exploring 45s",
		wantExploring: "",
	},
	{
		name: "gating",
		lf: &store.LiveFacts{
			PID:         1004,
			Usage:       &usage.Usage{Tokens: usage.Tokens{In: 4000, Out: 800}},
			Diff:        &store.DiffFacts{Files: 4, Added: 20, Removed: 8},
			GatingSince: arlNow.Add(-15 * time.Second),
		},
		stalled:       false,
		wantWord:      "gating 15s",
		wantExploring: "",
	},
	{
		name: "exit_3",
		lf: &store.LiveFacts{
			PID:      1005,
			Usage:    &usage.Usage{Tokens: usage.Tokens{In: 5000, Out: 900}},
			Diff:     &store.DiffFacts{Files: 5, Added: 25, Removed: 10},
			ExitCode: "3",
		},
		stalled:       false,
		wantWord:      "exited 3",
		wantExploring: "",
	},
	{
		name: "exit_unknown",
		lf: &store.LiveFacts{
			PID:      1006,
			Usage:    &usage.Usage{Tokens: usage.Tokens{In: 6000, Out: 1000}},
			Diff:     &store.DiffFacts{Files: 6, Added: 30, Removed: 12},
			ExitCode: "unknown",
		},
		stalled:       false,
		wantWord:      "exited",
		wantExploring: "",
	},
}

func TestApplyRemoteLive(t *testing.T) {
	t.Parallel()

	for _, tt := range applyRemoteLiveCases {
		t.Run(tt.name, func(t *testing.T) {
			var row BindingStatus
			ApplyRemoteLive(&row, tt.lf, tt.stalled, arlLogPath, arlNow)

			if row.BuilderStatus != tt.wantWord {
				t.Errorf("BuilderStatus = %q, want %q", row.BuilderStatus, tt.wantWord)
			}
			if row.Exploring != tt.wantExploring {
				t.Errorf("Exploring = %q, want %q", row.Exploring, tt.wantExploring)
			}
			if row.Headless == nil || row.Headless.PID != tt.lf.PID {
				t.Errorf("Headless.PID = %v, want %d", row.Headless, tt.lf.PID)
			}
			if row.Headless == nil || row.Headless.LogPath != arlLogPath {
				t.Errorf("Headless.LogPath = %v, want %q", row.Headless, arlLogPath)
			}
			if row.LiveUsage == nil || row.LiveUsage.Tokens != tt.lf.Usage.Tokens {
				t.Errorf("LiveUsage tokens = %v, want %v", row.LiveUsage, tt.lf.Usage.Tokens)
			}
			if row.Live == nil || row.Live.Files != tt.lf.Diff.Files || row.Live.Added != tt.lf.Diff.Added || row.Live.Removed != tt.lf.Diff.Removed {
				t.Errorf("Live = %+v, want Files:%d Added:%d Removed:%d", row.Live, tt.lf.Diff.Files, tt.lf.Diff.Added, tt.lf.Diff.Removed)
			}
		})
	}
}
func TestProcessWord(t *testing.T) {
	t.Parallel()

	rem := BindingStatus{Server: "contabo"}
	if got := rem.ProcessWord(); got != "remote" {
		t.Errorf("ProcessWord with server = %q, want remote", got)
	}
	local := BindingStatus{Server: ""}
	if got := local.ProcessWord(); got != "headless" {
		t.Errorf("ProcessWord without server = %q, want headless", got)
	}
}
func TestPriorTokensOf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		round   int
		entries []store.LogEntry
		want    usage.Tokens
	}{
		{
			name:    "no switches",
			round:   1,
			entries: []store.LogEntry{{Round: 1, Kind: store.KindPlan}},
			want:    usage.Tokens{},
		},
		{
			name:  "two switch entries in the round",
			round: 1,
			entries: []store.LogEntry{
				{Round: 1, Kind: store.KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}}},
				{Round: 1, Kind: store.KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 200, Out: 30}}},
			},
			want: usage.Tokens{In: 300, Out: 80},
		},
		{
			name:  "switch entry in another round ignored",
			round: 1,
			entries: []store.LogEntry{
				{Round: 1, Kind: store.KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}}},
				{Round: 2, Kind: store.KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 500, Out: 500}}},
			},
			want: usage.Tokens{In: 100, Out: 50},
		},
		{
			name:  "report with PriorTokens added",
			round: 1,
			entries: []store.LogEntry{
				{Round: 1, Kind: store.KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}}},
				{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, PriorTokens: &usage.Tokens{In: 400, Out: 150}},
			},
			want: usage.Tokens{In: 500, Out: 200},
		},
		{
			name:  "switch entry with nil Usage skipped",
			round: 1,
			entries: []store.LogEntry{
				{Round: 1, Kind: store.KindSwitch, Usage: nil},
				{Round: 1, Kind: store.KindSwitch, Usage: &usage.Usage{Tokens: usage.Tokens{In: 150, Out: 25}}},
			},
			want: usage.Tokens{In: 150, Out: 25},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PriorTokensOf(tc.entries, tc.round)
			if got != tc.want {
				t.Errorf("PriorTokensOf() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
