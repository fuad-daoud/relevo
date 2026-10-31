package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

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
			want: "Findings: relevo show reader-bind --round 1 --output",
		},
		{
			name: "researcher names notes", role: "researcher", shape: store.ShapeReader,
			want: "Notes: relevo show reader-bind --round 1 --output",
		},
		{
			name: "writer keeps the report word", role: "", shape: store.ShapeWriter,
			want: "Report: relevo show reader-bind --round 1 --report",
		},
		{
			name: "unknown reader role falls back to notes", role: "mystery", shape: store.ShapeReader,
			want: "Notes: relevo show reader-bind --round 1 --output",
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
	want := "The runner finished round 1 on zen. Findings: relevo show reader-bind --round 1 --output"
	if payload != want {
		t.Errorf("finished payload = %q, want %q", payload, want)
	}

	view.Stopped = "killed"
	payload, _ = catchUpPayload(b, view, true, clause)
	want = "The runner was stopped (killed) for round 1 on zen. Findings: relevo show reader-bind --round 1 --output"
	if payload != want {
		t.Errorf("stopped payload = %q, want %q", payload, want)
	}
}
