package relevo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// pendingAtDone stages a binding whose round has produced two payloads its
// MasterMind has never been told about -- a findings entry pointing at a real
// file and a halt -- and returns the findings text that must surface.
func pendingAtDone(t *testing.T, rt Runtime, b store.Binding) (store.Binding, string) {
	t.Helper()
	const fid = "abc"
	path := rt.Store.FindingsPath(b.Name, b.Round, fid)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir findings dir: %v", err)
	}
	const body = "# abc findings\n\nthe security scan found nothing\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write findings: %v", err)
	}
	if err := rt.Store.AppendLog(b.Name, store.LogEntry{
		Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindFindings,
		Payload: "Findings from critic consult " + fid + ": relevo show " + b.Name + " --findings " + fid,
		Path:    path,
	}); err != nil {
		t.Fatalf("AppendLog findings: %v", err)
	}
	// The halt a builder that timed out leaves behind, still pending too.
	next := haltOnce(t, rt, b, "builder timed out after 30m0s")
	if err := rt.Store.Save(next); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return next, body
}

// TestDoneDeliversPendingEntries pins step 7: `relevo done` is the last chance
// this binding has to hand its MasterMind something, and today the payloads
// left pending there are stranded -- reconcile's DONE gate only admits what a
// push route already took, and `relevo wait` answers gone without pulling. The
// entries must surface in the result and nothing may remain pending.
//
// Mutation check: delete the claim/confirm block in Done and the payload comes
// back empty with both entries still unconfirmed.
func TestDoneDeliversPendingEntries(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	rt.Git = &fakeGit{}
	b, body := pendingAtDone(t, rt, b)

	res, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}

	if res.Payload == "" {
		t.Fatal("Payload = \"\", want the entries that were pending at done")
	}
	if !strings.Contains(res.Payload, "Findings from critic consult abc") {
		t.Errorf("Payload = %q, want it to name the pending findings entry", res.Payload)
	}
	if !strings.Contains(res.Payload, "security scan found nothing") {
		t.Errorf("Payload = %q, want the findings file's own text expanded into it", res.Payload)
	}
	if !strings.Contains(res.Payload, "timed out after 30m0s") {
		t.Errorf("Payload = %q, want the pending halt's reason in it", res.Payload)
	}
	if body == "" {
		t.Fatal("fixture wrote no findings text; the test would prove nothing")
	}

	// Nothing may remain pending: that is the stranding this fixes.
	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && !e.Confirmed {
			t.Errorf("entry kind=%s round=%d is still unconfirmed after Done", e.Kind, e.Round)
		}
	}

	got, err := rt.Store.Load(b.Name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != store.StateDone {
		t.Errorf("state = %s, want done", got.State)
	}

	// Done is idempotent about the payloads: a second run has nothing left.
	again, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("second Done: %v", err)
	}
	if again.Payload != "" {
		t.Errorf("second Done payload = %q, want empty: nothing was pending", again.Payload)
	}
}

// TestDoneWithNothingPendingDeliversNothing guards the ordinary case: a done
// with an empty queue must not invent a payload, and must leave the binding
// done exactly as before.
func TestDoneWithNothingPendingDeliversNothing(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	rt.Git = &fakeGit{}

	res, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if res.Payload != "" {
		t.Errorf("Payload = %q, want empty when nothing was pending", res.Payload)
	}
}

