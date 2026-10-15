package pathscope

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// documentedExts is the extension set the README documents, which the table
// must equal.
var documentedExts = []string{
	".py", ".pyi", ".sh", ".bash", ".zsh", ".yaml", ".yml", ".toml", ".r",
	".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".java", ".kt", ".kts",
	".scala", ".cs", ".swift", ".dart", ".c", ".h", ".cc", ".cpp", ".hpp",
	".rs", ".sql", ".lua", ".hs",
}

// TestLanguageTable pins the shape of the compiled-in language table: its
// keys are lowercase, .go is absent, every documented extension resolves,
// the excluded ones do not, and every entry carries a comment marker.
func TestLanguageTable(t *testing.T) {
	t.Parallel()

	for ext, l := range extTable {
		if ext != strings.ToLower(ext) {
			t.Errorf("table key %q is not lowercase", ext)
		}
		if !l.hasCommentMarker() {
			t.Errorf("%s (%s): table entry carries no comment marker", ext, l.name)
		}
	}
	if _, ok := extTable[".go"]; ok {
		t.Error(".go is in the table; it keeps the go/scanner rule")
	}
	for _, ext := range documentedExts {
		if langFor("x"+ext) == nil {
			t.Errorf("x%s: extension is not in the table", ext)
		}
	}
	for _, name := range []string{"x.rb", "x.php", "x.pl", "x.json", "x.txt", "Makefile", "x"} {
		if langFor(name) != nil {
			t.Errorf("%q resolved to a table language", name)
		}
	}
}

// TestLanguageTableMatchesDocumentedSet pins that the table's extension set
// equals the documented set, so a language cannot be added without the
// README and the tests moving with it.
func TestLanguageTableMatchesDocumentedSet(t *testing.T) {
	t.Parallel()

	got := make([]string, 0, len(extTable))
	for ext := range extTable {
		got = append(got, ext)
	}
	want := append([]string(nil), documentedExts...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("table has %d extensions, documented set has %d: %v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("table extensions = %v, documented set = %v", got, want)
		}
	}
}

// langOf returns the table language for an extension, failing when absent.
func langOf(t *testing.T, ext string) *lang {
	t.Helper()
	l := langFor("x." + ext)
	if l == nil {
		t.Fatalf("x.%s: no table language", ext)
	}
	return l
}

// lexExt lexes src as ext, failing when the lex errors and none is wanted.
func lexExt(t *testing.T, ext, src string) lexOut {
	t.Helper()
	return lex(langOf(t, ext), []byte(src))
}

// spanTexts returns a comment span's texts.
func spanTexts(src []byte, spans []span) []string {
	out := make([]string, 0, len(spans))
	for _, s := range spans {
		out = append(out, string(src[s.start:s.end]))
	}
	return out
}

