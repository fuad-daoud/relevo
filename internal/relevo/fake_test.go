package relevo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/usage"
)

type startCall struct {
	Name, Kind, Pane string
	Args             []string
}

type addWorktreeCall struct {
	Dir, Path, Branch, Commit string
}

type addDetachedWorktreeCall struct {
	Dir, Path, Commit string
}

type checkoutWorktreeCall struct {
	Dir, Path, Branch string
}

type removeWorktreeCall struct {
	Dir, Path string
	Force     bool
}

type refSHACall struct {
	Dir, Ref string
}

type updateRefCall struct {
	Dir, Ref, NewSHA, OldSHA string
}

type deleteRefCall struct {
	Dir, Ref string
}

type listRefsCall struct {
	Dir, Prefix string
}

type refOnRemoteCall struct {
	Dir, Ref string
}

type commitTreeCall struct {
	Dir, Tree, Parent, Message string
}

type commitAllCall struct {
	Dir, Message string
}

type mergeFFCall struct {
	Dir, Ref string
}

type rootCommitCall struct {
	Dir string
}

type createBranchCall struct {
	Dir, Branch, Commit string
}

type createTrackingBranchCall struct {
	Dir, Branch, Upstream string
}

type deleteBranchCall struct {
	Dir, Branch string
}

type currentBranchCall struct{ Dir string }

type fetchCall struct {
	Dir, Remote, Ref string
}

type rebaseCall struct {
	Dir, Onto string
}

type mergeCall struct {
	Dir, Ref string
}

type pushCall struct {
	Dir, Remote, Branch string
	ForceWithLease      bool
}

type remoteBranchExistsCall struct {
	Dir, Remote, Branch string
}

// fakeGit is the in-memory Git used by tests in this package.
type fakeGit struct {
	snapshotTreeID  string
	snapshotTreeErr error
	snapshotCalls   int
	lastSnapshotDir string

	diffResult   git.Diff
	diffErr      error
	diffCalls    int
	lastDiffDir  string
	lastDiffFrom string
	lastDiffTo   string

	worktreeStat         git.Stat
	worktreeStatErr      error
	worktreeStatCalls    int
	lastWorktreeStatDir  string
	lastWorktreeStatTree string

	headCommitID  string
	headCommitErr error
	headCalls     int
	lastHeadDir   string

	branchExists    bool
	branchExistsErr error
	branchCalls     int
	lastBranchDir   string
	lastBranchName  string

	createBranchErr   error
	createBranchCalls []createBranchCall

	createTrackingBranchErr   error
	createTrackingBranchCalls []createTrackingBranchCall

	deleteBranchErr   error
	deleteBranchCalls []deleteBranchCall
	deleteBranchFunc  func(ctx context.Context, dir, branch string) error

	addWorktreeErr   error
	addWorktreeCalls []addWorktreeCall

	addDetachedWorktreeErr   error
	addDetachedWorktreeCalls []addDetachedWorktreeCall

	checkoutWorktreeErr   error
	checkoutWorktreeCalls []checkoutWorktreeCall

	removeWorktreeErr   error
	removeWorktreeCalls []removeWorktreeCall

	// materializeCalls records every MaterializeTree call; materializeErr
	// makes each one fail. Both are the scratch worktree's hooks.
	materializeCalls []struct{ dir, tree string }
	materializeErr   error

	dirtyResult  bool
	dirtyErr     error
	dirtyCalls   int
	lastDirtyDir string

	revListCount    int
	revListErr      error
	revListCalls    int
	lastRevListDir  string
	lastRevListFrom string
	lastRevListTo   string

	refSHACalls []refSHACall
	refSHA      map[string]string
	refSHAErr   error

	updateRefCalls []updateRefCall
	updateRefErr   error

	deleteRefCalls []deleteRefCall
	deleteRefErr   error

	listRefsCalls    []listRefsCall
	listRefsResult   []string
	listRefsByPrefix map[string][]string
	listRefsErr      error

	refOnRemoteCalls []refOnRemoteCall
	refOnRemote      map[string]bool
	refOnRemoteErr   error

	commitTreeCalls []commitTreeCall
	commitTreeSHA   string
	commitTreeErr   error

	commitAllCalls []commitAllCall
	commitAllSHA   string
	commitAllErr   error

	mergeFFCalls []mergeFFCall
	mergeFFErr   error

	rootCommitCalls []rootCommitCall
	rootCommitSHA   string
	rootCommitErr   error

	// repoFactsOrigin/repoFactsCommonDir configure RepoFacts's success
	// return; repoFactsCommonDir defaults to "<dir>/.git" when unset, since
	// that is what a real repo with no worktrees reports.
	repoFactsOrigin    string
	repoFactsCommonDir string
	repoFactsErr       error
	repoFactsCalls     []repoFactsCall

	// identityName/identityEmail/identityErr configure Identity (#335).
	// Most tests build fakeGit as a bare struct literal, so an unset pair
	// with identityUnset false returns the test defaults below -- what a
	// developer's own repo would resolve -- and every remote-add test keeps
	// passing without setting an identity. identityUnset true means "both
	// keys really are unset", which addRemote must refuse.
	identityName  string
	identityEmail string
	identityErr   error
	identityUnset bool

	// tags is what ListTags returns; ListTagsErr makes it fail.
	tags        map[string]string
	listTagsErr error

	// treeFingerprints is the sequence TreeFingerprint returns, one entry
	// per call; the last repeats once the sequence is exhausted, so a test
	// that wants a constant signal sets one entry. treeFingerprintErr makes
	// every call fail.
	treeFingerprints     []string
	treeFingerprintErr   error
	treeFingerprintCalls int

	// The land primitives (#136). rebaseConflicts/mergeConflicts are scripted
	// as the conflict set: setting either makes the corresponding call return
	// those paths with git.ErrMergeConflict. remoteBranchExists is what
	// RemoteBranchExists reports, which decides whether Push gets the lease.
	currentBranchResult string
	currentBranchErr    error
	currentBranchCalls  []currentBranchCall

	fetchCalls []fetchCall
	fetchErr   error

	rebaseCalls     []rebaseCall
	rebaseConflicts []string
	rebaseErr       error

	mergeCalls     []mergeCall
	mergeConflicts []string
	mergeErr       error

	pushCalls []pushCall
	pushErr   error

	remoteBranchExists      bool
	remoteBranchExistsErr   error
	remoteBranchExistsCalls []remoteBranchExistsCall
}

