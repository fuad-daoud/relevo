package bugreport

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update", false, "update golden files")

// TestMarkdownGolden pins the whole document at a fixed clock: a change to a
// heading, a column or the header fails here rather than in a reader's issue.
func TestMarkdownGolden(t *testing.T) {
	got := Markdown(fixtureBundle())
	path := filepath.Join("testdata", "bundle.golden.md")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (run with -update)", path, err)
	}
	if !bytes.Equal([]byte(got), want) {
		t.Errorf("%s: rendered bytes differ from golden\n got:\n%s\nwant:\n%s", path, got, want)
	}
}

// TestJSONDocShape pins the document's key paths -- the contract a consumer
// reads -- and that an empty bundle still marshals an empty section list rather
// than null.
func TestJSONDocShape(t *testing.T) {
	raw, err := json.Marshal(fixtureBundle().Doc())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantTop := []string{"created", "privacy", "raw", "sections", "title", "version"}
	if got := mapKeys(doc); !slices.Equal(got, wantTop) {
		t.Errorf("document keys = %q, want %q", got, wantTop)
	}

	var sections []map[string]json.RawMessage
	if err := json.Unmarshal(doc["sections"], &sections); err != nil {
		t.Fatalf("unmarshal sections: %v", err)
	}
	if len(sections) == 0 {
		t.Fatal("the fixture bundle has no sections")
	}
	wantSection := []string{"columns", "name", "rows"}
	if got := mapKeys(sections[0]); !slices.Equal(got, wantSection) {
		t.Errorf("section keys = %q, want %q", got, wantSection)
	}

	empty, err := json.Marshal(Collect("t", "v", time.Now(), nil).Doc())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(empty, []byte(`"sections":[]`)) {
		t.Errorf("an empty bundle marshals sections as %s, want []", empty)
	}
}

// TestTitle pins the header rule: the failing code and verb when one is
// recorded, the plain name otherwise.
func TestTitle(t *testing.T) {
	failure := LastError{Code: "internal", Verb: "send"}
	cases := []struct {
		name string
		e    LastError
		ok   bool
		want string
	}{
		{"a recorded failure names the code and verb", failure, true, "relevo 0.4.2: internal in send"},
		{"no recorded failure names the bundle", LastError{}, false, "relevo 0.4.2: bug report"},
		{"a record with no code names the bundle", LastError{Verb: "send"}, true, "relevo 0.4.2: bug report"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Title("0.4.2", tc.e, tc.ok); got != tc.want {
				t.Errorf("Title = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestMarkdownOmitsWithOneLine pins the shape of an omitted section and of an
// omission: both are the one line a reader can grep for.
func TestMarkdownOmitsWithOneLine(t *testing.T) {
	b := Bundle{
		Title:   "t",
		Created: fixedNow,
		Sections: []Section{
			{Name: SectionJournal, Omitted: "not available on darwin"},
			{Name: SectionStatus, Lines: []string{"nothing yet"}},
		},
		Omissions: []Omission{{Section: SectionGates, Reason: "no ledger"}},
	}
	md := Markdown(b)
	for _, want := range []string{
		"journal: omitted: not available on darwin",
		"gates: omitted: no ledger",
		"## Status",
		"nothing yet",
	} {
		if !bytes.Contains([]byte(md), []byte(want)) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

// mapKeys is one object's keys, sorted, so a shape assertion reads as one list.
func mapKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
