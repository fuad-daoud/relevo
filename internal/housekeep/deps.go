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
}

// Prefixer is the narrow git surface ResolveSubdir needs: the repository
// prefix of the directory a bind started from. The engine's git client
// satisfies it.
type Prefixer interface {
	ShowPrefix(ctx context.Context, dir string) (string, error)
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
