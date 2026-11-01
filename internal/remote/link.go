package remote

import (
	"github.com/fuad-daoud/relevo/internal/store"
)

// Link is the client row's link to the server's copy, from a create view:
// nil when the view carries no installation and no record id, which is what
// a server that does not advertise FeatureOrigin sends.
func Link(view BindingView) *store.RemoteLink {
	if view.Installation == "" && view.ID == "" {
		return nil
	}
	return &store.RemoteLink{Installation: view.Installation, ID: view.ID}
}
