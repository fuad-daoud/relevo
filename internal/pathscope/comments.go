package pathscope

import "strings"

// The code-line rule for a table language: two sources differ only in
// comments when every comment is removed and the remaining lines match one
// for one, in order. A line is compared after its comment spans are cut out
// and its trailing whitespace is trimmed; a line that is empty, or empty
// once its comments are cut out, is dropped from both sides. Leading
// whitespace is code: indentation is significant in Python and YAML, so it
// is never free. A string literal is code too: a change inside one is a
// code change, so a docstring stays out of scope.

// judgeComments judges an M-status table-language file: in scope exactly
// when the code lines and the ordered directive lists are equal. It mirrors
// judgeGo's reasons: an unlexable side is cannot judge, differing code
// lines are code change, and differing directive lists are directive
// comment.
func judgeComments(l *lang, oldSrc, newSrc []byte) (ok bool, reason string) {
	o := lex(l, oldSrc)
	n := lex(l, newSrc)
	if o.err != nil || n.err != nil {
		return false, ReasonCannotJudge
	}
	if !codeLinesEqual(oldSrc, o.spans, newSrc, n.spans) {
		return false, ReasonCodeChange
	}
	if !equalStrings(o.directives, n.directives) {
		return false, ReasonDirectiveComment
	}
	return true, ""
}

// codeLinesEqual compares the two sources' code lines one for one, in
// order: every line with its comment spans cut out and its trailing
// whitespace trimmed, the lines left empty dropped from both sides.
func codeLinesEqual(oldSrc []byte, oldSpans []span, newSrc []byte, newSpans []span) bool {
	old := codeLines(oldSrc, oldSpans)
	new := codeLines(newSrc, newSpans)
	if len(old) != len(new) {
		return false
	}
	for i := range old {
		if old[i] != new[i] {
			return false
		}
	}
	return true
}

// codeLines returns src's code lines: each line with its comment spans cut
// out and its trailing whitespace trimmed, the lines left empty dropped.
// The spans are the ones lex found, in scan order, and a block comment's
// span may run across several lines.
func codeLines(src []byte, spans []span) []string {
	out := []string{}
	si := 0
	for ls := 0; ls < len(src); {
		le := lineEnd(src, ls)
		var b []byte
		i := ls
		for si < len(spans) && spans[si].start < le {
			s := spans[si]
			from, to := s.start, s.end
			if from < i {
				from = i
			}
			if to > le {
				to = le
			}
			if from > i {
				b = append(b, src[i:from]...)
			}
			i = to
			if s.end <= le {
				si++
			} else {
				break // the comment continues on the next line
			}
		}
		if i < le {
			b = append(b, src[i:le]...)
		}
		if text := strings.TrimRight(string(b), " \t\r"); text != "" {
			out = append(out, text)
		}
		ls = le + 1
	}
	return out
}
