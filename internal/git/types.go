package git

import "errors"

var ErrNotRepo = errors.New("not a git repository")

var ErrGitUnavailable = errors.New("git binary unavailable")

var ErrBranchExists = errors.New("branch already exists")

var ErrWorktreeDirty = errors.New("worktree has uncommitted changes")

var ErrBranchCheckedOut = errors.New("branch is checked out in another worktree")

var ErrNotFastForward = errors.New("ref update is not a fast-forward")

var ErrMergeConflict = errors.New("uncommitted changes conflict with the update")

var ErrBadBundle = errors.New("bundle is malformed or its prerequisites are missing")

var ErrRefMissing = errors.New("ref does not exist")

const DefaultMaxPatchBytes = 4 << 20

// Stat is the shape of a diff, kept even when the patch body was too large to
// keep.
type Stat struct {
	FilesChanged int
	Insertions   int
	Deletions    int
}

func (s Stat) Empty() bool {
	return s.FilesChanged == 0
}

// Diff is one tree-to-tree comparison.
type Diff struct {
	Stat      Stat
	Patch     []byte // nil when Truncated or Stat.Empty()
	Truncated bool   // body exceeded the client's cap; Stat is still exact
}
