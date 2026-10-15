package pathscope

// The string-literal scanners the atString table dispatches to, one per
// prefix or quote form, and the helpers they share.

import (
	"bytes"
	"strings"
)

// rawQuoteHash scans a raw string whose closer is a quote and n '#'s.
func (x *lexer) rawQuoteHash(i, n int) bool {
	for j := i + 1; j < len(x.src); j++ {
		if x.src[j] != '"' {
			continue
		}
		k := j + 1
		for k < len(x.src) && x.src[k] == '#' {
			k++
		}
		if k-j-1 == n {
			x.i = k
			return true
		}
	}
	x.out.err = errUnterminated
	return true
}

// swiftRaw scans a Swift raw string #"..."# with any number of '#'s; ' is
// not a string in Swift, so only " is handled.
func (x *lexer) swiftRaw(i int) bool {
	j := i
	for j < len(x.src) && x.src[j] == '#' {
		j++
	}
	if j < len(x.src) && x.src[j] == '"' {
		return x.rawQuoteHash(j, j-i)
	}
	return false
}

// rsString scans a Rust string literal: an r/b/c prefix, or a pair, with
// any number of '#'s before the quote; the r forms are raw. ' is a
// lifetime, not a string.
func (x *lexer) rsString(i int) bool {
	p := 1
	if i+2 < len(x.src) && isRSPrefix2(x.src[i:i+2]) && (x.src[i+2] == '"' || x.src[i+2] == '#') {
		p = 2
	}
	j := i + p
	n := 0
	for j < len(x.src) && x.src[j] == '#' {
		n++
		j++
	}
	if j >= len(x.src) || x.src[j] != '"' {
		return false
	}
	if n > 0 || bytes.IndexByte(x.src[i:i+p], 'r') >= 0 {
		return x.rawQuoteHash(j, n)
	}
	return x.quoted(j, true, false)
}

// pyString scans a Python string literal after an r/b/f/u prefix run. A raw
// prefix changes the value, not the scanning: a backslash still hides the
// closer.
func (x *lexer) pyString(i int) bool {
	j := i
	for j < len(x.src) && isPyPrefixByte(x.src[j]) {
		j++
	}
	if x.hasPrefixAt(j, "'''") {
		return x.scanLiteral(j+3, "'''", true)
	}
	if x.hasPrefixAt(j, `"""`) {
		return x.scanLiteral(j+3, `"""`, true)
	}
	if j < len(x.src) && (x.src[j] == '\'' || x.src[j] == '"') {
		return x.quoted(j, true, false)
	}
	return false
}

// dartString scans a Dart raw string opening at r: r'...', r"...", and the
// triple forms, with no escapes at all.
func (x *lexer) dartString(i int) bool {
	j := i + 1
	if x.hasPrefixAt(j, "'''") {
		return x.scanLiteral(j+3, "'''", false)
	}
	if x.hasPrefixAt(j, `"""`) {
		return x.scanLiteral(j+3, `"""`, false)
	}
	if j < len(x.src) && (x.src[j] == '\'' || x.src[j] == '"') {
		return x.quoted(j, false, false)
	}
	return false
}

// dollarTagString scans a SQL $tag$...$tag$ string: the tag is identifier
// characters, and the same tag closes the string.
func (x *lexer) dollarTagString(i int) bool {
	j := i + 1
	for j < len(x.src) && isTagByte(x.src[j]) {
		j++
	}
	if j >= len(x.src) || x.src[j] != '$' {
		return false
	}
	return x.scanLiteral(j+1, string(x.src[i:j+1]), false)
}

// isTagByte reports whether c can be part of a SQL dollar-quote tag.
func isTagByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

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
	return bytes.Index(rest, []byte("//")) >= 0 || bytes.Index(rest, []byte("/*")) >= 0
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

// bracketIdent scans a T-SQL [identifier]: to the next ']'. Unterminated, it
// is refused: the lexer cannot be sure where the identifier ends.
func (x *lexer) bracketIdent(i int) bool {
	for j := i + 1; j < len(x.src); j++ {
		if x.src[j] == ']' {
			x.i = j + 1
			return true
		}
	}
	x.out.err = errUnterminated
	return true
}

// cRawString scans a C++ raw string literal opening at i, at one of the
// R, u8R, uR, UR, LR prefixes: "delim( ... )delim" with a delimiter of at
// most 16 characters. A malformed or unterminated raw string is refused:
// the lexer cannot be sure where the literal ends. A prefix the quote does
// not follow -- u8"...", L'a' -- is left as plain code.
func (x *lexer) cRawString(i int) bool {
	p := cRawPrefixLen(x.src, i)
	if p == 0 {
		return false
	}
	j := i + p
	if j >= len(x.src) || x.src[j] != '"' {
		return false
	}
	k := j + 1
	for k < len(x.src) && k-(j+1) < 16 && isCRawDelimByte(x.src[k]) {
		k++
	}
	if k >= len(x.src) || x.src[k] != '(' {
		x.out.err = errUnterminated
		return true
	}
	closer := ")" + string(x.src[j+1:k]) + "\""
	if o := bytes.Index(x.src[k+1:], []byte(closer)); o >= 0 {
		x.i = k + 1 + o + len(closer)
		return true
	}
	x.out.err = errUnterminated
	return true
}

// cRawPrefixLen returns the length of the raw string prefix at i, or 0 when
// there is none: R, u8R, uR, UR or LR.
func cRawPrefixLen(src []byte, i int) int {
	switch {
	case hasPrefixBytes(src, i, "u8R"):
		return 3
	case hasPrefixBytes(src, i, "uR"), hasPrefixBytes(src, i, "UR"), hasPrefixBytes(src, i, "LR"):
		return 2
	case src[i] == 'R':
		return 1
	}
	return 0
}

// hasPrefixBytes reports whether src has s at i.
func hasPrefixBytes(src []byte, i int, s string) bool {
	return i+len(s) <= len(src) && string(src[i:i+len(s)]) == s
}

// isCRawPrefixByte reports whether c can start a C++ raw string prefix.
func isCRawPrefixByte(c byte) bool {
	return c == 'R' || c == 'u' || c == 'U' || c == 'L'
}

// isCRawDelimByte reports whether c can be part of a raw string's
// delimiter: any printable character but a space, a parenthesis or a
// backslash.
func isCRawDelimByte(c byte) bool {
	return c > ' ' && c <= '~' && c != '(' && c != ')' && c != '\\'
}
