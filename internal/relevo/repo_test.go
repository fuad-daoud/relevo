package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// The per-repository settings: the identity the section keys on, the gate rule
// that reads one entry's gate as the tier between the flag and the policy
// default, and the repos config section every open tests a write inside.

// reposRootCommit is the root commit the tests' fake gits answer. The
// repositories' settings key on remote.RepoID of it, never the raw SHA (see
// TestRepoKeyIsTheRootCommitID).
const reposRootCommit = "2222222222222222222222222222222222222222"

// reposKeyOf is the identity remote.RepoID gives the test root commit.
func reposKeyOf(t *testing.T) string {
	t.Helper()
	id, err := remote.RepoID(reposRootCommit)
	if err != nil {
		t.Fatalf("remote.RepoID(%q): %v", reposRootCommit, err)
	}
	return id
}

// runtimeWithRepos is newTestRuntime whose config store carries one repos
// entry at the test root commit's identity, with the given gate.
func runtimeWithRepos(t *testing.T, fg *fakeGit, gate string) (Runtime, string) {
	t.Helper()
	id, err := remote.RepoID(reposRootCommit)
	if err != nil {
		t.Fatalf("remote.RepoID: %v", err)
	}
	body := fmt.Sprintf(`{%q:{"name":"shop","gate":%q}}`, id, gate)
	st := auditStore(t)
	if _, err := st.As("test", "seed repos").Put(config.Repos, []byte(body)); err != nil {
		t.Fatalf("Put(repos %s): %v", body, err)
	}
	rt := newTestRuntime(t, fg)
	rt.Config = st
	rt.Policy = policyWithGate("go vet ./...", nil)
	return rt, id
}

// seedOneRepos stores one section body through the store, for tests that need
// only one write.
func seedOneRepos(t *testing.T, st *config.Store, body string) {
	t.Helper()
	if _, err := st.As("test", "write "+string(config.Repos)).Put(config.Repos, []byte(body)); err != nil {
		t.Fatalf("Put(repos): %v", err)
	}
}

// TestResolveGatePrefersRepoOverPolicy pins the four living tiers plus the
// flag tiers: --no-gate wins over everything, an explicit --gate is used as
// given, an unset flag takes the repository's own gate before the policy
// default, and a tree with no settings and no policy takes no gate. Swapping
// the repo and policy tiers is the mutation this test catches.
func TestResolveGatePrefersRepoOverPolicy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		gate       string
		noGate     bool
		repo       RepoSettings
		hasPolicy  bool
		cmd, built string
	}{
		{"the flag wins over the repo", "--gate", false, RepoSettings{Gate: "make check"}, true, "--gate", GateSourceFlag},
		{"no-gate beats the repo", "x", true, RepoSettings{Gate: "make check"}, true, "", ""},
		{"the repo beats the policy", "", false, RepoSettings{Gate: "make check"}, true, "make check", GateSourceRepo},
		{"the policy tier when the repo sets nothing", "", false, RepoSettings{}, true, "go vet ./...", GateSourcePolicy},
		{"none when the repo and the policy are both empty", "", false, RepoSettings{}, false, "", ""},
	}
	for _, tc := range cases {
		pol := policy.Policy{}
		if tc.hasPolicy {
			pol = policyWithGate("go vet ./...", nil)
		}
		got := resolveGateFor(tc.gate, tc.noGate, pol, true, tc.repo)
		if got.Command != tc.cmd || got.Source != tc.built {
			t.Errorf("%s: resolveGateFor = %+v, want {%q %q}", tc.name, got, tc.cmd, tc.built)
		}
	}
}

// TestResolveGateTakesNoGateForARoleThatDoesNotCheck pins the role rule where
// the seed's judgement call keeps it: a role whose check flag is off takes no
// gate even when its repository sets one, because the role rule decides
// whether any check runs at all, not which tier supplies it. Dropping the
// roleChecks guard is the mutation this test catches.
func TestResolveGateTakesNoGateForARoleThatDoesNotCheck(t *testing.T) {
	t.Parallel()

	repo := RepoSettings{Key: "ident", Gate: "make check"}
	pol := policyWithGate("go vet ./...", nil)

	got := resolveGateFor("", false, pol, false, repo)
	if got.Command != "" || got.Source != "" {
		t.Errorf("resolveGateFor(a reader with a repo gate) = %+v, want no gate at all", got)
	}
}

