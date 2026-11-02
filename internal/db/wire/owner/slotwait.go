//go:build unix

package owner

import (
	"fmt"
	"time"
)

// defaultSlotWait bounds how long a request waits for a free pinned-connection
// slot before it is refused as busy. A slot frees only when a client
// disconnects, so a burst of short-lived clients drains on its own; thirty
// seconds lets that drain while a wedged slot stops holding a caller past it.
const defaultSlotWait = 30 * time.Second

// slotWait is the bound in force, a var so a test can shrink it the way it
// shrinks the client's handshake budget. It is read when a request waits, so
// a change affects later waits only.
var slotWait = defaultSlotWait

// SetSlotWait sets the bound a later slot wait gives up after. A non-positive
// d restores defaultSlotWait.
func SetSlotWait(d time.Duration) {
	if d <= 0 {
		d = defaultSlotWait
	}
	slotWait = d
}

// slotBusyError reports a pin that gave up waiting for a free slot. It carries
// the held and maximum counts the refusal names, so the message the client
// sees is fixed text with the numbers, never prose a caller must decode.
type slotBusyError struct {
	held int
	max  int
}

func (e *slotBusyError) Error() string {
	return fmt.Sprintf("the owner is busy (%d/%d connection slots held); retry in a moment", e.held, e.max)
}
