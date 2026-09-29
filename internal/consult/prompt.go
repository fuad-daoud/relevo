package consult

import (
	"strings"
)

// headlessPrompt is the whole prompt a headless consult runs with. Its single
// %s is the question reference: the question itself, delimited and inline when
// it fits InlineAskMax, and "Read: <ask path>" when it does not. The findings
// are the final message rather than a file, because a read-tier process may
// not be able to write one; relevo extracts that message at exit.
const headlessPrompt = "%s\n\nAnswer as your final message: your findings, complete, in markdown. Do not modify any file in this repository. Do not write a findings file; relevo records your final message."

// InlineAskMax is the largest whole prompt, in bytes, passed inline. The prompt
// is one argv element and Linux caps one argument at 128 KiB; half of that
// leaves room for the wrapper layers. A larger question falls back to a staged
// file.
const InlineAskMax = 64 << 10

// askInlineBlock wraps question for a consult's prompt. The delimiters make
// the question's own markdown unambiguous. One trailing newline is trimmed.
func askInlineBlock(question []byte) string {
	q := strings.TrimSuffix(string(question), "\n")
	return "The question:\n\n-----BEGIN QUESTION-----\n" + q + "\n-----END QUESTION-----"
}

// inlinePrompt renders the prompt with the question inlined and reports
// whether it fits InlineAskMax. render is the consult's template. The decision
// depends only on the question and the template, never on a path. A prompt
// over the limit returns "" and false.
func inlinePrompt(render func(ref string) string, question []byte) (string, bool) {
	p := render(askInlineBlock(question))
	if len(p) <= InlineAskMax {
		return p, true
	}
	return "", false
}