// TestRepoKeyIsTheRootCommitID pins the identity: remote.RepoID over the root
// commit, the lower-case hex sha256 the wire already travels -- never the raw
// 40-hex root SHA itself. Keying on the raw SHA is the mutation this test
// catches.
func TestRepoKeyIsTheRootCommitID(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{rootCommitSHA: reposRootCommit}
	key, ok, err := RepoKey(context.Background(), fg, "/repo")
	if err != nil {
		t.Fatalf("RepoKey: %v", err)
	}
	if !ok {
		t.Fatal("RepoKey ok = false, want true for a root commit that answers")
	}
	if want := reposKeyOf(t); key != want {
		t.Errorf("RepoKey = %q, want the repo id %q", key, want)
	}
	if key == reposRootCommit {
		t.Errorf("RepoKey = the raw root SHA %q, want remote.RepoID's hash of it", key)
	}
	if len(fg.rootCommitCalls) != 1 || fg.rootCommitCalls[0].Dir != "/repo" {
		t.Errorf("RootCommit calls = %+v, want one call named /repo", fg.rootCommitCalls)
	}
}

// TestRepoKeyWithoutCommitsIsNoSettings pins the failure table: a repository
// with no commits, a directory that is not a repository and a git that cannot
// answer each read as ok=false with no error, and any other error propagates.
// Turning one of the three into its error is the mutation this test catches.
func TestRepoKeyWithoutCommitsIsNoSettings(t *testing.T) {
	t.Parallel()

	for _, err := range []error{git.ErrRefMissing, git.ErrNotRepo, git.ErrGitUnavailable} {
		fg := &fakeGit{rootCommitErr: err}
		key, ok, got := RepoKey(context.Background(), fg, "/repo")
		if got != nil {
			t.Errorf("RepoKey after %v = %v, want no error", err, got)
		}
		if key != "" {
			t.Errorf("RepoKey after %v = %q, want no identity", err, key)
		}
		if ok {
			t.Errorf("RepoKey after %v: ok = true, want no settings", err)
		}
	}

	fg := &fakeGit{rootCommitErr: errors.New("disk on fire")}
	if _, _, got := RepoKey(context.Background(), fg, "/repo"); got == nil {
		t.Error("RepoKey after an unknown git error = nil, want the error to propagate")
	}
}

// TestRepoSettingsCostsNoGitWhenTheSectionIsEmpty pins the cheap path: a
// machine whose repos section is absent reads no git at all, so a machine
// that never used the feature pays nothing and a git that cannot answer
// cannot break its binds. Moving the RepoKey call above the empty-section
// check is the mutation this test catches.
func TestRepoSettingsCostsNoGitWhenTheSectionIsEmpty(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{rootCommitSHA: reposRootCommit}
	rt := newTestRuntime(t, fg)
	st := auditStore(t)
	seedOneRepos(t, st, `{}`) // a section present, but with no entry either
	rt.Config = st

	set, err := RepoSettingsFor(context.Background(), rt, "/repo")
	if err != nil {
		t.Fatalf("RepoSettingsFor: %v", err)
	}
	if set != (RepoSettings{}) {
		t.Errorf("RepoSettingsFor = %+v, want the zero value", set)
	}
	if fg.calls != 0 {
		t.Errorf("git calls = %d, want zero: an empty section must not reach git", fg.calls)
	}

	// A machine with no section at all is the same answer.
	rt2 := newTestRuntime(t, fg)
	rt2.Config = auditStore(t)
	set, err = RepoSettingsFor(context.Background(), rt2, "/repo")
	if err != nil {
		t.Fatalf("RepoSettingsFor(no section): %v", err)
	}
	if set != (RepoSettings{}) || fg.calls != 0 {
		t.Errorf("RepoSettingsFor(no section) = %+v after %d git calls, want the zero value and zero calls", set, fg.calls)
	}
}

