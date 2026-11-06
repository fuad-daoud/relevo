package serve

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// strayFreshGuard is how long a worktree outlives its missing record. A bind
// cuts the worktree before its row is saved, and a list reading between the
// two would take a live binding's tree for a stray.
const strayFreshGuard = 10 * time.Minute

// Stray reasons a serve status row carries. An unprovable tree reads as
// unrecognised: git may have lost the bare repo the tree points at, so the
// listing never calls such a tree clean.
const (
	strayClean        = "clean"
	strayDirty        = "dirty"
	strayUnrecognised = "git does not recognise it"
)

// StrayWorktree is one server worktree no live binding owns. Path is the
// server-local directory and Remove is the exact command that clears it;
// Size is advisory, a walk that never follows symlinks.
type StrayWorktree struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Size   int64  `json:"size"`
	Remove string `json:"remove"`
}

// listOwnerStrays reports every entry directly under the owner's worktree
// directory that no live binding owns. Ownership is the live rows alone, by
// binding name and by recorded worktree path: an archived record never comes
// back, so its tree is a stray like any other. Dot-prefixed entries are the
// reader's scratch and verify trees, never a binding's, and entries younger
// than the fresh guard may be a bind whose row has not landed yet. Nothing is
// removed; removal re-validates every skip.
func listOwnerStrays(ctx context.Context, rt relevo.Runtime, ownerRef string, now time.Time) ([]StrayWorktree, error) {
	dir := rt.Store.WorktreeDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	live, err := rt.Store.List()
	if err != nil {
		return nil, err
	}
	ownedNames := make(map[string]bool, len(live))
	ownedPaths := make(map[string]bool, len(live))
	for _, b := range live {
		ownedNames[b.Name] = true
		if b.Worktree != "" {
			ownedPaths[b.Worktree] = true
		}
	}
	var out []StrayWorktree
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(dir, name)
		if ownedNames[name] || ownedPaths[path] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if now.Sub(info.ModTime()) < strayFreshGuard {
			continue
		}
		out = append(out, StrayWorktree{
			Name:   name,
			Path:   path,
			Reason: classifyStray(ctx, rt.Git, path),
			Size:   straySize(path),
			Remove: fmt.Sprintf("relevo serve remove-stray --owner %s %s", ownerRef, name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// classifyStray names why path is reported. A missing .git, a .git that is
// not a file, and a dirty check git refuses all read as unrecognised: none of
// them proves a working tree, so none is ever called clean or dirty.
func classifyStray(ctx context.Context, g relevo.Git, path string) string {
	if fi, err := os.Lstat(filepath.Join(path, ".git")); err != nil || !fi.Mode().IsRegular() {
		return strayUnrecognised
	}
	dirty, err := g.Dirty(ctx, path)
	if err != nil {
		return strayUnrecognised
	}
	if dirty {
		return strayDirty
	}
	return strayClean
}

// straySize sums the regular files under root without following symlinks. It
// is advisory: an unreadable entry is skipped rather than failing the listing
// it decorates.
func straySize(root string) int64 {
	var size int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() {
			return nil
		}
		size += info.Size()
		return nil
	})
	return size
}

// strayOwnerRef is the --owner value the removal takes: the label when the
// owner carries one, else the full id, since the short fallback LabelOf
// returns does not resolve back to the owner.
func strayOwnerRef(s *Server, id remote.ClientID) string {
	for _, c := range s.clients.List() {
		if c.ID != id {
			continue
		}
		if c.Label != "" {
			return c.Label
		}
		return string(id)
	}
	return string(id)
}
