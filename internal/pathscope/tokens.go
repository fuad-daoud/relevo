package pathscope

// The string-literal scanners the atString table dispatches to, one per
// prefix or quote form, and the helpers they share.

import "bytes"

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

// dollarTagString scans a SQL $tag$...$tag$ string, closed by the same tag.
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

// rRawString scans an R raw string opening at i, at r or R. A shape the grammar
// does not match stays plain code; one it does but that never closes is refused.
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

// csInterpString scans a C# interpolated string opening at the '$' at i, whose
// { ... } holes may hold nested quotes. A form it does not open stays code.
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

// skipInterp skips an interpolation opening at j, e bytes long, to just past its
// closing bracket, tracking nesting and skipping nested strings whole: a quote
// inside does not close the string around it. -1 when the bracket or a nested
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

// skipNestedString skips a nested string opening at j with quote byte q, honoring
// the language's escapes, doubling and interpolations. -1 when it never closes.
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

// skipTemplate skips a template literal opening at the backtick at j, with its
// own interpolations. -1 when it never closes.
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

func isTagByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
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

// cRawString scans a C++ raw string opening at i, at an R-family prefix, with a
// delimiter of at most 16 characters. A malformed or unterminated raw string is
// refused: the lexer cannot be sure where it ends. A prefix the quote does not
// follow stays plain code.
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

func hasPrefixBytes(src []byte, i int, s string) bool {
	return i+len(s) <= len(src) && string(src[i:i+len(s)]) == s
}

func isCRawPrefixByte(c byte) bool {
	return c == 'R' || c == 'u' || c == 'U' || c == 'L'
}

// isCRawDelimByte reports whether c can be part of a raw string's
// delimiter: any printable character but a space, a parenthesis or a
// backslash.
func isCRawDelimByte(c byte) bool {
	return c > ' ' && c <= '~' && c != '(' && c != ')' && c != '\\'
}
