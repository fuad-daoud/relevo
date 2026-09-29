package patch

import "testing"

const fixtureSingle = "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1,2 @@\n hello\n+world\n"

const fixtureTwoFiles = "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,3 @@\n context\n-old\n+new\n+more\ndiff --git a/y.go b/y.go\n@@ -1 +1 @@\n-a\n+b\n"

func fileByPath(t *testing.T, p Patch, path string) File {
	t.Helper()
	for _, f := range p.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no file %q in patch (files: %v)", path, p.Files)
	return File{}
}

func newValues(f File) []int {
	var got []int
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			got = append(got, l.New)
		}
	}
	return got
}
