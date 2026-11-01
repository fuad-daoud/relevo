package housekeep

import (
	"context"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/spawn"
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
	// Abandon records the binding's current builder session as abandoned and
	// clears the id. The caller saves.
	Abandon func(b store.Binding) store.Binding
	// ClearProcess is the endpoint between rounds: no pid, no start time, no
	// log. Identity is untouched.
	ClearProcess func(e store.Endpoint) store.Endpoint
	// Halt settles a binding into needs-you with the message, through the
	// same path every other halt takes.
	Halt func(ctx context.Context, tx *store.Tx, b store.Binding, message string) (store.Binding, error)
	// Preflight refuses a process start when the tree is short of space. A nil
	// refusal means the disk is fine.
	Preflight func(tree string) error
	// ShowCommand renders the relevo show command that prints one round's
	// artifact, the durable way a halt names a closed round.
	ShowCommand func(name string, round int, section string) string
	// Admit starts a queued round's builder. NotQueued is the engine's
	// sentinel for a round that already left the queue, which an admit that
	// lost a race answers with.
	Admit     func(ctx context.Context, name string) error
	NotQueued error
	// AllowedCPUs is the runtime scope's allowed_cpus the round pool is
	// parsed from. Empty means no pool: the round runs unpinned.
	AllowedCPUs string
	// HeldCPUs returns the cores held by live rounds other than self. Nil
	// means the local census, which reads only the caller's tx.
	HeldCPUs func(tx *store.Tx, self string) ([]int, error)
	// Scope is the runtime's scope template. Nil means scopes are off, so no
	// oom probe runs.
	Scope *spawn.ScopeSpec
	// UnitName is the per-round scope unit's base name the oom probe asks for.
	UnitName func(b store.Binding) string
	// ScopeProber reports a scope unit's final result from the journal. Nil
	// means the runner cannot probe, so nothing is oom-killed.
	ScopeProber spawn.ScopeResultProber
	// Now is the runtime's clock.
	Now func() time.Time
	// HaltChainEnv runs the engine's chain halt for an environment note: the
	// definition and state reads, the cloned halted state and the terminal
	// transition, for the check row the caller already loaded.
	HaltChainEnv func(ctx context.Context, tx *store.Tx, c db.ChainRow, row db.ChainCheckRow, ev workflow.Event) (bool, error)
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

// Refs is the narrow git surface ref cleanup needs.
type Refs interface {
	RefSHA(ctx context.Context, dir, ref string) (string, bool, error)
	ListRefs(ctx context.Context, dir, prefix string) ([]string, error)
	RefOnRemote(ctx context.Context, dir, ref string) (bool, error)
	DeleteBranch(ctx context.Context, dir, branch string) error
	DeleteRef(ctx context.Context, dir, ref string) error
}

// Git is the narrow git surface scratch worktrees need.
type Git interface {
	HeadCommit(ctx context.Context, dir string) (string, error)
	SnapshotTree(ctx context.Context, dir string) (string, error)
	AddDetachedWorktree(ctx context.Context, dir, path, commit string) error
	MaterializeTree(ctx context.Context, dir, tree string) error
	RemoveWorktree(ctx context.Context, dir, path string, force bool) error
}
