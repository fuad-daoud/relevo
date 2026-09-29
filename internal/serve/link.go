package serve

import (
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// servedLink is the link a create request records on the server's own row: the
// client's installation id and the record id that client pre-minted for its
// copy. A request carrying neither -- an old client -- records no link.
func servedLink(req remote.CreateBindingRequest) *store.RemoteLink {
	if req.ClientInstallation == "" && req.ClientBindingID == "" {
		return nil
	}
	return &store.RemoteLink{Installation: req.ClientInstallation, ID: req.ClientBindingID}
}

// servedView is relevo.ServedView with the server's own facts: the binding's
// record id, which identifies the server copy, and this server's installation
// id. A record id that cannot be read is a warn and an empty id -- the view is
// still the caller's answer.
func (s *Server) servedView(rt relevo.Runtime, b store.Binding, entries []store.LogEntry) remote.BindingView {
	recordID, err := rt.Store.RecordID(b.Name)
	if err != nil {
		slog.Warn("served view record id", "binding", b.Name, "err", err)
	}
	return relevo.ServedView(b, entries, recordID, s.cfg.Installation.ID)
}
