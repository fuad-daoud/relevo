package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/transcript"
)

// classifyRoundEndCommands is the exit path's grammar, one row per round-end
// the exited builder can land on: the report closes whatever else is true, a
// marker without a report takes the noreport close, and the exit with neither
// is a nudge only for a turn the stream shows ending -- or a stream that
// establishes nothing -- and never at a non-zero an unknown code.
func TestClassifyRoundEndVerdictPerEnd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		c    roundEndCase
		want roundEndVerdict
	}{
		{"a report closes at any exit", roundEndCase{TurnEnd: transcript.TurnEndClean, Code: "1", Report: true}, roundEndReport},
		{"a report closes a cut turn too", roundEndCase{TurnEnd: transcript.TurnEndUnclean, Code: "0", Report: true}, roundEndReport},
		{"a marker without a report takes the noreport close", roundEndCase{TurnEnd: transcript.TurnEndUnclean, Code: "137", Marker: true}, roundEndMarker},
		{"a clean turn-end at code 0 is nudged", roundEndCase{TurnEnd: transcript.TurnEndClean, Code: "0"}, roundEndNudge},
		{"a stream that says nothing at code 0 keeps the nudge", roundEndCase{TurnEnd: transcript.TurnEndUnknown, Code: "0"}, roundEndNudge},
		{"a cut turn is not nudged even at code 0", roundEndCase{TurnEnd: transcript.TurnEndUnclean, Code: "0"}, roundEndFail},
		{"a non-zero exit is not nudged", roundEndCase{TurnEnd: transcript.TurnEndClean, Code: "137"}, roundEndFail},
		{"an unknown exit code is not nudged", roundEndCase{TurnEnd: transcript.TurnEndClean, Code: "unknown"}, roundEndFail},
	}
	for _, tt := range tests {
		if got := classifyRoundEnd(tt.c); got != tt.want {
			t.Errorf("%s: classifyRoundEnd(%+v) = %q, want %q", tt.name, tt.c, got, tt.want)
		}
	}
}

// TestClassifyRoundEndPinsTheCutTurnOverTheCode pins the one verdict the exit
// path changes: a stream that ends mid-turn is a runner failure by its own
// evidence, so its code zero reads as a failure and its nudge is not asked for
// -- the flip that the wiring test would catch if anyone reverts it.
func TestClassifyRoundEndPinsTheCutTurnOverTheCode(t *testing.T) {
	t.Parallel()

	if got := classifyRoundEnd(roundEndCase{TurnEnd: transcript.TurnEndUnclean, Code: "0"}); got == roundEndNudge {
		t.Fatal("a turn the stream cut inside itself would be nudged again")
	}
}
