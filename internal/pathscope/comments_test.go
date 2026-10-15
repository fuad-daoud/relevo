package pathscope

import (
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

// TestLex pins one case per string form in the table, plus heredocs, YAML
// block scalars, Lua long brackets, nested Rust block comments and the
// Haskell -- operator: each row's comment markers inside strings are code,
// so the only comment spans are the real comments in want.
func TestLex(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
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
	} {
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
	}
	for ext, l := range extTable {
		for _, src := range inputs {
			out := lex(l, []byte(src))
			if out.err == errStuck {
				t.Errorf("%s lex %q: %v", ext, src, out.err)
			}
		}
	}
}