// lexCases is TestLex's table: one case per string form in the table,
// plus heredocs, YAML block scalars, Lua long brackets, nested Rust block
// comments and the Haskell -- operator. Each row's comment markers inside
// strings are code, so the only comment spans are the real comments in
// want.
var lexCases = []struct {
	ext  string
	src  string
	want []string
}{
	{"py", "x = 'a # b'  # real\n", []string{"# real"}},
	{"py", `x = "a # b"  # real` + "\n", []string{"# real"}},
	{"py", "x = '''a # b'''  # real\n", []string{"# real"}},
	{"py", `x = """a # b"""  # real` + "\n", []string{"# real"}},
	{"py", `x = r'a # b'  # real` + "\n", []string{"# real"}},
	{"py", `x = b"a # b"  # real` + "\n", []string{"# real"}},
	{"py", `x = f"a # b"  # real` + "\n", []string{"# real"}},
	{"py", `x = rb'a # b'  # real` + "\n", []string{"# real"}},
	{"py", `x = R"a # b"  # real` + "\n", []string{"# real"}},
	{"sh", "echo 'a # b'  # real\n", []string{"# real"}},
	{"sh", "echo \"a # b\"  # real\n", []string{"# real"}},
	{"sh", "echo $'a # b'  # real\n", []string{"# real"}},
	{"sh", "echo $\"a # b\"  # real\n", []string{"# real"}},
	{"sh", "cat <<EOF\n# inside\nEOF\n# real\n", []string{"# real"}},
	{"sh", "cat <<'EOF'\n# inside\nEOF\n# real\n", []string{"# real"}},
	{"sh", "cat <<\"EOF\"\n# inside\nEOF\n# real\n", []string{"# real"}},
	{"sh", "cat <<\\EOF\n# inside\nEOF\n# real\n", []string{"# real"}},
	{"sh", "cat <<-EOF\n\t# inside\nEOF\n# real\n", []string{"# real"}},
	{"sh", "cat <<EOF <<OUT\n# inside\nEOF\n# in out\nOUT\n# real\n", []string{"# real"}},
	{"yaml", "k: 'a # b'  # real\n", []string{"# real"}},
	{"yaml", "k: \"a # b\"  # real\n", []string{"# real"}},
	{"yaml", "k: \"a '' b\"  # real\n", []string{"# real"}},
	{"yaml", "k: |\n  # inside\n  v\n# real\n", []string{"# real"}},
	{"yaml", "k: >-\n  # inside\n  v\n# real\n", []string{"# real"}},
	{"toml", "x = 'a # b'  # real\n", []string{"# real"}},
	{"toml", "x = \"a # b\"  # real\n", []string{"# real"}},
	{"toml", "x = '''a # b'''  # real\n", []string{"# real"}},
	{"toml", "x = \"\"\"a # b\"\"\"  # real\n", []string{"# real"}},
	{"r", "x <- 'a # b'  # real\n", []string{"# real"}},
	{"r", "x <- \"a # b\"  # real\n", []string{"# real"}},
	{"js", "let s = 'a // b';  // real\n", []string{"// real"}},
	{"js", `let s = "a // b";  // real` + "\n", []string{"// real"}},
	{"js", "let t = `a // b`;  // real\n", []string{"// real"}},
	{"js", "let t = `a ${x} // b`;  // real\n", []string{"// real"}},
	{"js", "let u = 'a /* b';  // real\n", []string{"// real"}},
	{"java", "char c = 'a';  // real\n", []string{"// real"}},
	{"java", `String s = "a // b";  // real` + "\n", []string{"// real"}},
	{"java", "String s = \"\"\"a // b\"\"\";  // real\n", []string{"// real"}},
	{"cs", "var c = 'a';  // real\n", []string{"// real"}},
	{"cs", `var s = "a // b";  // real` + "\n", []string{"// real"}},
	{"cs", "var s = @\"a // b\";  // real\n", []string{"// real"}},
	{"cs", "var s = @\"a \"\" b\";  // real\n", []string{"// real"}},
	{"cs", "var s = \"\"\"a // b\"\"\";  // real\n", []string{"// real"}},
	{"swift", `let s = "a // b"  // real` + "\n", []string{"// real"}},
	{"swift", "let s = \"\"\"a // b\"\"\"  // real\n", []string{"// real"}},
	{"swift", "let s = #\"a // b\"#  // real\n", []string{"// real"}},
	{"swift", "let s = ##\"a // b\"##  // real\n", []string{"// real"}},
	{"dart", "var s = 'a // b';  // real\n", []string{"// real"}},
	{"dart", `var s = "a // b";  // real` + "\n", []string{"// real"}},
	{"dart", "var s = '''a // b''';  // real\n", []string{"// real"}},
	{"dart", "var s = \"\"\"a // b\"\"\";  // real\n", []string{"// real"}},
	{"dart", "var s = r'a // b';  // real\n", []string{"// real"}},
	{"dart", `var s = r"a // b";  // real` + "\n", []string{"// real"}},
	{"c", "char c = 'a';  // real\n", []string{"// real"}},
	{"c", `char *s = "a // b";  // real` + "\n", []string{"// real"}},
	{"c", "char *s = \"a \\\n// b\";  // real\n", []string{"// real"}},
	{"rs", `let s = "a // b"; // real` + "\n", []string{"// real"}},
	{"rs", "let s = r\"a // b\"; // real\n", []string{"// real"}},
	{"rs", "let s = r#\"a // b\"#; // real\n", []string{"// real"}},
	{"rs", "let s = b\"a // b\"; // real\n", []string{"// real"}},
	{"rs", "let s = c\"a // b\"; // real\n", []string{"// real"}},
	{"rs", "let s = br#\"a // b\"#; // real\n", []string{"// real"}},
	{"rs", "/* /* a // b */ */ let x = 1; // real\n", []string{"/* /* a // b */ */", "// real"}},
	{"sql", "SELECT 'a -- b'  -- real\n", []string{"-- real"}},
	{"sql", "SELECT 'it''s -- ok'  -- real\n", []string{"-- real"}},
	{"sql", "SELECT $tag$a -- b$tag$  -- real\n", []string{"-- real"}},
	{"sql", "SELECT $$a -- b$$  -- real\n", []string{"-- real"}},
	{"sql", "SELECT 1 /* a -- b */  -- real\n", []string{"/* a -- b */", "-- real"}},
	{"lua", "s = 'a -- b'  -- real\n", []string{"-- real"}},
	{"lua", `s = "a -- b"  -- real` + "\n", []string{"-- real"}},
	{"lua", "s = [[a -- b]]  -- real\n", []string{"-- real"}},
	{"lua", "s = [=[a -- b]=]  -- real\n", []string{"-- real"}},
	{"lua", "--[[ a -- b ]]  -- real\n", []string{"--[[ a -- b ]]", "-- real"}},
	{"lua", "--[==[ a -- b ]==]  -- real\n", []string{"--[==[ a -- b ]==]", "-- real"}},
	{"hs", `s = "a -- b"  -- real` + "\n", []string{"-- real"}},
	{"hs", "a --> b  -- real\n", []string{"-- real"}},
	{"hs", "a --| b  -- real\n", []string{"-- real"}},
	{"hs", "{- a -- b -}  -- real\n", []string{"{- a -- b -}", "-- real"}},
	{"hs", "{- a {- b -} c -}  -- real\n", []string{"{- a {- b -} c -}", "-- real"}},
	{"hs", "s = \"a\\   \\b\"  -- real\n", []string{"-- real"}},
	{"js", "const r = /a \\/ b/;  // real\n", []string{"// real"}},
	{"js", "const r = /[//]/;  // real\n", []string{"// real"}},
	{"js", "const r = /a[/*]b/;  // real\n", []string{"// real"}},
	{"js", "x = a / b;  // real\n", []string{"// real"}},
	{"js", "const t = `${f(\"a//b\")}`;  // real\n", []string{"// real"}},
	{"js", "const t = `${f(`a//b`)} c`;  // real\n", []string{"// real"}},
	{"sql", "SELECT \"a--b\"  -- real\n", []string{"-- real"}},
	{"sql", "SELECT `a--b`  -- real\n", []string{"-- real"}},
	{"sql", "SELECT [a--b]  -- real\n", []string{"-- real"}},
	{"sql", "SELECT \"a\"\"--b\"  -- real\n", []string{"-- real"}},
	{"c", "char *s = R\"(a // b)\";  // real\n", []string{"// real"}},
	{"c", "char *s = LR\"delim(a // b)delim\";  // real\n", []string{"// real"}},
	{"c", "char *s = u8R\"(a // b)\";  // real\n", []string{"// real"}},
	{"c", "char *s = u8\"a // b\";  // real\n", []string{"// real"}},
	{"r", "x <- r\"(a # b)\"  # real\n", []string{"# real"}},
	{"r", "x <- r\"--(a # b)--\"  # real\n", []string{"# real"}},
	{"r", "x <- r\"{a # b}\"  # real\n", []string{"# real"}},
	{"r", "x <- R'[a # b]'  # real\n", []string{"# real"}},
	{"kt", "val s = \"a ${f(\"a//b\")} b\"  // real\n", []string{"// real"}},
	{"scala", "val s = s\"a ${f(\"a//b\")} b\"  // real\n", []string{"// real"}},
	{"swift", "let s = \"a \\(f(\"a//b\")) b\"  // real\n", []string{"// real"}},
	{"swift", "let s = #\"a \\#(f(\"a//b\")) b\"#  // real\n", []string{"// real"}},
	{"dart", "var s = 'a ${f(\"a//b\")} b';  // real\n", []string{"// real"}},
	{"dart", "var s = r'a ${f(\"a//b\")} b';  // real\n", []string{"// real"}},
	{"cs", "var s = $\"a {f(\"a//b\")} b\";  // real\n", []string{"// real"}},
	{"cs", "var s = $\"a {{x}} b\";  // real\n", []string{"// real"}},
}

