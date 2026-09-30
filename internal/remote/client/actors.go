package client

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// Actor asks a server what its create would answer for actor, so a caller can
// decide whether a placement is viable before anything is created. candidate
// pins one token and is left off when empty, exactly as a create leaves it off.
//
// The server serves this route only when it advertises remote.FeaturePlacement;
// a server that does not is missing the only way to ask, and the caller then
// treats the placement as reachable-only.
func (c *Client) Actor(ctx context.Context, server, actor, candidate string) (remote.ActorView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	path := fmt.Sprintf("/v1/actors/%s", url.PathEscape(actor))
	if candidate != "" {
		path += "?candidate=" + url.QueryEscape(candidate)
	}
	return getJSON[remote.ActorView](c, ctx, server, path, "decode actor view")
}
