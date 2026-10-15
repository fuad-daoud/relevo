package pathscope

import (
	"bytes"
	"errors"
	"strings"
)

// errUnterminated is a form the lexer cannot close: an unterminated string,
// block comment, long bracket or heredoc. Refuse, never guess.
var errUnterminated = errors.New("unterminated form")

// errStuck is the scan loop's guard error: a pass ended without moving
// forward, so the lexer stops instead of recording the same span forever.
var errStuck = errors.New("lexer did not advance")

// span is one comment's byte range inside a source.
type span struct {
	start int
	end   int
}

// lexOut is one source's lex: its comment spans, the ordered list of its
// directive comments, and any lex error.
type lexOut struct {
	spans      []span
	directives []string
	err        error
}

// heredoc is one shell heredoc waiting for its delimiter line.
type heredoc struct {
	word string
	tabs bool // <<- strips leading tabs before comparing a line
}

// lexer is the state one table language's scan carries.
type lexer struct {
	l        *lang
	src      []byte
	i        int
	out      lexOut
	arith    int       // (( )) nesting depth, where << is a shift
	pend     []heredoc // heredocs opened on the line being scanned
	act      *heredoc  // the heredoc whose body is being scanned
	yamlPend bool      // a block scalar header was just consumed
}

// lex scans src with l's rules. Every comment becomes a span; a comment
// that is a directive also joins the ordered list, in file order; an
// unterminated form is an error. Each pass records its position at the top,
// and a pass that ends without moving forward is errStuck, so a branch that
// forgets to consume stops the scan instead of looping forever.
func lex(l *lang, src []byte) lexOut {
	x := &lexer{l: l, src: src}
	for x.i < len(src) && x.out.err == nil {
		start := x.i
		switch {
		case x.act != nil:
			x.heredocStep()
		case x.atComment():
		case x.atString():
		case x.atArith():
		case x.atHeredocOpener():
		case x.atYamlHeader():
		default:
			if x.src[x.i] == '\n' {
				x.activateHeredoc()
				x.i++
				if x.yamlPend {
					x.yamlPend = false
					x.scanYamlBody()
				}
			} else {
				x.i++
			}
		}
		if x.out.err == nil && x.i <= start {
			x.out.err = errStuck
		}
	}
	if x.out.err == nil && (x.act != nil || len(x.pend) > 0) {
		x.out.err = errUnterminated
	}
	return x.out
}

// atComment consumes a comment starting at x.i, if one does.
func (x *lexer) atComment() bool {
	l, src, i := x.l, x.src, x.i
	switch {
	case l.hashAny && src[i] == '#':
		x.lineComment(i)
		return true
	case l.hashWord && src[i] == '#' && x.wordStart(i):
		x.lineComment(i)
		return true
	case l.slashLine && src[i] == '/' && i+1 < len(src) && src[i+1] == '/':
		x.lineComment(i)
		return true
	case l.dashLine && src[i] == '-' && i+1 < len(src) && src[i+1] == '-':
		return x.dashComment(i)
	case l.dashSymbol && src[i] == '-' && i+1 < len(src) &&
		src[i+1] == '-' && !isHsSymbol(byteAt(src, i+2)):
		// --> and --| stay code: a symbol after -- makes it an operator.
		x.lineComment(i)
		return true
	case l.blockSlash && src[i] == '/' && i+1 < len(src) && src[i+1] == '*':
		return x.blockComment(i, "/*", "*/", l.blockNest)
	case l.blockBrace && src[i] == '{' && i+1 < len(src) && src[i+1] == '-':
		return x.blockComment(i, "{-", "-}", l.braceNest)
	}
	return false
}

// dashComment records a -- comment: in Lua a -- that opens a long bracket
// starts a long comment instead, which runs to the matching closer.
func (x *lexer) dashComment(start int) bool {
	if x.l.dashBracket {
		if n := longOpener(x.src, start+2); n >= 0 {
			end := longCloser(x.src, start+2, n)
			if end < 0 {
				x.out.err = errUnterminated
				return true
			}
			x.record(start, end)
			return true
		}
	}
	x.lineComment(start)
	return true
}

