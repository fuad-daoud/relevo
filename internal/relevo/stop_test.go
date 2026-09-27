package relevo

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// stopEntries returns the binding's KindStop entries, oldest first.
func stopEntries(t *testing.T, rt Runtime, name string) []store.LogEntry {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindStop {
			out = append(out, e)
		}
	}
	return out
}

func TestStopDecisionTable(t *testing.T) {
	t.Parallel()

	open := store.Binding{Round: 1, RoundStartedAt: baseTime}
	queued := store.Binding{Round: 1, QueuedAt: baseTime}

	cases := []struct {
		name string
		b    store.Binding
		want stopAction
	}{
		{"no round", store.Binding{Round: 1}, stopNothing},
		{"open round", open, stopKill},
		{"queued round", queued, stopDequeue},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stopDecision(c.b, baseTime); got != c.want {
				t.Errorf("stopDecision = %v, want %v", got, c.want)
			}
		})
	}
}

// TestStopPayload pins the exact payload and note a stopped close writes, for
// both haveReport values and both where values (local "" and remote).
func TestStopPayload(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		how         string
		round       int
		where       string
		reportPath  string
		haveReport  bool
		wantPayload string
		wantNote    string
	}{
		{
			name: "report on disk", how: "killed", round: 2,
			reportPath: "/s/reports/002.md", haveReport: true,
			wantPayload: "The runner was stopped (killed) for round 2. Report: relevo show webshop --round 2 --report",
			wantNote:    "stopped",
		},
		{
			name: "no report", how: "killed", round: 2,
			reportPath: "/s/reports/002.md", haveReport: false,
			wantPayload: "The runner was stopped (killed) for round 2; no report was written.",
			wantNote:    "noreport stopped",
		},
		{
			name: "reaped, no report", how: "reaped", round: 2,
			reportPath: "/s/reports/002.md", haveReport: false,
			wantPayload: "The runner was stopped (reaped) for round 2; no report was written.",
			wantNote:    "noreport stopped",
		},
		{
			name: "remote, report on disk", how: "killed", round: 2, where: " on zen",
			reportPath: "/s/reports/002.md", haveReport: true,
			wantPayload: "The runner was stopped (killed) for round 2 on zen. Report: relevo show webshop --round 2 --report",
			wantNote:    "stopped",
		},
		{
			name: "remote, no report", how: "dequeued", round: 3, where: " on zen",
			reportPath: "/s/reports/003.md", haveReport: false,
			wantPayload: "The runner was stopped (dequeued) for round 3 on zen; no report was written.",
			wantNote:    "noreport stopped",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			payload, note := stopPayload(c.how, "webshop", c.round, c.where, c.haveReport)
			if payload != c.wantPayload {
				t.Errorf("payload = %q, want %q", payload, c.wantPayload)
			}
			if note != c.wantNote {
				t.Errorf("note = %q, want %q", note, c.wantNote)
			}
		})
	}
}

