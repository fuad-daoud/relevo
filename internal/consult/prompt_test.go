package consult

import (
	"fmt"
	"strings"
	"testing"
)

// TestInlinePrompt pins the inline decision: a question that fits is delimited
// and inline, one byte too large is not, and askInlineBlock trims exactly one
// trailing newline.
//
// Mutation check: `<` for `<=` fails on the exact-size case; an unconditional
// true fails the over-the-limit case.
func TestInlinePrompt(t *testing.T) {
	t.Parallel()

	render := func(ref string) string { return fmt.Sprintf(headlessPrompt, ref) }
	const question = "Why did round 1 change the schema?"

	prompt, ok := inlinePrompt(render, []byte(question))
	if !ok {
		t.Fatalf("inlinePrompt(%q) = not ok, want ok", question)
	}
	if !strings.Contains(prompt, question) {
		t.Errorf("prompt does not contain the question:\n%s", prompt)
	}
	for _, want := range []string{"-----BEGIN QUESTION-----", "-----END QUESTION-----"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt does not contain %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "Read:") {
		t.Errorf("prompt contains a Read: reference:\n%s", prompt)
	}

	// A question sized so the whole prompt is exactly InlineAskMax bytes
	// inlines; one byte more does not.
	fits := []byte(strings.Repeat("x", InlineAskMax-len(prompt)+len(question)))
	p, ok := inlinePrompt(render, fits)
	if !ok || len(p) != InlineAskMax {
		t.Errorf("prompt of %d bytes = (%d, %v), want (%d, true)", InlineAskMax, len(p), ok, InlineAskMax)
	}
	over := append(append([]byte(nil), fits...), 'x')
	if p, ok := inlinePrompt(render, over); ok {
		t.Errorf("prompt of %d bytes = (%d, true), want not ok", len(p), len(p))
	}

	// Exactly one trailing newline is trimmed.
	if a, b := askInlineBlock([]byte("hi\n")), askInlineBlock([]byte("hi")); a != b {
		t.Errorf("askInlineBlock kept or dropped more than one newline:\n%q\n%q", a, b)
	}
	if got := askInlineBlock([]byte("hi\n\n")); !strings.HasSuffix(got, "\n\n-----END QUESTION-----") {
		t.Errorf("askInlineBlock(hi\\n\\n) = %q, want the extra newline kept", got)
	}
}
