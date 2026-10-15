package pathscope

// The string-literal scanners the atString table dispatches to, one per
// prefix or quote form, and the helpers they share.

import (
	"bytes"
	"strings"
)

// rawQuoteHash scans a raw string whose closer is a quote and n '#'s, with,
// when interp, \#( interpolations holding nested quotes.
func (x *lexer) rawQuoteHash(i, n int, interp bool) bool {
	for j := i + 1; j < len(x.src); {
		if interp && x.src[j] == '\\' && j+1+n < len(x.src) &&
			x.src[j+1+n] == '(' && x.hashRun(j+1) == n {
			k := x.skipInterp(j, n+2)
			if k < 0 {
				x.out.err = errUnterminated
				return true
			}
			j = k
			continue
		}
		if x.src[j] != '"' {
			j++
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
		j++
	}
	x.out.err = errUnterminated
	return true
}

// hashRun returns the number of '#'s at i.
func (x *lexer) hashRun(i int) int {
	n := 0
	for i+n < len(x.src) && x.src[i+n] == '#' {
		n++
	}
	return n
}

// swiftRaw scans a Swift raw string #"..."# with any number of '#'s; ' is
// not a string in Swift, so only " is handled.
func (x *lexer) swiftRaw(i int) bool {
	j := i
	for j < len(x.src) && x.src[j] == '#' {
		j++
	}
	if j < len(x.src) && x.src[j] == '"' {
		return x.rawQuoteHash(j, j-i, x.l.interpParen)
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
		return x.rawQuoteHash(j, n, false)
	}
	return x.quoted(j, true, false, false)
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
		return x.scanLiteral(j+3, "'''", true, false)
	}
	if x.hasPrefixAt(j, `"""`) {
		return x.scanLiteral(j+3, `"""`, true, false)
	}
	if j < len(x.src) && (x.src[j] == '\'' || x.src[j] == '"') {
		return x.quoted(j, true, false, false)
	}
	return false
}

