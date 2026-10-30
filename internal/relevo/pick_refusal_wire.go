package relevo

import (
	"errors"
	"net/http"
	"strings"

	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/resolve"
)

// pickRefusalFromWire rebuilds the typed refusal a server's pick answered a
// create with: every candidate gated, or no candidate serving the actor. The
// server sends the local refusal's text under 422 invalid, so the client
// recognises it by the sentinel's text and the CLI classifies it exactly as a
// local bind's refusal, instead of as an internal failure. Anything else is
// returned as it came.
func pickRefusalFromWire(err error) error {
	var httpErr *client.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusUnprocessableEntity || httpErr.Body.Code != remote.CodeInvalid {
		return err
	}
	msg := httpErr.Body.Message
	for _, sentinel := range []error{resolve.ErrAllGated, resolve.ErrRoleNotServed} {
		if strings.Contains(msg, sentinel.Error()) {
			return refusal.Classed(sentinel, msg)
		}
	}
	return err
}
