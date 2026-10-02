package relevo

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/pathscope"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// scopedWriterRuntime is sentBinding with the fake Git installed and the
// binding moved onto a scoped librarian writer, one Send into round 1.
func scopedWriterRuntime(t *testing.T, fg *fakeGit, scope *pathscope.Scope) (Runtime, store.Binding) {
	t.Helper()
	rt, b := sentBinding(t)
	rt.Git = fg
	rt.Runner = newFakeRunner()
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, map[string]roles.Row{
		"librarian": {Shape: ptr("writer"), Scope: scope},
	})
	b.Role = "librarian"
	b.RoundBaselineTree = "tree-start"
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return rt, b
}

// writeCloseReport writes the closing round's report and completion marker.
func writeCloseReport(t *testing.T, rt Runtime, name string, round int, body string) {
	t.Helper()
	if err := os.WriteFile(rt.Store.ReportPath(name, round), []byte(body), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	touch(t, rt.Store.DonePath(name, round))
}

// codeChange is an out-of-scope .go modification whose blobs differ in a code
// token.
func codeChange(path string) []pathscope.Change {
	return []pathscope.Change{{
		Status: 'M', OldMode: "100644", NewMode: "100644",
		OldOID: "old", NewOID: "new", Path: path,
	}}
}

// TestScopeDocsOnlyClosesClean pins the pass: a scoped writer whose round only
// touched a docs path closes with the scope=ok note and no halt.
func TestScopeDocsOnlyClosesClean(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		snapshotTreeID: "tree-end",
		changedFiles: []pathscope.Change{{
			Status: 'M', OldMode: "100644", NewMode: "100644", Path: "docs/guide.md",
		}},
	}
	rt, b := scopedWriterRuntime(t, fg, &pathscope.Scope{Paths: []string{"@docs"}, Comments: true})
	writeCloseReport(t, rt, "webshop", 1, chainDoneBody())

	got, closed := closeOnMarkerUnderLock(t, rt, b)
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	if got.State != store.StateActive {
		t.Errorf("State = %q, want active", got.State)
	}
	if got.Halt != "" {
		t.Errorf("Halt = %q, want none", got.Halt)
	}
	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "scope=ok" {
		t.Errorf("note = %q, want scope=ok", pending.Note)
	}
	if pending.Outcome != "done" {
		t.Errorf("outcome = %q, want done", pending.Outcome)
	}
}

// TestScopeStatementChangeHaltsNamingFile pins the core refusal: an
// out-of-scope Go code change closes the round but forces outcome halted,
// sets NEEDS YOU naming the file, notes scope=refused, and runs no gate.
func TestScopeStatementChangeHaltsNamingFile(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		snapshotTreeID: "tree-end",
		changedFiles:   codeChange("internal/relevo/reconcile.go"),
		blobs: map[string][]byte{
			"old": []byte("package relevo\n\nfunc F() { x := 1; _ = x }\n"),
			"new": []byte("package relevo\n\nfunc F() { x := 2; _ = x }\n"),
		},
	}
	rt, b := scopedWriterRuntime(t, fg, &pathscope.Scope{Paths: []string{"@docs"}, Comments: true})
	// A gate is configured: the scope check must fire before it, so a refusal
	// leaves no KindGate entry and starts no gate process.
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	writeCloseReport(t, rt, "webshop", 1, chainDoneBody())

	got, closed := closeOnMarkerUnderLock(t, rt, b)
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("State = %q, want needs-you", got.State)
	}
	if !strings.Contains(got.Halt, "internal/relevo/reconcile.go") {
		t.Errorf("Halt = %q, want it to name the offending path", got.Halt)
	}
	if !strings.Contains(got.Halt, "scope") {
		t.Errorf("Halt = %q, want it to name the scope refusal", got.Halt)
	}

	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Outcome != "halted" {
		t.Errorf("outcome = %q, want halted", pending.Outcome)
	}
	if !strings.Contains(pending.Note, "scope=refused") {
		t.Errorf("note = %q, want scope=refused", pending.Note)
	}
	if strings.Contains(pending.Note, "gate=") {
		t.Errorf("note = %q, want no gate= on a scope halt", pending.Note)
	}
	if pending.HaltedAt != "scope: internal/relevo/reconcile.go" {
		t.Errorf("HaltedAt = %q, want scope: <path>", pending.HaltedAt)
	}
	if !strings.Contains(pending.Payload, "Scope: refused -- internal/relevo/reconcile.go: code change") {
		t.Errorf("payload = %q, want the scope refusal line", pending.Payload)
	}
	if !strings.Contains(pending.Payload, "relevo show webshop --round 1 --diff") {
		t.Errorf("payload = %q, want the --diff hint", pending.Payload)
	}
	if got := gates(t, rt); len(got) != 0 {
		t.Errorf("KindGate entries = %d, want 0: the scope check fires before the gate", len(got))
	}
	if fr := rt.Runner.(*fakeRunner); len(fr.specs) != 0 {
		t.Errorf("gate Start calls = %d, want 0: the scope check fires before the gate", len(fr.specs))
	}
}