// TestLex runs every lexCases row through the lexer.
func TestLex(t *testing.T) {
	t.Parallel()

	for _, tt := range lexCases {
		out := lexExt(t, tt.ext, tt.src)
		if out.err != nil {
			t.Errorf("%s lex %q: %v", tt.ext, tt.src, out.err)
			continue
		}
		got := spanTexts([]byte(tt.src), out.spans)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s lex %q: comments = %q, want %q", tt.ext, tt.src, got, tt.want)
		}
	}
}

// TestLexUnterminated pins that an unterminated form errors, one per
// family: refuse, never guess.
func TestLexUnterminated(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ ext, src string }{
		{"py", "x = \"\"\"abc\n"},
		{"py", "x = 'abc\n"},
		{"sh", "cat <<EOF\nbody\n"},
		{"yaml", "k: \"abc\n"},
		{"toml", "x = '''abc\n"},
		{"r", "x <- 'abc\n"},
		{"js", "let x = 1; /* abc\n"},
		{"java", "String s = \"\"\"abc\n"},
		{"cs", "var s = @\"abc\n"},
		{"swift", "let s = \"\"\"abc\n"},
		{"swift", "let s = #\"abc\n"},
		{"dart", "var s = '''abc\n"},
		{"c", "char *s = \"abc\n"},
		{"rs", "let s = r#\"abc\n"},
		{"sql", "SELECT 'abc\n"},
		{"sql", "SELECT $tag$abc\n"},
		{"lua", "s = [[abc\n"},
		{"lua", "--[[ abc\n"},
		{"hs", "x = 1\n{- abc\n"},
		{"js", "x = /abc\n"},
		{"sql", "select \"abc\n"},
		{"sql", "select `abc\n"},
		{"sql", "select [abc\n"},
		{"c", "char *s = R\"(abc\n"},
		{"c", "char *s = R\"abc\n"},
		{"r", "x <- r\"(abc\n"},
	} {
		if out := lex(langOf(t, tt.ext), []byte(tt.src)); out.err == nil {
			t.Errorf("%s lex %q: no error, want unterminated", tt.ext, tt.src)
		}
	}
}

