package relevo

import (
	"context"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/git"
)

// The patches the whole-chain-diff tests' fake git answers with: the full span
// carries one hunk per builder round, so a copy holding all three proves the
// diff starts at the chain's base rather than at a later round.
const (
	secDiffWhole    = "--- a/round-one\n+++ b/round-one\n+one\n--- a/round-two\n+++ b/round-two\n+two\n--- a/round-three\n+++ b/round-three\n+three\n"
	secDiffOnlyLast = "--- a/round-three\n+++ b/round-three\n+three\n"
)

// secDiffFunc answers the full patch only for the (base, end) pair the whole
// branch's diff must use; every other span gets the last round alone, so a diff
// from the wrong start or to the wrong end is missing the earlier rounds.
func secDiffFunc(base, end string) func(context.Context, string, string, string) (git.Diff, error) {
	return func(_ context.Context, _, from, to string) (git.Diff, error) {
		patch := secDiffOnlyLast
		if from == base && to == end {
			patch = secDiffWhole
		}
		return git.Diff{Stat: git.Stat{FilesChanged: 3, Insertions: 3}, Patch: []byte(patch)}, nil
	}
}

// TestChainSecuritySeedNamesTheWholeChainDiff pins the whole-branch span the
// security seed names: a 2-plan chain whose plan-2 start commit moved, three
// builder rounds (plan 1, one correction, plan 2), then a scan. The seed names
// the copy of the key keyed to the builder's newest closed round (3), the copy
// holds every round's change -- so the diff starts at the chain's base, not at
// a later round's baseline -- and the seed names neither the round_file key nor
// the old round-diff keyed to the closing reader's round. The close did not
// fail: the chain is scanning and awaiting the security round.
func TestChainSecuritySeedNamesTheWholeChainDiff(t *testing.T) {
	t.Parallel()

	const (
		base     = "commit-head-123"
		chainEnd = "tree-round-3"
	)
	rt, fg := chainRuntime(t)
	fg.snapshotTreeID = chainEnd
	fg.diffFunc = secDiffFunc(base, chainEnd)

	startedFlowChain(t, rt, ChainOptions{
		Plans:          []string{writePlan(t, "plan one"), writePlan(t, "plan two")},
		MaxCorrections: ptr(1),
		Security:       ptr(true),
	})

	// Builder round 1 is plan 1; the reviewer asks for one correction.
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))
	chainReaderClose(t, rt, "shop-plan", chainDoneBody())

	// Builder round 2 is the correction.
	chainBuilderClose(t, rt, "shop", chainDoneBody())

	// Plan 2's send records a new plan-start commit, so the chain's own base is
	// the only correct start for the whole-branch diff.
	fg.headCommitID = "head-plan2"
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))

	// Builder round 3 is plan 2; its reviewer's pass opens the security scan.
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))

	row := chainStoredRow(t, rt, "shop")
	if row.Status != string(chain.StatusRunning) || row.Phase != string(chain.PhaseSecurity) || row.Step != string(chain.StepScanning) {
		t.Fatalf("chain = status %q phase %q step %q; want running/security/scanning", row.Status, row.Phase, row.Step)
	}
	if row.Base != base {
		t.Fatalf("chain base = %q, want %q", row.Base, base)
	}
	sec := chainBinding(t, rt, "shop-sec")
	if row.AwaitingMember != chain.MemberSecurity || row.AwaitingRound != sec.Round {
		t.Fatalf("awaiting (%s, %d); want the security round (%s, %d)", row.AwaitingMember, row.AwaitingRound, chain.MemberSecurity, sec.Round)
	}

	key := rt.Store.ChainDiffPath("shop", 3)
	copyPath, ok := rt.Store.ChainInputPath("shop", key)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", key)
	}
	if !rt.Store.DiskRegularFile(copyPath) {
		t.Errorf("the whole-chain diff copy %s is not a regular file on disk", copyPath)
	}
	if got := fg.lastDiffFrom; got != row.Base {
		t.Errorf("the whole-chain diff ran from %q, want the chain base %q", got, row.Base)
	}
	if got := fg.lastDiffTo; got != chainEnd {
		t.Errorf("the whole-chain diff ran to %q, want the builder's closed tree %q", got, chainEnd)
	}

	seed, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-sec", sec.Round))
	if err != nil {
		t.Fatalf("read the security seed: %v", err)
	}
	got := string(seed)
	if !strings.Contains(got, copyPath) {
		t.Errorf("the security seed does not name the whole-chain diff copy %s:\n%s", copyPath, got)
	}
	if strings.Contains(got, key) {
		t.Errorf("the security seed names the round_file key %s:\n%s", key, got)
	}

	body, err := rt.Store.ReadFile(copyPath)
	if err != nil {
		t.Fatalf("read the whole-chain diff copy: %v", err)
	}
	want, err := rt.Store.ReadFile(key)
	if err != nil {
		t.Fatalf("read the whole-chain diff key: %v", err)
	}
	if string(body) != string(want) {
		t.Errorf("the copy = %q, want the key's bytes %q", body, want)
	}
	for _, marker := range []string{"round-one", "round-two", "round-three"} {
		if !strings.Contains(string(body), marker) {
			t.Errorf("the whole-chain diff copy lacks %q; a one-round diff cannot stand in:\n%s", marker, body)
		}
	}

	// The seed names neither the old closing reader's report nor the round diff
	// keyed to that reader's round: the value used to be one builder round.
	if oldCopy, ok := rt.Store.ChainInputPath("shop", rt.Store.DiffPath("shop", sec.Round)); ok && strings.Contains(got, oldCopy) {
		t.Errorf("the security seed names the old round-diff copy %s:\n%s", oldCopy, got)
	}
	if strings.Contains(got, rt.Store.ReportPath("shop", sec.Round)) {
		t.Errorf("the security seed names the closing reader's report %s:\n%s", rt.Store.ReportPath("shop", sec.Round), got)
	}
}

