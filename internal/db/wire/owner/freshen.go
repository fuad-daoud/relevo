//go:build unix

package owner

import "log/slog"

// freshen hands a reader's hint to the hook and answers nothing. It takes no
// request slot, so a verb in flight on this connection or another never delays
// it, and a hook that panics costs the hint rather than the connection.
func (c *conn) freshen() {
	hook := c.s.OnSyncFreshen
	if hook == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Error("owner: sync freshen hook panicked", "panic", r)
		}
	}()
	hook()
}
