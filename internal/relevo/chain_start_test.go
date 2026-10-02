package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// chainRows is the roles file the chain tests resolve against: the built-in
// builder and reviewer, plus the two actors the chain's default settings name,
// so a start with no actor flags resolves and each member has one candidate.
func chainRows() map[string]roles.Row {
	return map[string]roles.Row{
		"builder":  {Candidates: []string{testClaudeRef}},
		"reviewer": {Candidates: []string{testClaudeRef}},
		"lite-planner": {
			Shape:       ptr("reader"),
			Candidates:  []string{testClaudeRef},
			Definitions: map[string]roles.DefRow{"claude": {Agent: "architect"}},
		},
		"security": {
			Shape:       ptr("reader"),
			Candidates:  []string{testClaudeRef},
			Definitions: map[string]roles.DefRow{"claude": {Agent: "security-reviewer"}},
		},
		"assistant": {
			Shape:       ptr("reader"),
			Candidates:  []string{testClaudeRef},
			Definitions: map[string]roles.DefRow{"claude": {Agent: "reviewer"}},
		},
		"researcher": {
			Shape:       ptr("reader"),
			Candidates:  []string{testClaudeRef},
			Definitions: map[string]roles.DefRow{"claude": {Agent: "researcher"}},
		},
	}
}

// chainRuntime is a runtime a chain can start on: a fake git, a fake runner
// and the chain roles file. The fake git records every worktree call, so a
// test can prove what was cut, and from where.
func chainRuntime(t *testing.T) (Runtime, *fakeGit) {
	t.Helper()
	rt := newRuntime(t)
	fg := &fakeGit{headCommitID: "commit-head-123"}
	rt.Git = fg
	rt.Runner = newFakeRunner()
	rt.Registry = rolesFileRegistry(t, rt.Candidates, rt.Policy, chainRows())
	return rt, fg
}

// startedChain starts one chain through ChainStart and returns what it
// produced, so a test names only the options it varies.
func startedChain(t *testing.T, rt Runtime, opts ChainOptions) ChainResult {
	t.Helper()
	if opts.Name == "" {
		opts.Name = "shop"
	}
	if opts.Plans == nil {
		opts.Plans = []string{writePlan(t, "build it")}
	}
	if opts.Feature == "" && !opts.NoFeature {
		opts.Feature = "auth"
	}
	if opts.MasterMindID == "" {
		opts.MasterMindID = testMasterMindName
	}
	res, err := ChainStart(context.Background(), rt, opts)
	if err != nil {
		t.Fatalf("ChainStart: %v", err)
	}
	return res
}

// assertNothingCreated asserts a refused start created nothing: no chain row,
// no member binding and no worktree.
func assertNothingCreated(t *testing.T, rt Runtime, fg *fakeGit, name string) {
	t.Helper()
	if _, err := rt.Store.Chain(name); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("chain %q exists after a refusal: %v", name, err)
	}
	if _, err := rt.Store.Load(name); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("binding %q exists after a refusal: %v", name, err)
	}
	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("a refusal must cut no worktree: %+v", fg.addWorktreeCalls)
	}
}

// testChainRow is a valid chain row for the tests that plant one.
func testChainRow(name string) db.ChainRow {
	now := baseTime
	return db.ChainRow{
		ID: db.NewID(), Name: name, Status: "running", Phase: "build", Step: "building",
		Plan: 1, Plans: 1, PlanPathsJSON: []byte(`["/p/plan-1.md"]`), SettingsJSON: []byte(`{}`),
		Builder: name, CreatedAt: now, UpdatedAt: now,
	}
}

