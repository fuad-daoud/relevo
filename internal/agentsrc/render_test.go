package agentsrc

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/harness"
)

var updateGolden = flag.Bool("update", false, "update golden files")

func TestRenderGolden(t *testing.T) {
	cases := []struct {
		fixture string
		src     Source
		kinds   []string
	}{
		{"writer", writerFixture(), []string{"claude", "opencode", "agy", "codex"}},
		{"reader", readerFixture(), []string{"claude", "agy"}},
	}
	for _, tc := range cases {
		for _, kind := range tc.kinds {
			got, err := Render(tc.src, kind)
			if err != nil {
				t.Fatalf("Render(%s, %s): %v", tc.fixture, kind, err)
			}
			path := filepath.Join("testdata", tc.fixture+"-"+kind+".golden")
			if *updateGolden {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s: %v (run with -update)", path, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s: rendered bytes differ from golden\n got:\n%s\nwant:\n%s", path, got, want)
			}
		}
	}
}

func TestRenderedKeysMatchShipped(t *testing.T) {
	s := writerFixture()
	for _, kind := range []string{"claude", "opencode", "agy"} {
		shipped, err := harness.AgentDoc("plan-executor", kind)
		if err != nil {
			t.Fatalf("AgentDoc(plan-executor, %s): %v", kind, err)
		}
		rendered, err := Render(s, kind)
		if err != nil {
			t.Fatalf("Render(writer, %s): %v", kind, err)
		}
		checkRenderedKeys(t, kind, shipped, rendered)
	}
	checkCodexRender(t, s)
}

func checkRenderedKeys(t *testing.T, kind string, shipped, rendered []byte) {
	t.Helper()
	shippedKeys := frontmatterKeys(t, shipped)
	renderedKeys := frontmatterKeys(t, rendered)
	switch kind {
	case "claude":
		if !reflect.DeepEqual(shippedKeys, renderedKeys) {
			t.Errorf("claude keys = %v, want the shipped keys %v", renderedKeys, shippedKeys)
		}
	case "agy":
		if !reflect.DeepEqual(shippedKeys, renderedKeys) {
			t.Errorf("agy keys = %v, want the shipped keys %v", renderedKeys, shippedKeys)
		}
		got := frontmatterListItems(t, rendered, "tools")
		want := frontmatterListItems(t, shipped, "tools")
		if !reflect.DeepEqual(got, want) {
			t.Errorf("agy tools = %v, want the shipped list in order %v", got, want)
		}
	case "opencode":
		for k := range shippedKeys {
			if !renderedKeys[k] {
				t.Errorf("opencode keys = %v, missing the shipped key %q", renderedKeys, k)
			}
		}
		if len(renderedKeys) != len(shippedKeys)+1 || !renderedKeys["name"] {
			t.Errorf("opencode keys = %v, want the shipped keys %v plus name", renderedKeys, shippedKeys)
		}
	}
}

func checkCodexRender(t *testing.T, s Source) {
	t.Helper()
	rendered, err := Render(s, "codex")
	if err != nil {
		t.Fatalf("Render(writer, codex): %v", err)
	}
	txt := string(rendered)
	const marker = "developer_instructions = '''\n"
	i := strings.Index(txt, marker)
	if i < 0 {
		t.Fatalf("codex render does not contain %q", marker)
	}
	if got := strings.Count(txt[i+len(marker):], "'''"); got != 1 {
		t.Errorf("codex render: closing ''' count after the marker = %d, want 1", got)
	}
	for _, want := range []string{"[agents.researcher]", `config_file = "researcher.config.toml"`} {
		if !strings.Contains(txt, want) {
			t.Errorf("codex render must contain %q", want)
		}
	}
}

func TestRenderShapeDoesNotChangeTools(t *testing.T) {
	for _, kind := range []string{"claude", "opencode", "agy", "codex"} {
		w := writerFixture()
		r := writerFixture()
		r.Shape = ShapeReader
		gw, err := Render(w, kind)
		if err != nil {
			t.Fatalf("Render(writer, %s): %v", kind, err)
		}
		gr, err := Render(r, kind)
		if err != nil {
			t.Fatalf("Render(reader, %s): %v", kind, err)
		}
		if !bytes.Equal(gw, gr) {
			t.Errorf("%s: shape changed the rendered bytes\nwriter:\n%s\nreader:\n%s", kind, gw, gr)
		}
	}
}

func TestRenderRefusesKindNotListed(t *testing.T) {
	s := readerFixture()
	for _, kind := range []string{"codex", "nosuch"} {
		out, err := Render(s, kind)
		if err == nil {
			t.Fatalf("Render(reader, %s) = %q, want an error", kind, out)
		}
		if !errors.Is(err, ErrBadSource) {
			t.Errorf("Render(reader, %s): error %v does not wrap ErrBadSource", kind, err)
		}
		if out != nil {
			t.Errorf("Render(reader, %s) returned bytes with an error: %q", kind, out)
		}
	}
}

func TestYamlScalar(t *testing.T) {
	plain := []string{
		"hello world",
		"Builds the feature described by the plan it is given.",
		"ui-designer",
		"Designs a page — with an em dash.",
		"C# developer",
		"a:b",
	}
	for _, s := range plain {
		if got := yamlScalar(s); got != s {
			t.Errorf("yamlScalar(%q) = %q, want unchanged", s, got)
		}
	}

	quoted := []string{
		"- leading dash",
		"has: a colon space",
		"yes",
		"42",
		" leading space",
		"`backtick",
		`"leading quote"`,
		"true",
		"null",
		"3.14",
	}
	for _, s := range quoted {
		got := yamlScalar(s)
		if got == s {
			t.Errorf("yamlScalar(%q) = %q, want a quoted string", s, got)
			continue
		}
		if len(got) < 2 || got[0] != '"' || got[len(got)-1] != '"' {
			t.Errorf("yamlScalar(%q) = %q, want a double-quoted string", s, got)
			continue
		}
		var back string
		if err := json.Unmarshal([]byte(got), &back); err != nil {
			t.Errorf("yamlScalar(%q) = %q, not valid JSON: %v", s, got, err)
			continue
		}
		if back != s {
			t.Errorf("yamlScalar(%q) round-trips to %q", s, back)
		}
	}
}

func frontmatterText(t *testing.T, doc []byte) string {
	t.Helper()
	s := string(doc)
	if !strings.HasPrefix(s, "---\n") {
		t.Fatalf("document does not open with a --- fence: %q", s)
	}
	rest := s[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		t.Fatal("document frontmatter never closes")
	}
	return rest[:end]
}

func frontmatterKeys(t *testing.T, doc []byte) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	for _, line := range strings.Split(frontmatterText(t, doc), "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		i := strings.Index(line, ":")
		if i < 0 {
			continue
		}
		keys[line[:i]] = true
	}
	return keys
}

func frontmatterListItems(t *testing.T, doc []byte, key string) []string {
	t.Helper()
	var out []string
	inList := false
	for _, line := range strings.Split(frontmatterText(t, doc), "\n") {
		if strings.TrimSpace(line) == key+":" {
			inList = true
			continue
		}
		if !inList {
			continue
		}
		item := strings.TrimSpace(line)
		if strings.HasPrefix(item, "- ") {
			out = append(out, strings.TrimPrefix(item, "- "))
			continue
		}
		inList = false
	}
	return out
}