// TestLexDirectives pins the ordered directive list: a comment whose raw
// text starts with one of the language's prefixes joins it, #! on the first
// line only, and an unterminated form errors before any of it matters.
func TestLexDirectives(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		ext  string
		src  string
		want []string
	}{
		{"py", "#!/usr/bin/env python\n# noqa: E501\nx = 1  # note\n",
			[]string{"#!/usr/bin/env python", "# noqa: E501"}},
		{"py", "x = 1  #!/bin/sh\n", nil},
		{"sh", "#!/bin/sh\n# shellcheck disable=SC2086\necho hi  # note\n",
			[]string{"#!/bin/sh", "# shellcheck disable=SC2086"}},
		{"js", "// @ts-ignore\nlet x = 1;  // note\n", []string{"// @ts-ignore"}},
		{"js", "/* eslint-disable */\nlet x = 1;\n", []string{"/* eslint-disable */"}},
		{"lua", "---@type string\nlocal x = 1  -- note\n", []string{"---@type string"}},
		{"hs", "{-# LANGUAGE LambdaCase #-}\nx = 1\n", []string{"{-# LANGUAGE LambdaCase #-}"}},
		{"sql", "-- name: get-user\nSELECT 1;\n", []string{"-- name: get-user"}},
	} {
		out := lexExt(t, tt.ext, tt.src)
		if out.err != nil {
			t.Errorf("%s lex %q: %v", tt.ext, tt.src, out.err)
			continue
		}
		if !reflect.DeepEqual(out.directives, tt.want) {
			t.Errorf("%s lex %q: directives = %q, want %q", tt.ext, tt.src, out.directives, tt.want)
		}
	}
}

// TestLexAlwaysAdvances pins the scan loop's guard: an empty input, a lone
// marker at end of file, and every unterminated form finish without a pass
// that fails to move forward. A branch that forgets to consume must end the
// scan with the guard's error, never a hang or a memory runaway.
func TestLexAlwaysAdvances(t *testing.T) {
	t.Parallel()

	// Every unterminated form from the table's families, a lone comment
	// marker at end of file for each family, and the empty input.
	inputs := []string{
		"",
		"#", "/", "-", "{", "<<", "$", "[",
		"x = \"\"\"abc\n", "x = 'abc\n", "cat <<EOF\nbody\n", "k: \"abc\n",
		"x = '''abc\n", "x <- 'abc\n", "let x = 1; /* abc\n",
		"String s = \"\"\"abc\n", "var s = @\"abc\n", "let s = \"\"\"abc\n",
		"let s = #\"abc\n", "var s = '''abc\n", "char *s = \"abc\n",
		"let s = r#\"abc\n", "SELECT 'abc\n", "SELECT $tag$abc\n",
		"s = [[abc\n", "--[[ abc\n", "x = 1\n{- abc\n",
		"/abc", "x = /abc\n", "select [abc", "auto s = R\"(abc", "x <- r\"(abc",
	}
	for ext, l := range extTable {
		for _, src := range inputs {
			out := lex(l, []byte(src))
			if errors.Is(out.err, errStuck) {
				t.Errorf("%s lex %q: %v", ext, src, out.err)
			}
		}
	}
}

