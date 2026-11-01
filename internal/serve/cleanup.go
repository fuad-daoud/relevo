package serve

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// settledGrace is how long a DONE binding the owner never acked is left alone
// before the server collects it: an unacked result is no proof the client
// absorbed it, and seven days is longer than any live mastermind reads it.
const settledGrace = 7 * 24 * time.Hour

// ackedGrace is how long an all-acked DONE binding stays before it is collected:
// a client that has just run `relevo done` reads the binding's view right after,
// and must not get a 404 because the server's next tick collected it.
const ackedGrace = time.Hour

// settled reports whether a served binding may be collected: DONE with Serve
// facts, no builder process, consult or gate, and either acked and quiet for
// ackedGrace or quiet for settledGrace.
func settled(b store.Binding, now time.Time) bool {
	if b.State != store.StateDone || b.Serve == nil {
		return false
	}
	if b.Builder.PID != 0 || b.GateRun != nil {
		return false
	}
	for _, c := range b.Consults {
		if c.State == store.ConsultSpawning || c.State == store.ConsultRunning {
			return false
		}
	}
	if b.Serve.AckedRound >= b.Serve.ClosedRound {
		return now.Sub(b.UpdatedAt) >= ackedGrace
	}
	return now.Sub(b.UpdatedAt) >= settledGrace
}

// collectSettled archives and releases every settled served binding, per
// binding: force-remove the worktree, delete the branch and refs/relevo/<name>/*
// refs, then archive the record. If teardown fails the archive does not run, so
// a failed cleanup is retried next tick. An error skips that binding.
func (s *Server) collectSettled(ctx context.Context) (int, error) {
	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	now := s.cfg.Now()
	collected := 0

	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			slog.Warn("unexpected entry in bindings dir", "entry", entry.Name())
			continue
		}
		ownerPath := filepath.Join(bindingsDir, entry.Name())
		rt := s.runtimeAt(ownerPath)

		bindings, err := rt.Store.List()
		if err != nil {
			slog.Warn("collect settled: list owner bindings failed", "owner", id, "err", err)
			continue
		}

		for _, b := range bindings {
			if !settled(b, now) {
				continue
			}

			if worktreeDirtyOrUnknown(ctx, rt, b.Worktree) {
				slog.Debug("collect settled: worktree is dirty or check errored, skipping", "owner", id, "binding", b.Name)
				continue
			}

			if err := teardownServed(ctx, rt, b); err != nil {
				slog.Warn("collect settled binding failed", "owner", id, "binding", b.Name, "err", err)
				continue
			}

			if _, err := relevo.Unbind(ctx, rt, b.Name, true); err != nil {
				slog.Warn("collect settled binding failed", "owner", id, "binding", b.Name, "err", err)
				continue
			}

			slog.Info("collected settled binding", "owner", id, "binding", b.Name)
			collected++
		}
	}

	return collected, nil
}

// worktreeDirtyOrUnknown reports whether wt must stop a settled binding from
// being collected: it exists on disk and is dirty, or the check itself failed.
// A missing worktree is collectable, as is a clean one. A check that cannot be
// trusted is treated like a dirty tree, because the removal this guards is
// forced and cannot undo a lost uncommitted change.
func worktreeDirtyOrUnknown(ctx context.Context, rt relevo.Runtime, wt string) bool {
	if wt == "" {
		return false
	}
	if _, err := os.Lstat(wt); errors.Is(err, os.ErrNotExist) {
		return false
	}
	dirty, err := rt.Git.Dirty(ctx, wt)
	if err != nil {
		slog.Debug("collect settled: worktree dirty check failed", "worktree", wt, "err", err)
		return true
	}
	return dirty
}

func releaseServedRefs(ctx context.Context, rt relevo.Runtime, b store.Binding) error {
	if b.Serve == nil || b.Serve.BareRepo == "" {
		return nil
	}
	bare := b.Serve.BareRepo
	// A branchless reader (a chain reader that shares the builder's tree) has no
	// branch of its own; its refs/relevo/<name>/* are still its own to drop.
	if b.Branch != "" {
		if err := rt.Git.DeleteBranch(ctx, bare, b.Branch); err != nil {
			return err
		}
	}
	refs, err := rt.Git.ListRefs(ctx, bare, "refs/relevo/"+b.Name+"/")
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if err := rt.Git.DeleteRef(ctx, bare, ref); err != nil {
			return err
		}
	}
	return nil
}

