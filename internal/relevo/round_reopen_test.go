package relevo

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// This file holds the reproductions for a remote round that never closes after
// a resend over NEEDS YOU: the send-side reset, the collect path that skips the
// close, and the wait classification that reads the stale halt.

// haltedRemoteBinding is a remote binding whose round halted: the server's
// answer is NEEDS YOU, and the client has recorded that as its own state, the
// way applyRemote's halt arm records it.
func haltedRemoteBinding(server string) store.Binding {
	b := remoteBinding(server)
	b.State = store.StateNeedsYou
	b.Halt = "api: zen: builder exited without a report"
	b.HaltAt = baseTime.Add(-time.Minute)
	b.HaltNotifiedRound = b.Round
	b.RoundStartedAt = baseTime.Add(-time.Hour)
	b.StalledSince = baseTime.Add(-30 * time.Minute)
	return b
}

// reopenRemoteRuntime is the runtime a remote binding's send needs: the store,
// a fake git that resolves the branch, a fake transport whose snapshot is
// empty, and a fake server whose answers a test sets.
func reopenRemoteRuntime(t *testing.T, b store.Binding) (Runtime, *fakeRemote) {
	t.Helper()
	st := store.New(t.TempDir())
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/" + b.Branch: "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{}
	ft := &fakeTransport{snapshotResp: remote.Snapshot{Empty: true}}
	return Runtime{Store: st, Git: fg, Remote: fr, Transport: ft, Now: func() time.Time { return baseTime }}, fr
}

// roundFiles is the fakeRemote RoundFile answer a close needs: a report body
// and a builder log. Every other kind answers 404, which is how a server says
// "this round has no such file" -- a plain error would abort the catch-up.
func roundFiles(body string) func(context.Context, string, string, int, string) (io.ReadCloser, error) {
	return func(_ context.Context, _, _ string, _ int, kind string) (io.ReadCloser, error) {
		switch kind {
		case "report":
			return io.NopCloser(bytes.NewReader([]byte(body))), nil
		case "log":
			return io.NopCloser(bytes.NewReader([]byte("builder log\n"))), nil
		}
		return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: remote.CodeNotFound}}
	}
}

// TestRemoteResendOverNeedsYouClearsTheHalt is the send half of the
// reproduction: a resend that starts (or restarts) a round is a fresh attempt,
// so the client's own copy must leave NEEDS YOU exactly as Send's local branch
// does -- state active, halt and halt-at cleared, and the per-round bookkeeping
// a fresh process implies cleared with it.
func TestRemoteResendOverNeedsYouClearsTheHalt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := haltedRemoteBinding("zen")
	rt, fr := reopenRemoteRuntime(t, b)
	fr.startRoundResp = remote.BindingView{RoundState: remote.RoundRunning}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Send(ctx, rt, "api", planFile, SendOptions{}); err != nil {
		t.Fatalf("Send over NEEDS YOU: %v", err)
	}

	got, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateActive {
		t.Errorf("State = %q, want %q: a resend that started a round left the binding %q", got.State, store.StateActive, got.State)
	}
	if got.Halt != "" {
		t.Errorf("Halt = %q, want empty", got.Halt)
	}
	if !got.HaltAt.IsZero() {
		t.Errorf("HaltAt = %v, want zero", got.HaltAt)
	}
	if got.HaltNotifiedRound != 0 {
		t.Errorf("HaltNotifiedRound = %d, want 0", got.HaltNotifiedRound)
	}
	if !got.StalledSince.IsZero() {
		t.Errorf("StalledSince = %v, want zero", got.StalledSince)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0", got.RoundSwitches)
	}
	if got.RoundExcluded != nil {
		t.Errorf("RoundExcluded = %v, want nil", got.RoundExcluded)
	}
	if got.RoundOOMKills != 0 {
		t.Errorf("RoundOOMKills = %d, want 0", got.RoundOOMKills)
	}
	if !got.StopRequestedAt.IsZero() {
		t.Errorf("StopRequestedAt = %v, want zero", got.StopRequestedAt)
	}
	if got.RoundClosedTree != "" {
		t.Errorf("RoundClosedTree = %q, want empty", got.RoundClosedTree)
	}
	if !got.FinishPending {
		t.Error("FinishPending = false, want true after a resend opens the round")
	}

	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if !HasPromptEntry(entries, b.Round) {
		t.Errorf("no prompt entry for round %d: the resend left no open round: %+v", b.Round, entries)
	}
}

// TestRoundStateOfReportsAnUnackedCloseOverNeedsYou is the server half of the
// same story, at the wire boundary. A round with a done marker closes whatever
// the earlier state was, so an un-acked close must be reported even when the
// binding has since halted again: the owner's client fetches the close by that
// word, and a NEEDS YOU word makes it skip the fetch.
func TestRoundStateOfReportsAnUnackedCloseOverNeedsYou(t *testing.T) {
	t.Parallel()

	b := store.Binding{
		Name:       "api",
		Round:      2,
		State:      store.StateNeedsYou,
		Halt:       "api: round 1 closed with 400 artifacts over the cap",
		HaltAt:     baseTime,
		Serve:      &store.ServeFacts{ClosedRound: 1, AckedRound: 0},
		MasterMind: store.Endpoint{PaneID: "w2:p3"},
	}
	// Round 1 ran: a prompt went out and a report came back.
	entries := []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true},
	}

	if got := RoundStateOf(b, entries); got != remote.RoundClosed {
		t.Errorf("RoundStateOf = %q, want %q: the un-acked close of round 1 is hidden behind the halt of round 2", got, remote.RoundClosed)
	}
}

