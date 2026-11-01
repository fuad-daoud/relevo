package client

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// Actor probes whether a placement is viable before anything is created. The
// server serves this route only when it advertises remote.FeaturePlacement;
// without it the caller treats the placement as reachable-only.
func (c *Client) Actor(ctx context.Context, server, actor, candidate string) (remote.ActorView, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	path := fmt.Sprintf("/v1/actors/%s", url.PathEscape(actor))
	if candidate != "" {
		path += "?candidate=" + url.QueryEscape(candidate)
	}
	return getJSON[remote.ActorView](c, ctx, server, path, "decode actor view")
}
