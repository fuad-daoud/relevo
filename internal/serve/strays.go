package serve

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
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

// fillOwnerStrays sets each owner's strays.
func fillOwnerStrays(ctx context.Context, s *Server, owners []OwnerStatus) error {
	now := s.cfg.Now()
	for i := range owners {
		rt, err := s.runtime(owners[i].Owner)
		if err != nil {
			return err
		}
		owners[i].Strays, err = listOwnerStrays(ctx, rt, strayOwnerRef(s, owners[i].Owner), now)
		if err != nil {
			return err
		}
	}
	return nil
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
		// A file or symlink is no worktree, and RemoveStray refuses it, so
		// listing one would print a command that can never work.
		if !info.IsDir() {
			continue
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

// The removal refusals: a live binding still owns the entry, the entry is too
// young to tell apart from a bind in progress, or the entry is not a
// directory at all. Each is a distinct sentinel so the verb can report every
// refusal without inventing a code.
var (
	ErrStrayOwned  = errors.New("stray is owned by a live binding")
	ErrStrayFresh  = errors.New("stray is too fresh to remove")
	ErrStrayNotDir = errors.New("stray is not a directory")
)

// RemovedStray is the entry RemoveStray cleared: the reason and size the
// listing reported, measured before the removal, so the verb prints the same
// facts the listing showed.
type RemovedStray struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Reason string `json:"reason"`
	Size   int64  `json:"size"`
}

// RemoveStray clears one stray worktree on the owner's explicit command. The
// refusals rerun every skip the listing applied, in listing order, so a
// binding created after the listing still owns its tree: unknown owner, an
// invalid name, a live-owned name or worktree path, a missing entry, an entry
// that is not a directory, and an entry too young to tell apart from a bind
// still cutting its tree. A tree git recognises goes through git's own
// removal from inside the tree, which also clears git's registration; a tree
// git does not recognise has no registration left and goes through the
// .worktrees root, which refuses a symlink planted out of it. A git failure
// is reported as is, never retried as a plain removal.
func RemoveStray(ctx context.Context, s *Server, owner, name string) (RemovedStray, error) {
	id, err := s.resolveOwner(owner)
	if err != nil {
		return RemovedStray{}, err
	}
	if err := store.ValidName(name); err != nil {
		return RemovedStray{}, err
	}
	rt, err := s.runtime(id)
	if err != nil {
		return RemovedStray{}, err
	}
	path, err := strayRemovalEntry(rt, name, s.cfg.Now())
	if err != nil {
		return RemovedStray{}, err
	}
	removed := RemovedStray{
		Name:   name,
		Path:   path,
		Reason: classifyStray(ctx, rt.Git, path),
		Size:   straySize(path),
	}
	if removed.Reason == strayClean || removed.Reason == strayDirty {
		if err := rt.Git.RemoveWorktree(ctx, path, path, true); err != nil {
			return RemovedStray{}, err
		}
	} else if err := removeUnrecognisedStray(rt.Store, name); err != nil {
		return RemovedStray{}, err
	}
	rt.Store.PruneWorktreeDirs()
	return removed, nil
}

// strayRemovalEntry rechecks the listing's skips against the live store: a
// name or worktree path a live row owns is refused, an archived record owning
// nothing. The shape check runs before the youth check so a planted entry is
// refused for what it is whatever its age: a fresh symlink is still a
// symlink, and only a link's own age (never its target's) is readable.
func strayRemovalEntry(rt relevo.Runtime, name string, now time.Time) (string, error) {
	if _, err := rt.Store.Load(name); err == nil {
		return "", fmt.Errorf("remove %s: %w", name, ErrStrayOwned)
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	path := filepath.Join(rt.Store.WorktreeDir(), name)
	live, err := rt.Store.List()
	if err != nil {
		return "", err
	}
	for _, b := range live {
		if b.Worktree == path {
			return "", fmt.Errorf("remove %s: %w", name, ErrStrayOwned)
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("remove %s: %w", name, store.ErrNotFound)
		}
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("remove %s: %w", name, ErrStrayNotDir)
	}
	if now.Sub(info.ModTime()) < strayFreshGuard {
		return "", fmt.Errorf("remove %s: %w", name, ErrStrayFresh)
	}
	return path, nil
}

// removeUnrecognisedStray deletes an entry git does not recognise through the
// .worktrees root, so a directory swapped for a symlink out of .worktrees
// leads nowhere: the removal refuses rather than deleting the target.
func removeUnrecognisedStray(st *store.Store, name string) error {
	root, err := st.WorktreeRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.RemoveAll(name)
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