// TestRepoSettingsReadsTheEntryForThisRepositoryOnly pins that the entry read
// is the one this repository's identity keys, and an identity the section
// does not name reads as the zero value with no error.
func TestRepoSettingsReadsTheEntryForThisRepositoryOnly(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{rootCommitSHA: reposRootCommit}
	rt, key := runtimeWithRepos(t, fg, "make check")

	set, err := RepoSettingsFor(context.Background(), rt, "/repo")
	if err != nil {
		t.Fatalf("RepoSettingsFor: %v", err)
	}
	want := RepoSettings{Key: key, Name: "shop", Gate: "make check"}
	if set != want {
		t.Errorf("RepoSettingsFor = %+v, want %+v", set, want)
	}

	// A tree whose identity the section does not name.
	fg.rootCommitSHA = "3333333333333333333333333333333333333333"
	set, err = RepoSettingsFor(context.Background(), rt, "/repo")
	if err != nil {
		t.Fatalf("RepoSettingsFor(other tree): %v", err)
	}
	if set != (RepoSettings{}) {
		t.Errorf("RepoSettingsFor(other tree) = %+v, want the zero value", set)
	}
}

// TestBindStoresTheRepoGate pins the create path: a bind in a repository whose
// settings name a gate stores that gate, and a policy default the repository
// does not override stays behind it. Passing the zero record at the create's
// gate tier is the mutation this test catches.
func TestBindStoresTheRepoGate(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: reposRootCommit,
	}
	rt, _ := runtimeWithRepos(t, fg, "make check")

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.Gate != "make check" {
		t.Errorf("binding gate = %q, want the repository's own make check", b.Gate)
	}
}

// TestBindIgnoresAGitThatCannotIdentifyTheTree pins the seed's rule that a
// directory git cannot identify holds no settings and a bind must not start
// failing because of it.
func TestBindIgnoresAGitThatCannotIdentifyTheTree(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{
		repoFactsOrigin:     "git@github.com:o/r.git",
		rootCommitErr:       git.ErrNotRepo,
		currentBranchResult: "main",
	}
	rt, _ := runtimeWithRepos(t, fg, "make check")

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind must tolerate a tree git cannot identify, got %v", err)
	}
	if b.Gate != "go vet ./..." {
		t.Errorf("binding gate = %q, want the policy's go vet ./...", b.Gate)
	}
}

// TestAddStoresTheRepoGate pins the local add path: a worktree add whose
// parent checkout (opts.Repo) settings name a gate stores that gate on the new
// binding, and rides the resolution back out for the CLI's printed line.
// Passing the zero record at the add's gate tier is the mutation this test
// catches.
func TestAddStoresTheRepoGate(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123", rootCommitSHA: reposRootCommit}
	rt, _ := runtimeWithRepos(t, fg, "make check")

	res, err := Add(context.Background(), rt, AddOptions{
		Name: "api-auth", Branch: "feature/api-auth", Candidate: testAgyRef,
		MasterMindID: testMasterMindName, Repo: addRepo(t),
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := res.Resolution.Gate; got.Command != "make check" || got.Source != GateSourceRepo {
		t.Errorf("resolution gate = %+v, want make check from the repo setting", got)
	}
	if res.Binding.Gate != "make check" {
		t.Errorf("binding gate = %q, want make check", res.Binding.Gate)
	}
}

// TestRemoteAddSendsTheRepoGate pins the remote add path: the repository's own
// settings are read on the client, the gate they name rides the create request
// on the wire, and the mirror's resolution names the tier. Keeping the create
// request's gate from another tier is the mutation this test catches.
func TestRemoteAddSendsTheRepoGate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: reposRootCommit,
	}
	rt, _ := runtimeWithRepos(t, fg, "make check")
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{
			Name:      "api",
			Candidate: "claude/anthropic/haiku",
		},
	}
	rt.Remote = fr

	res, err := Add(ctx, rt, AddOptions{
		Name: "api", Server: "zen", Repo: "/fake/repo", MasterMindID: testMasterMindName,
	})
	if err != nil {
		t.Fatalf("Add --server: %v", err)
	}
	if fr.createBindingReq.Gate != "make check" {
		t.Errorf("create Gate = %q, want the repository's own make check", fr.createBindingReq.Gate)
	}
	if got := res.Resolution.Gate; got.Command != "make check" || got.Source != GateSourceRepo {
		t.Errorf("mirror resolution gate = %+v, want make check from the repo setting", got)
	}
}

