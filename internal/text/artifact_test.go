package text_test

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/text"
)

// TestArtifactClause pins the artifact clause of a close payload: a writer
// always gets the literal word "Report" with --report, a reader gets its
// agent's output label (title-cased) with --output, and an empty label falls
// back to "Notes".
func TestArtifactClause(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		isReader bool
		output   string
		bind     string
		want     string
	}{
		{
			name:     "writer keeps the report word",
			isReader: false, output: "report", bind: "webshop",
			want: "Report: relevo show webshop --round 3 --report",
		},
		{
			name:     "reader names its agent's output label",
			isReader: true, output: "plan", bind: "architect-bind",
			want: "Plan: relevo show architect-bind --round 3 --output",
		},
		{
			name:     "reader with no output label names notes",
			isReader: true, output: "", bind: "architect-bind",
			want: "Notes: relevo show architect-bind --round 3 --output",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := text.ArtifactClause(c.isReader, c.output, c.bind, 3); got != c.want {
				t.Errorf("ArtifactClause = %q, want %q", got, c.want)
			}
		})
	}
}
