package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDBRelabelUsage pins the refusals that happen before the database is
// opened: no subcommand runs against a live db here.
func TestDBRelabelUsage(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.tsv")
	writeRelabelFile(t, ok, "id1\tauth\n")
	empty := filepath.Join(dir, "empty.tsv")
	writeRelabelFile(t, empty, "# nothing\n\nid\tfeature\n")
	bad := filepath.Join(dir, "bad.tsv")
	writeRelabelFile(t, bad, "id1 no tab\n")
	for name, args := range map[string][]string{
		"no source":             {"db", "relabel"},
		"binding without label": {"db", "relabel", "--binding", "x"},
		"feature without id":    {"db", "relabel", "--feature", "x"},
		"file and binding":      {"db", "relabel", "--file", ok, "--binding", "x", "--feature", "y"},
		"file and feature":      {"db", "relabel", "--file", ok, "--feature", "y"},
		"invalid label":         {"db", "relabel", "--binding", "x", "--feature", "bad/label"},
		"empty label":           {"db", "relabel", "--binding", "x", "--feature", ""},
		"missing file":          {"db", "relabel", "--file", filepath.Join(dir, "none.tsv")},
		"empty file":            {"db", "relabel", "--file", empty},
		"malformed file":        {"db", "relabel", "--file", bad},
		"unknown flag":          {"db", "relabel", "--bogus"},
		"positional":            {"db", "relabel", "--binding", "x", "--feature", "y", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := captureOutput(t, func() error { return run(args) })
			requireCLIError(t, err, codeUsage, "")
		})
	}
}

func TestDBRelabelFileParse(t *testing.T) {
	t.Run("accepts", func(t *testing.T) {
		in := "id\tfeature\r\n# comment\n\nA1\tauth\r\nB2\tmy feature\nA1\tauth\n"
		got, err := parseRelabelFile(in)
		if err != nil {
			t.Fatalf("parseRelabelFile: %v", err)
		}
		if len(got) != 2 || got[0].ID != "A1" || got[0].Feature != "auth" || got[0].Line != 4 ||
			got[1].ID != "B2" || got[1].Feature != "my feature" || got[1].Line != 5 {
			t.Errorf("entries = %+v, want A1/auth (line 4) and B2/my feature (line 5) with the repeat deduped", got)
		}
	})
	t.Run("names every offending line", func(t *testing.T) {
		in := "no tab here\nA1\tbad/label\nA2\tx\n# skipped\nA2\ty\nA3\t trailing \n\tnoid\n"
		_, err := parseRelabelFile(in)
		ce := requireCLIError(t, err, codeUsage, "")
		for _, want := range []string{"line 1", "line 2", "line 5", "line 6", "line 7"} {
			if !strings.Contains(ce.Error(), want) {
				t.Errorf("error %q does not name %s", ce.Error(), want)
			}
		}
		if strings.Contains(ce.Error(), "; line 3:") || strings.Contains(ce.Error(), "; line 4:") {
			t.Errorf("error %q names a good or skipped line", ce.Error())
		}
	})
}

func writeRelabelFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
