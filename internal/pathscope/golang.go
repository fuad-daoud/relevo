package pathscope

import (
	"errors"
	"go/parser"
	"go/scanner"
	"go/token"
	"regexp"
	"strings"
)

// codeGeneratedRe is rule 6's generated-code marker: a line comment of the
// exact form `// Code generated ... DO NOT EDIT.`.
var codeGeneratedRe = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// goScan is one Go source's scan: its non-comment token stream, the ordered
// list of its directive comments, whether it imports "C", and any scan error.
type goScan struct {
	tokens     []goToken
	directives []string
	cgo        bool
	err        error
}

type goToken struct {
	tok token.Token
	lit string
}

// scanGo scans src with go/scanner in ScanComments mode. Comments are kept out
// of the token stream; a comment that is a directive goes to directives in
// order.
func scanGo(src []byte) goScan {
	var s scanner.Scanner
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var scanErr error
	s.Init(file, src, func(_ token.Position, msg string) {
		if scanErr == nil {
			scanErr = errors.New(msg)
		}
	}, scanner.ScanComments)

	var out goScan
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.COMMENT {
			if goDirective(lit) {
				out.directives = append(out.directives, lit)
			}
			continue
		}
		out.tokens = append(out.tokens, goToken{tok: tok, lit: lit})
	}
	out.err = scanErr
	out.cgo = detectCgo(out.tokens)
	return out
}

// parsesCleanly reports whether src is a syntactically valid Go file: the
// scanner tokenizes almost anything, so the parser is what refuses a blob the
// comment rule cannot judge.
func parsesCleanly(src []byte) bool {
	_, err := parser.ParseFile(token.NewFileSet(), "", src, 0)
	return err == nil
}

// goDirective reports whether a comment's raw text is a directive: a comment
// the toolchain or a tool reads, which a comment-only change may not touch.
// The table is compiled in, not in config: an editable list would let an actor
// loosen its own check.
func goDirective(lit string) bool {
	switch {
	case strings.HasPrefix(lit, "//go:"):
		return true
	case strings.HasPrefix(lit, "// +build"):
		return true
	case strings.HasPrefix(lit, "//line "), strings.HasPrefix(lit, "/*line "):
		return true
	case strings.HasPrefix(lit, "//export "), strings.HasPrefix(lit, "//extern "):
		return true
	case strings.HasPrefix(lit, "//nolint"), strings.HasPrefix(lit, "// nolint"):
		return true
	case strings.HasPrefix(lit, "//lint:"):
		return true
	case strings.Contains(lit, "#nosec"):
		return true
	}
	if codeGeneratedRe.MatchString(lit) {
		return true
	}
	text := commentText(lit)
	return strings.HasPrefix(text, "Output:") || strings.HasPrefix(text, "Unordered output:")
}

// commentText strips a comment's markers and surrounding space: the text a
// directive such as the go test "Output:" example is recognised by.
func commentText(lit string) string {
	t := strings.TrimPrefix(lit, "//")
	t = strings.TrimPrefix(t, "/*")
	t = strings.TrimSuffix(t, "*/")
	return strings.TrimSpace(t)
}

// detectCgo reports whether the token stream imports "C": the cgo preamble is
// code, so a file that does cannot be judged as comments alone.
func detectCgo(tokens []goToken) bool {
	for i, t := range tokens {
		if t.tok != token.IMPORT {
			continue
		}
		for j := i + 1; j < len(tokens); j++ {
			tt := tokens[j]
			if tt.tok == token.STRING && tt.lit == `"C"` {
				return true
			}
			switch tt.tok {
			case token.LPAREN, token.SEMICOLON, token.IDENT, token.PERIOD:
				// Part of the import spec or its parenthesised group.
			default:
				j = len(tokens)
			}
		}
	}
	return false
}

// judgeGo judges an M-status .go file: in scope exactly when the non-comment
// token streams and the ordered directive lists are equal. ok is false with
// the reason it was refused otherwise.
func judgeGo(oldSrc, newSrc []byte) (ok bool, reason string) {
	if !parsesCleanly(oldSrc) || !parsesCleanly(newSrc) {
		return false, ReasonCannotJudge
	}
	o := scanGo(oldSrc)
	n := scanGo(newSrc)
	if o.err != nil || n.err != nil || o.cgo || n.cgo {
		return false, ReasonCannotJudge
	}
	if !tokensEqual(o.tokens, n.tokens) {
		return false, ReasonCodeChange
	}
	if !equalStrings(o.directives, n.directives) {
		return false, ReasonDirectiveComment
	}
	return true, ""
}

// tokensEqual compares two token streams by (kind, literal), except SEMICOLON,
// which is compared by kind only: gofmt moves and drops the synthesized
// semicolons, so comparing their literal would refuse a whitespace-only edit.
func tokensEqual(a, b []goToken) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].tok != b[i].tok {
			return false
		}
		if a[i].tok == token.SEMICOLON {
			continue
		}
		if a[i].lit != b[i].lit {
			return false
		}
	}
	return true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