func TestDoneRemoteKept(t *testing.T) {
	t.Parallel()

	rt, b := sentBinding(t)
	rt.Git = &fakeGit{}
	fr := &fakeRemote{
		doneResp: remote.BindingView{
			WorktreeKept: "/path/to/server/wt",
			KeptReason:   "uncommitted changes",
		},
	}
	rt.Remote = fr

	b.Builder = store.Endpoint{Mode: store.ModeRemote, Server: "zen"}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	res, err := Done(context.Background(), rt, b.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if res.WorktreeKept != "/path/to/server/wt" {
		t.Errorf("res.WorktreeKept = %q, want %q", res.WorktreeKept, "/path/to/server/wt")
	}
	if res.KeptReason != "on zen: uncommitted changes" {
		t.Errorf("res.KeptReason = %q, want %q", res.KeptReason, "on zen: uncommitted changes")
	}
	text := DoneText(b.Name, res)
	if !strings.Contains(text, "kept worktree /path/to/server/wt") {
		t.Errorf("DoneText = %q, want kept worktree line", text)
	}
	if !strings.Contains(text, "on zen: uncommitted changes") {
		t.Errorf("DoneText = %q, want on zen: uncommitted changes", text)
	}

	b2 := b
	b2.Name = "webshop-empty"
	b2.State = store.StateActive
	if err := rt.Store.Save(b2); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fr.doneResp = remote.BindingView{}

	res2, err := Done(context.Background(), rt, b2.Name)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	if res2.WorktreeKept != "" {
		t.Errorf("res2.WorktreeKept = %q, want empty", res2.WorktreeKept)
	}
	text2 := DoneText(b2.Name, res2)
	if strings.Contains(text2, "kept worktree") {
		t.Errorf("DoneText = %q, want no kept worktree line", text2)
	}
}

func TestChainServerDoneKept(t *testing.T) {
	t.Parallel()

	fr := chainPullFake(chainPullView("shop", string(chain.StatusHalted), 0, 0, 0))
	fr.chainDoneResp = remote.ChainView{
		WorktreeKept: "/path/to/chain/wt",
		KeptReason:   "uncommitted changes",
	}
	rt := chainPullRuntime(t, fr)
	seedServerChain(t, rt, "shop")
	pullRounds(t, rt, "shop")

	res, err := ChainDone(context.Background(), rt, "shop")
	if err != nil {
		t.Fatalf("ChainDone: %v", err)
	}
	if res.WorktreeKept != "/path/to/chain/wt" {
		t.Errorf("res.WorktreeKept = %q, want %q", res.WorktreeKept, "/path/to/chain/wt")
	}
	if res.KeptReason != "on zen: uncommitted changes" {
		t.Errorf("res.KeptReason = %q, want %q", res.KeptReason, "on zen: uncommitted changes")
	}
	text := DoneText("shop", res)
	if !strings.Contains(text, "kept worktree /path/to/chain/wt") {
		t.Errorf("DoneText = %q, want kept worktree line", text)
	}
	if !strings.Contains(text, "on zen: uncommitted changes") {
		t.Errorf("DoneText = %q, want on zen: uncommitted changes", text)
	}

	fr2 := chainPullFake(chainPullView("shop2", string(chain.StatusHalted), 0, 0, 0))
	fr2.chainDoneResp = remote.ChainView{}
	rt2 := chainPullRuntime(t, fr2)
	seedServerChain(t, rt2, "shop2")
	pullRounds(t, rt2, "shop2")

	res2, err := ChainDone(context.Background(), rt2, "shop2")
	if err != nil {
		t.Fatalf("ChainDone: %v", err)
	}
	if res2.WorktreeKept != "" {
		t.Errorf("res2.WorktreeKept = %q, want empty", res2.WorktreeKept)
	}
	text2 := DoneText("shop2", res2)
	if strings.Contains(text2, "kept worktree") {
		t.Errorf("DoneText = %q, want no kept worktree line", text2)
	}
}

func openRoundWithoutProcess(t *testing.T) Runtime {
	t.Helper()
	rt, b := seedHeadless(t, newFakeRunner())
	b.Round = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return rt
}

func TestDoneWritesStopEntryForARoundThatNeverSpawned(t *testing.T) {
	t.Parallel()

	rt := openRoundWithoutProcess(t)
	if err := rt.Store.AppendLog("webshop", store.LogEntry{
		Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Path: "p.md",
	}); err != nil {
		t.Fatalf("AppendLog: %v", err)
	}
	if _, err := Done(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	st := stopEntries(t, rt, "webshop")
	if len(st) != 1 || st[0].Note != "stopped/done" || st[0].Round != 1 {
		t.Errorf("stop entries = %+v, want one stopped/done on round 1", st)
	}
}

func TestDoneWritesNoStopEntryWhenNoRoundIsOpen(t *testing.T) {
	t.Parallel()

	rt := openRoundWithoutProcess(t)
	if _, err := Done(context.Background(), rt, "webshop"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if st := stopEntries(t, rt, "webshop"); len(st) != 0 {
		t.Errorf("stop entries = %+v, want none", st)
	}
}