// dartString scans a Dart string literal opening at r: r'...', r"...", and
// the triple forms, with no escapes at all. A raw prefix keeps the
// interpolation: only the escapes are off.
func (x *lexer) dartString(i int) bool {
	j := i + 1
	if x.hasPrefixAt(j, "'''") {
		return x.scanLiteral(j+3, "'''", false, x.l.interp)
	}
	if x.hasPrefixAt(j, `"""`) {
		return x.scanLiteral(j+3, `"""`, false, x.l.interp)
	}
	if j < len(x.src) && (x.src[j] == '\'' || x.src[j] == '"') {
		return x.quoted(j, false, false, x.l.interp)
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
	return x.scanLiteral(j+1, string(x.src[i:j+1]), false, false)
}

// rRawString scans an R raw string literal opening at i, at r or R: a
// quote, any number of dashes, an opener bracket ( [ {, the content, the
// matching closer, the same dashes, and the quote. A shape the raw string
// grammar does not match is left as plain code; one it does but that never
// closes is refused.
func (x *lexer) rRawString(i int) bool {
	if i+1 >= len(x.src) {
		return false
	}
	q := x.src[i+1]
	if q != '"' && q != '\'' {
		return false
	}
	j := i + 2
	n := 0
	for j < len(x.src) && x.src[j] == '-' {
		n++
		j++
	}
	if j >= len(x.src) {
		return false
	}
	var close byte
	switch x.src[j] {
	case '(':
		close = ')'
	case '[':
		close = ']'
	case '{':
		close = '}'
	default:
		return false
	}
	for k := j + 1; k < len(x.src); k++ {
		if x.src[k] != close {
			continue
		}
		d := k + 1
		for d < len(x.src) && x.src[d] == '-' {
			d++
		}
		if d-k-1 == n && d < len(x.src) && x.src[d] == q {
			x.i = d + 1
			return true
		}
	}
	x.out.err = errUnterminated
	return true
}

// csInterpString scans a C# interpolated string opening at the '$' at i:
// $"...", $@"...", @$"..." and their """ raw forms, where the { ... }
// holes may hold nested quotes. A form it does not open is left as plain
// code.
func (x *lexer) csInterpString(i int) bool {
	j := i + 1
	verbatim := i > 0 && x.src[i-1] == '@'
	if j < len(x.src) && x.src[j] == '@' && !verbatim {
		verbatim = true
		j++
	}
	if x.hasPrefixAt(j, `"""`) {
		return x.scanLiteral(j+3, `"""`, false, true)
	}
	if j < len(x.src) && x.src[j] == '"' {
		return x.quoted(j, !verbatim, verbatim, true)
	}
	return false
}

// interpOpenAt returns the length of the string interpolation opening at j,
// or 0 when there is none: ${ for most languages, \( where interpParen, and
// a bare { in C#'s interpolated strings.
func (x *lexer) interpOpenAt(j int) int {
	if j+1 >= len(x.src) {
		return 0
	}
	switch {
	case x.l.interpParen:
		if x.src[j] == '\\' && x.src[j+1] == '(' {
			return 2
		}
	case x.l.csDollar:
		if x.src[j] == '{' {
			return 1
		}
	default:
		if x.src[j] == '$' && x.src[j+1] == '{' {
			return 2
		}
	}
	return 0
}

// skipInterp skips an interpolation opening at j, e bytes long -- ${ or
// \( or { -- to just past its closing bracket, tracking bracket nesting and
// skipping nested string literals whole: a quote inside an interpolation
// does not close the string around it. -1 when the bracket or a nested
// string never closes, which refuses the string around it too.
func (x *lexer) skipInterp(j, e int) int {
	open, close := byte('{'), byte('}')
	if x.l.interpParen {
		open, close = '(', ')'
	}
	depth := 1
	j += e
	for j < len(x.src) {
		c := x.src[j]
		switch {
		case c == open:
			depth++
			j++
		case c == close:
			depth--
			j++
			if depth == 0 {
				return j
			}
		case c == '"' || (c == '\'' && x.l.single) || (c == '`' && x.l.interpBt):
			k := x.skipNestedString(j, c)
			if k < 0 {
				return -1
			}
			j = k
		default:
			j++
		}
	}
	return -1
}

// skipNestedString skips a nested string literal opening at j with the
// quote byte q, to just past its closer, honoring the language's escapes
// and doubling and its own interpolations. -1 when it never closes.
func (x *lexer) skipNestedString(j int, q byte) int {
	if q == '`' {
		return x.skipTemplate(j)
	}
	esc, doubling := x.l.dqEsc, false
	if q == '\'' {
		esc, doubling = x.l.sqEsc, x.l.sqDouble
	}
	k := j + 1
	for k < len(x.src) {
		if e := x.interpOpenAt(k); e > 0 {
			n := x.skipInterp(k, e)
			if n < 0 {
				return -1
			}
			k = n
			continue
		}
		c := x.src[k]
		if esc && c == '\\' {
			k += 2
			continue
		}
		if c != q {
			k++
			continue
		}
		if doubling && k+1 < len(x.src) && x.src[k+1] == q {
			k += 2
			continue
		}
		return k + 1
	}
	return -1
}

// skipTemplate skips a template literal opening at the backtick at j, to
// just past its closer, with its own interpolations. -1 when it never
// closes.
func (x *lexer) skipTemplate(j int) int {
	k := j + 1
	for k < len(x.src) {
		if e := x.interpOpenAt(k); e > 0 {
			n := x.skipInterp(k, e)
			if n < 0 {
				return -1
			}
			k = n
			continue
		}
		if x.src[k] == '\\' {
			k += 2
			continue
		}
		if x.src[k] == '`' {
			return k + 1
		}
		k++
	}
	return -1
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
	if !(isWordByte(c) || c == '!' || c == '?') {
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