// lineComment records a comment that runs to the end of its line.
func (x *lexer) lineComment(start int) {
	x.record(start, lineEnd(x.src, start))
}

// record appends one comment's span and, when it is a directive, its raw
// text to the ordered directive list, and moves the scan past the comment:
// the comment branch is the one the scan loop must always see consume.
func (x *lexer) record(start, end int) {
	x.out.spans = append(x.out.spans, span{start: start, end: end})
	text := strings.TrimRight(string(x.src[start:end]), " \t\r")
	if isDirective(x.l, text, start) {
		x.out.directives = append(x.out.directives, text)
	}
	x.i = end
}

// blockComment records a /* */ or {- -} comment, nested when the language's
// block comments nest, and errors when it never closes.
func (x *lexer) blockComment(start int, open, close string, nest bool) bool {
	depth := 1
	for j := start + len(open); j < len(x.src); {
		if nest && x.hasPrefixAt(j, open) {
			depth++
			j += len(open)
			continue
		}
		if x.hasPrefixAt(j, close) {
			depth--
			j += len(close)
			if depth == 0 {
				x.record(start, j)
				return true
			}
			continue
		}
		j++
	}
	x.out.err = errUnterminated
	return true
}

// hasPrefixAt reports whether the source has s at j.
func (x *lexer) hasPrefixAt(j int, s string) bool {
	return j+len(s) <= len(x.src) && string(x.src[j:j+len(s)]) == s
}

// wordStart reports whether the '#' at i starts a word: at the line start,
// or after whitespace or one of ;&|()<>. So echo a#b, $# and ${#x} are code.
func (x *lexer) wordStart(i int) bool {
	if i == 0 {
		return true
	}
	switch x.src[i-1] {
	case ' ', '\t', '\n', ';', '&', '|', '(', ')', '<', '>':
		return true
	}
	return false
}

// atString consumes a string literal starting at x.i, if one does. Longer
// openers are tried before shorter ones, so """ wins over ".
func (x *lexer) atString() bool {
	l, src, i := x.l, x.src, x.i
	switch {
	case l.swiftHash && src[i] == '#':
		return x.swiftRaw(i)
	case l.rsPrefix && isRSPrefixByte(src[i]):
		return x.rsString(i)
	case l.pyPrefix && isPyPrefixByte(src[i]):
		return x.pyString(i)
	case l.dartPrefix && src[i] == 'r':
		return x.dartString(i)
	case l.dollarQuote && src[i] == '$' && i+1 < len(src) && (src[i+1] == '\'' || src[i+1] == '"'):
		return x.quoted(i+1, true, false)
	case l.verbatim && src[i] == '@' && i+1 < len(src) && src[i+1] == '"':
		return x.verbatimString(i)
	case l.dollarTag && src[i] == '$':
		return x.dollarTagString(i)
	case l.longBracket && src[i] == '[':
		return x.luaLongString(i)
	case l.triple && src[i] == '\'' && x.hasPrefixAt(i, "'''"):
		return x.scanLiteral(i+3, "'''", l.sqEsc)
	case l.triple && src[i] == '"' && x.hasPrefixAt(i, `"""`):
		return x.scanLiteral(i+3, `"""`, l.dqEsc)
	case l.single && src[i] == '\'':
		return x.quoted(i, l.sqEsc, l.sqDouble)
	case l.double && src[i] == '"':
		return x.quoted(i, l.dqEsc, false)
	case l.backtick && src[i] == '`':
		return x.scanLiteral(i+1, "`", true)
	}
	return false
}

// quoted scans a quoted string opening at i: esc lets a backslash eat the
// next character, doubling makes a doubled quote character content. The
// string may span lines.
func (x *lexer) quoted(i int, esc, doubling bool) bool {
	q := x.src[i]
	for j := i + 1; j < len(x.src); j++ {
		c := x.src[j]
		if esc && c == '\\' {
			j++
			continue
		}
		if c != q {
			continue
		}
		if doubling && j+1 < len(x.src) && x.src[j+1] == q {
			j++
			continue
		}
		x.i = j + 1
		return true
	}
	x.out.err = errUnterminated
	return true
}