// TestStopPaneRequestsAndRecords pins the pane path: the wrap-up prompt goes
// through the same delivery path as a plan, and the request and its grace are
// recorded while the round stays open.
// TestStopHeadlessKillsAndClosesWithoutSwitch pins design question 1's option
// (a): a headless round has no stdin, so a stop kills now and closes the
// round without a report -- never through the exit-without-report path, which
// would switch the builder and charge the round.
func TestStopHeadlessKillsAndClosesWithoutSwitch(t *testing.T) {
	t.Parallel()

	t.Run("no report", func(t *testing.T) {
		fr := newFakeRunner()
		rt, b := sentHeadless(t, fr)
		h := handleOf(b.Builder)

		res, err := Stop(context.Background(), rt, "webshop", StopOptions{})
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if res.Action != "killed" {
			t.Errorf("Action = %q, want killed", res.Action)
		}

		if len(fr.kills) != 1 || fr.kills[0] != h {
			t.Fatalf("kills = %+v, want the round's process %+v", fr.kills, h)
		}
		if len(fr.specs) != 1 {
			t.Errorf("specs = %d, want 1: a stop must not start a replacement", len(fr.specs))
		}

		got, err := rt.Store.Load("webshop")
		if err != nil {
			t.Fatal(err)
		}
		if got.Round != 2 {
			t.Errorf("Round = %d, want 2: the round closed", got.Round)
		}
		if got.RoundSwitches != 0 {
			t.Errorf("RoundSwitches = %d, want 0: a stop never charges a switch", got.RoundSwitches)
		}
		if got.RoundExcluded != nil {
			t.Errorf("RoundExcluded = %v, want it to exclude nobody", got.RoundExcluded)
		}
		if got.Builder.PID != 0 {
			t.Errorf("Builder.PID = %d, want 0 after the process was stopped", got.Builder.PID)
		}

		pending, found, err := rt.Store.PendingForPlanner("webshop")
		if err != nil || !found {
			t.Fatalf("report must be queued: found=%v err=%v", found, err)
		}
		if !strings.Contains(pending.Note, "noreport stopped") {
			t.Errorf("report note = %q, want noreport stopped", pending.Note)
		}
		stops := stopEntries(t, rt, "webshop")
		if len(stops) != 1 || stops[0].Note != "stopped/killed" {
			t.Errorf("stop entries = %+v, want one stopped/killed", stops)
		}
	})

	t.Run("with a report on disk", func(t *testing.T) {
		fr := newFakeRunner()
		rt, _ := sentHeadless(t, fr)
		reportPath := rt.Store.ReportPath("webshop", 1)
		if err := os.WriteFile(reportPath, []byte("I stopped where I was\n"), 0o644); err != nil {
			t.Fatalf("write report: %v", err)
		}

		if _, err := Stop(context.Background(), rt, "webshop", StopOptions{}); err != nil {
			t.Fatalf("Stop: %v", err)
		}

		pending, found, err := rt.Store.PendingForPlanner("webshop")
		if err != nil || !found {
			t.Fatalf("report must be queued: found=%v err=%v", found, err)
		}
		if !strings.Contains(pending.Note, "stopped") {
			t.Errorf("report note = %q, want it to say stopped", pending.Note)
		}
		if !strings.Contains(pending.Payload, "relevo show webshop --round 1 --report") {
			t.Errorf("payload = %q, must name the show command", pending.Payload)
		}
	})
}

// deadRunner loads webshop and clears its recorded process, so a Stop sees a
// round whose runner has already gone.
func deadRunner(t *testing.T, rt Runtime) {
	t.Helper()
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	b.Builder = clearProcess(b.Builder)
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
}

// TestStopReapsTheScopeWhenTheRunnerIsGone pins the reaped action: the runner
// is already gone, so there is no process to kill, but its scope is still
// loaded and is ended. The close says reaped and records stopped/reaped.
func TestStopReapsTheScopeWhenTheRunnerIsGone(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Scope = &spawn.ScopeSpec{}
	deadRunner(t, rt)
	unit := scopeUnitName(b)
	fr.scopeActive = map[string]bool{unit: true}

	res, err := Stop(context.Background(), rt, "webshop", StopOptions{})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if res.Action != "reaped" {
		t.Errorf("Action = %q, want reaped", res.Action)
	}
	if len(fr.kills) != 0 {
		t.Errorf("kills = %+v, want none: the runner was already gone", fr.kills)
	}
	if len(fr.scopeStops) != 1 || fr.scopeStops[0] != unit {
		t.Errorf("scopeStops = %v, want [%s]", fr.scopeStops, unit)
	}
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 2 {
		t.Errorf("Round = %d, want 2: the round closed", got.Round)
	}
	if stops := stopEntries(t, rt, "webshop"); len(stops) != 1 || stops[0].Note != "stopped/reaped" {
		t.Errorf("stop entries = %+v, want one stopped/reaped", stops)
	}
}

// TestStopReportsGoneWhenNothingIsLeftToStop pins the gone action: no live
// process and no loaded scope means nothing was stopped and the close says so.
func TestStopReportsGoneWhenNothingIsLeftToStop(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := sentHeadless(t, fr)
	rt.Scope = &spawn.ScopeSpec{}
	deadRunner(t, rt)

	res, err := Stop(context.Background(), rt, "webshop", StopOptions{})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if res.Action != "gone" {
		t.Errorf("Action = %q, want gone", res.Action)
	}
	if len(fr.kills) != 0 {
		t.Errorf("kills = %+v, want none", fr.kills)
	}
	if len(fr.scopeStops) != 0 {
		t.Errorf("scopeStops = %v, want none", fr.scopeStops)
	}
	if stops := stopEntries(t, rt, "webshop"); len(stops) != 1 || stops[0].Note != "stopped/gone" {
		t.Errorf("stop entries = %+v, want one stopped/gone", stops)
	}
}

