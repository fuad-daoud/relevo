package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestDefaultWaitRound(t *testing.T) {
	t.Parallel()

	t.Run("no entries falls back to b.Round", func(t *testing.T) {
		b := store.Binding{Round: 1}
		if got := DefaultWaitRound(b, nil); got != 1 {
			t.Errorf("DefaultWaitRound = %d, want 1", got)
		}
	})

	t.Run("an open round's plan entry is the default", func(t *testing.T) {
		b := store.Binding{Round: 3}
		entries := []store.LogEntry{
			{Round: 3, Direction: store.DirToBuilder, Kind: store.KindPrompt},
		}
		if got := DefaultWaitRound(b, entries); got != 3 {
			t.Errorf("DefaultWaitRound = %d, want 3", got)
		}
	})

	t.Run("after a close, the newest planned round wins over b.Round", func(t *testing.T) {
		b := store.Binding{Round: 4}
		entries := []store.LogEntry{
			{Round: 3, Direction: store.DirToBuilder, Kind: store.KindPrompt},
			{Round: 3, Direction: store.DirToMasterMind, Kind: store.KindReport},
		}
		if got := DefaultWaitRound(b, entries); got != 3 {
			t.Errorf("DefaultWaitRound = %d, want 3", got)
		}
	})

	t.Run("a legacy plan entry still names the round", func(t *testing.T) {
		b := store.Binding{Round: 4}
		entries := []store.LogEntry{
			{Round: 2, Direction: store.DirToBuilder, Kind: store.Kind("plan")},
		}
		if got := DefaultWaitRound(b, entries); got != 2 {
			t.Errorf("DefaultWaitRound = %d, want 2 (the legacy plan entry counts)", got)
		}
	})

	t.Run("a nudge is not a send", func(t *testing.T) {
		b := store.Binding{Round: 4}
		entries := []store.LogEntry{
			{Round: 3, Direction: store.DirToBuilder, Kind: store.KindPrompt},
			{Round: 3, Direction: store.DirToMasterMind, Kind: store.KindReport},
			{Round: 4, Direction: store.DirToBuilder, Kind: store.KindPrompt, Note: nudgeNote},
		}
		if got := DefaultWaitRound(b, entries); got != 3 {
			t.Errorf("DefaultWaitRound = %d, want 3 (the round-4 entry is a nudge, not a send)", got)
		}
	})
}

