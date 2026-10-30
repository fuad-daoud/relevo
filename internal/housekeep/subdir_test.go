package housekeep

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestNormalizeSubdir(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, in, want string }{
		{"empty", "", ""},
		{"a plain segment", "src/api", "src/api"},
		{"git's trailing slash dropped", "src/", "src"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeSubdir(tc.in)
			if err != nil {
				t.Fatalf("NormalizeSubdir(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("NormalizeSubdir(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	refusals := []struct{ name, in string }{
		{"absolute", "/abs"},
		{"parent segment", "a/../b"},
		{"a dot segment", "a/./b"},
		{"the root itself", "/"},
	}
	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeSubdir(tc.in)
			if err == nil {
				t.Fatalf("NormalizeSubdir(%q) = %q, want a refusal", tc.in, got)
			}
		})
	}
}

// TestRoundDir is the join itself: the tree plus its subdirectory, and the
// tree alone when the binding has none. Returning CWD as-is is the mutation
// the round-tree tests catch too.
func TestRoundDir(t *testing.T) {
	t.Parallel()

	under := store.Binding{Name: "a", CWD: "/trees/api", Subdir: "src/api"}
	if got, want := RoundDir(under), "/trees/api/src/api"; got != want {
		t.Errorf("RoundDir = %q, want %q", got, want)
	}
	plain := store.Binding{Name: "a", CWD: "/trees/api"}
	if got, want := RoundDir(plain), "/trees/api"; got != want {
		t.Errorf("RoundDir without a subdir = %q, want the tree itself", got)
	}
}
