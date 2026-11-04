package relevo

import (
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// ChainServerTakesWorkflow reports whether --workflow may be combined with
// --server. Shipped names ride the workflow wire path the server already
// validates, so the client can post them; anything else still needs a server
// the client has not proven to run workflows.
func ChainServerTakesWorkflow(name string) bool {
	if name == "" {
		return true
	}
	_, ok := workflow.Shipped(name)
	return ok
}
