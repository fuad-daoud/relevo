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

// atRegex consumes a regex literal starting at x.i, if one does. A '/' that is
// neither '//' nor '/*' -- atComment ran first -- starts a regex when the
// previous significant token allows an expression to start, and is division
// otherwise. After ')' or '}' both readings are live: a marker later on the
// line refuses the file, and without one the regex is taken when it closes on
// its line and division when it does not.
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

func isRegexPunct(c byte) bool {
	return strings.IndexByte("(,=:[!&|?{};+-*%<>~^", c) >= 0
}

// jsRegexKeyword reports whether the token is one of the keywords after
// which a regex literal may start; the spread counts too, because it always
// precedes an expression.
func jsRegexKeyword(w string) bool {
	switch w {
	case "return", "typeof", "instanceof", "in", "of", "new", "delete",
		"void", "throw", "case", "do", "else", "yield", "await", "...":
		return true
	}
	return false
}

// prevToken returns the previous significant token before i: "" at the start, a
// punctuation byte, an identifier or keyword, or a run of dots. Whitespace and
// recorded comment spans are skipped whole: a comment is not a token.
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
	if c == '.' {
		k := j - 1
		for k > 0 && x.src[k-1] == '.' {
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

// markerLaterInFile reports whether a // or /* starts anywhere in src after
// i. Raw bytes: a marker inside a later string still counts, because the
// lexer cannot be sure it is code.
func (x *lexer) markerLaterInFile(i int) bool {
	rest := x.src[i+1:]
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

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// atXMLLiteral refuses a Scala file whose XML literal position the lexer cannot
// model: '<' where an expression may start opens one, whose content is a
// language of its own. A '<' after an operand is a comparison and stays code,
// as does one opening <-, <:, or <=. After ')' or '}' only a letter, '_' or '!'
// can open the XML form, and a comment marker anywhere later refuses the file.
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
	if cls == regexStarts {
		if !isWordByte(c) && c != '!' && c != '?' {
			return false
		}
		x.out.err = errUnterminated
		return true
	}
	// Ambiguous: '<' after ')' or '}' may open an XML literal or compare.
	// Only a letter, '_' or '!' can start the XML form, so anything else
	// stays code. A comment marker anywhere later in the file makes the two
	// readings unreadable apart -- the literal's content is a language of
	// its own, and a marker may sit inside it -- so the file is refused.
	if !isXMLNameStart(c) {
		return false
	}
	if x.markerLaterInFile(x.i) {
		x.out.err = errUnterminated
		return true
	}
	return false
}

// isXMLNameStart reports whether c can start the XML form a '<' opens: a
// letter or '_' opens a tag, '!' opens <!-- and <![.
func isXMLNameStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		c == '_' || c == '!'
}
