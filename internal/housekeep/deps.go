package housekeep

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// Deps carries the engine-owned dependencies housekeeping code needs, so the
// engine passes them explicitly instead of the package reaching back into it.
type Deps struct {
	// Store confines every temp entry under TmpRoot, so a planted name or
	// link never escapes the root.
	Store *store.Store
	// TmpRoot is the state directory whose per-process entries the rounds and
	// checks create. Empty means no per-process dirs: callers remove nothing.
	TmpRoot string
	// ChainCheckSettled reports whether a chain check row already carries a
	// terminal result. The engine owns the row's lifecycle states.
	ChainCheckSettled func(db.ChainCheckRow) bool
	// ChainFlowTerminal reports whether a run status ends the chain. Terminal
	// chains keep no check directories: their killed runs' leftovers are the
	// sweep's reason to walk the chains at all.
	ChainFlowTerminal func(workflow.Status) bool
	// GateUnit names the gate scope unit for a binding's own gate run.
	GateUnit func(owner, name string, round int) string
	// ServedCheckUnit names the gate scope unit for one served check run.
	ServedCheckUnit func(b store.Binding, run store.CheckRun) string
	// ChainCheckUnit names the gate scope unit for one chain check run.
	ChainCheckUnit func(c db.ChainRow, run int) string
	// Git is the slice of the git CLI scratch worktrees need. The engine's
	// git client satisfies it.
	Git Git
	// Refs is the narrow git surface ref cleanup needs: the head existence
	// probe, the relevo ref listing, the pushed check, and the branch and ref
	// deletes. The engine's git client satisfies it.
	Refs Refs
	// Teardown is the engine's worktree teardown as plain values: what the
	// finished binding's worktree came to, without the engine's outcome type.
	Teardown func(ctx context.Context, b store.Binding, dryRun bool) TeardownOutcome
	// AskUnbind asks a remote binding's server to release it, outside any
	// lock. The engine owns the remote client the ask goes through.
	AskUnbind func(ctx context.Context, b store.Binding, force bool) error
	// ErrKept is the engine's kept-worktree sentinel: the ask or teardown
	// outcome that keeps the binding in place. GC matches it with errors.Is.
	ErrKept error
	// KeptReason projects the reason out of an ErrKept error, or "" when the
	// error carries none.
	KeptReason func(err error) string
}

// TeardownOutcome is what the engine's worktree teardown decided, as plain
// values: the worktree it removed, left in place, or found already gone, and
// why it was kept. Exactly one of Removed, Kept and Gone is set, or none
// when the binding never had a worktree.
type TeardownOutcome struct {
	Removed string
	Kept    string
	Reason  string
	Gone    string
}

// Prefixer is the narrow git surface ResolveSubdir needs: the repository
// prefix of the directory a bind started from. The engine's git client
// satisfies it.
type Prefixer interface {
	ShowPrefix(ctx context.Context, dir string) (string, error)
}

// Refs is the narrow git surface ref cleanup needs: the head existence
// probe, the relevo ref listing, the pushed check, and the branch and ref
// deletes. The engine's git client satisfies it.
type Refs interface {
	RefSHA(ctx context.Context, dir, ref string) (string, bool, error)
	ListRefs(ctx context.Context, dir, prefix string) ([]string, error)
	RefOnRemote(ctx context.Context, dir, ref string) (bool, error)
	DeleteBranch(ctx context.Context, dir, branch string) error
	DeleteRef(ctx context.Context, dir, ref string) error
}

// Git is the narrow git surface scratch worktrees need: the HEAD and
// snapshot reads and the detached-worktree add, materialize and remove. The
// engine's git client satisfies it.
type Git interface {
	HeadCommit(ctx context.Context, dir string) (string, error)
	SnapshotTree(ctx context.Context, dir string) (string, error)
	AddDetachedWorktree(ctx context.Context, dir, path, commit string) error
	MaterializeTree(ctx context.Context, dir, tree string) error
	RemoveWorktree(ctx context.Context, dir, path string, force bool) error
}