// TestScopeDocCommentOnlyClosesClean pins rule 3: an out-of-path .go change
// whose only difference is a non-directive comment closes clean.
func TestScopeDocCommentOnlyClosesClean(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		snapshotTreeID: "tree-end",
		changedFiles:   codeChange("internal/relevo/reconcile.go"),
		blobs: map[string][]byte{
			"old": []byte("package relevo\n\n// old wording\nfunc F() {}\n"),
			"new": []byte("package relevo\n\n// new wording\nfunc F() {}\n"),
		},
	}
	rt, b := scopedWriterRuntime(t, fg, &pathscope.Scope{Paths: []string{"@docs"}, Comments: true})
	writeCloseReport(t, rt, "webshop", 1, chainDoneBody())

	got, closed := closeOnMarkerUnderLock(t, rt, b)
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	if got.State != store.StateActive {
		t.Errorf("State = %q, want active", got.State)
	}
	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if pending.Note != "scope=ok" {
		t.Errorf("note = %q, want scope=ok", pending.Note)
	}
}

// TestScopeDirectiveChangeRefused pins rule 5: a change to a directive comment
// is refused even though the non-comment tokens are equal.
func TestScopeDirectiveChangeRefused(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		snapshotTreeID: "tree-end",
		changedFiles:   codeChange("internal/relevo/reconcile.go"),
		blobs: map[string][]byte{
			"old": []byte("//go:build linux\n\npackage relevo\n"),
			"new": []byte("//go:build darwin\n\npackage relevo\n"),
		},
	}
	rt, b := scopedWriterRuntime(t, fg, &pathscope.Scope{Paths: []string{"@docs"}, Comments: true})
	writeCloseReport(t, rt, "webshop", 1, chainDoneBody())

	got, _ := closeOnMarkerUnderLock(t, rt, b)
	if got.State != store.StateNeedsYou {
		t.Errorf("State = %q, want needs-you", got.State)
	}
	pending, _, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if pending.Outcome != "halted" {
		t.Errorf("outcome = %q, want halted", pending.Outcome)
	}
	if !strings.Contains(pending.Payload, "directive comment") {
		t.Errorf("payload = %q, want it to name the directive comment", pending.Payload)
	}
}

// TestUnscopedWriterUnaffected pins that an actor with no scope closes exactly
// as it did before: the check is never consulted and no note is added.
func TestUnscopedWriterUnaffected(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		snapshotTreeID: "tree-end",
		changedFiles:   codeChange("internal/relevo/reconcile.go"),
		blobs: map[string][]byte{
			"old": []byte("package relevo\n\nfunc F() { x := 1; _ = x }\n"),
			"new": []byte("package relevo\n\nfunc F() { x := 2; _ = x }\n"),
		},
	}
	rt, b := sentBinding(t)
	rt.Git = fg
	b.RoundBaselineTree = "tree-start"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	writeCloseReport(t, rt, "webshop", 1, chainDoneBody())

	got, closed := closeOnMarkerUnderLock(t, rt, b)
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	if got.State != store.StateActive || got.Halt != "" {
		t.Errorf("state/halt = %q/%q, want active and no halt", got.State, got.Halt)
	}
	pending, found, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil || !found {
		t.Fatalf("report must be queued: found=%v err=%v", found, err)
	}
	if strings.Contains(pending.Note, "scope") {
		t.Errorf("note = %q, want no scope note for an unscoped actor", pending.Note)
	}
	if pending.Outcome != "done" {
		t.Errorf("outcome = %q, want done", pending.Outcome)
	}
}

