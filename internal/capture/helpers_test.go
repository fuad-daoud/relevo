package capture

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/git"
)

// fakeGit scripts the git calls capture makes -- SnapshotTree, DiffTrees,
// HeadCommit, RevListCount and Dirty -- with the fields and counters the tests
// assert on. Nothing here runs a process.
type fakeGit struct {
	snapshotTreeID  string
	snapshotTreeErr error
	snapshotCalls   int

	diffResult   git.Diff
	diffErr      error
	diffCalls    int
	lastDiffDir  string
	lastDiffFrom string
	lastDiffTo   string

	headCommitID  string
	headCommitErr error
	headCalls     int

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
}

func (f *fakeGit) SnapshotTree(_ context.Context, _ string) (string, error) {
	f.snapshotCalls++
	if f.snapshotTreeErr != nil {
		return "", f.snapshotTreeErr
	}
	return f.snapshotTreeID, nil
}

func (f *fakeGit) DiffTrees(_ context.Context, dir, from, to string) (git.Diff, error) {
	f.diffCalls++
	f.lastDiffDir = dir
	f.lastDiffFrom = from
	f.lastDiffTo = to
	if f.diffErr != nil {
		return git.Diff{}, f.diffErr
	}
	return f.diffResult, nil
}

func (f *fakeGit) HeadCommit(_ context.Context, _ string) (string, error) {
	f.headCalls++
	if f.headCommitErr != nil {
		return "", f.headCommitErr
	}
	return f.headCommitID, nil
}

func (f *fakeGit) RevListCount(_ context.Context, dir, from, to string) (int, error) {
	f.revListCalls++
	f.lastRevListDir = dir
	f.lastRevListFrom = from
	f.lastRevListTo = to
	if f.revListErr != nil {
		return 0, f.revListErr
	}
	return f.revListCount, nil
}

func (f *fakeGit) Dirty(_ context.Context, dir string) (bool, error) {
	f.dirtyCalls++
	f.lastDirtyDir = dir
	if f.dirtyErr != nil {
		return false, f.dirtyErr
	}
	return f.dirtyResult, nil
}
