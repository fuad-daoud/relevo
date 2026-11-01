package setup

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
)

var errBrokenTerminal = errors.New("broken terminal")

// failingWriter fails only its failAt-th write, so a caller that drops that
// one error goes on and finishes cleanly. failAt 0 never fails, so a run with
// it counts writes.
type failingWriter struct {
	failAt, writes int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, errBrokenTerminal
	}
	return len(p), nil
}

// assertEveryWriteFailureStops runs walk once to count its writes, then once
// per write with that write failing, and requires each run to return the
// write's error: a prompt that cannot reach the terminal stops the walk
// rather than reading answers to questions nobody saw.
func assertEveryWriteFailureStops(t *testing.T, walk func(w io.Writer) error) {
	t.Helper()
	count := &failingWriter{}
	if err := walk(count); err != nil {
		t.Fatalf("walk with a working terminal: %v", err)
	}
	if count.writes == 0 {
		t.Fatal("walk wrote nothing")
	}
	for k := 1; k <= count.writes; k++ {
		if err := walk(&failingWriter{failAt: k}); !errors.Is(err, errBrokenTerminal) {
			t.Errorf("write %d of %d failing: err = %v, want the write error", k, count.writes, err)
		}
	}
}

func TestSetupPromptWriteFailureStopsTheWalk(t *testing.T) {
	builder := Question{
		ID: "builder", Prompt: "Which models build code?", Multi: true,
		Recommended: []string{"codex/openai/gpt-6-luna:high"},
		Choices: []Choice{
			{Value: "codex/openai/gpt-6-luna:high"},
			{Value: "claude/anthropic/sonnet"},
		},
	}
	plugin := Question{
		ID: "plugin", Prompt: "Install the relevo plugin?", Recommended: []string{"yes"},
		Choices: []Choice{{Value: "yes"}, {Value: "no"}},
	}
	// "maybe" is unusable, so the walk also prints the parse error and asks again.
	assertEveryWriteFailureStops(t, func(w io.Writer) error {
		_, err := askSetupQuestions(w, strings.NewReader("1,2\nmaybe\nn\n"), []Question{builder, plugin}, nil, nil)
		return err
	})
}

func TestOpencodeWalkWriteFailureStopsTheWalk(t *testing.T) {
	q := opencodeTestQuestion(t)
	// The opencode walk of TestOpencodeWalkPicksByProvider, plus a search
	// with no match.
	in := "1,2\n2\n\nqwen\n2\nvendor/model-07\nzzz-none\n\n"
	assertEveryWriteFailureStops(t, func(w io.Writer) error {
		_, err := askOpencodeModels(w, bufio.NewScanner(strings.NewReader(in)), q)
		return err
	})
}

func TestSetupPrintStepWriteFailureIsReturned(t *testing.T) {
	for _, doc := range []SetupStepDoc{
		{Step: "agents", OK: true, Detail: "installed 4 definitions"},
		{Step: "smoke", Detail: "the round did not finish", Next: "relevo doctor"},
	} {
		assertEveryWriteFailureStops(t, func(w io.Writer) error {
			return setupPrintStep(w, doc)
		})
	}
}