// judgeExt runs Judge over one M change of an extension with the two blob
// sides given, returning the violations.
func judgeExt(t *testing.T, ext, oldSrc, newSrc string, comments bool) []Violation {
	t.Helper()
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	c := Change{Status: 'M', OldMode: "100644", NewMode: "100644",
		OldOID: "old", NewOID: "new", Path: "p/f" + ext}
	read := blobs(map[string]string{"old": oldSrc, "new": newSrc})
	return Judge(Scope{Comments: comments}, []Change{c}, read)
}

// commentMarker returns the language's line comment marker, derived from
// the table so every extension gets its family's marker.
func commentMarker(l *lang) string {
	switch {
	case l.hashAny, l.hashWord:
		return "# "
	case l.slashLine:
		return "// "
	case l.dashLine, l.dashSymbol:
		return "-- "
	}
	return ""
}

// wantReason asserts one violation with the reason wanted.
func wantReason(t *testing.T, ext, src string, v []Violation, reason string) {
	t.Helper()
	if len(v) != 1 || v[0].Reason != reason {
		t.Errorf("%s %q: violations = %v, want one %q", ext, src, v, reason)
	}
}

// TestCommentOnlyEditPassesEveryTableLanguage gives every extension one
// old/new pair differing only inside a comment: each is in scope.
func TestCommentOnlyEditPassesEveryTableLanguage(t *testing.T) {
	t.Parallel()

	for _, ext := range documentedExts {
		m := commentMarker(langOf(t, ext))
		old := "x = 1\n" + m + "old note\n"
		new := "x = 1\n" + m + "new note\n"
		if v := judgeExt(t, ext, old, new, true); len(v) != 0 {
			t.Errorf("%s: comment-only edit refused: %v", ext, v)
		}
	}
}

// TestCodeChangeRefusedEveryTableLanguage is the same pairs with one byte
// changed outside a comment: each is a code change.
func TestCodeChangeRefusedEveryTableLanguage(t *testing.T) {
	t.Parallel()

	for _, ext := range documentedExts {
		m := commentMarker(langOf(t, ext))
		old := "x = 1\n" + m + "note\n"
		new := "x = 2\n" + m + "note\n"
		wantReason(t, ext, old, judgeExt(t, ext, old, new, true), ReasonCodeChange)
	}
}

// TestCommentMarkerInsideStringIsCode pins that a comment marker inside a
// string is code: a change inside one is a code change.
func TestCommentMarkerInsideStringIsCode(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		ext, old, new string
	}{
		{"js", `let s = "a // b";`, `let s = "a // c";`},
		{"py", `s = 'a # b'`, `s = 'a # c'`},
		{"js", "let t = `// x`;", "let t = `// y`;"},
		{"lua", "s = [[a -- b]]", "s = [[a -- c]]"},
	} {
		wantReason(t, tt.ext, tt.old,
			judgeExt(t, tt.ext, tt.old+"\n", tt.new+"\n", true), ReasonCodeChange)
	}
}

// TestDirectiveCommentRefused pins that a comment whose text is a
// directive may not change: the ordered lists must be equal.
func TestDirectiveCommentRefused(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		ext, old, new string
	}{
		{"py", "x = 1\n# noqa: E501\n", "x = 1\n# noqa: W605\n"},
		{"py", "x = 1\n# type: ignore\n", "x = 1\n# type: ignore-report\n"},
		{"py", "# -*- coding: utf-8 -*-\nx = 1\n", "# -*- coding: latin-1 -*-\nx = 1\n"},
		{"js", "// @ts-ignore\nlet x = 1;\n", "// @ts-expect-error\nlet x = 1;\n"},
		{"js", "// eslint-disable-next-line\nlet x = 1;\n", "// eslint-disable-next-line no-console\nlet x = 1;\n"},
		{"py", "#!/usr/bin/env python\nx = 1\n", "#!/usr/bin/env python3\nx = 1\n"},
		{"lua", "---@type string\nlocal x = 1\n", "---@class string\nlocal x = 1\n"},
		{"hs", "{-# LANGUAGE LambdaCase #-}\nx = 1\n", "{-# LANGUAGE OverloadedStrings #-}\nx = 1\n"},
	} {
		wantReason(t, tt.ext, tt.old,
			judgeExt(t, tt.ext, tt.old, tt.new, true), ReasonDirectiveComment)
	}
}

// TestCannotJudgeUnterminated pins that either side failing to lex is
// cannot judge: an unclosed /*, {-, """, [[ or heredoc, refuse, never
// guess.
func TestCannotJudgeUnterminated(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		ext, old, new string
	}{
		{"js", "let x = 1;\n", "let x = 1; /* abc\n"},
		{"hs", "x = 1\n", "x = 1\n{- abc\n"},
		{"py", "x = 1\n", "x = \"\"\"abc\n"},
		{"lua", "x = 1\n", "s = [[abc\n"},
		{"sh", "echo hi\n", "cat <<EOF\nbody\n"},
	} {
		wantReason(t, tt.ext, tt.new,
			judgeExt(t, tt.ext, tt.old, tt.new, true), ReasonCannotJudge)
	}
}

