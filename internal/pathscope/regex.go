package pathscope

// The regex literal, and the previous-token reading its classification
// needs. Scala reuses the same reading for its XML literals.

import (
	"bytes"
	"strings"
)

// The three readings a '/' outside a comment can have.
const (
	regexStarts    = iota // a regex literal may start here
	regexDivision         // the '/' is division
	regexAmbiguous        // ')' or '}': both readings stay live
)

// atRegex consumes a regex literal starting at x.i, if one does. A '/' that
// is neither '//' nor '/*' -- atComment ran first -- starts a regex when the
// previous significant token allows an expression to start, and is division
// otherwise. After ')' or '}' both readings are live: a marker later on the
// line makes the lexer refuse the file, and without one the regex is taken
// when it closes on its line and division when it does not. A regex that
// does not close on its line is refused.
func (x *lexer) atRegex() bool {
	if !x.l.regex || x.src[x.i] != '/' {
		return false
	}
	switch x.regexClass(x.i) {
	case regexDivision:
		return false
	case regexAmbiguous:
		if x.markerLaterOnLine(x.i) {
			x.out.err = errUnterminated
			return true
		}
		if end, ok := x.scanRegex(x.i); ok {
			x.i = end
			return true
		}
		return false // division: with no marker on the line, code is safe
	}
	end, ok := x.scanRegex(x.i)
	if !ok {
		x.out.err = errUnterminated
		return true
	}
	x.i = end
	return true
}

// regexClass classifies the '/' at i, which atComment has already declined
// as a comment: the previous significant token decides.
func (x *lexer) regexClass(i int) int {
	tok := x.prevToken(i)
	if tok == "" {
		return regexStarts
	}
	if len(tok) == 1 {
		c := tok[0]
		if isRegexPunct(c) {
			return regexStarts
		}
		if c == ')' || c == '}' {
			return regexAmbiguous
		}
		return regexDivision
	}
	if jsRegexKeyword(tok) {
		return regexStarts
	}
	return regexDivision
}

// isRegexPunct reports whether c is one of the bytes after which a regex
// literal may start.
func isRegexPunct(c byte) bool {
	return strings.IndexByte("(,=:[!&|?{};+-*%<>~^", c) >= 0
}

// jsRegexKeyword reports whether the word is one of the keywords after
// which a regex literal may start.
func jsRegexKeyword(w string) bool {
	switch w {
	case "return", "typeof", "instanceof", "in", "of", "new", "delete",
		"void", "throw", "case", "do", "else", "yield", "await":
		return true
	}
	return false
}

// prevToken returns the previous significant token before i: "" at the start
// of the source, the byte of a punctuation token, or the text of an
// identifier or keyword. Whitespace is skipped, and a recorded comment span
// is skipped whole: a comment is not a token, so the token before it is what
// a '/' after it sees.
func (x *lexer) prevToken(i int) string {
	j := i
	for {
		for j > 0 && isSpaceByte(x.src[j-1]) {
			j--
		}
		if j == 0 {
			return ""
		}
		if s := x.spanContaining(j - 1); s != nil {
			j = s.start
			continue
		}
		break
	}
	c := x.src[j-1]
	if isWordByte(c) {
		k := j - 1
		for k > 0 && isWordByte(x.src[k-1]) {
			k--
		}
		return string(x.src[k:j])
	}
	return string(c)
}

// spanContaining returns the recorded comment span covering q, or nil. The
// spans are in scan order and do not overlap, so their ends are ordered too.
func (x *lexer) spanContaining(q int) *span {
	for k := len(x.out.spans) - 1; k >= 0; k-- {
		s := &x.out.spans[k]
		if s.start <= q && q < s.end {
			return s
		}
		if s.end <= q {
			break
		}
	}
	return nil
}

// markerLaterOnLine reports whether a // or /* appears in src after i, on
// the same line. Raw bytes: a marker inside a later string still counts,
// because the lexer cannot be sure it is code.
func (x *lexer) markerLaterOnLine(i int) bool {
	rest := x.src[i+1 : lineEnd(x.src, x.i)]
	return bytes.Contains(rest, []byte("//")) || bytes.Contains(rest, []byte("/*"))
}

// scanRegex scans a regex literal opening at i: to the next unescaped '/'
// outside a [...] class, then its flags. It returns the end and whether the
// literal closed on its line.
func (x *lexer) scanRegex(i int) (int, bool) {
	inClass := false
	for j := i + 1; j < len(x.src); j++ {
		c := x.src[j]
		if c == '\n' {
			return 0, false
		}
		if c == '\\' {
			j++
			continue
		}
		if c == '[' {
			inClass = true
			continue
		}
		if c == ']' {
			inClass = false
			continue
		}
		if c == '/' && !inClass {
			j++
			for j < len(x.src) && isWordByte(x.src[j]) {
				j++
			}
			return j, true
		}
	}
	return 0, false
}

// isWordByte reports whether c can be part of an identifier or number.
func isWordByte(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// isSpaceByte reports whether c is whitespace the token scan skips.
func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// atXMLLiteral refuses a Scala file whose XML literal position the lexer
// cannot model: '<' where an expression may start opens one, and its
// content is a language of its own, tags and all. A '<' that follows an
// operand is a comparison and stays code, and so is '<' that opens one of
// the operators -- <-, <:, <= -- whose next byte cannot start an XML name.
func (x *lexer) atXMLLiteral() bool {
	if !x.l.scalaXML || x.src[x.i] != '<' {
		return false
	}
	cls := x.regexClass(x.i)
	if cls == regexDivision {
		return false
	}
	j := x.i + 1
	if j >= len(x.src) {
		return false
	}
	c := x.src[j]
	if !isWordByte(c) && c != '!' && c != '?' {
		return false
	}
	if cls == regexStarts {
		x.out.err = errUnterminated
		return true
	}
	// Ambiguous: '<' after ')' or '}' may open an XML literal or compare.
	// A marker later on the line makes the two unreadable apart; without
	// one, plain code is safe either way.
	if x.markerLaterOnLine(x.i) {
		x.out.err = errUnterminated
		return true
	}
	return false
}
