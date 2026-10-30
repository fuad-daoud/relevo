package widget

import "time"

// TickMsg is the poll timer's message; the shell re-arms it on every tick.
type TickMsg time.Time
