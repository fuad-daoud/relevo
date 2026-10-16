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

// turnEndTable is one kind's turn grammar: the event that marks a clean turn
// end, the events that only ever appear mid-turn, and the event that says the
// turn itself did not finish.
type turnEndTable struct {
	end  func(obj map[string]any) bool
	mid  map[string]bool
	fail func(obj map[string]any) bool
}

// turnEndTables map the rendered kinds onto the shapes their renderer walks:
// claude by type, opencode by type, codex by type, agy by event.
var turnEndTables = map[string]turnEndTable{
	"claude":   {end: typeIs("result"), mid: names("assistant", "user", "error")},
	"opencode": {end: typeIs("step_finish"), mid: names("step_start", "text", "tool_use", "reasoning", "error")},
	"codex":    {end: typeIs("turn.completed"), fail: typeIs("turn.failed"), mid: names("item.started", "item.completed", "error")},
	"agy":      {end: eventIs("result"), mid: names("step_update")},
}

func typeIs(t string) func(map[string]any) bool {
	return func(obj map[string]any) bool { return str(obj["type"]) == t }
}

func eventIs(e string) func(map[string]any) bool {
	return func(obj map[string]any) bool { return str(obj["event"]) == e }
}

func names(ts ...string) map[string]bool {
	out := make(map[string]bool, len(ts))
	for _, t := range ts {
		out[t] = true
	}
	return out
}

// TurnEndState walks a stream's complete lines and says how its end reads for
// kind: Clean on the kind's end event, Unclean when mid-turn events -- or the
// kind's own turn-failure event -- are followed by nothing, and Unknown when
// the stream establishes neither. Only complete lines count: a trailing
// partial line is the same wait every drain leaves it as.
func TurnEndState(kind string, stream []byte) TurnEnd {
	t, ok := turnEndTables[kind]
	if !ok {
		return TurnEndUnknown
	}
	end, cut := false, false
	for _, line := range bytes.Split(stream, []byte{'\n'}) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
			continue
		}
		switch {
		case t.end != nil && t.end(obj):
			end = true
		case t.fail != nil && t.fail(obj):
			cut = true
		case t.mid != nil && t.mid[markedEvent(kind, obj)]:
			cut = true
		}
	}
	if end {
		return TurnEndClean
	}
	if cut {
		return TurnEndUnclean
	}
	return TurnEndUnknown
}

// markedEvent is the key a kind's events are marked by: type, or the event
// field the agy stream keys every event by.
func markedEvent(kind string, obj map[string]any) string {
	if kind == "agy" {
		return str(obj["event"])
	}
	return str(obj["type"])
}