// TestHashNotAtWordStartIsCode pins that a # that does not start a word is
// code: echo a#b and key: a#b are not comments.
func TestHashNotAtWordStartIsCode(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		ext, old, new string
	}{
		{"sh", "echo a#b\n", "echo a#c\n"},
		{"yaml", "key: a#b\n", "key: a#c\n"},
	} {
		wantReason(t, tt.ext, tt.old,
			judgeExt(t, tt.ext, tt.old, tt.new, true), ReasonCodeChange)
	}
}

// TestShellHeredocBodyIsCode pins that a heredoc body is code, so a body
// line starting with # may not change.
func TestShellHeredocBodyIsCode(t *testing.T) {
	t.Parallel()

	old := "cat <<EOF\n# one\nEOF\necho done\n"
	new := "cat <<EOF\n# two\nEOF\necho done\n"
	wantReason(t, "sh", old, judgeExt(t, "sh", old, new, true), ReasonCodeChange)
}

// TestShellArithmeticShiftIsNotAHeredoc pins that a << inside $(( )) or
// (( )) is a shift, not a heredoc: an unrelated comment edit stays in
// scope.
func TestShellArithmeticShiftIsNotAHeredoc(t *testing.T) {
	t.Parallel()

	old := "x=$((1 << 2))\n# note\n"
	new := "x=$((1 << 2))\n# other\n"
	if v := judgeExt(t, "sh", old, new, true); len(v) != 0 {
		t.Errorf("arithmetic shift read as a heredoc: %v", v)
	}
}

// TestYamlBlockScalarBodyIsCode pins that a block scalar's body is a
// string, so a body line starting with # may not change.
func TestYamlBlockScalarBodyIsCode(t *testing.T) {
	t.Parallel()

	old := "k: |\n  # one\n  v\n"
	new := "k: |\n  # two\n  v\n"
	wantReason(t, "yaml", old, judgeExt(t, "yaml", old, new, true), ReasonCodeChange)
}

// TestIndentationChangeRefused pins that leading whitespace is code: an
// indentation change in Python or YAML is a code change.
func TestIndentationChangeRefused(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		ext, old, new string
	}{
		{"py", "if x:\n    pass\n", "if x:\n        pass\n"},
		{"yaml", "a:\n  b: 1\n", "a:\n    b: 1\n"},
	} {
		wantReason(t, tt.ext, tt.old,
			judgeExt(t, tt.ext, tt.old, tt.new, true), ReasonCodeChange)
	}
}

// TestCommentAddedRemovedAndMovedPasses pins that a whole-line comment
// added, another deleted and a third moved are free: the code lines match.
func TestCommentAddedRemovedAndMovedPasses(t *testing.T) {
	t.Parallel()

	old := "x = 1\n# one\ny = 2\n# two\n# three\n"
	new := "# two\nx = 1\n# new\ny = 2\n"
	if v := judgeExt(t, "py", old, new, true); len(v) != 0 {
		t.Errorf("comment add, delete and move refused: %v", v)
	}
}

// TestNestedBlockCommentEditPasses pins that Rust block comments nest: an
// edit inside a /* /* ... */ */ is a comment-only edit.
func TestNestedBlockCommentEditPasses(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ old, new string }{
		{"let x = 1; /* /* old */ */\n", "let x = 1; /* /* new */ */\n"},
		{"let v = /* /* a */ b */ 1;\n", "let v = /* /* a */ c */ 1;\n"},
	} {
		if v := judgeExt(t, "rs", tt.old, tt.new, true); len(v) != 0 {
			t.Errorf("nested block comment edit refused: %q -> %q: %v", tt.old, tt.new, v)
		}
	}
}

// TestNewOrDeletedTableLanguageRefused pins that a table language added or
// deleted is new/deleted source file: a new file has no old side to judge.
func TestNewOrDeletedTableLanguageRefused(t *testing.T) {
	t.Parallel()

	for _, status := range []byte{'A', 'D'} {
		oldMode, newMode := "000000", "100644"
		if status == 'D' {
			oldMode, newMode = "100644", "000000"
		}
		c := Change{Status: status, OldMode: oldMode, NewMode: newMode, Path: "p/new.py"}
		v := Judge(Scope{Comments: true}, []Change{c}, blobs(nil))
		if len(v) != 1 || v[0].Reason != ReasonNewOrDeletedSource {
			t.Errorf("status %s: violations = %v, want one %q",
				string(status), v, ReasonNewOrDeletedSource)
		}
	}
}

