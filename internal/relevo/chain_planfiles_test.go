package relevo

import (
	"os"
	"strings"
	"testing"
)

// TestChainSeedsCarryPlanNamedFilesFromTheBaseBranch pins that a file the plan
// text names and only the base branch holds reaches the reviewer's and the
// correction planner's seeds as a readable copy, while a name the base branch
// does not hold is left out.
func TestChainSeedsCarryPlanNamedFilesFromTheBaseBranch(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	fg.blobPaths = map[string]string{"docs/plans/source-plan.md": "oid-source"}
	fg.blobs = map[string][]byte{"oid-source": []byte("# Source plan\nonly on main\n")}
	startedChain(t, rt, ChainOptions{
		Plans: []string{writePlan(t, "Follow `docs/plans/source-plan.md` and add internal/new/file.go.")},
	})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	assertSeedNamesPlanFile(t, rt, "shop-rev")

	chainReaderClose(t, rt, "shop-rev", chainVerdictBody("changes"))
	assertSeedNamesPlanFile(t, rt, "shop-plan")
}

func assertSeedNamesPlanFile(t *testing.T, rt Runtime, name string) {
	t.Helper()
	b := chainBinding(t, rt, name)
	prompt, err := os.ReadFile(rt.Store.PromptPath(name, b.Round))
	if err != nil {
		t.Fatalf("read %s prompt: %v", name, err)
	}
	copyPath, _ := rt.Store.ChainRepoFilePath("shop", "docs/plans/source-plan.md")
	if !strings.Contains(string(prompt), "docs/plans/source-plan.md: "+copyPath) {
		t.Errorf("%s seed does not name the base-branch copy %s:\n%s", name, copyPath, prompt)
	}
	body, err := os.ReadFile(copyPath)
	if err != nil || !strings.Contains(string(body), "only on main") {
		t.Errorf("copy = %q, %v; want the base-branch text", body, err)
	}
	if strings.Contains(string(prompt), "internal/new/file.go") {
		t.Errorf("%s seed names a file the base branch does not hold:\n%s", name, prompt)
	}
}

// TestChainSeedPlanFilesDegradeToAClauseWhenTheBaseCannotBeRead pins that a
// base branch that resolves nowhere renders the path-free clause and does not
// fail the send.
func TestChainSeedPlanFilesDegradeToAClauseWhenTheBaseCannotBeRead(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	fg.refSHA = map[string]string{}
	startedChain(t, rt, ChainOptions{Plans: []string{writePlan(t, "Read docs/plans/x.md.")}})

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	b := chainBinding(t, rt, "shop-rev")
	prompt, err := os.ReadFile(rt.Store.PromptPath("shop-rev", b.Round))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "base branch: not available:") {
		t.Errorf("seed lacks the not-available clause:\n%s", prompt)
	}
	if n := len(fg.fetchCalls); n != 0 {
		t.Errorf("Fetch ran %d times under the seed transaction, want 0", n)
	}
}

// TestChainSeedPlanFilesReadABaseHeldOnlyAsAnOriginRef pins that a base present
// only as its origin remote-tracking ref resolves with no fetch.
func TestChainSeedPlanFilesReadABaseHeldOnlyAsAnOriginRef(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	startedChain(t, rt, ChainOptions{
		Plans: []string{writePlan(t, "Follow `docs/plans/source-plan.md`.")},
	})
	c, err := rt.Store.Chain("shop")
	if err != nil {
		t.Fatal(err)
	}
	base := c.Base
	if b := chainBinding(t, rt, "shop"); b.BaseRef != "" {
		base = b.BaseRef
	}
	fg.refSHA = map[string]string{"origin/" + base: "originsha"}
	fg.blobPaths = map[string]string{"docs/plans/source-plan.md": "oid-source"}
	fg.blobs = map[string][]byte{"oid-source": []byte("only on origin\n")}

	chainBuilderClose(t, rt, "shop", chainDoneBody())
	copyPath, _ := rt.Store.ChainRepoFilePath("shop", "docs/plans/source-plan.md")
	if body, err := os.ReadFile(copyPath); err != nil || !strings.Contains(string(body), "only on origin") {
		t.Errorf("copy = %q, %v; want the origin-ref text", body, err)
	}
	if n := len(fg.fetchCalls); n != 0 {
		t.Errorf("Fetch ran %d times, want 0", n)
	}
}
