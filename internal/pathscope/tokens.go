package pathscope

// The string-literal scanners the atString table dispatches to, one per
// prefix or quote form, and the helpers they share.

import "bytes"

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