// TestStopKillsTheProcessAndReapsTheScope pins the combined action: a live
// process is signalled and the scope a straggler may still hold is ended, and
// the close still says killed.
func TestStopKillsTheProcessAndReapsTheScope(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	rt.Scope = &spawn.ScopeSpec{}
	h := handleOf(b.Builder)
	unit := scopeUnitName(b)
	fr.scopeActive = map[string]bool{unit: true}

	res, err := Stop(context.Background(), rt, "webshop", StopOptions{})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if res.Action != "killed" {
		t.Errorf("Action = %q, want killed", res.Action)
	}
	if len(fr.kills) != 1 || fr.kills[0] != h {
		t.Errorf("kills = %+v, want the round's handle %+v", fr.kills, h)
	}
	if len(fr.scopeStops) != 1 || fr.scopeStops[0] != unit {
		t.Errorf("scopeStops = %v, want [%s]", fr.scopeStops, unit)
	}
}

// TestStopWithScopesOffNeverProbesAScope pins the fallback: with rt.Scope nil,
// Stop asks the runner about no scope at all.
func TestStopWithScopesOffNeverProbesAScope(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := sentHeadless(t, fr)
	deadRunner(t, rt)

	res, err := Stop(context.Background(), rt, "webshop", StopOptions{})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if res.Action != "gone" {
		t.Errorf("Action = %q, want gone", res.Action)
	}
	if len(fr.scopeQueries) != 0 {
		t.Errorf("scopeQueries = %v, want none (rt.Scope is nil)", fr.scopeQueries)
	}
	if len(fr.scopeStops) != 0 {
		t.Errorf("scopeStops = %v, want none (rt.Scope is nil)", fr.scopeStops)
	}
}

