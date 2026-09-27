package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestArtifactClause pins the artifact clause of a close payload: a writer
// always gets the literal word "Report" with --report, a reader gets its
// agent's output label (title-cased) with --summary, and an empty label falls
// back to "Notes".
func TestArtifactClause(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		shape  string
		output string
		bind   string
		want   string
	}{
		{
			name:  "writer keeps the report word",
			shape: store.ShapeWriter, output: "report", bind: "webshop",
			want: "Report: relevo show webshop --round 3 --report",
		},
		{
			name:  "reader names its agent's output label",
			shape: store.ShapeReader, output: "plan", bind: "architect-bind",
			want: "Plan: relevo show architect-bind --round 3 --summary",
		},
		{
			name:  "reader with no output label names notes",
			shape: store.ShapeReader, output: "", bind: "architect-bind",
			want: "Notes: relevo show architect-bind --round 3 --summary",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := artifactClause(c.shape, c.output, c.bind, 3); got != c.want {
				t.Errorf("artifactClause = %q, want %q", got, c.want)
			}
		})
	}
}

// TestCloseClauseResolvesTheActorLabel pins the label closeClause derives for
// a binding: a known reader role's shipped output, the report word for a
// writer, and "Notes" with no panic for a reader role the registry does not
// know.
func TestCloseClauseResolvesTheActorLabel(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	cases := []struct {
		name  string
		role  string
		shape string
		want  string
	}{
		{
			name: "reviewer names findings", role: "reviewer", shape: store.ShapeReader,
			want: "Findings: relevo show reader-bind --round 1 --summary",
		},
		{
			name: "researcher names notes", role: "researcher", shape: store.ShapeReader,
			want: "Notes: relevo show reader-bind --round 1 --summary",
		},
		{
			name: "writer keeps the report word", role: "", shape: store.ShapeWriter,
			want: "Report: relevo show reader-bind --round 1 --report",
		},
		{
			name: "unknown reader role falls back to notes", role: "mystery", shape: store.ShapeReader,
			want: "Notes: relevo show reader-bind --round 1 --summary",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := store.Binding{Name: "reader-bind", Role: c.role, Shape: c.shape}
			b.Builder.Kind = "claude"
			if got := closeClause(rt, b, 1); got != c.want {
				t.Errorf("closeClause = %q, want %q", got, c.want)
			}
		})
	}
}

// TestCatchUpPayloadNamesTheReaderSummary pins the catch-up payload's clause
// for a reader: the finished form names the summary under the agent's output
// label, and a stopped round adds the stopped line rather than the report word.
func TestCatchUpPayloadNamesTheReaderSummary(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	b := store.Binding{Name: "reader-bind", Role: "reviewer", Shape: store.ShapeReader}
	b.Builder.Kind = "claude"
	b.Builder.Server = "zen"
	view := remote.BindingView{ClosedRound: 1}
	clause := closeClause(rt, b, view.ClosedRound)

	payload, _ := catchUpPayload(b, view, true, clause)
	want := "The runner finished round 1 on zen. Findings: relevo show reader-bind --round 1 --summary"
	if payload != want {
		t.Errorf("finished payload = %q, want %q", payload, want)
	}

	view.Stopped = "killed"
	payload, _ = catchUpPayload(b, view, true, clause)
	want = "The runner was stopped (killed) for round 1 on zen. Findings: relevo show reader-bind --round 1 --summary"
	if payload != want {
		t.Errorf("stopped payload = %q, want %q", payload, want)
	}
}
