//go:build unix

package owner

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/db/wire"
)

// startVerb launches one sync verb.
//
// It deliberately does not pin a SQL connection: a verb runs against the
// handles the server already holds, so taking a pool slot would hold a
// connection this request never uses. It does take the connection's single
// request slot, so a client that sends a second verb before reading the first
// answer waits for the clear rather than having its connection dropped.
//
// The token arrives in the frame's raw tail. It is handed to the hook and to
// nothing else: it is never logged, never formatted into an error, and never
// written into the response, whose only token field is a bool.
func (c *conn) startVerb(frame []byte) error {
	var m wire.SyncVerb
	token, err := wire.Decode(frame, &m)
	if err != nil {
		return err
	}
	if c.s.OnSyncVerb == nil {
		return c.refuse(wire.RefuseNoSyncVerb,
			"this owner does not run sync verbs; upgrade the daemon that is serving it")
	}
	// A drain refuses a verb for the same reason it refuses a statement: the
	// owner guarantees the request never ran, so the client may retry it. A verb
	// inside an open transaction runs, so that transaction can commit.
	if c.s.isDraining() && !c.transactionOpen() {
		return c.refuse(wire.RefuseRestarting, "the owner is restarting")
	}

	r, ctx, err := c.claim(m.ID)
	if err != nil {
		return err
	}
	go func() {
		defer func() {
			c.mu.Lock()
			if c.cur == r {
				c.cur = nil
			}
			c.mu.Unlock()
			close(r.done)
			r.cancel()
		}()
		c.runVerb(ctx, r, &m, token)
	}()
	return nil
}

// runVerb answers one verb with the hook's result. A panic in the hook is turned
// into a fixed-text error rather than taking the daemon down with it: the token
// was on the stack, and the text that goes back must not be whatever the
// runtime happened to print.
func (c *conn) runVerb(ctx context.Context, r *request, m *wire.SyncVerb, token []byte) {
	out := c.invoke(ctx, m, token)
	out.ID = r.id
	if err := c.send(wire.KindSyncResult, out, nil); err != nil {
		return
	}
}

// invoke calls the hook under a recover, so a panic becomes a refusal whose
// text is written here rather than formatted from the panic value.
func (c *conn) invoke(ctx context.Context, m *wire.SyncVerb, token []byte) (out *wire.SyncResult) {
	defer func() {
		if rec := recover(); rec != nil {
			out = &wire.SyncResult{
				Header:  wire.Header{Type: wire.TypeSyncResult},
				OK:      false,
				Code:    wire.SyncCodeInternal,
				Message: "the daemon failed while running this sync verb; nothing was reported",
			}
		}
	}()
	res := c.s.OnSyncVerb(ctx, m, token)
	if res == nil {
		return &wire.SyncResult{
			Header:  wire.Header{Type: wire.TypeSyncResult},
			OK:      false,
			Code:    wire.SyncCodeInternal,
			Message: "the daemon ran this sync verb and reported nothing",
		}
	}
	if res.Type == "" {
		res.Type = wire.TypeSyncResult
	}
	return res
}

// current is the request this connection is serving, nil when it has none.
func (c *conn) current() *request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}
