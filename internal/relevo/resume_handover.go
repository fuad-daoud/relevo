package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/store"
)

// resumeLoad loads the binding a resume names. A name this machine does not
// write is refused with the sentence that says where it is, instead of the bare
// not-found, which would send the owner to bind a new one.
func resumeLoad(rt Runtime, name string) (store.Binding, error) {
	b, err := rt.Store.Load(name)
	if err == nil || !errors.Is(err, store.ErrNotFound) {
		return b, err
	}
	d, derr := rt.Store.DB()
	if derr != nil {
		return b, err
	}
	_, _, writer, found, ferr := d.ForeignRoots(rt.Store.Owner(), name)
	if ferr != nil {
		return b, err
	}
	if rerr := ResumeRefusal(ResumeFacts{Name: name, ForeignWriter: foreignWriter(found, writer)}); rerr != nil {
		return store.Binding{}, rerr
	}
	return b, err
}

func foreignWriter(found bool, writer string) string {
	if !found {
		return ""
	}
	return writer
}

// handedToMe reports whether this machine writes the binding but did not create
// it, which is what a hand-over leaves on the target.
func handedToMe(rt Runtime, name string) (bool, error) {
	d, err := rt.Store.DB()
	if err != nil {
		return false, err
	}
	_, creator, writer, found, err := d.ForeignRoots(rt.Store.Owner(), name)
	if err != nil || !found {
		return false, err
	}
	return writer == d.Origin() && creator != d.Origin() && creator != "", nil
}

// carriesOldPaths is true while a handed-over record still holds the paths of
// the machine that created it, which is what makes the first resume the one
// that has to cut this machine's own worktree.
func carriesOldPaths(rt Runtime, b store.Binding) bool {
	if b.Worktree != "" {
		return b.Worktree != rt.Store.WorktreePath(b.Name)
	}
	_, err := os.Stat(b.CWD)
	return err != nil
}

// resumeHandedOver prepares a handed-over binding for the first resume on its
// new machine. MasterMind and MasterMindID are set by resume itself right after,
// from the caller; binding_record.owner is the serve tenant, "" on a local
// machine, and is not touched, so the record stays visible to the resuming
// MasterMind.
func resumeHandedOver(ctx context.Context, rt Runtime, opts BindOptions, b store.Binding) (store.Binding, error) {
	if rt.Git == nil {
		return store.Binding{}, ErrGitRequired
	}
	originURL, commonDir, err := rt.Git.RepoFacts(ctx, opts.CWD)
	if err != nil {
		return store.Binding{}, ResumeCheckoutRefusal(b.Name, b.RepoRef.OriginURL, "")
	}
	local := git.NormalizeOriginURL(originURL)
	if !SameRepo(b.RepoRef.OriginURL, local) {
		return store.Binding{}, ResumeCheckoutRefusal(b.Name, b.RepoRef.OriginURL, local)
	}
	if b.Branch == "" {
		return store.Binding{}, ResumeBranchRefusal(b.Name, "(none recorded)")
	}
	if err := rt.Git.Fetch(ctx, opts.CWD, "origin", b.Branch); err != nil {
		return store.Binding{}, fmt.Errorf("%w: %w", ResumeBranchRefusal(b.Name, b.Branch), err)
	}
	if _, ok, err := rt.Git.RefSHA(ctx, opts.CWD, "refs/remotes/origin/"+b.Branch); err != nil || !ok {
		return store.Binding{}, ResumeBranchRefusal(b.Name, b.Branch)
	}
	if exists, err := rt.Git.BranchExists(ctx, opts.CWD, b.Branch); err != nil {
		return store.Binding{}, err
	} else if !exists {
		if err := rt.Git.CreateTrackingBranch(ctx, opts.CWD, b.Branch, "origin/"+b.Branch); err != nil {
			return store.Binding{}, err
		}
	}
	if b.Worktree != "" {
		b.Worktree = rt.Store.WorktreePath(b.Name)
		b.CWD = b.Worktree
	} else {
		b.CWD = opts.CWD
	}
	b.RepoRef = &store.RepoRef{OriginURL: local, CommonDir: commonDir}
	if err := rt.Store.Save(b); err != nil {
		return store.Binding{}, err
	}
	return b, repointMirror(rt, b.Name, local, commonDir)
}

func repointMirror(rt Runtime, name, url, commonDir string) error {
	d, err := rt.Store.DB()
	if err != nil {
		return err
	}
	return d.Tx(func(t *db.Tx) error {
		r := db.Repo{OriginURL: &url}
		if commonDir != "" {
			r.CommonDir = &commonDir
		}
		id, err := t.UpsertRepo(r)
		if err != nil {
			return err
		}
		return t.RepointMirror(name, id)
	})
}