// TestChainStartCreatesMembersAndSendsPlanOne pins the whole start: three
// members and the chain row exist, the builder is cut on its own branch and
// tree, the two readers share that tree with their own actor and shape, and
// plan 1 was handed to the builder as round 1 -- the builder's log carries the
// prompt entry.
func TestChainStartCreatesMembersAndSendsPlanOne(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	res := startedChain(t, rt, ChainOptions{})

	if res.Plans != 1 || len(res.Members) != 3 {
		t.Fatalf("result = %d plans, %d members; want 1 plan, 3 members", res.Plans, len(res.Members))
	}
	if res.Chain.Status != string(chain.StatusRunning) || res.Chain.Phase != string(chain.PhaseBuild) ||
		res.Chain.Step != string(chain.StepBuilding) || res.Chain.Plan != 1 || res.Chain.Plans != 1 ||
		res.Chain.Corrections != 0 {
		t.Errorf("chain row = %+v, want running/build/building plan 1 of 1", res.Chain)
	}
	if res.Chain.AwaitingMember != chain.MemberBuilder || res.Chain.AwaitingRound != 1 {
		t.Errorf("awaiting = (%q, %d), want (builder, 1)", res.Chain.AwaitingMember, res.Chain.AwaitingRound)
	}
	if res.Chain.Builder != "shop" || res.Chain.Reviewer != "shop-rev" || res.Chain.Planner != "shop-plan" {
		t.Errorf("member columns = %q/%q/%q, want shop/shop-rev/shop-plan",
			res.Chain.Builder, res.Chain.Reviewer, res.Chain.Planner)
	}
	if res.Chain.Security != "" {
		t.Errorf("Security = %q, want empty when the phase is off", res.Chain.Security)
	}
	if res.Chain.Feature != "auth" {
		t.Errorf("Feature = %q, want auth on every member", res.Chain.Feature)
	}

	stored, err := rt.Store.Chain("shop")
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	if stored.Name != "shop" || stored.Status != string(chain.StatusRunning) {
		t.Errorf("stored chain = %+v", stored)
	}

	builder := memberByName(t, res.Members, "shop")
	if builder.Shape != store.ShapeWriter || builder.Worktree != rt.Store.WorktreePath("shop") || builder.Branch != "relevo/shop" {
		t.Errorf("builder = %+v, want a writer in its own worktree on relevo/shop", builder)
	}
	if builder.Role != "builder" {
		t.Errorf("builder role = %q, want builder", builder.Role)
	}
	for _, tc := range []struct{ name, role string }{
		{"shop-rev", "reviewer"},
		{"shop-plan", "lite-planner"},
	} {
		m := memberByName(t, res.Members, tc.name)
		if m.Shape != store.ShapeReader || m.Role != tc.role {
			t.Errorf("%s = %q/%q, want a reader running %s", tc.name, m.Role, m.Shape, tc.role)
		}
		if m.Worktree != "" || m.Gate != "" {
			t.Errorf("%s carries worktree %q and gate %q; a reader carries neither", tc.name, m.Worktree, m.Gate)
		}
		if m.CWD != builder.CWD {
			t.Errorf("%s CWD = %q, want the builder's tree %q", tc.name, m.CWD, builder.CWD)
		}
	}

	if len(fg.addWorktreeCalls) != 1 || fg.addWorktreeCalls[0].Branch != "relevo/shop" {
		t.Fatalf("AddWorktree calls = %+v, want one branch relevo/shop", fg.addWorktreeCalls)
	}

	entries, err := rt.Store.ReadLog("shop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Kind == store.KindPrompt && e.Round == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("builder log = %+v, want a prompt entry for round 1", entries)
	}
}

// TestChainStartCreatesTheSecurityMemberWhenOn pins the fourth member: with
// the security phase on, the -sec member exists, the row names it, and it is a
// reader like the other two.
func TestChainStartCreatesTheSecurityMemberWhenOn(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	res := startedChain(t, rt, ChainOptions{Security: ptr(true)})

	if len(res.Members) != 4 {
		t.Fatalf("members = %d, want 4", len(res.Members))
	}
	if res.Chain.Security != "shop-sec" {
		t.Errorf("Security = %q, want shop-sec", res.Chain.Security)
	}
	m := memberByName(t, res.Members, "shop-sec")
	if m.Shape != store.ShapeReader || m.Role != "security" {
		t.Errorf("security member = %q/%q, want a reader running security", m.Role, m.Shape)
	}
}

