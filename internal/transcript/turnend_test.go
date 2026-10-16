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
	opencodeStart       = `{"type":"step_start","part":{}}`
	opencodeText        = `{"type":"text","part":{"type":"text","text":"done"}}`
	codexItem           = `{"type":"item.completed","item":{"type":"agent_message","text":"half"}}`
	codexFail           = `{"type":"turn.failed","error":{"message":"cut"}}`
	codexCompletedEvent = `{"type":"turn.completed"}`
	agyStep             = `{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE"}}`
	agyResultFail       = `{"event":"result","result":{"status":"ERROR","error":"cut"}}`
	agyResultOK         = `{"event":"result","result":{"status":"SUCCESS","response":"done"}}`

	relevoRusageTrailer = "relevo-rusage:cpu_usec=123456 mem_peak=891289600"
	relevoExitTrailer   = "relevo-exit:0"
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

// TestTurnEndStateReadsTheLastEvent replaces the old sticky rule: opencode
// emits step_finish after EVERY step, so an end event anywhere in the stream
// reads Clean for a stream cut mid-reasoning after a finished step -- the
// failure the sticky rule was written into the classifier over. The verdict
// reads the stream's LAST recognised harness event, and the runner's own
// relevo-rusage:/relevo-exit: trailers behind it do not change it.
func TestTurnEndStateReadsTheLastEvent(t *testing.T) {
	t.Parallel()

	// Three finished steps: opencode emits step_finish after EVERY step, so
	// the stream's middle is full of them -- an end-event-first rule stops
	// reading at the first.
	steps := ""
	for i := 0; i < 3; i++ {
		steps += opencodeStart + "\n" + opencodeText + "\n" + opencodeFinish + "\n"
	}
	trailers := "\n" + relevoRusageTrailer + "\n" + relevoExitTrailer + "\n"

	for _, tt := range []struct {
		name   string
		stream string
		want   TurnEnd
	}{
		{"several steps then a cut reasoning event", steps + opencodeReason + "\n", TurnEndUnclean},
		{"several steps ending in text", steps + opencodeText + "\n", TurnEndClean},
		{"several steps ending in step_finish", steps + opencodeFinish + "\n", TurnEndClean},
		{"trailers behind the cut", steps + opencodeReason + "\n" + trailers, TurnEndUnclean},
		{"trailers behind the clean text", steps + opencodeText + "\n" + trailers, TurnEndClean},
		{"trailers behind the clean finish", steps + opencodeFinish + "\n" + trailers, TurnEndClean},
	} {
		if got := TurnEndState("opencode", []byte(tt.stream)); got != tt.want {
			t.Errorf("%s: TurnEndState = %d, want %d", tt.name, got, tt.want)
		}
	}
}

// TestTurnEndStateFollowsTheResumedTurn pins the rule the resume depends on:
// a nudged session appends to the same stream, so the last event is the
// latest turn's -- a cut first turn finished by a clean resumed one reads
// Clean, and a clean first turn followed by a cut one reads Unclean.
func TestTurnEndStateFollowsTheResumedTurn(t *testing.T) {
	t.Parallel()

	cutTurn := opencodeReason + "\n"
	cleanTurn := opencodeStart + "\n" + opencodeText + "\n" + opencodeFinish + "\n"

	if got := TurnEndState("opencode", []byte(cutTurn+cleanTurn)); got != TurnEndClean {
		t.Errorf("cut turn, clean resume: TurnEndState = %d, want Clean: the resumed turn's event is the last", got)
	}
	if got := TurnEndState("opencode", []byte(cleanTurn+cutTurn)); got != TurnEndUnclean {
		t.Errorf("clean turn, cut resume: TurnEndState = %d, want Unclean: the resumed turn was cut", got)
	}
}