func teardownServed(ctx context.Context, rt relevo.Runtime, b store.Binding) error {
	if b.Serve == nil || b.Serve.BareRepo == "" {
		return nil
	}
	bare := b.Serve.BareRepo
	// A branchless reader names no worktree: removing the empty path would ask
	// git to remove something -- and the builder's tree is not the reader's to
	// remove.
	if b.Worktree != "" {
		if err := rt.Git.RemoveWorktree(ctx, bare, b.Worktree, true); err != nil {
			return err
		}
	}
	return releaseServedRefs(ctx, rt, b)
}

// unusedRepos returns the entries of repos that no live binding references.
func unusedRepos(repos []string, live []store.Binding) []string {
	var unused []string
	for _, p := range repos {
		cleanP := filepath.Clean(p)
		referenced := false
		for _, b := range live {
			var ref string
			if b.Serve != nil {
				ref = b.Serve.BareRepo
			} else {
				ref = b.Repo
			}
			if ref != "" && filepath.Clean(ref) == cleanP {
				referenced = true
				break
			}
		}
		if !referenced {
			unused = append(unused, p)
		}
	}
	return unused
}

// pruneUnusedRepos deletes every bare repo no live binding references. It runs
// under the tick's admitMu hold, so the live set it reads is the post-collect
// one. Each owner's pass runs under the bare-repo pin mutex; see
// pruneOwnerRepos for why the pass and the pins share that mutex.
func (s *Server) pruneUnusedRepos(ctx context.Context) int {
	reposDir := filepath.Join(s.cfg.Root, "repos")
	entries, err := os.ReadDir(reposDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		slog.Warn("prune unused repos: read repos dir failed", "err", err)
		return 0
	}

	pruned := 0
	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			slog.Warn("unexpected entry in repos dir", "entry", entry.Name())
			continue
		}

		ownerRepoDir := filepath.Join(reposDir, entry.Name())
		ownerBindingsDir := filepath.Join(s.cfg.Root, "bindings", entry.Name())
		pruned += s.pruneOwnerRepos(id, ownerRepoDir, s.runtimeAt(ownerBindingsDir))
	}

	return pruned
}

// pruneOwnerRepos is one owner's prune pass: list the rows, apply unusedRepos,
// spare every pinned path, delete the rest, and remove the owner's repo dir
// when it emptied. It holds the bare-repo pin mutex across the whole pass, so a
// bind cannot slip in between the row read and the delete: a bind pins the bare
// repo before it creates or absorbs into it and unpins only after the row that
// references it is saved, so the pass sees either the pin or the row -- never
// neither -- and a repo a bind is mid-way into survives even though no row
// names it yet. The mutex is a leaf: nothing below it waits on another server
// lock, so a pin never queues a bind behind a round start and a pass never
// queues one behind an owner section.
func (s *Server) pruneOwnerRepos(id remote.ClientID, ownerRepoDir string, rt relevo.Runtime) int {
	barePins.Lock()
	defer barePins.Unlock()

	live, err := rt.Store.List()
	if err != nil {
		slog.Warn("prune unused repos: list owner bindings failed", "owner", id, "err", err)
		return 0
	}

	repoEntries, err := os.ReadDir(ownerRepoDir)
	if err != nil {
		slog.Warn("prune unused repos: read owner repo dir failed", "owner", id, "err", err)
		return 0
	}

	var ownerRepos []string
	for _, re := range repoEntries {
		if re.IsDir() && strings.HasSuffix(re.Name(), ".git") {
			ownerRepos = append(ownerRepos, filepath.Join(ownerRepoDir, re.Name()))
		}
	}

	pruned := 0
	for _, path := range unusedRepos(ownerRepos, live) {
		if pinnedLocked(path) {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			slog.Warn("prune unused bare repo failed", "owner", id, "repo", filepath.Base(path), "err", err)
			continue
		}
		slog.Info("pruned unused bare repo", "owner", id, "repo", filepath.Base(path))
		pruned++
	}

	// A pin may name a repo not created yet: InitBare makes the owner dir
	// first and runs git init inside it, so removing that dir while it is
	// still empty would fail the bind the pin protects.
	if !pinnedUnderLocked(ownerRepoDir) {
		_ = os.Remove(ownerRepoDir)
	}
	return pruned
}