// TestChainStartRefusesAnEmptyOrUnreadablePlan pins the plan checks: no plan,
// a missing file and a blank file each refuse, and nothing is created.
func TestChainStartRefusesAnEmptyOrUnreadablePlan(t *testing.T) {
	t.Parallel()

	t.Run("no plan", func(t *testing.T) {
		t.Parallel()

		rt, fg := chainRuntime(t)
		_, err := ChainStart(context.Background(), rt, ChainOptions{
			Name: "shop", Plans: []string{}, Feature: "auth", MasterMindID: testMasterMindName,
		})
		if err == nil || !strings.Contains(err.Error(), "plans is required") {
			t.Fatalf("err = %v, want the workflow's missing-plans refusal", err)
		}
		if !errors.Is(err, ErrRefused) {
			t.Errorf("err = %v, want errors.Is(err, ErrRefused): a bad argument is refused, not internal", err)
		}
		assertNothingCreated(t, rt, fg, "shop")
	})

	t.Run("an unreadable plan", func(t *testing.T) {
		t.Parallel()

		rt, fg := chainRuntime(t)
		_, err := ChainStart(context.Background(), rt, ChainOptions{
			Name: "shop", Plans: []string{filepath.Join(t.TempDir(), "gone.md")},
			Feature: "auth", MasterMindID: testMasterMindName,
		})
		if err == nil || !strings.Contains(err.Error(), "read plan") {
			t.Fatalf("err = %v, want a read failure", err)
		}
		if !errors.Is(err, ErrRefused) {
			t.Errorf("err = %v, want errors.Is(err, ErrRefused)", err)
		}
		assertNothingCreated(t, rt, fg, "shop")
	})

	t.Run("a blank plan", func(t *testing.T) {
		t.Parallel()

		rt, fg := chainRuntime(t)
		_, err := ChainStart(context.Background(), rt, ChainOptions{
			Name: "shop", Plans: []string{writePlan(t, "  \n\t\n")},
			Feature: "auth", MasterMindID: testMasterMindName,
		})
		if err == nil || !strings.Contains(err.Error(), "is empty") {
			t.Fatalf("err = %v, want an empty-plan refusal", err)
		}
		if !errors.Is(err, ErrRefused) {
			t.Errorf("err = %v, want errors.Is(err, ErrRefused)", err)
		}
		assertNothingCreated(t, rt, fg, "shop")
	})
}

// TestChainStartRefusesATakenMemberName pins both halves of the name-free
// check: a member name that is already a binding refuses, and one that is
// already a chain refuses.
func TestChainStartRefusesATakenMemberName(t *testing.T) {
	t.Parallel()

	t.Run("a binding", func(t *testing.T) {
		t.Parallel()

		rt, fg := chainRuntime(t)
		if err := rt.Store.Save(store.Binding{Name: "shop-rev", CWD: "/taken", State: store.StateActive}); err != nil {
			t.Fatalf("seed binding: %v", err)
		}
		_, err := ChainStart(context.Background(), rt, ChainOptions{
			Name: "shop", Plans: []string{writePlan(t, "p")}, Feature: "auth", MasterMindID: testMasterMindName,
		})
		if err == nil || !strings.Contains(err.Error(), `"shop-rev" already exists`) {
			t.Fatalf("err = %v, want a taken-name refusal", err)
		}
		if !errors.Is(err, ErrRefused) {
			t.Errorf("err = %v, want errors.Is(err, ErrRefused): a taken name is refused, not internal", err)
		}
		if len(fg.addWorktreeCalls) != 0 {
			t.Errorf("a refusal must cut no worktree: %+v", fg.addWorktreeCalls)
		}
	})

	t.Run("a chain", func(t *testing.T) {
		t.Parallel()

		rt, fg := chainRuntime(t)
		row := testChainRow("shop-plan")
		if err := rt.Store.WithLock(func(tx *store.Tx) error { return tx.ChainPut(row) }); err != nil {
			t.Fatalf("seed chain: %v", err)
		}
		_, err := ChainStart(context.Background(), rt, ChainOptions{
			Name: "shop", Plans: []string{writePlan(t, "p")}, Feature: "auth", MasterMindID: testMasterMindName,
		})
		if err == nil || !strings.Contains(err.Error(), `chain "shop-plan" already exists`) {
			t.Fatalf("err = %v, want a taken-chain refusal", err)
		}
		if !errors.Is(err, ErrRefused) {
			t.Errorf("err = %v, want errors.Is(err, ErrRefused)", err)
		}
		if len(fg.addWorktreeCalls) != 0 {
			t.Errorf("a refusal must cut no worktree: %+v", fg.addWorktreeCalls)
		}
	})
}

