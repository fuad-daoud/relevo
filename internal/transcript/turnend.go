package transcript

import (
	"bytes"
	"encoding/json"
)

// TurnEnd says what the end of a harness stream says about its last turn.
type TurnEnd int

const (
	// TurnEndUnknown: the stream shows nothing either way -- an unknown kind, an
	// empty stream, or no parseable lines. No verdict was earned.
	TurnEndUnknown TurnEnd = iota
	// TurnEndClean: the harness's own end-of-turn event appears, so the turn ran
	// to its end.
	TurnEndClean
	// TurnEndUnclean: the stream's last events are inside a turn, so the turn was
	// cut off before its end.
	TurnEndUnclean
)

// turnEndTable is one kind's turn grammar: the events a stream may end on and
// read Clean, and those it may end on and read Unclean.
type turnEndTable struct {
	clean   map[string]bool
	unclean map[string]bool
}

// turnEndTables map each rendered kind onto the key its renderer walks: type,
// except agy by event and muse by payload_type.
var turnEndTables = map[string]turnEndTable{
	"claude":   {clean: names("result"), unclean: names("assistant", "user")},
	"opencode": {clean: names("step_finish", "text"), unclean: names("reasoning", "step_start", "tool_use", "error")},
	"codex":    {clean: names("turn.completed"), unclean: names("turn.failed", "item.started", "item.completed")},
	"agy":      {clean: names("result"), unclean: names("step_update")},
	"muse": {
		clean:   names("run.terminal.completed"),
		unclean: names("run.terminal.failed", "run.terminal.cancelled", "tool.result", "run.output.delta"),
	},
}

func names(ts ...string) map[string]bool {
	out := make(map[string]bool, len(ts))
	for _, t := range ts {
		out[t] = true
	}
	return out
}

// TurnEndState classifies a stream by its LAST recognised harness event for kind,
// skipping blank, non-JSON and out-of-grammar lines; a stream establishing
// nothing at all is Unknown. The last event, not the first: opencode emits
// step_finish after EVERY step, so one finished step and then a cut mid-reasoning
// stays Unclean. A resumed session appends to the same stream, so that last event
// is the latest turn's. Only complete lines count: a trailing partial line is the
// same wait every drain leaves it as.
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

// markedEvent is the key a kind's events are marked by: type, except agy's
// event field and muse's payload_type.
func markedEvent(kind string, obj map[string]any) string {
	if kind == "agy" {
		return str(obj["event"])
	}
	if kind == "muse" {
		return musePayloadType(obj)
	}
	return str(obj["type"])
}
