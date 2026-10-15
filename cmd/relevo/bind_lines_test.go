package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestAddedTreeLinesNamesTheRoundDirectory pins the added line's directory
// rule: a bind that provisioned a tree below the root names the directory the
// round actually runs in, one the mastermind can cd into; a bind from the
// tree root stays silent, and the mapped branch/worktree lines keep the
// provisioned facts.
func TestAddedTreeLinesNamesTheRoundDirectory(t *testing.T) {
	worktree := "/repo/.tree/webshop"
	base := relevo.AddResult{
		Binding: store.Binding{
			Name:     "webshop",
			CWD:      worktree,
			Worktree: worktree,
			Branch:   "relevo/webshop",
			Subdir:   "src/api",
		},
		Worktree: worktree,
		Branch:   "relevo/webshop",
		Base:     "1111111111111111111111111111111111111111",
	}
	lines := addedTreeLines(base)
	if want := []string{"  worktree /repo/.tree/webshop on relevo/webshop (from 1111111111111111111111111111111111111111)", "  round runs in /repo/.tree/webshop/src/api"}; !slices.Equal(lines, want) {
		t.Fatalf("lines = %#v, want %#v", lines, want)
	}

	plain := base
	plain.Binding.Subdir = ""
	if lines := addedTreeLines(plain); len(lines) != 1 || !strings.Contains(lines[0], "worktree /repo/.tree/webshop") {
		t.Fatalf("lines = %#v, want the worktree line only at the tree root", lines)
	}

	remote := base
	remote.Binding.Builder = store.Endpoint{Mode: store.ModeRemote, Server: "zen"}
	remote.Worktree = ""
	want := []string{"  branch relevo/webshop (from 1111111111111111111111111111111111111111)", "  round runs in /repo/.tree/webshop/src/api"}
	if lines := addedTreeLines(remote); !slices.Equal(lines, want) {
		t.Fatalf("remote lines = %#v, want %#v", lines, want)
	}
}
