package relevo

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/view"
)

// MasterMindStatus builds the statusline's rows for one scope: the same scoped
// read every other status format does, with the line's own DONE rule set on it
// -- a DONE binding never takes a line. --all is the one view that shows them,
// because that is the view in which the reader asked for everything.
func MasterMindStatus(ctx context.Context, rt Runtime, scope Scope) (view.Report, error) {
	scope.HideDone = !scope.All
	return ScopedStatus(ctx, rt, scope)
}