// TestCommentsDisallowedTableLanguageRefused pins that a scope without
// comment judgement refuses a table language the way it refuses .go,
// before any blob is read.
func TestCommentsDisallowedTableLanguageRefused(t *testing.T) {
	t.Parallel()

	c := Change{Status: 'M', OldMode: "100644", NewMode: "100644", Path: "p/f.py"}
	v := Judge(Scope{Comments: false}, []Change{c}, blobs(nil))
	if len(v) != 1 || v[0].Reason != ReasonOutsideScope {
		t.Errorf("violations = %v, want one %q", v, ReasonOutsideScope)
	}
}

// TestExtensionOutsideTableRefused pins today's refusal for an extension
// the table does not carry, an extensionless name included.
func TestExtensionOutsideTableRefused(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"x.json", "x.txt", "x.rb", "x.php", "x.pl", "Makefile", "Dockerfile"} {
		c := Change{Status: 'M', OldMode: "100644", NewMode: "100644", Path: path}
		v := Judge(Scope{Comments: true}, []Change{c}, blobs(nil))
		if len(v) != 1 || v[0].Reason != ReasonOutsideScope {
			t.Errorf("%q: violations = %v, want one %q", path, v, ReasonOutsideScope)
		}
	}
}

// TestExtensionLookupIsCaseInsensitive pins that the lookup lowercases the
// extension: x.PY is judged as Python.
func TestExtensionLookupIsCaseInsensitive(t *testing.T) {
	t.Parallel()

	c := Change{Status: 'M', OldMode: "100644", NewMode: "100644",
		OldOID: "old", NewOID: "new", Path: "p/x.PY"}
	read := blobs(map[string]string{
		"old": "x = 1\n# old note\n",
		"new": "x = 1\n# new note\n",
	})
	if v := Judge(Scope{Comments: true}, []Change{c}, read); len(v) != 0 {
		t.Errorf("x.PY not judged as Python: %v", v)
	}
}

// judgeFile runs Judge over one M change of the named path with the two blob
// sides given, returning the violations.
func judgeFile(t *testing.T, path, oldSrc, newSrc string) []Violation {
	t.Helper()
	c := Change{Status: 'M', OldMode: "100644", NewMode: "100644",
		OldOID: "old", NewOID: "new", Path: path}
	read := blobs(map[string]string{"old": oldSrc, "new": newSrc})
	return Judge(Scope{Comments: true}, []Change{c}, read)
}

// refuseReason asserts one violation whose reason is a refusal -- code
// change or cannot judge, never a pass.
func refuseReason(t *testing.T, path, oldSrc, newSrc string, v []Violation) {
	t.Helper()
	if len(v) != 1 || (v[0].Reason != ReasonCodeChange && v[0].Reason != ReasonCannotJudge) {
		t.Errorf("%s: %q -> %q: violations = %v, want one refusal", path, oldSrc, newSrc, v)
	}
}

