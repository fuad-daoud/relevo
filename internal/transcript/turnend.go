package transcript

import (
	"bytes"
	"encoding/json"
)

// TurnEnd says what the end of a harness stream says about its last turn.
type TurnEnd int

const (
	// TurnEndUnknown: the stream shows nothing either way -- an unknown kind,
	// an empty stream, a stream of no parseable lines. No verdict was earned.
	TurnEndUnknown TurnEnd = iota
	// TurnEndClean: the harness's own end-of-turn event appears, so the turn
	// ran to its end.
	TurnEndClean
	// TurnEndUnclean: the stream's last events are inside a turn, so the turn
	// was cut off before its end.
	TurnEndUnclean
)

// turnEndTable is one kind's turn grammar: the events a stream may end on and
// read Clean, and the ones it may end on and read Unclean.
type turnEndTable struct {
	clean   map[string]bool
	unclean map[string]bool
}

// turnEndTables map the rendered kinds onto the shapes their renderer walks:
// claude by type, opencode by type, codex by type, agy by event.
var turnEndTables = map[string]turnEndTable{
	"claude":   {clean: names("result"), unclean: names("assistant", "user")},
	"opencode": {clean: names("step_finish", "text"), unclean: names("reasoning", "step_start", "tool_use", "error")},
	"codex":    {clean: names("turn.completed"), unclean: names("turn.failed", "item.started", "item.completed")},
	"agy":      {clean: names("result"), unclean: names("step_update")},
}

func names(ts ...string) map[string]bool {
	out := make(map[string]bool, len(ts))
	for _, t := range ts {
		out[t] = true
	}
	return out
}

// TurnEndState classifies a stream by its LAST recognised harness event for
// kind: lines that are blank, not JSON, or outside the kind's grammar are
// skipped, and a stream whose recognised events establish nothing -- none at
// all, an unknown kind, an empty stream -- is Unknown. The last event, not
// the first: opencode emits step_finish after EVERY step, so the first one
// says nothing about the turn the stream ends on -- one finished step and
// then a cut mid-reasoning stays Unclean. A resumed (nudged) session
// appends to the same stream, so that last event is the latest turn's.
// Only complete lines count: a trailing partial line is the same wait every
// drain leaves it as.
func TurnEndState(kind string, stream []byte) TurnEnd {
	t, ok := turnEndTables[kind]
	if !ok {
		return TurnEndUnknown
	}
	verdict := TurnEndUnknown
	for _, line := range bytes.Split(stream, []byte{'\n'}) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
			continue
		}
		name := markedEvent(kind, obj)
		switch {
		case t.clean[name]:
			verdict = TurnEndClean
		case t.unclean[name]:
			verdict = TurnEndUnclean
		}
	}
	return verdict
}

// markedEvent is the key a kind's events are marked by: type, or the event
// field the agy stream keys every event by.
func markedEvent(kind string, obj map[string]any) string {
	if kind == "agy" {
		return str(obj["event"])
	}
	return str(obj["type"])
}