type repoFactsCall struct{ Dir string }

func (f *fakeGit) SnapshotTree(ctx context.Context, dir string) (string, error) {
	f.snapshotCalls++
	f.lastSnapshotDir = dir
	if f.snapshotTreeErr != nil {
		return "", f.snapshotTreeErr
	}
	return f.snapshotTreeID, nil
}

func (f *fakeGit) DiffTrees(ctx context.Context, dir, from, to string) (git.Diff, error) {
	f.diffCalls++
	f.lastDiffDir = dir
	f.lastDiffFrom = from
	f.lastDiffTo = to
	if f.diffErr != nil {
		return git.Diff{}, f.diffErr
	}
	return f.diffResult, nil
}

func (f *fakeGit) DiffWorktreeStat(ctx context.Context, dir, tree string) (git.Stat, error) {
	f.worktreeStatCalls++
	f.lastWorktreeStatDir = dir
	f.lastWorktreeStatTree = tree
	if f.worktreeStatErr != nil {
		return git.Stat{}, f.worktreeStatErr
	}
	return f.worktreeStat, nil
}

func (f *fakeGit) HeadCommit(ctx context.Context, dir string) (string, error) {
	f.headCalls++
	f.lastHeadDir = dir
	if f.headCommitErr != nil {
		return "", f.headCommitErr
	}
	return f.headCommitID, nil
}

func (f *fakeGit) BranchExists(ctx context.Context, dir, branch string) (bool, error) {
	f.branchCalls++
	f.lastBranchDir = dir
	f.lastBranchName = branch
	if f.branchExistsErr != nil {
		return false, f.branchExistsErr
	}
	return f.branchExists, nil
}

