package pathscope

import (
	"slices"
	"strings"
)

// The code-line rule for a table language: two sources differ only in comments
// when every comment is removed and the remaining lines match one for one, in
// order, after trailing whitespace is trimmed and emptied lines are dropped.
// Leading whitespace is code: indentation is significant in Python and YAML. A
// string literal is code too, so a docstring stays out of scope.

// judgeComments judges an M-status table-language file: in scope exactly when
// the code lines and ordered directive lists are equal, with judgeGo's reasons.
func judgeComments(l *lang, oldSrc, newSrc []byte) (ok bool, reason string) {
	o := lex(l, oldSrc)
	n := lex(l, newSrc)
	if o.err != nil || n.err != nil {
		return false, ReasonCannotJudge
	}
	if !codeLinesEqual(oldSrc, o.spans, newSrc, n.spans) {
		return false, ReasonCodeChange
	}
	if !slices.Equal(o.directives, n.directives) {
		return false, ReasonDirectiveComment
	}
	return true, ""
}

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

// codeLines returns src's code lines: each line with its comment spans cut out
// and trailing whitespace trimmed, emptied lines dropped. Spans are in scan
// order, and a block comment's span may run across lines.
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
