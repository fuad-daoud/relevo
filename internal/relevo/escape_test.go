package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestEscapeOutcome pins escapeOutcome over all eight input combinations
// (#192). It is the mutation target: flip which input the function looks at
// and this must catch it.
func TestEscapeOutcome(t *testing.T) {
	t.Parallel()

	cases := []struct {
		treeUnchanged, repoDirty, hasReport bool
		want                                EscapeOutcome
	}{
		{false, false, false, EscapeNone},
		{false, false, true, EscapeNone},
		{false, true, false, EscapeNone},
		{false, true, true, EscapeNone},
		{true, false, false, EscapeNone},
		{true, false, true, EscapeNone},
		{true, true, false, EscapeHalt},
		{true, true, true, EscapeNote},
	}
	for _, c := range cases {
		got := escapeOutcome(c.treeUnchanged, c.repoDirty, c.hasReport)
		if got != c.want {
			t.Errorf("escapeOutcome(treeUnchanged=%v, repoDirty=%v, hasReport=%v) = %v, want %v",
				c.treeUnchanged, c.repoDirty, c.hasReport, got, c.want)
		}
	}
}

// TestEscapeApplies pins the pure precondition behind escapeCheck (#192).
// Build the "true" base case (local headless binding with Repo and baseline)
// once and derive all "false" cases from it by changing exactly one field.
func TestEscapeApplies(t *testing.T) {
	t.Parallel()

	// base: local headless binding with Repo and RoundBaselineTree -> true
	base := store.Binding{
		Builder:           store.Endpoint{Mode: store.ModeHeadless},
		Repo:              "/src/myrepo",
		RoundBaselineTree: "abc123",
		Serve:             nil,
	}

	cases := []struct {
		name string
		b    store.Binding
		want bool
	}{
		{
			name: "local headless with repo and baseline",
			b:    base,
			want: true,
		},
		{
			name: "pane builder (not headless)",
			b: func() store.Binding {
				b := base
				b.Builder = store.Endpoint{} // zero Mode is not ModeHeadless
				return b
			}(),
			want: false,
		},
		{
			name: "Repo empty",
			b: func() store.Binding {
				b := base
				b.Repo = ""
				return b
			}(),
			want: false,
		},
		{
			name: "RoundBaselineTree empty",
			b: func() store.Binding {
				b := base
				b.RoundBaselineTree = ""
				return b
			}(),
			want: false,
		},
		{
			name: "served binding (Serve != nil)",
			b: func() store.Binding {
				b := base
				b.Serve = &store.ServeFacts{RepoID: "x", BareRepo: "/tmp/x.git"}
				return b
			}(),
			want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := escapeApplies(c.b)
			if got != c.want {
				t.Errorf("escapeApplies() = %v, want %v", got, c.want)
			}
		})
	}
}