func (f *fakeGit) CreateBranch(ctx context.Context, dir, branch, commit string) error {
	f.createBranchCalls = append(f.createBranchCalls, createBranchCall{
		Dir: dir, Branch: branch, Commit: commit,
	})
	if f.createBranchErr != nil {
		return f.createBranchErr
	}
	return nil
}

func (f *fakeGit) CreateTrackingBranch(ctx context.Context, dir, branch, upstream string) error {
	f.createTrackingBranchCalls = append(f.createTrackingBranchCalls, createTrackingBranchCall{
		Dir: dir, Branch: branch, Upstream: upstream,
	})
	return f.createTrackingBranchErr
}

func (f *fakeGit) DeleteBranch(ctx context.Context, dir, branch string) error {
	f.deleteBranchCalls = append(f.deleteBranchCalls, deleteBranchCall{
		Dir: dir, Branch: branch,
	})
	if f.deleteBranchFunc != nil {
		return f.deleteBranchFunc(ctx, dir, branch)
	}
	return f.deleteBranchErr
}

func (f *fakeGit) AddWorktree(ctx context.Context, dir, path, branch, commit string) error {
	f.addWorktreeCalls = append(f.addWorktreeCalls, addWorktreeCall{
		Dir: dir, Path: path, Branch: branch, Commit: commit,
	})
	if f.addWorktreeErr != nil {
		return f.addWorktreeErr
	}
	return nil
}

func (f *fakeGit) AddDetachedWorktree(ctx context.Context, dir, path, commit string) error {
	f.addDetachedWorktreeCalls = append(f.addDetachedWorktreeCalls, addDetachedWorktreeCall{
		Dir: dir, Path: path, Commit: commit,
	})
	if f.addDetachedWorktreeErr != nil {
		return f.addDetachedWorktreeErr
	}
	return nil
}

// MaterializeTree records the call and touches no disk, like the other
// worktree methods here.
func (f *fakeGit) MaterializeTree(ctx context.Context, dir, tree string) error {
	f.materializeCalls = append(f.materializeCalls, struct{ dir, tree string }{dir, tree})
	return f.materializeErr
}

func (f *fakeGit) CheckoutWorktree(ctx context.Context, dir, path, branch string) error {
	f.checkoutWorktreeCalls = append(f.checkoutWorktreeCalls, checkoutWorktreeCall{
		Dir: dir, Path: path, Branch: branch,
	})
	if f.checkoutWorktreeErr != nil {
		return f.checkoutWorktreeErr
	}
	return nil
}

func (f *fakeGit) RemoveWorktree(ctx context.Context, dir, path string, force bool) error {
	f.removeWorktreeCalls = append(f.removeWorktreeCalls, removeWorktreeCall{
		Dir: dir, Path: path, Force: force,
	})
	if f.removeWorktreeErr != nil {
		return f.removeWorktreeErr
	}
	return nil
}

func (f *fakeGit) Dirty(ctx context.Context, dir string) (bool, error) {
	f.dirtyCalls++
	f.lastDirtyDir = dir
	if f.dirtyErr != nil {
		return false, f.dirtyErr
	}
	return f.dirtyResult, nil
}

func (f *fakeGit) RevListCount(ctx context.Context, dir, from, to string) (int, error) {
	f.revListCalls++
	f.lastRevListDir = dir
	f.lastRevListFrom = from
	f.lastRevListTo = to
	if f.revListErr != nil {
		return 0, f.revListErr
	}
	return f.revListCount, nil
}

func (f *fakeGit) RefSHA(ctx context.Context, dir, ref string) (string, bool, error) {
	f.refSHACalls = append(f.refSHACalls, refSHACall{Dir: dir, Ref: ref})
	if f.refSHAErr != nil {
		return "", false, f.refSHAErr
	}
	if f.refSHA != nil {
		sha, ok := f.refSHA[ref]
		return sha, ok, nil
	}
	return "fakerefsha", true, nil
}

