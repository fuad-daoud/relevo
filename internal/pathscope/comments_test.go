package pathscope

import (
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