// TestFetchRemoteCollectsACloseOverNeedsYou is the fetch half: the catch-up a
// close earns is fetched whenever the server says this round closed, whatever
// the state word rides along with it.
func TestFetchRemoteCollectsACloseOverNeedsYou(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := haltedRemoteBinding("zen")
	b.Round = 1
	rt, fr := reopenRemoteRuntime(t, b)
	fr.getBindingResp = remote.BindingView{
		RoundState:  remote.RoundNeedsYou,
		ClosedRound: 1,
		Halt:        "api: zen: round 1 closed with 400 artifacts over the cap",
	}
	fr.roundFileFunc = roundFiles("report body\n")

	f := fetchRemote(ctx, rt, b)
	defer f.release()
	if f.CatchUp == nil {
		t.Fatal("no catch-up fetched: a NEEDS YOU view whose ClosedRound is this round is never collected")
	}
	if f.CatchUp.Round != 1 {
		t.Errorf("CatchUp.Round = %d, want 1", f.CatchUp.Round)
	}
}

// TestApplyRemoteCollectsBeforeItHalts is the apply half: a fetched close is
// collected even when the local state is NEEDS YOU -- catch-up first, halt
// second -- so the round advances instead of stranding the round behind the
// halt word.
func TestApplyRemoteCollectsBeforeItHalts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	b := haltedRemoteBinding("zen")
	b.Round = 1
	rt, fr := reopenRemoteRuntime(t, b)
	fr.roundFileFunc = roundFiles("report body\n")
	fr.getBindingResp = remote.BindingView{
		Name:        "api",
		Round:       1,
		RoundState:  remote.RoundNeedsYou,
		ClosedRound: 1,
		Halt:        "api: zen: round 1 closed with 400 artifacts over the cap",
		Stopped:     "killed",
	}

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		next, deliver, err := observeRemote(ctx, rt, tx, b)
		if err != nil {
			return err
		}
		if !deliver {
			t.Error("deliver = false: the NEEDS YOU halt answered instead of collecting the close")
		}
		entries, rerr := tx.ReadLog("api")
		if rerr != nil {
			return rerr
		}
		if !HasEntry(entries, 1, store.DirToMasterMind, store.KindReport) {
			t.Errorf("no report entry for round 1 after the close was applied: %+v", entries)
		}
		if next.Round < 2 {
			t.Errorf("Round = %d, want the round to advance past the closed one", next.Round)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("observeRemote over NEEDS YOU with a close: %v", err)
	}
}

// TestWaitOutcomeOnAReSentRoundDoesNotExitNeedsYou is the wait half. It walks
// the whole shape the issue names: a round that halted, a resend that starts it
// again, the server then closing it and halting once more (the reader artifact
// cap), and `relevo wait` on the client. The round has a done marker, so it is
// closed whatever the later halt says: wait must report the close, not exit 3
// on a halt that describes a round already finished.
func TestWaitOutcomeOnAReSentRoundDoesNotExitNeedsYou(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Round 1 halted and the client recorded it.
	b := haltedRemoteBinding("zen")
	b.Round = 1
	rt, fr := reopenRemoteRuntime(t, b)

	// The human re-sends. The server accepts and starts the round again.
	fr.startRoundResp = remote.BindingView{RoundState: remote.RoundRunning}
	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Send(ctx, rt, "api", planFile, SendOptions{}); err != nil {
		t.Fatalf("resend over NEEDS YOU: %v", err)
	}

	// The round then finished, and the server halted again while closing it:
	// the close is un-acked, so the view carries both facts.
	fr.getBindingResp = remote.BindingView{
		Name:        "api",
		Round:       1,
		RoundState:  remote.RoundNeedsYou,
		ClosedRound: 1,
		Halt:        "api: zen: round 1 closed with 400 artifacts over the cap",
		Stopped:     "killed",
	}
	fr.roundFileFunc = roundFiles("report body\n")

	// `relevo wait` syncs before it classifies, so the sync is part of the wait.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		next, _, err := observeRemote(ctx, rt, tx, b)
		if err != nil {
			return err
		}
		return tx.Save(next)
	}); err != nil {
		t.Fatalf("observeRemote after the resend: %v", err)
	}

	after, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}

	res := WaitOutcome(after, entries, 1, func(string, int) string { return "" })
	if res.Code == WaitNeedsYou {
		t.Errorf("WaitOutcome = %+v: wait exits 3 on a round with a done marker, stranding a live runner behind a halt word", res)
	}
	if res.Done && res.Code != WaitClosed && res.Code != WaitUnmarked && res.Code != WaitHalted {
		t.Errorf("WaitOutcome = %+v (code %d), want the closed round's verdict", res, res.Code)
	}
}