func (f *fakeGit) UpdateRef(ctx context.Context, dir, ref, newSHA, oldSHA string) error {
	f.updateRefCalls = append(f.updateRefCalls, updateRefCall{
		Dir: dir, Ref: ref, NewSHA: newSHA, OldSHA: oldSHA,
	})
	if f.refSHA != nil {
		f.refSHA[ref] = newSHA
	}
	return f.updateRefErr
}

func (f *fakeGit) DeleteRef(ctx context.Context, dir, ref string) error {
	f.deleteRefCalls = append(f.deleteRefCalls, deleteRefCall{Dir: dir, Ref: ref})
	return f.deleteRefErr
}

func (f *fakeGit) ListRefs(ctx context.Context, dir, prefix string) ([]string, error) {
	f.listRefsCalls = append(f.listRefsCalls, listRefsCall{Dir: dir, Prefix: prefix})
	if f.listRefsErr != nil {
		return nil, f.listRefsErr
	}
	if f.listRefsByPrefix != nil {
		return f.listRefsByPrefix[prefix], nil
	}
	return f.listRefsResult, nil
}

func (f *fakeGit) RefOnRemote(ctx context.Context, dir, ref string) (bool, error) {
	f.refOnRemoteCalls = append(f.refOnRemoteCalls, refOnRemoteCall{Dir: dir, Ref: ref})
	if f.refOnRemoteErr != nil {
		return false, f.refOnRemoteErr
	}
	return f.refOnRemote[ref], nil
}

func (f *fakeGit) CommitTree(ctx context.Context, dir, tree, parent, message string) (string, error) {
	f.commitTreeCalls = append(f.commitTreeCalls, commitTreeCall{
		Dir: dir, Tree: tree, Parent: parent, Message: message,
	})
	if f.commitTreeErr != nil {
		return "", f.commitTreeErr
	}
	if f.commitTreeSHA != "" {
		return f.commitTreeSHA, nil
	}
	return "fakecommit", nil
}

func (f *fakeGit) CommitAll(ctx context.Context, dir, message string) (string, error) {
	f.commitAllCalls = append(f.commitAllCalls, commitAllCall{Dir: dir, Message: message})
	if f.commitAllErr != nil {
		return "", f.commitAllErr
	}
	return f.commitAllSHA, nil
}

func (f *fakeGit) MergeFF(ctx context.Context, dir, ref string) error {
	f.mergeFFCalls = append(f.mergeFFCalls, mergeFFCall{Dir: dir, Ref: ref})
	return f.mergeFFErr
}

func (f *fakeGit) RootCommit(ctx context.Context, dir string) (string, error) {
	f.rootCommitCalls = append(f.rootCommitCalls, rootCommitCall{Dir: dir})
	if f.rootCommitErr != nil {
		return "", f.rootCommitErr
	}
	if f.rootCommitSHA != "" {
		return f.rootCommitSHA, nil
	}
	return "fakerootcommit", nil
}

func (f *fakeGit) ListTags(ctx context.Context, dir string) (map[string]string, error) {
	if f.listTagsErr != nil {
		return nil, f.listTagsErr
	}
	return f.tags, nil
}

func (f *fakeGit) RepoFacts(ctx context.Context, dir string) (originURL, commonDir string, err error) {
	f.repoFactsCalls = append(f.repoFactsCalls, repoFactsCall{Dir: dir})
	if f.repoFactsErr != nil {
		return "", "", f.repoFactsErr
	}
	commonDir = f.repoFactsCommonDir
	if commonDir == "" {
		commonDir = dir + "/.git"
	}
	return f.repoFactsOrigin, commonDir, nil
}

// Identity returns the configured identity, or the defaults when no
// identity was configured at all (see the field comment above).
func (f *fakeGit) Identity(ctx context.Context, dir string) (name, email string, err error) {
	if f.identityErr != nil {
		return "", "", f.identityErr
	}
	if f.identityName == "" && f.identityEmail == "" && !f.identityUnset {
		return "Test User", "test@example.com", nil
	}
	return f.identityName, f.identityEmail, nil
}