// TestChainStartRefusesANameOver27 pins the extra length rule: a name
// store.ValidName accepts is still refused when the longest member suffix
// would push a member past the cap. Names are never truncated.
func TestChainStartRefusesANameOver27(t *testing.T) {
	t.Parallel()

	name := "c" + strings.Repeat("h", 27) // 28 characters
	if err := store.ValidName(name); err != nil {
		t.Fatalf("test premise: %q must pass ValidName: %v", name, err)
	}

	rt, fg := chainRuntime(t)
	_, err := ChainStart(context.Background(), rt, ChainOptions{
		Name: name, Plans: []string{writePlan(t, "p")}, Feature: "auth", MasterMindID: testMasterMindName,
	})
	if err == nil || !strings.Contains(err.Error(), "27") {
		t.Fatalf("err = %v, want a refusal naming 27", err)
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want errors.Is(err, ErrRefused)", err)
	}
	assertNothingCreated(t, rt, fg, name)
}

// TestChainStartRefusesAMissingBase pins the base refusal's class: a --base
// that does not resolve is a refused input, not an internal failure, and it
// cuts no worktree.
func TestChainStartRefusesAMissingBase(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	fg.refSHA = map[string]string{} // every ref resolves to nothing
	_, err := ChainStart(context.Background(), rt, ChainOptions{
		Name: "shop", Plans: []string{writePlan(t, "p")}, Feature: "auth",
		Base: "refs/heads/nope", MasterMindID: testMasterMindName,
	})
	if err == nil || !strings.Contains(err.Error(), `base "refs/heads/nope" not found`) {
		t.Fatalf("err = %v, want a missing-base refusal", err)
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want errors.Is(err, ErrRefused)", err)
	}
	if len(fg.addWorktreeCalls) != 0 {
		t.Errorf("a refusal must cut no worktree: %+v", fg.addWorktreeCalls)
	}
}

// TestChainStartRefusesAWriterReviewer pins the reviewer's shape: a writer
// actor cannot fill the review part, so the default workflow refuses it against
// the actor's declared outputs.
func TestChainStartRefusesAWriterReviewer(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	_, err := ChainStart(context.Background(), rt, ChainOptions{
		Name: "shop", Plans: []string{writePlan(t, "p")}, Feature: "auth",
		ReviewerActor: "builder", MasterMindID: testMasterMindName,
	})
	if err == nil || !strings.Contains(err.Error(), "rule 3") {
		t.Fatalf("err = %v, want the workflow's reviewer refusal", err)
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want errors.Is(err, ErrRefused): a bad reviewer actor is refused, not internal", err)
	}
	assertNothingCreated(t, rt, fg, "shop")
}

// TestChainStartRefusesAReaderBuilder pins the builder's shape: the builder
// param names the member that owns the chain's tree, so a reader there is
// refused rather than left as the chain's writer.
func TestChainStartRefusesAReaderBuilder(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	_, err := ChainStart(context.Background(), rt, ChainOptions{
		Name: "shop", Plans: []string{writePlan(t, "p")}, Feature: "auth",
		BuilderActor: "assistant", MasterMindID: testMasterMindName,
	})
	if err == nil || !strings.Contains(err.Error(), "chain builder actor") {
		t.Fatalf("err = %v, want a refusal naming the builder actor", err)
	}
	assertNothingCreated(t, rt, fg, "shop")
}

// TestChainStartCutsTheBuilderFromBase pins --base on the cut: the worktree is
// cut from the commit the named ref resolves to, and the chain row records it.
func TestChainStartCutsTheBuilderFromBase(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	fg.refSHA = map[string]string{"release/v1": "commit-base-456"}

	res := startedChain(t, rt, ChainOptions{Base: "release/v1"})

	if len(fg.refSHACalls) == 0 || fg.refSHACalls[0].Ref != "release/v1" {
		t.Fatalf("RefSHA calls = %+v, want release/v1 resolved", fg.refSHACalls)
	}
	if len(fg.addWorktreeCalls) != 1 || fg.addWorktreeCalls[0].Commit != "commit-base-456" {
		t.Fatalf("AddWorktree calls = %+v, want the resolved base commit", fg.addWorktreeCalls)
	}
	if res.Chain.Base != "commit-base-456" || res.Chain.Branch != "relevo/shop" {
		t.Errorf("chain row base/branch = %q/%q, want the resolved base and relevo/shop", res.Chain.Base, res.Chain.Branch)
	}
}