// ownedStopFixture is a served (owned) binding with a bare repo and a
// worktree on relevo/api -- the shape the closeServedRound tests build -- plus
// a plan entry for round 1 so RoundStateOf reports it running (or queued when
// queued is true).
func ownedStopFixture(t *testing.T, fr *fakeRunner, queued bool) (Runtime, store.Binding) {
	t.Helper()

	bare := t.TempDir()
	runGit(t, bare, "init", "--bare")

	seedDir := t.TempDir()
	runGit(t, seedDir, "clone", bare, ".")
	if err := os.WriteFile(filepath.Join(seedDir, "file.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seedDir, "add", "file.txt")
	runGit(t, seedDir, "commit", "-m", "init")
	runGit(t, seedDir, "push", "origin", "HEAD:refs/heads/relevo/api")

	wt := t.TempDir()
	runGit(t, bare, "worktree", "add", wt, "refs/heads/relevo/api")

	st := store.New(t.TempDir())
	b := store.Binding{
		Name:     "api",
		Owner:    "client1",
		Branch:   "relevo/api",
		CWD:      wt,
		Worktree: wt,
		Round:    1,
		State:    store.StateActive,
		Builder: store.Endpoint{
			Kind: "claude", Mode: store.ModeHeadless, AgentName: "api",
			LogPath: filepath.Join(t.TempDir(), "builder.log"),
		},
		Serve: &store.ServeFacts{BareRepo: bare},
	}
	if queued {
		b.QueuedAt = baseTime
	} else {
		b.RoundStartedAt = baseTime
		b.Builder.PID = 4242
		b.Builder.StartedAt = baseTime.Unix()
		// The fixture is a running round, so the runner must report its
		// recorded process alive: Stop now signals only a live process.
		fr.script(4242, true)
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("api", store.LogEntry{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}

	rt := Runtime{
		Store:  st,
		Git:    git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes),
		Runner: fr,
		Now:    func() time.Time { return baseTime },
	}
	return rt, b
}

// TestStopOwnedRunningRoundClosesServedRound pins #344's served close: after
// Stop on an owned binding with an open running round, the binding's
// Serve.ClosedRound names the stopped round and RoundStateOf reports closed,
// so the owner's ServedView is no longer "running" (nor "idle").
func TestStopOwnedRunningRoundClosesServedRound(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := ownedStopFixture(t, fr, false)

	res, err := Stop(context.Background(), rt, "api", StopOptions{})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if res.Action != "killed" {
		t.Errorf("Action = %q, want killed", res.Action)
	}
	if len(fr.kills) != 1 {
		t.Fatalf("kills = %+v, want the round's process killed", fr.kills)
	}

	got, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Serve == nil || got.Serve.ClosedRound != 1 {
		t.Fatalf("Serve.ClosedRound = %+v, want 1", got.Serve)
	}
	if got.Builder.PID != 0 {
		t.Errorf("Builder.PID = %d, want 0 after the kill", got.Builder.PID)
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if state := RoundStateOf(got, entries); state != remote.RoundClosed {
		t.Errorf("RoundStateOf = %v, want %v", state, remote.RoundClosed)
	}
}

// TestStopOwnedQueuedRoundDequeues pins #344's queued decision: a queued round
// is dropped from the queue rather than refused or killed. QueuedAt is
// cleared (the server's census is derived from it), no process is killed, and
// the round closes as stopped with the dequeued wording.
func TestStopOwnedQueuedRoundDequeues(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := ownedStopFixture(t, fr, true)

	res, err := Stop(context.Background(), rt, "api", StopOptions{})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if res.Action != "dequeued" {
		t.Errorf("Action = %q, want dequeued", res.Action)
	}
	if len(fr.kills) != 0 {
		t.Errorf("kills = %+v, want none: a queued round has no process", fr.kills)
	}

	got, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if !got.QueuedAt.IsZero() {
		t.Errorf("QueuedAt = %v, want zero (the round was dropped)", got.QueuedAt)
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if state := RoundStateOf(got, entries); state != remote.RoundClosed {
		t.Errorf("RoundStateOf = %v, want %v", state, remote.RoundClosed)
	}
	if text := StopText("api", res); !strings.Contains(text, "dropped from the server queue") {
		t.Errorf("StopText = %q, want the dequeued wording", text)
	}
}

func TestStopNothingToStop(t *testing.T) {
	t.Parallel()

	t.Run("no open round", func(t *testing.T) {
		rt, b := sentBinding(t)
		b.RoundStartedAt = time.Time{}
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		if _, err := Stop(context.Background(), rt, "webshop", StopOptions{}); !errors.Is(err, ErrNothingToStop) {
			t.Fatalf("err = %v, want ErrNothingToStop", err)
		}
	})

	t.Run("done", func(t *testing.T) {
		rt, b := sentBinding(t)
		b.State = store.StateDone
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		_, err := Stop(context.Background(), rt, "webshop", StopOptions{})
		if err == nil || !strings.Contains(err.Error(), "done") {
			t.Fatalf("err = %v, want a refusal naming done", err)
		}
	})

	t.Run("paused", func(t *testing.T) {
		rt, b := sentBinding(t)
		b.State = store.StatePaused
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		_, err := Stop(context.Background(), rt, "webshop", StopOptions{})
		if err == nil || !strings.Contains(err.Error(), "paused") {
			t.Fatalf("err = %v, want a refusal naming paused", err)
		}
	})

	t.Run("remote without a client", func(t *testing.T) {
		rt, b := sentBinding(t)
		b.Builder.Mode = store.ModeRemote
		if err := rt.Store.Save(b); err != nil {
			t.Fatal(err)
		}
		_, err := Stop(context.Background(), rt, "webshop", StopOptions{})
		if !errors.Is(err, ErrRemoteUnavailable) {
			t.Fatalf("err = %v, want ErrRemoteUnavailable", err)
		}
	})
}

// remoteStopFixture is a client-side remote binding with an open round 1 and
// a fakeRemote that advertises FeatureStop. The stopped view it answers with
// is a closed round the server stopped by killing the builder.
func remoteStopFixture(t *testing.T, fr *fakeRemote) (Runtime, remote.BindingView) {
	t.Helper()
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.RoundStartedAt = baseTime
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	view := remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1, Stopped: "killed"}
	fr.stopResp = view
	fr.getBindingResp = view
	// A killed builder with nothing on disk: every round file answers 404.
	fr.roundFileFunc = func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
		return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: remote.CodeNotFound, Message: "no " + kind}}
	}
	return Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}, view
}