// scanLiteral scans a string whose closer is the literal close, with a
// backslash eating the next character when esc.
func (x *lexer) scanLiteral(i int, close string, esc bool) bool {
	for j := i; j < len(x.src); j++ {
		if esc && x.src[j] == '\\' {
			j++
			continue
		}
		if x.hasPrefixAt(j, close) {
			x.i = j + len(close)
			return true
		}
	}
	x.out.err = errUnterminated
	return true
}

// rawQuoteHash scans a raw string opened by a quote at i whose closer is a
// quote followed by exactly n '#'s: r#"..."#, #"..."#.
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

// swiftRaw scans a Swift raw string opening at the '#' run i: #"..."# with
// any number of '#'s; ' is not a string in Swift, so only " is handled.
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
// any number of '#'s before the quote. The r forms are raw, closed by a
// quote followed by exactly that many '#'s; ' is a lifetime, not a string.
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
// closer, so r"a\" does not close.
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

// dartString scans a Dart raw string opening at r: r'...', r"...",
// r”'...”' and r"""...""", with no escapes at all.
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

// verbatimString scans a C# @"..." string: no escapes, a doubled quote is
// content, and the string may span lines.
func (x *lexer) verbatimString(i int) bool {
	return x.quoted(i+1, false, true)
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

// luaLongString scans a [[...]] or [=[...]=] long string.
func (x *lexer) luaLongString(i int) bool {
	n := longOpener(x.src, i)
	if n < 0 {
		return false
	}
	end := longCloser(x.src, i, n)
	if end < 0 {
		x.out.err = errUnterminated
		return true
	}
	x.i = end
	return true
}

// atArith consumes an arithmetic delimiter, so a << inside $(( )) or (( ))
// is a shift, not a heredoc.
func (x *lexer) atArith() bool {
	if !x.l.arith || x.i+1 >= len(x.src) {
		return false
	}
	switch {
	case x.src[x.i] == '(' && x.src[x.i+1] == '(':
		x.arith++
		x.i += 2
		return true
	case x.arith > 0 && x.src[x.i] == ')' && x.src[x.i+1] == ')':
		x.arith--
		x.i += 2
		return true
	}
	return false
}

// atHeredocOpener queues a heredoc, if a << redirection starts at x.i:
// <<WORD, <<-WORD, <<'WORD', <<"WORD" and <<\WORD. Inside arithmetic a <<
// is a shift, not a heredoc, and <<< is a here-string.
func (x *lexer) atHeredocOpener() bool {
	if !x.l.heredoc || x.i+1 >= len(x.src) || x.src[x.i] != '<' || x.src[x.i+1] != '<' {
		return false
	}
	if x.i+2 < len(x.src) && x.src[x.i+2] == '<' {
		x.i += 3
		return true
	}
	if x.arith > 0 {
		return false
	}
	return x.openHeredoc()
}

// openHeredoc parses the delimiter word and queues the heredoc; its body
// starts on the next line, and one line may open several heredocs.
func (x *lexer) openHeredoc() bool {
	j := x.i + 2
	tabs := false
	if j < len(x.src) && x.src[j] == '-' {
		tabs = true
		j++
	}
	for j < len(x.src) && (x.src[j] == ' ' || x.src[j] == '\t') {
		j++
	}
	quote := byte(0)
	if j < len(x.src) && (x.src[j] == '\'' || x.src[j] == '"' || x.src[j] == '\\') {
		quote = x.src[j]
		j++
	}
	start := j
	for j < len(x.src) && strings.IndexByte(" \t\n;&|()<>\"'`\\", x.src[j]) < 0 {
		j++
	}
	if j == start {
		return false // no delimiter word: << stays code
	}
	end := j
	if quote != 0 && quote != '\\' {
		if j >= len(x.src) || x.src[j] != quote {
			return false // the quote never closes on the header line
		}
		j++
	}
	x.pend = append(x.pend, heredoc{word: string(x.src[start:end]), tabs: tabs})
	x.i = j
	return true
}

// activateHeredoc makes the first heredoc opened on the line that just
// ended active; the queue keeps the rest in order.
func (x *lexer) activateHeredoc() {
	if x.act == nil && len(x.pend) > 0 {
		x.act = &x.pend[0]
		x.pend = x.pend[1:]
	}
}

// heredocStep consumes one line of the active heredoc: every byte is code
// until the line that equals the delimiter ends the body.
func (x *lexer) heredocStep() {
	eol := lineEnd(x.src, x.i)
	text := string(x.src[x.i:eol])
	if x.act.tabs {
		text = strings.TrimLeft(text, "\t")
	}
	if eol < len(x.src) {
		eol++
	}
	x.i = eol
	if text == x.act.word {
		x.act = nil
		x.activateHeredoc()
	}
}

// atYamlHeader consumes a block scalar header, | or > with its chomping and
// indentation indicators, and arms the body scanner: the body is a string,
// so a body line starting # is code.
func (x *lexer) atYamlHeader() bool {
	if !x.l.yamlBlock {
		return false
	}
	c := x.src[x.i]
	if c != '|' && c != '>' {
		return false
	}
	j := x.i + 1
	for j < len(x.src) && strings.IndexByte("-+0123456789", x.src[j]) >= 0 {
		j++
	}
	eol := lineEnd(x.src, j)
	rest := x.src[j:eol]
	k := 0
	for k < len(rest) && (rest[k] == ' ' || rest[k] == '\t') {
		k++
	}
	if k < len(rest) && rest[k] != '#' {
		return false // content after the header: not a block scalar
	}
	x.i = j
	x.yamlPend = true
	return true
}

// scanYamlBody consumes a block scalar's body: every blank line and every
// line that starts with whitespace is the scalar's content, which is code,
// and the first line that starts at column 0 ends the scalar.
func (x *lexer) scanYamlBody() {
	for x.i < len(x.src) {
		eol := lineEnd(x.src, x.i)
		line := x.src[x.i:eol]
		if len(line) > 0 && line[0] != ' ' && line[0] != '\t' {
			return
		}
		if eol < len(x.src) {
			eol++
		}
		x.i = eol
	}
}

// isDirective reports whether one comment's raw text is a directive: a
// comment a tool reads, which a comment-only change may not touch. The
// "#!" prefix counts only for a comment that starts the file: a shebang
// sits at byte 0, and a "#!" further into a line is ordinary text.
func isDirective(l *lang, text string, start int) bool {
	for _, p := range l.directives {
		if p == "#!" && start != 0 {
			continue
		}
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// lineEnd returns the index of the newline at or after from, or len(src).
func lineEnd(src []byte, from int) int {
	if i := bytes.IndexByte(src[from:], '\n'); i >= 0 {
		return from + i
	}
	return len(src)
}

// longOpener reports the level of the Lua long bracket a '[' at j opens:
// [[ is level 0, [=[ level 1. -1 when j opens no long form.
func longOpener(src []byte, j int) int {
	if j >= len(src) || src[j] != '[' {
		return -1
	}
	k := j + 1
	for k < len(src) && src[k] == '=' {
		k++
	}
	if k < len(src) && src[k] == '[' {
		return k - j - 1
	}
	return -1
}

// longCloser returns one past the level-n long bracket closer found from
// j, or -1 when the form never closes.
func longCloser(src []byte, j, n int) int {
	closer := "]" + strings.Repeat("=", n) + "]"
	if o := strings.Index(string(src[j:]), closer); o >= 0 {
		return j + o + len(closer)
	}
	return -1
}

// byteAt returns src[i], or 0 when i is past the end.
func byteAt(src []byte, i int) byte {
	if i < len(src) {
		return src[i]
	}
	return 0
}

func isPyPrefixByte(c byte) bool { return strings.IndexByte("rbfuRBFU", c) >= 0 }

func isRSPrefixByte(c byte) bool { return c == 'r' || c == 'b' || c == 'c' }

func isRSPrefix2(s []byte) bool {
	switch string(s) {
	case "br", "cr", "rb", "rc":
		return true
	}
	return false
}

// isTagByte reports whether c can be part of a SQL dollar-quote tag.
func isTagByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

// isHsSymbol reports whether c is a Haskell symbol character, one of the
// characters that keeps -- an operator instead of a comment.
func isHsSymbol(c byte) bool {
	return strings.IndexByte("!#$%&*+./<=>?@\\^|-~:", c) >= 0
}