// TestChainStartCopiesPlansSoLaterEditsDoNotChangeThem pins the plan copies:
// the chain reads its own copy, so editing the source afterwards changes
// nothing.
func TestChainStartCopiesPlansSoLaterEditsDoNotChangeThem(t *testing.T) {
	t.Parallel()

	rt, _ := chainRuntime(t)
	source := writePlan(t, "the original plan")
	startedChain(t, rt, ChainOptions{Plans: []string{source}})

	if err := os.WriteFile(source, []byte("edited after the start"), 0o644); err != nil {
		t.Fatalf("edit the source: %v", err)
	}

	copied, err := os.ReadFile(rt.Store.ChainPlanPath("shop", 1))
	if err != nil {
		t.Fatalf("read the copy: %v", err)
	}
	if string(copied) != "the original plan" {
		t.Errorf("copy = %q, want the original plan", copied)
	}

	row, err := rt.Store.Chain("shop")
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	var paths []string
	if err := json.Unmarshal(row.PlanPathsJSON, &paths); err != nil {
		t.Fatalf("decode plan paths: %v", err)
	}
	if len(paths) != 1 || paths[0] != rt.Store.ChainPlanPath("shop", 1) {
		t.Errorf("plan paths = %v, want the stored copy", paths)
	}
}

// TestChainStartWritesNothingWhenCreateChainFails pins the atomicity from the
// chain's side: a member whose directory cannot be made leaves no chain row,
// no member and no worktree behind.
func TestChainStartWritesNothingWhenCreateChainFails(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	// A member's binding directory that is a file refuses prepareSave inside
	// CreateChain's transaction.
	if err := os.WriteFile(rt.Store.Dir("shop-rev"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("plant the blocking file: %v", err)
	}

	_, err := ChainStart(context.Background(), rt, ChainOptions{
		Name: "shop", Plans: []string{writePlan(t, "p")}, Feature: "auth", MasterMindID: testMasterMindName,
	})
	if err == nil {
		t.Fatal("ChainStart = nil, want the member's error")
	}

	for _, name := range []string{"shop", "shop-rev", "shop-plan"} {
		if _, err := rt.Store.Load(name); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("binding %q exists after the failed create: %v", name, err)
		}
	}
	if _, err := rt.Store.Chain("shop"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("chain exists after the failed create: %v", err)
	}
	if len(fg.removeWorktreeCalls) != 1 || !fg.removeWorktreeCalls[0].Force {
		t.Errorf("rollback = %+v, want one forced RemoveWorktree", fg.removeWorktreeCalls)
	}
	if len(fg.deleteBranchCalls) != 1 {
		t.Errorf("branch rollback = %+v, want one DeleteBranch", fg.deleteBranchCalls)
	}
	if _, err := os.Stat(rt.Store.ChainPlanPath("shop", 1)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("plan copy exists after the failed create: %v", err)
	}
}

// TestChainStartStoresTheResolvedSettings pins the settings chain row: the
// flags beat policy.chain, which beats the defaults, and what is stored is the
// resolved values.
func TestChainStartStoresTheResolvedSettings(t *testing.T) {
	t.Parallel()

	t.Run("the policy defaults", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		res := startedChain(t, rt, ChainOptions{Security: ptr(true)})

		set := storedSettings(t, res.Chain)
		want := chain.Settings{
			MaxCorrections: policy.DefaultChainMaxCorrections,
			ReviewerActor:  policy.DefaultChainReviewerActor,
			PlannerActor:   policy.DefaultChainPlannerActor,
			SecurityActor:  policy.DefaultChainSecurityActor,
			Security:       true,
		}
		if set != want {
			t.Errorf("settings = %+v, want %+v", set, want)
		}
	})

	t.Run("policy values", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		rt.Policy = policy.Policy{Chain: &policy.ChainPolicy{
			MaxCorrections: ptr(5),
			ReviewerActor:  "reviewer",
			PlannerActor:   "lite-planner",
			SecurityActor:  "security",
			Security:       ptr(true),
		}}
		res := startedChain(t, rt, ChainOptions{})

		set := storedSettings(t, res.Chain)
		if set.MaxCorrections != 5 || !set.Security {
			t.Errorf("settings = %+v, want the policy's 5 and security on", set)
		}
	})

	t.Run("flags beat policy", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		res := startedChain(t, rt, ChainOptions{
			MaxCorrections: ptr(1),
			ReviewerActor:  "assistant",
			Security:       ptr(false),
		})

		set := storedSettings(t, res.Chain)
		if set.MaxCorrections != 1 || set.ReviewerActor != "assistant" || set.Security {
			t.Errorf("settings = %+v, want the flags' 1/assistant/off", set)
		}
	})
}