// TestChainStartTakesTheRepoGate pins the start path: a chain started on a
// remote builder in a repository whose settings name a gate stores it in the
// chain's Settings.Gate, rides the server create, and names the repository
// tier on the result. A local workflow start runs the check as a step, so the
// remote start is where the resolved check travels; the dry-run test pins the
// local workflow's param. Dropping the tier from chainSettings is the mutation
// this test catches.
func TestChainStartTakesTheRepoGate(t *testing.T) {
	t.Parallel()

	fr := chainCheckFake()
	rt, _, _ := chainRemoteRuntime(t, fr)
	id, err := remote.RepoID(reposRootCommit)
	if err != nil {
		t.Fatalf("remote.RepoID: %v", err)
	}
	st := auditStore(t)
	seedOneRepos(t, st, fmt.Sprintf(`{%q:{"name":"shop","gate":"make check"}}`, id))
	rt.Config = st
	rt.Policy = policyWithGate("go vet ./...", nil)

	res := startedChain(t, rt, ChainOptions{})

	if fr.createBindingReq.Gate != "make check" {
		t.Errorf("create Gate = %q, want the repository's own make check", fr.createBindingReq.Gate)
	}
	if res.Check != "make check" || res.CheckSource != GateSourceRepo {
		t.Errorf("result check = %q (%q), want make check named from the repo setting", res.Check, res.CheckSource)
	}
	if got := storedSettings(t, res.Chain).Gate; got != "make check" {
		t.Errorf("settings gate = %q, want the repository's own make check", got)
	}
}

// TestChainDryRunFillsGateFromTheRepo pins the dry-run path: the workflow's
// gate param renders the repository's own gate, ahead of the policy default
// chainParamsFor would otherwise fill. Keeping pol.GateDefault() in the gate
// slot is the mutation this test catches.
func TestChainDryRunFillsGateFromTheRepo(t *testing.T) {
	t.Parallel()

	fg := &fakeGit{headCommitID: "commit-head-123", rootCommitSHA: reposRootCommit}
	rt, _ := runtimeWithRepos(t, fg, "make check")
	if _, err := os.Getwd(); err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	doc, err := ChainDryRun(context.Background(), rt, ChainOptions{Name: "shop"})
	if err != nil {
		t.Fatalf("ChainDryRun: %v", err)
	}
	for _, p := range doc.Params {
		if p.Name == "gate" {
			if p.Value != "make check" {
				t.Errorf("gate param = %q, want the repository's own make check", p.Value)
			}
			return
		}
	}
	t.Fatalf("the dry run took no params gate: %+v", doc.Params)
}

// TestGateLineNamesTheSource pins all five renderings the CLI prints: the
// gate line with each tier's parenthetical, the bare form a source with no
// word renders (the binding's stored command) and the chain's own check line.
// Printing the command with no source, or always with the policy tier, is the
// mutation this test catches.
func TestGateLineNamesTheSource(t *testing.T) {
	t.Parallel()

	cases := []struct{ cmd, source, want string }{
		{"make check", GateSourceFlag, "gate: make check (--gate)"},
		{"make check", GateSourceRepo, "gate: make check (repo setting)"},
		{"make check", GateSourcePolicy, "gate: make check (policy.gate.default)"},
		{"make check", GateSourceBinding, "gate: make check (binding)"},
		{"", GateSourcePolicy, "gate: none"},
		{"make check", "", "gate: make check"},
	}
	for _, tc := range cases {
		if got := GateLine(tc.cmd, tc.source); got != tc.want {
			t.Errorf("GateLine(%q, %q) = %q, want %q", tc.cmd, tc.source, got, tc.want)
		}
	}
	if got := CheckLine("make check", GateSourceRepo); got != "check: make check (repo setting)" {
		t.Errorf("CheckLine = %q, want check: make check (repo setting)", got)
	}
}
