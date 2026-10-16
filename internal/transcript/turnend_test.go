package transcript

import (
	"testing"
)

// The claude, opencode and codex shapes come from the renderers that walk the
// same events; agy keys everything by event.
const (
	claudeAssistant     = `{"type":"assistant","message":{"content":[{"type":"text","text":"half the work"}]}}`
	claudeUser          = `{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`
	claudeEndEvent      = `{"type":"result","result":"done"}`
	opencodeReason      = `{"type":"reasoning","part":{"text":"hmm"}}`
	opencodeToolUse     = `{"type":"tool_use","part":{"tool":"bash"}}`
	opencodeFinish      = `{"type":"step_finish"}`
	codexItem           = `{"type":"item.completed","item":{"type":"agent_message","text":"half"}}`
	codexFail           = `{"type":"turn.failed","error":{"message":"cut"}}`
	codexCompletedEvent = `{"type":"turn.completed"}`
	agyStep             = `{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE"}}`
	agyResultFail       = `{"event":"result","result":{"status":"ERROR","error":"cut"}}`
	agyResultOK         = `{"event":"result","result":{"status":"SUCCESS","response":"done"}}`

	relevoExitTrailer = "relevo-exit: 0"
)

func TestTurnEndStateShowsTheTablesTurnEnds(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		kind   string
		stream string
		want   TurnEnd
	}{
		{"claude", claudeAssistant + "\n" + claudeEndEvent + "\n", TurnEndClean},
		{"claude", claudeAssistant + "\n", TurnEndUnclean},
		{"opencode", opencodeReason + "\n" + opencodeToolUse + "\n" + opencodeFinish + "\n", TurnEndClean},
		{"opencode", opencodeReason + "\n" + opencodeToolUse + "\n", TurnEndUnclean},
		{"codex", codexItem + "\n" + codexCompletedEvent + "\n", TurnEndClean},
		{"codex", codexItem + "\n", TurnEndUnclean},
		{"codex", codexFail + "\n", TurnEndUnclean},
		{"agy", agyStep + "\n" + agyResultOK + "\n", TurnEndClean},
		{"agy", agyStep + "\n", TurnEndUnclean},
		{"agy", agyStep + "\n" + agyResultFail + "\n", TurnEndClean},
	} {
		if got := TurnEndState(tt.kind, []byte(tt.stream)); got != tt.want {
			t.Errorf("%s stream %q: TurnEndState = %d, want %d", tt.kind, tt.stream, got, tt.want)
		}
	}
}

// TestTurnEndStateAnswersUnknownWithoutEvidence pins the no-verdict rows: an
// unknown kind, an empty stream, a stream of no parseable lines, a stream of
// only the harness's own preamble, and a partial trailing line each say
// nothing, because an unclean verdict needs positive evidence of a cut turn.
func TestTurnEndStateAnswersUnknownWithoutEvidence(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		kind   string
		stream string
	}{
		{"claude", ""},
		{"ghost", claudeAssistant + "\n"},
		{"claude", "not json at all\n" + relevoExitTrailer + "\n"},
		{"claude", `{"type":"system","subtype":"init"}` + "\n"},
		{"agy", `{"event":"init"}` + "\n"},
	} {
		if got := TurnEndState(tt.kind, []byte(tt.stream)); got != TurnEndUnknown {
			t.Errorf("kind %q stream %q: TurnEndState = %d, want Unknown", tt.kind, tt.stream, got)
		}
	}
}

// TestTurnEndStateCleanWins pins precedence: an end event anywhere in the
// stream outweighs every mid-turn event before it, and a codex turn.failed
// must not outweigh a turn.completed after it.
func TestTurnEndStateCleanWins(t *testing.T) {
	t.Parallel()

	if got := TurnEndState("claude", []byte(claudeUser+"\n"+claudeAssistant+"\n"+claudeEndEvent+"\n")); got != TurnEndClean {
		t.Errorf("TurnEndState = %d, want Clean: the end event outranks what it ends", got)
	}
	if got := TurnEndState("codex", []byte(codexFail+"\n"+codexCompletedEvent+"\n")); got != TurnEndClean {
		t.Errorf("TurnEndState = %d, want Clean: the completed turn outranks the failed one before it", got)
	}
}