// TestChainStartResolvesTheBuilderCheck pins the check the builder member is
// started with: policy.gate.default and gate.regate fill an unset flag, each
// flag beats the policy, and no gate anywhere leaves no check. The readers
// never hold a gate, and the stored settings and the result agree.
func TestChainStartResolvesTheBuilderCheck(t *testing.T) {
	t.Parallel()

	gatePolicy := func() policy.Policy {
		return policy.Policy{Gate: &policy.GatePolicy{Default: "make check", Regate: ptr(2)}}
	}

	t.Run("policy fills an unset flag", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		rt.Policy = gatePolicy()
		res := startedChain(t, rt, ChainOptions{})

		assertBuilderCheck(t, rt, res, "make check")
		if set := storedSettings(t, res.Chain); set.Regate != 2 {
			t.Errorf("settings regate = %d, want the policy's 2", set.Regate)
		}
	})

	t.Run("gate beats the policy", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		rt.Policy = gatePolicy()
		res := startedChain(t, rt, ChainOptions{Gate: "go test ./..."})

		assertBuilderCheck(t, rt, res, "go test ./...")
	})

	t.Run("no-gate beats the policy", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		rt.Policy = gatePolicy()
		res := startedChain(t, rt, ChainOptions{NoGate: true})

		assertBuilderCheck(t, rt, res, "")
	})

	t.Run("regate beats the policy", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		rt.Policy = gatePolicy()
		res := startedChain(t, rt, ChainOptions{Regate: ptr(5)})

		if set := storedSettings(t, res.Chain); set.Regate != 5 {
			t.Errorf("settings regate = %d, want the flag's 5", set.Regate)
		}
	})

	t.Run("no flags and no policy leaves no check", func(t *testing.T) {
		t.Parallel()

		rt, _ := chainRuntime(t)
		res := startedChain(t, rt, ChainOptions{})

		assertBuilderCheck(t, rt, res, "")
	})
}

// assertBuilderCheck pins the resolved gate on the stored setting and the
// workflow's gate param, and that a local workflow writer and its readers carry
// no member gate: a workflow chain runs its checks as steps.
func assertBuilderCheck(t *testing.T, rt Runtime, res ChainResult, want string) {
	t.Helper()
	if set := storedSettings(t, res.Chain); set.Gate != want {
		t.Errorf("settings gate = %q, want %q", set.Gate, want)
	}
	if got := storedFlowParam(t, res.Chain, "gate"); got != want {
		t.Errorf("workflow gate param = %q, want %q", got, want)
	}
	if res.Check != "" {
		t.Errorf("result check = %q, want empty: a workflow chain runs its checks as steps", res.Check)
	}
	for _, m := range res.Members {
		if m.Gate != "" || m.Regate != 0 {
			t.Errorf("member %s carries gate %q regate %d, want none", m.Name, m.Gate, m.Regate)
		}
	}
}

// storedSettings decodes a chain row's stored settings.
func storedSettings(t *testing.T, row db.ChainRow) chain.Settings {
	t.Helper()
	var set chain.Settings
	if err := json.Unmarshal(row.SettingsJSON, &set); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	return set
}

// memberByName returns the member binding named name, failing the test when it
// is missing.
func memberByName(t *testing.T, members []store.Binding, name string) store.Binding {
	t.Helper()
	for _, m := range members {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("no member %q in %d members", name, len(members))
	return store.Binding{}
}

// TestChainStartRecordsThePlanStartCommit pins plan 1's start: the chain row
// carries the commit its worktree was cut from.
func TestChainStartRecordsThePlanStartCommit(t *testing.T) {
	t.Parallel()

	rt, fg := chainRuntime(t)
	res := startedChain(t, rt, ChainOptions{})

	if fg.headCommitID == "" {
		t.Fatal("test premise: the fake git must report a head commit")
	}
	if res.Chain.PlanStartCommit != fg.headCommitID {
		t.Errorf("result plan start = %q, want the cut commit %q", res.Chain.PlanStartCommit, fg.headCommitID)
	}
	if got := chainStoredRow(t, rt, "shop").PlanStartCommit; got != fg.headCommitID {
		t.Errorf("stored plan start = %q, want %q", got, fg.headCommitID)
	}
}
