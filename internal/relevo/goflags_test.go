package relevo

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestGoFlagsEntry pins the whole table the seam names: entry added, entry
// appended, entry already carried (every "=..." form included), the opt-out,
// and the Go-tree guard. Each case holds the parent's other entries in want
// too, so an overwrite that drops a value fails and not just a loss of the
// append.
func TestGoFlagsEntry(t *testing.T) {
	t.Parallel()

	goMod := tempTreeWith(t, "go.mod")
	goWork := tempTreeWith(t, "go.work")
	plain := t.TempDir()

	cases := []struct {
		name   string
		parent []string
		tree   string
		on     bool
		want   []string
	}{
		{"no GOFLAGS yet", nil, goMod, true, []string{"GOFLAGS=-trimpath"}},
		{"appended to an existing GOFLAGS", []string{"GOFLAGS=-mod=vendor"}, goMod, true, []string{"GOFLAGS=-mod=vendor -trimpath"}},
		{"the parent's other entries survive", []string{"PATH=/p", "GOFLAGS=-mod=vendor", "GOOS=linux"}, goMod, true, []string{"PATH=/p", "GOFLAGS=-mod=vendor -trimpath", "GOOS=linux"}},
		{"carried already", []string{"GOFLAGS=-trimpath"}, goMod, true, []string{"GOFLAGS=-trimpath"}},
		{"carried as =true", []string{"GOFLAGS=-trimpath=true"}, goMod, true, []string{"GOFLAGS=-trimpath=true"}},
		{"carried as =false", []string{"GOFLAGS=-trimpath=false"}, goMod, true, []string{"GOFLAGS=-trimpath=false"}},
		{"an empty GOFLAGS value is filled", []string{"GOFLAGS="}, goMod, true, []string{"GOFLAGS=-trimpath"}},
		{"not a go tree", nil, plain, true, nil},
		{"setting off", nil, goMod, false, nil},
		{"setting off beside an existing GOFLAGS", []string{"GOFLAGS=-mod=vendor"}, goMod, false, []string{"GOFLAGS=-mod=vendor"}},
		{"go.work only", nil, goWork, true, []string{"GOFLAGS=-trimpath"}},
	}
	for _, tc := range cases {
		got := goFlagsEntry(tc.parent, tc.tree, tc.on)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: goFlagsEntry = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// tempTreeWith is a tree whose root carries name, the file that makes a tree
func tempTreeWith(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return dir
}

// TestRepoSettingsTrimpathOn pins the effective value off one method: unset
// and on both read on, and only the stored refusal opts a repository out.
func TestRepoSettingsTrimpathOn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		stored string
		want   bool
	}{
		{"", true},
		{"on", true},
		{"off", false},
	} {
		if got := (RepoSettings{Trimpath: tc.stored}).TrimpathOn(); got != tc.want {
			t.Errorf("TrimpathOn(%q) = %v, want %v", tc.stored, got, tc.want)
		}
	}
}