// TestChainSecuritySeedListsEveryPlanCopy pins the plan copies the security
// seed lists: a 2-plan chain's scan names both plan copies under a Plan copies
// line.
func TestChainSecuritySeedListsEveryPlanCopy(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	fg.snapshotTreeID = "tree-end"
	startedFlowChain(t, rt, ChainOptions{
		Plans:    []string{writePlan(t, "plan one"), writePlan(t, "plan two")},
		Security: ptr(true),
	})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))

	if row := chainStoredRow(t, rt, "shop"); row.Step != string(chain.StepScanning) {
		t.Fatalf("step = %q, want scanning", row.Step)
	}
	sec := chainBinding(t, rt, "shop-sec")
	seed, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-sec", sec.Round))
	if err != nil {
		t.Fatalf("read the security seed: %v", err)
	}
	got := string(seed)
	if !strings.Contains(got, "Plan copies:") {
		t.Errorf("the security seed carries no Plan copies line:\n%s", got)
	}
	for i := 1; i <= 2; i++ {
		if want := "Plan copy: " + rt.Store.ChainPlanPath("shop", i) + "."; !strings.Contains(got, want) {
			t.Errorf("the security seed does not list plan copy %d (%q):\n%s", i, want, got)
		}
	}
}

// TestChainSecuritySeedNamesTheBaseWhenTheBranchDiffIsTruncated pins the
// truncated capture: no key is written, the seed names the chain's base, and
// the security round still opens.
func TestChainSecuritySeedNamesTheBaseWhenTheBranchDiffIsTruncated(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	fg.snapshotTreeID = "tree-end"
	fg.diffResult = git.Diff{
		Stat:      git.Stat{FilesChanged: 312, Insertions: 48120, Deletions: 9033},
		Truncated: true,
	}
	startedFlowChain(t, rt, ChainOptions{Security: ptr(true)})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))

	row := chainStoredRow(t, rt, "shop")
	if row.Step != string(chain.StepScanning) {
		t.Fatalf("step = %q, want scanning: a truncated capture must not fail the close", row.Step)
	}
	key := rt.Store.ChainDiffPath("shop", 1)
	if _, err := rt.Store.ReadFile(key); err == nil {
		t.Errorf("a truncated branch diff wrote the key %s; it must write nothing", key)
	}
	sec := chainBinding(t, rt, "shop-sec")
	seed, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-sec", sec.Round))
	if err != nil {
		t.Fatalf("read the security seed: %v", err)
	}
	got := string(seed)
	if want := "No branch diff was captured; diff the branch yourself from " + row.Base + "."; !strings.Contains(got, want) {
		t.Errorf("the security seed does not word the miss with the base %q:\n%s", row.Base, got)
	}
	if strings.Contains(got, key) {
		t.Errorf("the security seed names the key %s:\n%s", key, got)
	}
}

