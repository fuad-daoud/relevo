package patch

import (
	"reflect"
	"testing"
)

func TestParseNumbersPostImage(t *testing.T) {
	p, err := Parse([]byte(fixtureTwoFiles))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, tt := range []struct {
		path string
		want []int
	}{
		{"x.go", []int{1, 0, 2, 3}},
		{"y.go", []int{0, 1}},
	} {
		if got := newValues(fileByPath(t, p, tt.path)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s New values = %v, want %v", tt.path, got, tt.want)
		}
	}

	if _, l, ok := p.HasLine("x.go", 3); !ok {
		t.Error(`HasLine("x.go", 3) = false, want true`)
	} else if l.Text != "more" {
		t.Errorf(`HasLine("x.go", 3) line text = %q, want "more"`, l.Text)
	}
	if _, _, ok := p.HasLine("x.go", 4); ok {
		t.Error(`HasLine("x.go", 4) = true, want false`)
	}
	if _, _, ok := p.HasLine("x.go", 0); ok {
		t.Error(`HasLine("x.go", 0) = true, want false`)
	}
	if _, _, ok := p.HasLine("z.go", 1); ok {
		t.Error(`HasLine("z.go", 1) = true, want false`)
	}
}

func TestParseCountMismatchIsAnError(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"at end of input", "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1,3 @@\n hello\n+world\n", "hunk at file.txt line 4: expected 3 new lines, found 2"},
		{"at the next file's header", "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1,2 @@\n hello\ndiff --git a/b.txt b/b.txt\n--- a/b.txt\n+++ b/b.txt\n@@ -1 +1 @@\n x\n", "hunk at a.txt line 4: expected 2 new lines, found 1"},
		{"at the next hunk's header", "diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1,2 @@\n hello\n@@ -5 +5 @@\n world\n", "hunk at a.txt line 4: expected 2 new lines, found 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.in))
			if err == nil || err.Error() != tt.want {
				t.Errorf("Parse error = %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := Annotate([]byte(tests[0].in)); err == nil {
		t.Error("Annotate: expected error for a hunk count mismatch, got nil")
	}
}

func TestParsePathEdgeCases(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"text before the first file is ignored", "stray preamble\n" + fixtureSingle, "file.txt"},
		{`a "diff --git" line with no " b/" marker falls back to the trimmed line`, "diff --git onlyfile\n@@ -1 +1 @@\n+x\n", "onlyfile"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Parse([]byte(tt.in))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(p.Files) != 1 || p.Files[0].Path != tt.want {
				t.Fatalf("Parse = %+v, want one file %q", p.Files, tt.want)
			}
		})
	}
}

func TestAnnotateGutter(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"single file", fixtureSingle, "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\nfile.txt:1  @@ -1 +1,2 @@\nfile.txt:1  hello\nfile.txt:2 +world\n"},
		{"two files", fixtureTwoFiles, "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\nx.go:1  @@ -1,2 +1,3 @@\nx.go:1  context\nx.go:-      -old\nx.go:2 +new\nx.go:3 +more\ndiff --git a/y.go b/y.go\ny.go:1  @@ -1 +1 @@\ny.go:-      -a\ny.go:1 +b\n"},
		{"widest gutter forces padding", "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n@@ -8,3 +8,3 @@\n eight\n nine\n ten\n", "diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\nf.txt:8   @@ -8,3 +8,3 @@\nf.txt:8   eight\nf.txt:9   nine\nf.txt:10  ten\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Annotate([]byte(tt.in))
			if err != nil {
				t.Fatalf("Annotate: %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("Annotate =\n%q\nwant\n%q", string(got), tt.want)
			}
		})
	}
}
