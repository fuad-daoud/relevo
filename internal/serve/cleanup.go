package serve

import (
	"context"
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
// ackedGrace or quiet for settledGrace. now is a parameter so tests can pin the
// boundary.
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

func releaseServedRefs(ctx context.Context, rt relevo.Runtime, b store.Binding) error {
	if b.Serve == nil || b.Serve.BareRepo == "" {
		return nil
	}
	bare := b.Serve.BareRepo
	if err := rt.Git.DeleteBranch(ctx, bare, b.Branch); err != nil {
		return err
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
	if err := rt.Git.RemoveWorktree(ctx, bare, b.Worktree, true); err != nil {
		return err
	}
	return releaseServedRefs(ctx, rt, b)
}

// unusedRepos returns the entries of repos that no live binding references: a
// binding references p when its Serve.BareRepo (or Repo) cleans to p, in order.
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

// pruneUnusedRepos deletes every bare repo no live binding references. The
// caller holds s.mu.
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
		rt := s.runtimeAt(ownerBindingsDir)

		live, err := rt.Store.List()
		if err != nil {
			slog.Warn("prune unused repos: list owner bindings failed", "owner", id, "err", err)
			continue
		}

		repoEntries, err := os.ReadDir(ownerRepoDir)
		if err != nil {
			slog.Warn("prune unused repos: read owner repo dir failed", "owner", id, "err", err)
			continue
		}

		var ownerRepos []string
		for _, re := range repoEntries {
			if re.IsDir() && strings.HasSuffix(re.Name(), ".git") {
				ownerRepos = append(ownerRepos, filepath.Join(ownerRepoDir, re.Name()))
			}
		}

		for _, path := range unusedRepos(ownerRepos, live) {
			if err := os.RemoveAll(path); err != nil {
				slog.Warn("prune unused bare repo failed", "owner", id, "repo", filepath.Base(path), "err", err)
				continue
			}
			slog.Info("pruned unused bare repo", "owner", id, "repo", filepath.Base(path))
			pruned++
		}

		_ = os.Remove(ownerRepoDir)
	}

	return pruned
}
