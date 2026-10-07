package relevo

import (
	"context"
)

// queueSync is the seal trigger's half of the sync seam.
//
// It is called where a seal that moved bytes used to hand a change set to the
// background, and it is called still: the trigger stays on the tick so the
// wiring has one place to come back to. What it does is nothing, because there
// is no engine to hand a change set to in this build. It takes no slot, starts
// no goroutine and opens no handle, so a seal costs exactly what it cost before
// any of this was wired.
func (d *Daemon) queueSync(context.Context) {}

// idleSync is the window trigger's half of the sync seam.
//
// The window existed to catch edits no round produced, by polling the network
// on an interval. With no engine there is nothing to poll and nothing to catch,
// so the window opens no handle and writes no marker. It stays on the tick, and
// it stays callable, so the path a later engine is wired into is the path this
// one already occupies rather than a new one.
func (d *Daemon) idleSync(context.Context) {}
