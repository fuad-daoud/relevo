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
	// ParseScratchName splits a scratch directory name "<name>-<NNN>".
	ParseScratchName func(entry string) (name string, round int, ok bool)
}

// Prefixer is the narrow git surface ResolveSubdir needs: the repository
// prefix of the directory a bind started from. The engine's git client
// satisfies it.
type Prefixer interface {
	ShowPrefix(ctx context.Context, dir string) (string, error)
}
