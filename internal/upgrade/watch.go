package upgrade

import (
	"context"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// PreflightTimeout bounds the `--preflight` run Check makes, so a hung
// preflight cannot wedge the daemon.
const PreflightTimeout = 30 * time.Second

// Action is what Check decided about the watched file.
type Action int

const (
	None    Action = iota // the file is ours, or a build already refused
	Wait                  // the debounce has not yet seen one identity twice
	Reexec                // the new binary passed preflight; exec into it
	Refused               // the new binary failed preflight; not tried again
)

func (a Action) String() string {
	switch a {
	case None:
		return "none"
	case Wait:
		return "wait"
	case Reexec:
		return "reexec"
	case Refused:
		return "refused"
	}
	return "unknown"
}

// Decision is Check's answer, with Reason set when Action is Refused.
type Decision struct {
	Action Action
	Reason string
}

// Watcher decides when the file at Path is a new binary worth re-exec'ing
// into. Stat and Preflight are injected, so it is testable without a real
// file, process or clock.
type Watcher struct {
	Path      string
	Started   store.FileID
	Stat      func(string) (store.FileID, error)
	Preflight func(ctx context.Context, path string) error

	pending *store.FileID // identity seen once, awaiting a second Check
	refused *store.FileID // identity that failed preflight; never retried
}

// Check runs one decision step; called once per daemon tick, so the debounce
// counts ticks.
func (w *Watcher) Check(ctx context.Context) Decision {
	cur, err := w.Stat(w.Path)
	if err != nil {
		w.pending = nil
		return Decision{Action: Wait}
	}

	if cur == w.Started {
		// Back to our own binary, as after a rollback: a refused build may be
		// installed and tried again.
		w.pending = nil
		w.refused = nil
		return Decision{Action: None}
	}

	if w.refused != nil && cur == *w.refused {
		return Decision{Action: None}
	}

	if w.pending == nil || *w.pending != cur {
		c := cur
		w.pending = &c
		return Decision{Action: Wait}
	}

	pctx, cancel := context.WithTimeout(ctx, PreflightTimeout)
	defer cancel()

	if err := w.Preflight(pctx, w.Path); err != nil {
		c := cur
		w.refused = &c
		return Decision{Action: Refused, Reason: reasonText(err)}
	}

	return Decision{Action: Reexec}
}

// Refused returns the identity this watcher refused, or nil.
func (w *Watcher) Refused() *store.FileID { return w.refused }

// reasonText is a preflight failure's first line, capped at 300 bytes: this
// goes into daemon.json for a human, not a full stderr dump.
func reasonText(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