// TreeFingerprint returns the next configured fingerprint; the last repeats
// once the sequence is exhausted. No entries means "".
func (f *fakeGit) TreeFingerprint(ctx context.Context, dir string) (string, error) {
	if f.treeFingerprintErr != nil {
		return "", f.treeFingerprintErr
	}
	if len(f.treeFingerprints) == 0 {
		return "", nil
	}
	i := f.treeFingerprintCalls
	f.treeFingerprintCalls++
	if i >= len(f.treeFingerprints) {
		i = len(f.treeFingerprints) - 1
	}
	return f.treeFingerprints[i], nil
}

func (f *fakeGit) CurrentBranch(ctx context.Context, dir string) (string, error) {
	f.currentBranchCalls = append(f.currentBranchCalls, currentBranchCall{Dir: dir})
	if f.currentBranchErr != nil {
		return "", f.currentBranchErr
	}
	return f.currentBranchResult, nil
}

func (f *fakeGit) Fetch(ctx context.Context, dir, remote, ref string) error {
	f.fetchCalls = append(f.fetchCalls, fetchCall{Dir: dir, Remote: remote, Ref: ref})
	return f.fetchErr
}

func (f *fakeGit) Rebase(ctx context.Context, dir, onto string) ([]string, error) {
	f.rebaseCalls = append(f.rebaseCalls, rebaseCall{Dir: dir, Onto: onto})
	if f.rebaseErr != nil {
		return nil, f.rebaseErr
	}
	if len(f.rebaseConflicts) > 0 {
		return f.rebaseConflicts, git.ErrMergeConflict
	}
	return nil, nil
}

func (f *fakeGit) Merge(ctx context.Context, dir, ref string) ([]string, error) {
	f.mergeCalls = append(f.mergeCalls, mergeCall{Dir: dir, Ref: ref})
	if f.mergeErr != nil {
		return nil, f.mergeErr
	}
	if len(f.mergeConflicts) > 0 {
		return f.mergeConflicts, git.ErrMergeConflict
	}
	return nil, nil
}

func (f *fakeGit) Push(ctx context.Context, dir, remote, branch string, forceWithLease bool) error {
	f.pushCalls = append(f.pushCalls, pushCall{
		Dir: dir, Remote: remote, Branch: branch, ForceWithLease: forceWithLease,
	})
	return f.pushErr
}

func (f *fakeGit) RemoteBranchExists(ctx context.Context, dir, remote, branch string) (bool, error) {
	f.remoteBranchExistsCalls = append(f.remoteBranchExistsCalls,
		remoteBranchExistsCall{Dir: dir, Remote: remote, Branch: branch})
	if f.remoteBranchExistsErr != nil {
		return false, f.remoteBranchExistsErr
	}
	return f.remoteBranchExists, nil
}

func TestFakeSatisfiesGit(t *testing.T) {
	t.Parallel()

	var _ Git = (*fakeGit)(nil)
	var _ Git = (*git.Client)(nil)
}

// fakeClock is a movable Now for the tests that need time to pass. newRuntime's
// clock is fixed, which is what most tests want; withClock swaps it out on an
// already-built runtime rather than duplicating every seed helper.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// withClock returns rt with its clock replaced. Runtime is a value with a
// *store.Store inside, so the copy shares the same state directory.
func withClock(rt Runtime, c *fakeClock) Runtime {
	rt.Now = c.Now
	return rt
}

