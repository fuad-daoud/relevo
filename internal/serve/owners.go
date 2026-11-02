package serve

import (
	"log/slog"
	"sync"
)

// strayBindingsWarned remembers the stray bindings-dir names already warned
// about. Five per-owner walks scan the same directory, so without it one
// stray entry warns once per pass per walk.
var strayBindingsWarned sync.Map

func warnStrayBindingsEntry(name string) {
	if _, seen := strayBindingsWarned.LoadOrStore(name, struct{}{}); seen {
		return
	}
	slog.Warn("unexpected entry in bindings dir", "entry", name)
}
