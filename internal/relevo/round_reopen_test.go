package relevo

import (
	"bytes"
	"context"
	"io"
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
// close earns is fetched whenever the server names a ClosedRound at or past
// this round, whatever the state word rides along with it.
//
// The two words that must NOT collect are pinned here too, so the gate is
// load-bearing in both directions: a running or queued round has no close to
// collect and keeps its log mirror, and a round whose report this client
// already holds is not fetched again.
func TestFetchRemoteCollectsACloseOverNeedsYou(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	closeView := remote.BindingView{
		RoundState:  remote.RoundNeedsYou,
		ClosedRound: 1,
		Halt:        "api: zen: round 1 closed with 400 artifacts over the cap",
	}

	t.Run("a needs_you view that also closed collects", func(t *testing.T) {
		t.Parallel()
		b := haltedRemoteBinding("zen")
		b.Round = 1
		rt, fr := reopenRemoteRuntime(t, b)
		fr.getBindingResp = closeView
		fr.roundFileFunc = roundFiles("report body\n")

		f := fetchRemote(ctx, rt, b)
		defer f.release()
		if f.CatchUp == nil {
			t.Fatal("no catch-up fetched: a NEEDS YOU view whose ClosedRound is this round is never collected")
		}
		if f.CatchUp.Round != 1 {
			t.Errorf("CatchUp.Round = %d, want 1", f.CatchUp.Round)
		}
	})

	for _, st := range []remote.RoundState{remote.RoundRunning, remote.RoundQueued} {
		t.Run("a "+string(st)+" round does not collect", func(t *testing.T) {
			t.Parallel()
			b := haltedRemoteBinding("zen")
			b.Round = 1
			rt, fr := reopenRemoteRuntime(t, b)
			fr.getBindingResp = remote.BindingView{
				RoundState:   st,
				ClosedRound:  1,
				StalledSince: baseTime.Add(-time.Minute),
			}
			fr.roundFileFunc = roundFiles("report body\n")

			f := fetchRemote(ctx, rt, b)
			defer f.release()
			if f.CatchUp != nil {
				t.Errorf("CatchUp fetched for a %s round: there is no close to collect", st)
			}
		})
	}

	t.Run("a round already collected is not fetched again", func(t *testing.T) {
		t.Parallel()
		b := haltedRemoteBinding("zen")
		b.Round = 1
		rt, fr := reopenRemoteRuntime(t, b)
		if err := rt.Store.AppendLog("api", store.LogEntry{
			TS: baseTime, Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true,
		}); err != nil {
			t.Fatal(err)
		}
		fr.getBindingResp = closeView
		fr.roundFileFunc = roundFiles("report body\n")

		f := fetchRemote(ctx, rt, b)
		defer f.release()
		if f.CatchUp != nil {
			t.Error("CatchUp fetched for a round whose report this client already holds")
		}
	})
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

	// The human re-sent and the server started the round again: the binding
	// is active with the halt gone and the prompt filed, the post-send shape
	// uplink's record tests pin. The wait below classifies that shape.
	b.State = store.StateActive
	b.Halt = ""
	b.HaltAt = time.Time{}
	b.HaltNotifiedRound = 0
	b.RoundStartedAt = baseTime
	b.StalledSince = time.Time{}
	b.FinishPending = true
	b.Builder.RemoteStatus = string(remote.RoundRunning)
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save the re-sent round: %v", err)
	}
	if err := rt.Store.AppendLog("api", store.LogEntry{
		TS: baseTime, Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt,
		Path: rt.Store.PromptPath("api", 1), Confirmed: true,
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if err := StagePlan(rt.Store.PromptPath("api", 1), []byte("# Plan")); err != nil {
		t.Fatalf("StagePlan: %v", err)
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