// fakeRunner is the in-memory Runner (#99). It records every Start and
// Kill, answers Alive from a per-pid script (the last answer repeats; an
// unscripted pid is alive until killed), and reports the exit code a test
// set with exit(). Nothing here runs a process.
type fakeRunner struct {
	specs   []spawn.ProcSpec
	handles []spawn.ProcHandle
	kills   []spawn.ProcHandle

	startErr error
	aliveErr error
	killErr  error

	// onStart runs at the top of Start, before the spec is recorded. Ask
	// calls Runner.Start as its first action after releasing the lock, which
	// is where a test proves the lock is free and where it can rewrite the
	// reservation to simulate a slow spawn.
	onStart func()

	// onAlive runs at the top of Alive, before the scripted answer is read.
	// A test uses it to act in the window between closeOnMarker's read and
	// the liveness observation, e.g. to write the marker file (#328).
	onAlive func()

	alive     map[int][]bool
	exits     map[int]int
	nextPID   int
	exitPaths []string
	rusages   map[int]spawn.ProcRusage

	// scopeActive is the answer ScopeActive gives per unit base name; a
	// missing key is false. scopeQueries records every unit asked for, in
	// order, so a test can prove that no probe ran.
	scopeActive  map[string]bool
	scopeQueries []string

	// scopeResults is the answer ScopeResult gives per unit base name; a
	// missing key returns "". scopeResultQueries records every unit asked for,
	// in order, so a test can prove that no probe ran.
	scopeResults       map[string]string
	scopeResultQueries []string

	// scopeStops records every unit StopScope was asked to end, in order, so
	// a test can prove that a scope was reaped -- or that none was.
	scopeStops []string
	// scopeStopErr, when set, is what StopScope returns, so a test can pin
	// the refusal when a scope cannot be ended.
	scopeStopErr error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{alive: map[int][]bool{}, exits: map[int]int{}, nextPID: 4000}
}

// script sets the sequence Alive returns for pid; the last answer repeats.
func (f *fakeRunner) script(pid int, answers ...bool) {
	f.alive[pid] = append([]bool(nil), answers...)
}

// exit sets the code ExitCode reports for pid.
func (f *fakeRunner) exit(pid, code int) { f.exits[pid] = code }

// setRusage sets what Rusage reports for pid; an unset pid reports ok=false.
func (f *fakeRunner) setRusage(pid int, r spawn.ProcRusage) {
	if f.rusages == nil {
		f.rusages = map[int]spawn.ProcRusage{}
	}
	f.rusages[pid] = r
}

func (f *fakeRunner) Start(_ context.Context, spec spawn.ProcSpec) (spawn.ProcHandle, error) {
	if f.onStart != nil {
		f.onStart()
	}
	if f.startErr != nil {
		return spawn.ProcHandle{}, f.startErr
	}
	f.nextPID++
	h := spawn.ProcHandle{PID: f.nextPID, StartedAt: time.Unix(1_700_000_000+int64(f.nextPID), 0)}
	f.specs = append(f.specs, spec)
	f.handles = append(f.handles, h)
	if _, scripted := f.alive[h.PID]; !scripted {
		f.alive[h.PID] = []bool{true}
	}
	return h, nil
}

func (f *fakeRunner) Alive(_ context.Context, h spawn.ProcHandle) (bool, error) {
	if f.onAlive != nil {
		f.onAlive()
	}
	if f.aliveErr != nil {
		return false, f.aliveErr
	}
	seq := f.alive[h.PID]
	if len(seq) == 0 {
		return false, nil
	}
	if len(seq) > 1 {
		f.alive[h.PID] = seq[1:]
	}
	return seq[0], nil
}

func (f *fakeRunner) ExitCode(_ context.Context, h spawn.ProcHandle, path string) (int, bool) {
	f.exitPaths = append(f.exitPaths, path)
	code, ok := f.exits[h.PID]
	return code, ok
}

func (f *fakeRunner) Kill(_ context.Context, h spawn.ProcHandle) error {
	if f.killErr != nil {
		return f.killErr
	}
	f.kills = append(f.kills, h)
	f.alive[h.PID] = []bool{false}
	return nil
}

func (f *fakeRunner) Rusage(_ context.Context, h spawn.ProcHandle, _ string) (spawn.ProcRusage, bool) {
	r, ok := f.rusages[h.PID]
	return r, ok
}

// ScopeActive implements ScopeProber: it records the unit and answers from
// scopeActive, so a send test can prove both that the guard fired and that
// scopes-off never probes.
func (f *fakeRunner) ScopeActive(_ context.Context, unit string) (bool, error) {
	f.scopeQueries = append(f.scopeQueries, unit)
	return f.scopeActive[unit], nil
}

// ScopeResult implements ScopeResultProber: it records the unit and answers
// from scopeResults, so a test can prove that oom detection ran or did not.
func (f *fakeRunner) ScopeResult(_ context.Context, unit string) (string, error) {
	f.scopeResultQueries = append(f.scopeResultQueries, unit)
	return f.scopeResults[unit], nil
}