// TestStopRemoteKillsAndCollects pins #344's client half: Stop asks the
// server (WhoAmI first, then POST stop), reports the server's "killed", and
// runs one observe pass so the round is closed locally with the stopped
// payload pending -- never delivered.
func TestStopRemoteKillsAndCollects(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureStop}}}
	rt, _ := remoteStopFixture(t, fr)

	res, err := Stop(ctx, rt, "api", StopOptions{})
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if res.Action != "killed" {
		t.Errorf("Action = %q, want killed", res.Action)
	}
	if len(fr.calls) < 2 || fr.calls[0] != "WhoAmI:zen" || fr.calls[1] != "Stop:zen:api" {
		t.Fatalf("calls = %v, want WhoAmI then Stop first", fr.calls)
	}

	got, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 2 {
		t.Errorf("Round = %d, want 2: the stopped round closed locally", got.Round)
	}
	if got.State == store.StateNeedsYou {
		t.Errorf("state = %s, want active: a stopped close must not halt", got.State)
	}

	pending, found, err := rt.Store.PendingForPlanner("api")
	if err != nil || !found {
		t.Fatalf("report must be pending: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Payload, "The runner was stopped (killed) for round 1 on zen") {
		t.Errorf("payload = %q, want the stopped text", pending.Payload)
	}
	if pending.Confirmed {
		t.Errorf("payload was delivered; Stop must leave it pending")
	}
}

// TestStopRemotePreStopServerRefuses pins the pre-stop-server refusal: a
// server whose WhoAmI advertises no FeatureStop is told nothing, and the hint
// names relevo unbind.
func TestStopRemotePreStopServerRefuses(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{whoAmIResp: remote.WhoAmI{}}
	rt, _ := remoteStopFixture(t, fr)

	_, err := Stop(ctx, rt, "api", StopOptions{})
	if err == nil || !strings.Contains(err.Error(), "predates remote stop") {
		t.Fatalf("err = %v, want the pre-stop-server refusal", err)
	}
	if !strings.Contains(err.Error(), "relevo unbind api") {
		t.Errorf("err = %q, want it to name relevo unbind api", err)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Stop:") {
			t.Fatalf("server Stop called on a pre-stop server: %v", fr.calls)
		}
	}
}

// TestStopRemoteNothingToStop pins the 409 nothing_to_stop mapping: the
// server's answer becomes the local sentinel, so the CLI prints "nothing to
// stop" and exits 0.
func TestStopRemoteNothingToStop(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{
		whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureStop}},
		stopErr:    &client.HTTPError{Status: 409, Body: remote.ErrorBody{Code: remote.CodeNothingToStop}},
	}
	rt, _ := remoteStopFixture(t, fr)

	if _, err := Stop(ctx, rt, "api", StopOptions{}); !errors.Is(err, ErrNothingToStop) {
		t.Fatalf("err = %v, want ErrNothingToStop", err)
	}
}

// TestStopRemote404NamesUnbind pins the server-404 refusal and its hint: the
// binding is gone there, so the local record should be unbound.
func TestStopRemote404NamesUnbind(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{
		whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureStop}},
		stopErr:    &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: remote.CodeNotFound}},
	}
	rt, _ := remoteStopFixture(t, fr)

	_, err := Stop(ctx, rt, "api", StopOptions{})
	want := `zen no longer has binding "api"; relevo unbind api to drop it here`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

// TestStopRemotePartialSuccess pins the partial-success rule: the server
// stopped the round, so a failed local observe is not an error. Stop still
// reports killed and leaves the binding alone, not halted.
func TestStopRemotePartialSuccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{
		whoAmIResp:    remote.WhoAmI{Features: []string{remote.FeatureStop}},
		getBindingErr: errors.New("boom"),
	}
	rt, _ := remoteStopFixture(t, fr)

	res, err := Stop(ctx, rt, "api", StopOptions{})
	if err != nil {
		t.Fatalf("Stop: %v, want success when the server stop succeeded", err)
	}
	if res.Action != "killed" {
		t.Errorf("Action = %q, want killed", res.Action)
	}

	got, lerr := rt.Store.Load("api")
	if lerr != nil {
		t.Fatal(lerr)
	}
	if got.State == store.StateNeedsYou || got.Halt != "" {
		t.Errorf("binding halted (%s / %q); a failed observe must not halt it", got.State, got.Halt)
	}
}

func TestSendClearsStopRequest(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	b.StopRequestedAt = baseTime
	b.StopGraceMS = 300000
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	endProcess(t, rt, b)

	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "keep going"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if !got.StopRequestedAt.IsZero() || got.StopGraceMS != 0 {
		t.Errorf("a send must clear the stop bookkeeping: at=%s grace=%d", got.StopRequestedAt, got.StopGraceMS)
	}
}
