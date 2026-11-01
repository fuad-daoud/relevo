package store

import (
	"errors"
	"fmt"
)

// BindingFormat is the format of the Binding JSON this binary writes: 18 drops
// round_repo_baseline_tree, so an older relevo judges that round's close
// against a plain dirty check and halts on a worktree escape that never
// happened. 17 drops subdir, so an older relevo runs the round at the tree
// root, the wrong directory, silently. 16 drops remote_halt_kind, so an older
// relevo reading the episode from the text takes a halt it did not write for
// one it did. 15 drops owed_halt, so the owed entry is lost. 14 drops
// check_run, so the check the daemon is advancing vanishes. 13 and 12 are the
// out/ layout lock-out, the intended break: an older relevo does not know the
// out/ layout and would name, read and seal the old paths.
//
// Later fields (abandoned_sessions, oom_requeue, round_oom_kills, link,
// remote_bundle_failures, check_run, round_closed_baseline_head, builder_pinned,
// serve's trimpath, and progress's resumed_at, cpu_ticks and cpu_ticks_at) are
// fields recordFormat never stamps: an older relevo that drops
// abandoned_sessions loses only pending session deletes, one that drops
// oom_requeue or round_oom_kills loses only oom-kill tracking for in-flight
// rounds, one that drops link loses only the name of a remote binding's other
// copy, and one that drops remote_bundle_failures loses only a failure run,
// which restarts from zero. One that drops builder_pinned loses only the pin.
// One that drops serve's trimpath builds trimmed. One that drops progress's
// resumed_at, cpu_ticks or cpu_ticks_at loses only the resumed-idle and
// model-silence labels for the round in flight. One that drops
// round_closed_baseline_head leaves the uncommitted-work recovery without a
// starting commit, and the recovery then refuses rather than advances. subdir
// is not one of these: a dropped subdirectory changes where a round's own files
// are written, not just one tracked fact. Stamping a field would lock that
// older relevo out of loading the binding. Bump BindingFormat whenever
// Binding's JSON shape changes in a way that must lock an older relevo out.
const BindingFormat = 18

// recordFormat is the format to write b at. Every record is format 18: the
// binary writes and migrates the out/ layout, which an older relevo cannot
// serve.
func recordFormat(b Binding) int {
	return BindingFormat
}

// StoredFormat is the number written for a known format: 1 becomes 0 so the
// "format" key is omitted, and every other format is written as itself.
func StoredFormat(n int) int {
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