// TestChainSecuritySeedNamesTheBaseWhenTheBuilderHasNoClosedTree pins the empty
// closed tree: a builder whose RoundClosedTree was cleared captures nothing,
// the seed names the base, and the security round still opens.
func TestChainSecuritySeedNamesTheBaseWhenTheBuilderHasNoClosedTree(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	fg.snapshotTreeID = "tree-end"
	startedFlowChain(t, rt, ChainOptions{Security: ptr(true)})

	chainBuilderClose(t, rt, "shop", chainDoneBody())

	// Clear the tree the builder's close recorded, as a resumed or crashed
	// builder can: the seed must word the miss rather than fail the close.
	b := chainBinding(t, rt, "shop")
	b.RoundClosedTree = ""
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))

	row := chainStoredRow(t, rt, "shop")
	if row.Step != string(chain.StepScanning) {
		t.Fatalf("step = %q, want scanning", row.Step)
	}
	key := rt.Store.ChainDiffPath("shop", 1)
	if _, err := rt.Store.ReadFile(key); err == nil {
		t.Errorf("an empty closed tree wrote the key %s; it must write nothing", key)
	}
	sec := chainBinding(t, rt, "shop-sec")
	seed, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-sec", sec.Round))
	if err != nil {
		t.Fatalf("read the security seed: %v", err)
	}
	if want := "No branch diff was captured; diff the branch yourself from " + row.Base + "."; !strings.Contains(string(seed), want) {
		t.Errorf("the security seed does not word the miss with the base %q:\n%s", row.Base, seed)
	}
}

// TestChainFixesSeedNamesTheSameWholeChainDiff pins the fixes seed's diff: a
// chain whose builder newest closed round is 2 while the security member's
// round is 1. The fixes seed names the same whole-chain diff copy the security
// seed named, keyed to the builder's newest closed round (2), not to the
// closing reader's round (1); it keeps the security output.
func TestChainFixesSeedNamesTheSameWholeChainDiff(t *testing.T) {
	t.Parallel()

	const (
		base     = "commit-head-123"
		chainEnd = "tree-round-2"
	)
	rt, fg := chainRuntime(t)
	fg.snapshotTreeID = chainEnd
	fg.diffFunc = secDiffFunc(base, chainEnd)

	startedFlowChain(t, rt, ChainOptions{
		MaxCorrections: ptr(1),
		Security:       ptr(true),
	})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))
	chainReaderClose(t, rt, "shop-plan", chainDoneBody())
	chainBuilderClose(t, rt, "shop", chainDoneBody())
	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("pass"))

	secKey := rt.Store.ChainDiffPath("shop", 2)
	secCopy, ok := rt.Store.ChainInputPath("shop", secKey)
	if !ok {
		t.Fatalf("ChainInputPath(%s) = false", secKey)
	}
	sec := chainBinding(t, rt, "shop-sec")
	secSeed, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-sec", sec.Round))
	if err != nil {
		t.Fatalf("read the security seed: %v", err)
	}
	if !strings.Contains(string(secSeed), secCopy) {
		t.Errorf("the security seed does not name the whole-chain diff copy %s:\n%s", secCopy, secSeed)
	}

	// The scan finds one thing, seeding the fix planner.
	chainReaderClose(t, rt, "shop-sec", chainFindingsBody(1))

	planner := chainBinding(t, rt, "shop-plan")
	fixSeed, err := rt.Store.ReadFile(rt.Store.PromptPath("shop-plan", planner.Round))
	if err != nil {
		t.Fatalf("read the fixes seed: %v", err)
	}
	got := string(fixSeed)
	if !strings.Contains(got, secCopy) {
		t.Errorf("the fixes seed does not name the same whole-chain diff copy %s:\n%s", secCopy, got)
	}
	if !strings.Contains(got, "Security output: ") {
		t.Errorf("the fixes seed names no security output:\n%s", got)
	}
	// Keying the diff on the closing reader's round (1) would name a different
	// copy; the fixes seed must not.
	oldKey := rt.Store.ChainDiffPath("shop", 1)
	if oldCopy, ok := rt.Store.ChainInputPath("shop", oldKey); ok && strings.Contains(got, oldCopy) {
		t.Errorf("the fixes seed names the diff keyed to the closing reader's round (%s):\n%s", oldCopy, got)
	}

	body, err := rt.Store.ReadFile(secCopy)
	if err != nil {
		t.Fatalf("read the whole-chain diff copy: %v", err)
	}
	for _, marker := range []string{"round-one", "round-two", "round-three"} {
		if !strings.Contains(string(body), marker) {
			t.Errorf("the fixes seed's whole-chain diff copy lacks %q:\n%s", marker, body)
		}
	}
}
