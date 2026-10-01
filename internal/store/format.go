package store

import (
	"errors"
	"fmt"
)

// BindingFormat is the format of the Binding JSON this binary writes. It is 13
// because the mastermind transcript locator was removed from the endpoint: an
// older relevo would keep writing the key and reading the MasterMind's own
// transcript, which this binary no longer does. It was 12 because the
// runner-writable state layout moved: a round's report, done marker
// and artifact directories now live under the binding's out/ directory, and
// relevo migrates an older binding's files into it on the first tick. The file
// names and the round_file row names are unchanged, so the format bump exists
// only to lock an older relevo out: it does not know the out/ layout and would
// name, read and seal the old paths. That refusal is the intended clean break.
//
// Later fields (abandoned_sessions, oom_requeue, round_oom_kills, link,
// remote_bundle_failures) are fields recordFormat never stamps: an older relevo
// that drops abandoned_sessions loses only pending session deletes, one that
// drops oom_requeue or round_oom_kills loses only oom-kill tracking for
// in-flight rounds, one that drops link loses only the name of a remote
// binding's other copy, and one that drops remote_bundle_failures loses only a
// failure run, which restarts from zero. Stamping a field would lock that older
// relevo out of loading the binding. Bump BindingFormat whenever Binding's JSON
// shape changes in a way that must lock an older relevo out.
const BindingFormat = 13

// recordFormat is the format to write b at. Every record is format 13: the
// binary writes and migrates the out/ layout, which an older relevo cannot
// serve, and no longer carries the mastermind transcript locator.
func recordFormat(b Binding) int {
	return BindingFormat
}

// storedFormat is the number written for a known format: 1 becomes 0 so the
// "format" key is omitted, and every other format is written as itself.
func storedFormat(n int) int {
	if n == 1 {
		return 0
	}
	return n
}

// ErrNewerFormat reports a binding or mastermind record written by a relevo that
// knows a newer format; writing it back would erase fields this relevo does
// not understand, so callers refuse instead.
type ErrNewerFormat struct {
	Kind string
	Name string
	Have int
	Know int
}

var ErrNewerFormatSentinel = errors.New("relevo: newer format")

func (e *ErrNewerFormat) Error() string {
	return fmt.Sprintf("%s %q was written by a newer relevo (format %d; this relevo knows %d): upgrade relevo; a mastermind session reconnects relevo mcp with /mcp",
		e.Kind, e.Name, e.Have, e.Know)
}

func (e *ErrNewerFormat) Is(target error) bool {
	return target == ErrNewerFormatSentinel
}

// KnownState reports whether s is a state this relevo defines; false means a
// newer relevo wrote it, so the daemon leaves the binding alone.
func KnownState(s State) bool {
	switch s {
	case StateActive, StateNeedsYou, StateBroken, StateDone, StatePaused:
		return true
	}
	return false
}