// StopScope implements ScopeStopper: it records every unit it is asked to end
// and, unless a test scripted an error, clears that unit's active flag so a
// later probe sees it gone.
func (f *fakeRunner) StopScope(_ context.Context, unit string) error {
	f.scopeStops = append(f.scopeStops, unit)
	if f.scopeStopErr != nil {
		return f.scopeStopErr
	}
	delete(f.scopeActive, unit)
	return nil
}

// fakeUsage scripts what the usage reader returns and records the Source
// it was asked for.
type fakeUsage struct {
	samples     []usage.Sample
	note        string
	sources     []usage.Source
	block       bool // when true, Read waits for ctx and returns nothing
	peekSamples []usage.Sample
	peekNote    string
	peeks       []usage.Source // recorded by Peek; Peek never blocks
}

func (f *fakeUsage) Read(ctx context.Context, src usage.Source) ([]usage.Sample, string) {
	f.sources = append(f.sources, src)
	if f.block {
		<-ctx.Done()
		return nil, "blocked"
	}
	return f.samples, f.note
}

func (f *fakeUsage) Peek(ctx context.Context, src usage.Source) ([]usage.Sample, string) {
	f.peeks = append(f.peeks, src)
	return f.peekSamples, f.peekNote
}

func TestFakeRunnerScriptsAliveAndRecordsKills(t *testing.T) {
	t.Parallel()

	f := newFakeRunner()
	var _ spawn.Runner = f
	var _ spawn.ScopeStopper = (*fakeRunner)(nil)

	h, err := f.Start(context.Background(), spawn.ProcSpec{Dir: "/tree", Argv: []string{"agy", "-p", "x"}, LogPath: "/state/x/001-builder.log"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(f.specs) != 1 || f.specs[0].Dir != "/tree" || f.specs[0].LogPath != "/state/x/001-builder.log" {
		t.Fatalf("specs = %+v", f.specs)
	}
	if h.PID == 0 || h.StartedAt.IsZero() {
		t.Fatalf("handle = %+v; want a pid and a time", h)
	}
	// Unscripted: alive forever.
	for i := 0; i < 3; i++ {
		if alive, _ := f.Alive(context.Background(), h); !alive {
			t.Fatalf("unscripted Alive #%d = false", i)
		}
	}
	// Scripted: true, true, then false forever.
	f.script(h.PID, true, true, false)
	want := []bool{true, true, false, false}
	for i, w := range want {
		if alive, _ := f.Alive(context.Background(), h); alive != w {
			t.Errorf("scripted Alive #%d = %v, want %v", i, alive, w)
		}
	}
	// ExitCode is absent until set.
	if _, ok := f.ExitCode(context.Background(), h, ""); ok {
		t.Error("ExitCode before exit() must be ok=false")
	}
	f.exit(h.PID, 3)
	if code, ok := f.ExitCode(context.Background(), h, ""); !ok || code != 3 {
		t.Errorf("ExitCode = %d, %v; want 3, true", code, ok)
	}

	// A second Start gets a distinct pid; Kill records it and makes it dead.
	h2, _ := f.Start(context.Background(), spawn.ProcSpec{Dir: "/tree", Argv: []string{"agy"}, LogPath: "/state/x/002-builder.log"})
	if h2.PID == h.PID {
		t.Fatal("two Starts returned the same pid")
	}
	if err := f.Kill(context.Background(), h2); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if len(f.kills) != 1 || f.kills[0] != h2 {
		t.Errorf("kills = %+v, want [h2]", f.kills)
	}
	if alive, _ := f.Alive(context.Background(), h2); alive {
		t.Error("a killed handle must read as not alive")
	}

	// Errors pass through.
	f.startErr = errors.New("no binary")
	if _, err := f.Start(context.Background(), spawn.ProcSpec{Argv: []string{"x"}}); err == nil {
		t.Error("startErr not returned")
	}
	f.aliveErr = errors.New("ps refused")
	if _, err := f.Alive(context.Background(), h); err == nil {
		t.Error("aliveErr not returned")
	}
}