func TestWaitOutcome(t *testing.T) {
	t.Parallel()

	noQuestion := mapQuestion(nil)

	t.Run("a marked close is WaitClosed", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Note: ""},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("marked close carrying a gate result is WaitClosed (0)", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Note: "gate=pass"},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("marked close carrying a stopped note is WaitClosed (0)", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Note: "stopped"},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("marked report with Outcome: halted is WaitHalted (5)", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Outcome: reporttail.OutcomeHalted},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitHalted, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("unmarked with Outcome: blocked is WaitHalted (5)", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Note: "unmarked", Outcome: reporttail.OutcomeBlocked},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitHalted, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("marked done is WaitClosed (0)", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Outcome: reporttail.OutcomeDone},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("marked unstructured is WaitClosed (0)", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Outcome: reporttail.OutcomeUnstructured},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("deferred is WaitClosed (0)", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Outcome: reporttail.OutcomeDeferred},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("marked close with a scope verdict is WaitClosed (0)", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Note: "scope=ok"},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("marked close that escaped is WaitClosed (0)", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Note: "escaped"},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("marked close with a remote-catchup note is WaitClosed (0)", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{
				Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
				Path: "/x/001-report.md", Note: "uncommitted work at refs/relevo/ev-wait/round-1",
			},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("marked close refused by scope is WaitHalted (5)", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{
				Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
				Path: "/x/001-report.md", Note: "scope=refused", Outcome: reporttail.OutcomeHalted,
			},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitHalted, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("a joined no-marker note is WaitUnmarked", func(t *testing.T) {
		cases := []struct {
			note string
			line string
		}{
			{note: "noreport stopped", line: "/x/001-report.md"},
			{note: "unmarked escaped", line: "/x/001-report.md"},
			// The dash line is reserved for a bare "noreport": a joined note
			// keeps the report path, since the close did write one.
			{note: "noreport gate=fail", line: "/x/001-report.md"},
		}
		for _, tc := range cases {
			t.Run(tc.note, func(t *testing.T) {
				b := store.Binding{Round: 1}
				entries := []store.LogEntry{
					{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Note: tc.note},
				}
				got := WaitOutcome(b, entries, 1, noQuestion)
				want := WaitResult{Code: WaitUnmarked, Line: tc.line, Done: true}
				if got != want {
					t.Errorf("WaitOutcome = %+v, want %+v", got, want)
				}
			})
		}
	})

	t.Run("unmarked is WaitUnmarked with the report path", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Note: "unmarked"},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitUnmarked, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("scraped is WaitUnmarked with the report path", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Note: "scraped"},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitUnmarked, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("noreport is WaitUnmarked with a dash", func(t *testing.T) {
		b := store.Binding{Round: 1}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Note: "noreport"},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitUnmarked, Line: "-", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("DONE with no report is WaitGone", func(t *testing.T) {
		b := store.Binding{Round: 1, State: store.StateDone}
		got := WaitOutcome(b, nil, 1, noQuestion)
		want := WaitResult{Code: WaitGone, Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("needs_you with a halt is WaitNeedsYou", func(t *testing.T) {
		b := store.Binding{
			Round: 1, State: store.StateNeedsYou,
			Halt: "round 1 has run past 2h0m0s", HaltAt: time.Unix(1757000000, 0).UTC(),
		}
		got := WaitOutcome(b, nil, 1, noQuestion)
		if got.Code != WaitNeedsYou || got.Line != b.Halt || !got.Done {
			t.Errorf("WaitOutcome = %+v", got)
		}
	})

	t.Run("active with an open, sent round and no report is not done", func(t *testing.T) {
		b := store.Binding{Round: 1, State: store.StateActive}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		if got.Done {
			t.Errorf("WaitOutcome = %+v, want Done == false", got)
		}
	})

	t.Run("active with a legacy plan entry is still open, not never-sent", func(t *testing.T) {
		b := store.Binding{Round: 1, State: store.StateActive}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToBuilder, Kind: store.Kind("plan")},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		if got.Done || got.Code == WaitNotStarted {
			t.Errorf("WaitOutcome = %+v, want the legacy round read as open", got)
		}
	})

	t.Run("DONE with a report for the asked round: the report wins", func(t *testing.T) {
		b := store.Binding{Round: 2, State: store.StateDone}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Note: ""},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("broken after a clean close, asked for the closed round, still reports the close", func(t *testing.T) {
		b := store.Binding{Round: 2, State: store.StateBroken}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Note: ""},
		}
		got := WaitOutcome(b, entries, 1, noQuestion)
		want := WaitResult{Code: WaitClosed, Line: "/x/001-report.md", Done: true}
		if got != want {
			t.Errorf("WaitOutcome = %+v, want %+v", got, want)
		}
	})

	t.Run("the same broken binding asked for the next, unsent round needs a human", func(t *testing.T) {
		b := store.Binding{Round: 2, State: store.StateBroken}
		entries := []store.LogEntry{
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/001-report.md", Note: ""},
		}
		got := WaitOutcome(b, entries, 2, noQuestion)
		if got.Code != WaitNeedsYou || !got.Done {
			t.Errorf("WaitOutcome = %+v", got)
		}
	})
}

// manualSent seeds a binding one round into an open, active send, using only
// the store -- no Bind/Send -- so a Wait test can build several independent
// bindings without wiring a shared fixture.
func manualSent(t *testing.T, rt Runtime, name, cwd string) store.Binding {
	t.Helper()
	b := store.Binding{
		Name: name, CWD: cwd, Round: 1, State: store.StateActive,
		RoundStartedAt: rt.Now().UTC(),
		MasterMind:     store.Endpoint{PaneID: "w2:p3"},
		Builder:        store.Endpoint{PaneID: "w2:p4"},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save %s: %v", name, err)
	}
	if err := rt.Store.AppendLog(name, store.LogEntry{
		TS: rt.Now().UTC(), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt,
		Path: rt.Store.PromptPath(name, 1), Confirmed: true,
	}); err != nil {
		t.Fatalf("AppendLog %s: %v", name, err)
	}
	return b
}

func TestWaitAnyReturnsTheFirstThatCloses(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	manualSent(t, rt, "first", "/repo/first")
	manualSent(t, rt, "second", "/repo/second")

	reportPath := rt.Store.ReportPath("second", 1)
	if err := rt.Store.AppendLog("second", store.LogEntry{
		TS: rt.Now().UTC(), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
		Path: reportPath, Payload: "done",
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	name, res, err := Wait(ctx, rt, WaitOptions{Names: []string{"first", "second"}, Timeout: time.Minute, Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "second" || res.Code != WaitClosed {
		t.Errorf("Wait = (%q, %+v), want second closed", name, res)
	}
}

func TestWaitNamesUnknownBindingIsAnError(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _, err := Wait(ctx, rt, WaitOptions{Names: []string{"nope"}, Timeout: time.Minute, Interval: time.Millisecond})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v, want it to wrap store.ErrNotFound", err)
	}
}

func TestWaitReturnsAtOnceWhenAlreadyClosed(t *testing.T) {
	t.Parallel()

	rt, _ := sentBinding(t)
	reportPath := rt.Store.ReportPath("webshop", 1)
	if err := rt.Store.AppendLog("webshop", store.LogEntry{
		TS: rt.Now().UTC(), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
		Path: reportPath, Payload: "done",
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Interval is an hour: if the loop slept before checking, the 5s guard
	// fires first and the test fails with context deadline exceeded.
	name, res, err := Wait(ctx, rt, WaitOptions{Names: []string{"webshop"}, Timeout: time.Minute, Interval: time.Hour})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "webshop" || res.Code != WaitClosed || res.Line != reportPath || !res.Done {
		t.Errorf("Wait = (%q, %+v), want (webshop, {%d %q true})", name, res, WaitClosed, reportPath)
	}
}

func TestWaitGoneWhenUnboundMidWait(t *testing.T) {
	t.Parallel()

	rt, _ := sentBinding(t)

	calls := 0
	rt.Now = func() time.Time {
		calls++
		if calls == 4 {
			_ = rt.Store.Delete("webshop")
		}
		return baseTime
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	name, res, err := Wait(ctx, rt, WaitOptions{Names: []string{"webshop"}, Timeout: time.Minute, Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "webshop" || res.Code != WaitGone || !res.Done {
		t.Errorf("Wait = (%q, %+v), want (webshop, {%d ... true})", name, res, WaitGone)
	}
}

func TestWaitTimesOut(t *testing.T) {
	t.Parallel()

	rt, _ := sentBinding(t)

	tick := 0
	rt.Now = func() time.Time {
		now := baseTime.Add(time.Duration(tick) * time.Minute)
		tick++
		return now
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	name, res, err := Wait(ctx, rt, WaitOptions{Names: []string{"webshop"}, Timeout: 5 * time.Minute, Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "" || res.Code != WaitTimeout || !res.Done {
		t.Errorf("Wait = (%q, %+v), want (\"\", {%d ... true})", name, res, WaitTimeout)
	}
	// §4.1: a timeout prints nothing extra and delivers nothing.
	if res.Payload != "" || res.DeliverErr != nil {
		t.Errorf("Wait payload = %q (err %v), want nothing delivered on a timeout", res.Payload, res.DeliverErr)
	}
}

func TestWaitNotStartedReturnsAtOnce(t *testing.T) {
	t.Parallel()

	rt, _ := sentBinding(t)

	// A binding that was never sent: no log entries at all.
	unsent := store.Binding{
		Name: "unsent", CWD: "/repo/unsent", Round: 1, State: store.StateActive,
	}
	if err := rt.Store.Save(unsent); err != nil {
		t.Fatalf("Save: %v", err)
	}

	tick := 0
	rt.Now = func() time.Time {
		now := baseTime.Add(time.Duration(tick) * time.Minute)
		tick++
		return now
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	name, res, err := Wait(ctx, rt, WaitOptions{Names: []string{"unsent"}, Timeout: time.Hour, Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "unsent" || res.Code != WaitNotStarted || !res.Done {
		t.Fatalf("Wait = (%q, %+v), want (\"unsent\", {%d ... true})", name, res, WaitNotStarted)
	}
	if !strings.Contains(res.Line, "never sent") {
		t.Fatalf("Wait line = %q, want it to say never sent", res.Line)
	}
	if tick > 1 {
		t.Fatalf("Now called %d times, want at most 1: it must not poll to the timeout", tick)
	}
}

func TestWaitExplicitUnsentRoundReturnsAtOnce(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Round 3 has no plan entry: nothing is in flight.
	name, res, err := Wait(ctx, rt, WaitOptions{Names: []string{b.Name}, Round: 3, Timeout: time.Hour, Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != b.Name || res.Code != WaitNotStarted || !res.Done {
		t.Fatalf("Wait = (%q, %+v), want (%q, {%d ... true})", name, res, b.Name, WaitNotStarted)
	}
	if !strings.Contains(res.Line, "never sent") {
		t.Fatalf("Wait line = %q, want it to say never sent", res.Line)
	}

	// Round 1 was sent: the same binding keeps waiting and times out.
	tick := 0
	rt.Now = func() time.Time {
		now := baseTime.Add(time.Duration(tick) * time.Minute)
		tick++
		return now
	}
	name, res, err = Wait(ctx, rt, WaitOptions{Names: []string{b.Name}, Round: 1, Timeout: 5 * time.Minute, Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "" || res.Code != WaitTimeout || !res.Done {
		t.Fatalf("Wait = (%q, %+v), want (\"\", {%d ... true})", name, res, WaitTimeout)
	}
}

func TestWaitDefaultRoundIsTheNewestPlanned(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	reportPath := rt.Store.ReportPath("webshop", 1)
	if err := rt.Store.AppendLog("webshop", store.LogEntry{
		TS: rt.Now().UTC(), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
		Path: reportPath, Payload: "done",
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	// Leave b.Round at the next, unsent round: no plan entry exists for it.
	b.Round = 2
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Round: 0 must default to round 1 (the newest planned round), not
	// b.Round (2, which never closes) -- exit 0, not a 124 timeout.
	name, res, err := Wait(ctx, rt, WaitOptions{Names: []string{"webshop"}, Round: 0, Timeout: time.Minute, Interval: time.Millisecond})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "webshop" || res.Code != WaitClosed || res.Line != reportPath || !res.Done {
		t.Errorf("Wait = (%q, %+v), want closed round 1", name, res)
	}
}

// TestWaitDeliversThePendingReport pins §4.1's core case: a seeded binding
// with a queued report. Wait prints the outcome and then the report text, and
// marks it delivered with route "wait".
func TestWaitDeliversThePendingReport(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	name, res, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{"webshop"}, Timeout: time.Minute, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "webshop" || res.Code != WaitClosed || !res.Done {
		t.Fatalf("Wait = (%q, %+v), want the seeded round closed", name, res)
	}
	if !strings.Contains(res.Payload, "round 1 report") {
		t.Errorf("Payload = %q, want the pending report's text", res.Payload)
	}
	if res.DeliverErr != nil {
		t.Errorf("DeliverErr = %v, want nil", res.DeliverErr)
	}
	if _, still, err := rt.Store.PendingForMasterMind("webshop"); err != nil || still {
		t.Errorf("the report must be delivered (still pending=%v err=%v)", still, err)
	}
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	last := entries[len(entries)-1]
	if !last.Confirmed || last.Route != "wait" {
		t.Errorf("entry = confirmed:%v route:%q, want confirmed with route=wait", last.Confirmed, last.Route)
	}
}

// TestWaitPeekLeavesTheReportPending: --peek reports the outcome only and
// leaves the entry pending (§4.1).
func TestWaitPeekLeavesTheReportPending(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	seedPending(t, rt, "webshop", "pl_aaaaaaaabbbb", "claude")

	name, res, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{"webshop"}, Timeout: time.Minute, Interval: time.Millisecond, Peek: true,
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "webshop" || res.Code != WaitClosed || !res.Done {
		t.Fatalf("Wait = (%q, %+v), want the seeded round closed", name, res)
	}
	if res.Payload != "" {
		t.Errorf("Payload = %q, want nothing delivered with --peek", res.Payload)
	}
	if _, still, err := rt.Store.PendingForMasterMind("webshop"); err != nil || !still {
		t.Errorf("--peek must leave the entry pending (still pending=%v err=%v)", still, err)
	}
}

// seedTwoRounds seeds an active binding with an undelivered round-1 report, a
// sent round 2 and its report. It is the fixture #433 is about: round 1's
// delivery failed, so it is still pending when round 2 closes.
func seedTwoRounds(t *testing.T, rt Runtime) (round1Path, round2Path string) {
	t.Helper()
	dir := t.TempDir()
	round1Path = filepath.Join(dir, "001-report.md")
	round2Path = filepath.Join(dir, "002-report.md")
	if err := os.WriteFile(round1Path, []byte("round 1 body\n"), 0o644); err != nil {
		t.Fatalf("write round 1 report: %v", err)
	}
	if err := os.WriteFile(round2Path, []byte("round 2 body\n"), 0o644); err != nil {
		t.Fatalf("write round 2 report: %v", err)
	}

	b := store.Binding{
		Name: "webshop", CWD: "/repo/webshop", Round: 2, State: store.StateActive,
		MasterMind: store.Endpoint{Kind: "claude", SessionID: "sess"}, MasterMindID: "pl_aaaaaaaabbbb",
		Builder: store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		for _, e := range []store.LogEntry{
			{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Path: rt.Store.PromptPath("webshop", 1), Confirmed: true},
			{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Payload: "round 1 report", Path: round1Path, Confirmed: false},
			{Round: 2, Direction: store.DirToBuilder, Kind: store.KindPrompt, Path: rt.Store.PromptPath("webshop", 2), Confirmed: true},
			{Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport, Payload: "round 2 report", Path: round2Path, Confirmed: false},
		} {
			if err := tx.AppendLog("webshop", e); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed two rounds: %v", err)
	}
	return round1Path, round2Path
}

// TestWaitDeliversTheWaitedRoundAfterAFailedOne is #433's regression: round 1's
// report was never delivered, round 2 then closed. Wait must print round 2's
// outcome line and round 2's report text last, carry round 1's under its own
// header, name round 2 in Round, and leave nothing pending.
func TestWaitDeliversTheWaitedRoundAfterAFailedOne(t *testing.T) {
	t.Parallel()

	rt := routeRuntime(t)
	round1Path, round2Path := seedTwoRounds(t, rt)

	name, res, err := Wait(context.Background(), rt, WaitOptions{
		Names: []string{"webshop"}, Timeout: time.Minute, Interval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if name != "webshop" || res.Code != WaitClosed || !res.Done {
		t.Fatalf("Wait = (%q, %+v), want the seeded round closed", name, res)
	}
	if res.Line != round2Path {
		t.Errorf("Line = %q, want round 2's report path %q", res.Line, round2Path)
	}
	if res.Round != 2 {
		t.Errorf("Round = %d, want 2", res.Round)
	}
	if !strings.HasSuffix(res.Payload, "round 2 body\n") {
		t.Errorf("Payload does not end with round 2's report text:\n%s", res.Payload)
	}
	header := fmt.Sprintf("── round 1: not delivered earlier (%s) ──", round1Path)
	if !strings.Contains(res.Payload, header) {
		t.Errorf("Payload does not carry round 1's header %q:\n%s", header, res.Payload)
	}
	if !strings.Contains(res.Payload, "round 1 body") {
		t.Errorf("Payload does not carry round 1's report text:\n%s", res.Payload)
	}
	if _, still, err := rt.Store.PendingForMasterMind("webshop"); err != nil || still {
		t.Errorf("a following pullPending must find nothing (still pending=%v err=%v)", still, err)
	}

	payload, found, err := delivery.Pull(context.Background(), rt.Store, "webshop", "wait")
	if err != nil {
		t.Fatalf("pullPending: %v", err)
	}
	if found || payload != "" {
		t.Errorf("pullPending after Wait = (%q, %v), want nothing pending", payload, found)
	}
}

func TestWaitOwned(t *testing.T) {
	t.Parallel()

	t.Run("owned-set resolution", func(t *testing.T) {
		rt := newRuntime(t)
		b1 := store.Binding{Name: "b1", MasterMindID: "mm1", State: store.StateActive, Round: 1, CWD: "/repo1"}
		b2 := store.Binding{Name: "b2", MasterMindID: "mm2", State: store.StateActive, Round: 1, CWD: "/repo2"}
		if err := rt.Store.Save(b1); err != nil {
			t.Fatalf("Save b1: %v", err)
		}
		if err := rt.Store.Save(b2); err != nil {
			t.Fatalf("Save b2: %v", err)
		}
		report := store.LogEntry{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/report.md",
		}
		if err := rt.Store.AppendLog("b1", report); err != nil {
			t.Fatalf("AppendLog b1: %v", err)
		}

		name, res, err := WaitOwned(context.Background(), rt, "mm1", 1, time.Minute, time.Millisecond)
		if err != nil {
			t.Fatalf("WaitOwned: %v", err)
		}
		if name != "b1" || res.Code != WaitClosed {
			t.Fatalf("WaitOwned = (%q, %d), want (b1, WaitClosed)", name, res.Code)
		}
	})

	t.Run("empty-set error", func(t *testing.T) {
		rt := newRuntime(t)
		_, _, err := WaitOwned(context.Background(), rt, "mm-missing", 0, time.Minute, time.Millisecond)
		if err == nil {
			t.Fatal("WaitOwned on empty set want error, got nil")
		}
		if !strings.Contains(err.Error(), "mm-missing") {
			t.Errorf("WaitOwned error %q must name mastermind id", err.Error())
		}
	})

	t.Run("done-exclusion", func(t *testing.T) {
		rt := newRuntime(t)
		bDone := store.Binding{Name: "bdone", MasterMindID: "mm-done", State: store.StateDone, Round: 1, CWD: "/repo"}
		if err := rt.Store.Save(bDone); err != nil {
			t.Fatalf("Save: %v", err)
		}
		_, _, err := WaitOwned(context.Background(), rt, "mm-done", 0, time.Minute, time.Millisecond)
		if err == nil {
			t.Fatal("WaitOwned with only done bindings want error, got nil")
		}

		bActive := store.Binding{Name: "bactive", MasterMindID: "mm-done", State: store.StateActive, Round: 1, CWD: "/repo"}
		if err := rt.Store.Save(bActive); err != nil {
			t.Fatalf("Save bActive: %v", err)
		}
		report := store.LogEntry{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/report.md",
		}
		if err := rt.Store.AppendLog("bactive", report); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
		name, res, err := WaitOwned(context.Background(), rt, "mm-done", 0, time.Minute, time.Millisecond)
		if err != nil {
			t.Fatalf("WaitOwned: %v", err)
		}
		if name != "bactive" || res.Code != WaitClosed {
			t.Fatalf("WaitOwned = (%q, %d), want (bactive, WaitClosed)", name, res.Code)
		}
	})

	t.Run("round-0 passthrough", func(t *testing.T) {
		rt := newRuntime(t)
		b := store.Binding{Name: "bround0", MasterMindID: "mm-r0", State: store.StateActive, Round: 1, CWD: "/repo"}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("Save: %v", err)
		}
		prompt := store.LogEntry{
			Round: 2, Direction: store.DirToBuilder, Kind: store.KindPrompt,
		}
		report := store.LogEntry{
			Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: "/x/002-report.md",
		}
		if err := rt.Store.AppendLog("bround0", prompt); err != nil {
			t.Fatalf("AppendLog prompt: %v", err)
		}
		if err := rt.Store.AppendLog("bround0", report); err != nil {
			t.Fatalf("AppendLog report: %v", err)
		}
		name, res, err := WaitOwned(context.Background(), rt, "mm-r0", 0, time.Minute, time.Millisecond)
		if err != nil {
			t.Fatalf("WaitOwned: %v", err)
		}
		if name != "bround0" || res.Round != 2 || res.Code != WaitClosed {
			t.Fatalf("WaitOwned = (%q, round %d, code %d), want (bround0, 2, WaitClosed)", name, res.Round, res.Code)
		}
	})
}