// TestScopeNoBaselineRefuses pins that a round the check cannot judge halts:
// no baseline tree refuses with the reason.
func TestScopeNoBaselineRefuses(t *testing.T) {
	t.Parallel()

	rt, b := scopedWriterRuntime(t, &fakeGit{snapshotTreeID: "tree-end"},
		&pathscope.Scope{Paths: []string{"@docs"}, Comments: true})
	b.RoundBaselineTree = ""
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	writeCloseReport(t, rt, "webshop", 1, chainDoneBody())

	got, _ := closeOnMarkerUnderLock(t, rt, b)
	if got.State != store.StateNeedsYou {
		t.Errorf("State = %q, want needs-you", got.State)
	}
	if !strings.Contains(got.Halt, "no baseline tree") {
		t.Errorf("Halt = %q, want it to name the missing baseline", got.Halt)
	}
	pending, _, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if pending.Outcome != "halted" || !strings.Contains(pending.Note, "scope=refused") {
		t.Errorf("entry = outcome %q note %q, want halted and scope=refused", pending.Outcome, pending.Note)
	}
}

// TestScopeRejudgedAfterGate pins that the check runs again on the tick the
// gate finishes: a pass before the gate, a refusal once the tree has moved
// while the gate ran.
func TestScopeRejudgedAfterGate(t *testing.T) {
	t.Parallel()

	refusal := codeChange("internal/relevo/reconcile.go")
	fg := &fakeGit{
		snapshotTreeID: "tree-end",
		blobs: map[string][]byte{
			"old": []byte("package relevo\n\nfunc F() { x := 1; _ = x }\n"),
			"new": []byte("package relevo\n\nfunc F() { x := 2; _ = x }\n"),
		},
	}
	calls := 0
	fg.changedFilesFunc = func(_ context.Context, _, _, _ string) ([]pathscope.Change, error) {
		calls++
		if calls == 1 {
			return nil, nil // the pre-gate pass
		}
		return refusal, nil // the tree moved while the gate ran
	}

	rt, b := scopedWriterRuntime(t, fg, &pathscope.Scope{Paths: []string{"@docs"}, Comments: true})
	b.Gate = "make check"
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	writeCloseReport(t, rt, "webshop", 1, chainDoneBody())

	fr := rt.Runner.(*fakeRunner)
	got, closed, gating := closeOnMarkerUnderLockGating(t, rt, b)
	if closed || !gating {
		t.Fatalf("closed=%v gating=%v after starting the gate, want gating only", closed, gating)
	}
	fr.script(fr.handles[0].PID, false)
	fr.exit(fr.handles[0].PID, 0)

	got, closed, gating = closeOnMarkerUnderLockGating(t, rt, got)
	if gating {
		t.Fatal("gating = true after the gate exited")
	}
	if !closed || got.Round != 2 {
		t.Fatalf("closed=%v round=%d, want a close into round 2", closed, got.Round)
	}
	if calls != 2 {
		t.Errorf("judge calls = %d, want 2 (before the gate and after it finished)", calls)
	}
	if got.State != store.StateNeedsYou {
		t.Errorf("State = %q, want needs-you after the rejudge", got.State)
	}
	pending, _, err := rt.Store.PendingForMasterMind("webshop")
	if err != nil {
		t.Fatal(err)
	}
	if pending.Outcome != "halted" || !strings.Contains(pending.Note, "scope=refused") {
		t.Errorf("entry = outcome %q note %q, want halted and scope=refused", pending.Outcome, pending.Note)
	}
	if got := gates(t, rt); len(got) == 0 {
		t.Error("KindGate entries = 0, want the gate that ran before the rejudge")
	}
}

// TestScopeRefusalHaltsChainMember pins that the forced outcome is set before
// the chain event: a chain member's scope refusal halts its chain.
func TestScopeRefusalHaltsChainMember(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	fg.snapshotTreeID = "tree-start"
	fg.changedFiles = codeChange("internal/relevo/reconcile.go")
	fg.blobs = map[string][]byte{
		"old": []byte("package relevo\n\nfunc F() { x := 1; _ = x }\n"),
		"new": []byte("package relevo\n\nfunc F() { x := 2; _ = x }\n"),
	}
	rows := chainRows()
	rows["builder"] = roles.Row{
		Candidates: []string{testClaudeRef},
		Scope:      &pathscope.Scope{Paths: []string{"@docs"}, Comments: true},
	}
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, rows)
	startedChain(t, rt, ChainOptions{})

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusHalted) {
		t.Errorf("chain status = %q, want halted after a scope refusal", row.Status)
	}
}