// TestCodeHiddenBehindAnUnmodelledTokenIsRefused pins the round's rule: a
// token the lexer does not model must never turn a code change into a
// comment-only pass. The first five rows are the probes the MasterMind ran
// against round 1, all of which passed there; the rest are one probe per
// token form the round-2 audit found, where the marker hides inside the
// form or the form leaves the lexer unsure.
func TestCodeHiddenBehindAnUnmodelledTokenIsRefused(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ path, old, new string }{
		// The MasterMind's five probes: good -> evil behind a token the
		// round-1 lexer read as a comment.
		{"a.js", "const r = /\\//; good();", "const r = /\\//; evil();"},
		{"a.ts", "const r = /[//]/; good();", "const r = /[//]/; evil();"},
		{"a.sql", `select "a--b", good from t;`, `select "a--b", evil from t;`},
		{"a.sql", "select `a--b`, good from t;", "select `a--b`, evil from t;"},
		{"a.cpp", `auto s = R"(a"//)"; good();`, `auto s = R"(a"//)"; evil();`},
		// The audit's forms, one probe each.
		{"a.js", "const r = /a[/*]b/; good();", "const r = /a[/*]b/; evil();"},
		{"a.js", "function f() { return /\\/\\//; good(); }", "function f() { return /\\/\\//; evil(); }"},
		{"a.js", "const t = `${f(`a//good`)}`;", "const t = `${f(`a//evil`)}`;"},
		{"a.js", "x = 1;\n", "x = /abc\n"},
		{"a.js", "(a) / b // one\ngood();\n", "(a) / b // one\nevil();\n"},
		{"a.js", "{ a: 1 } / y /* z */\ngood();\n", "{ a: 1 } / y /* z */\nevil();\n"},
		{"a.sql", "select [a--b], good from t;", "select [a--b], evil from t;"},
		{"a.sql", "x = 1\n", "select \"abc\n"},
		{"a.cpp", "auto a = u8R\"(x//y)\"; good();", "auto a = u8R\"(x//y)\"; evil();"},
		{"a.cpp", "auto b = uR\"(s//t)\"; good();", "auto b = uR\"(s//t)\"; evil();"},
		{"a.cpp", "auto c = UR\"(s//t)\"; good();", "auto c = UR\"(s//t)\"; evil();"},
		{"a.cpp", "auto d = LR\"delim(u//v)delim\"; good();", "auto d = LR\"delim(u//v)delim\"; evil();"},
		{"a.cpp", "x = 1;\n", "auto s = R\"abc\n"},
		{"a.r", "x <- r\"(a # b)\"\ngood()", "x <- r\"(a # b)\"\nevil()"},
		{"a.r", "x <- r\"--(a # b)--\"\ngood()", "x <- r\"--(a # b)--\"\nevil()"},
		{"a.r", "x <- r\"{a # b}\"\ngood()", "x <- r\"{a # b}\"\nevil()"},
		{"a.kt", "val s = \"a ${f(\"a//good\")}\"", "val s = \"a ${f(\"a//evil\")}\""},
		{"a.scala", "val s = s\"a ${f(\"a//good\")} b\"", "val s = s\"a ${f(\"a//evil\")} b\""},
		{"a.scala", "val x = <a>good // b</a>", "val x = <a>evil // b</a>"},
		{"a.scala", "x = 1\n", "val x = <a>one</a>\n"},
		{"a.swift", "let s = \"a \\(f(\"a//good\")) b\"", "let s = \"a \\(f(\"a//evil\")) b\""},
		{"a.swift", "let s = #\"a \\#(f(\"a//good\")) b\"#", "let s = #\"a \\#(f(\"a//evil\")) b\"#"},
		{"a.dart", "var s = 'a ${f(\"a//good\")} b';", "var s = 'a ${f(\"a//evil\")} b';"},
		{"a.cs", "var s = $\"a {f(\"a//good\")} b\";", "var s = $\"a {f(\"a//evil\")} b\";"},
		// Round 3's residuals, one probe each: good -> evil behind the
		// ambiguity the trigger refuses.
		{"a.sql", "select 1--2, good from t;", "select 1--2, evil from t;"},
		{"a.js", "f(.../[a//b]/); good();", "f(.../[a//b]/); evil();"},
		{"a.scala", "if (c) <a>\n// x\n</a>\ngood()\n", "if (c) <a>\n// x\n</a>\nevil()\n"},
	} {
		refuseReason(t, tt.path, tt.old, tt.new, judgeFile(t, tt.path, tt.old, tt.new))
	}
}

// TestCommentEditNextToARegexOrQuotedIdentifierPasses pins that the new
// token forms do not swallow the comments next to them: a genuine
// comment-only edit after a division, after a regex literal and after a
// quoted identifier stays in scope.
func TestCommentEditNextToARegexOrQuotedIdentifierPasses(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ path, old, new string }{
		{"a.js", "x = a / b; // one\n", "x = a / b; // two\n"},
		{"a.js", "const r = /\\//; // one\n", "const r = /\\//; // two\n"},
		{"a.js", "const r = /[//]/; // one\n", "const r = /[//]/; // two\n"},
		{"a.sql", "select \"a--b\" -- one\n", "select \"a--b\" -- two\n"},
		{"a.cpp", "auto s = R\"(a//b)\"; // one\n", "auto s = R\"(a//b)\"; // two\n"},
		{"a.scala", "val s = s\"a ${f(\"x\")} b\" // one\n", "val s = s\"a ${f(\"x\")} b\" // two\n"},
		// Round 3's triggers stay narrow: the commented edit beside them
		// passes on both.
		{"a.sql", "x = 1 -- one\n", "x = 1 -- two\n"},
		{"a.sql", "-- one\n", "-- two\n"},
		{"a.scala", "if (a) < b // one\n", "if (a) < b // two\n"},
	} {
		if v := judgeFile(t, tt.path, tt.old, tt.new); len(v) != 0 {
			t.Errorf("%s: %q -> %q: refused: %v", tt.path, tt.old, tt.new, v)
		}
	}
}
