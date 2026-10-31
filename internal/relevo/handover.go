package relevo

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/text"
)

// HandoverOptions names the binding, the machine it goes to and the remote its
// branch is pushed to.
type HandoverOptions struct {
	Name   string
	To     string
	Remote string
}

// HandoverResult is what a hand-over did.
type HandoverResult struct {
	Target string
	Branch string
	// Already is true when the binding had gone to Target before this call.
	Already         bool
	WorktreeRemoved string
	WorktreeKept    string
	KeptReason      string
}

// Handover gives a binding to another machine: it pushes the branch, releases a
// clean worktree, and flips the writer of both roots in one transaction. The
// push comes first, so a failed push changes no state and a rerun repeats it.
func Handover(ctx context.Context, rt Runtime, opts HandoverOptions) (HandoverResult, error) {
	d, err := rt.Store.DB()
	if err != nil {
		return HandoverResult{}, err
	}
	facts, b, err := gatherHandoverFacts(ctx, rt, d, opts)
	if err != nil {
		return HandoverResult{}, err
	}
	target, err := HandoverRefusal(facts)
	if err != nil {
		return HandoverResult{}, err
	}
	res := HandoverResult{Target: label(target.InstallationFact), Branch: b.Branch, Already: target.Already}
	if target.Already {
		return res, nil
	}
	dir := b.Worktree
	if dir == "" {
		dir = b.CWD
	}
	if err := rt.Git.Push(ctx, dir, opts.Remote, b.Branch, false); err != nil {
		return HandoverResult{}, err
	}
	outcome := worktreeTeardown(ctx, rt, b, false)
	res.WorktreeRemoved, res.WorktreeKept, res.KeptReason = outcome.Removed, outcome.Kept, outcome.Reason
	err = rt.Store.WithLock(func(tx *store.Tx) error {
		return d.Tx(func(t *db.Tx) error {
			_, err := t.HandOver(rt.Store.Owner(), opts.Name, target.ID, time.Now())
			return err
		})
	})
	if err != nil {
		return HandoverResult{}, err
	}
	if rt.SyncNow != nil {
		rt.SyncNow()
	}
	return res, nil
}

// gatherHandoverFacts reads the state the refusal rules judge. A binding this
// machine already gave away has no record to read, so only the sync and target
// facts are gathered for it.
func gatherHandoverFacts(ctx context.Context, rt Runtime, d *db.DB, opts HandoverOptions) (HandoverFacts, store.Binding, error) {
	f := HandoverFacts{Name: opts.Name, To: opts.To, This: d.Origin()}
	local, err := relevosync.LocalHandle(d)
	if err != nil {
		return f, store.Binding{}, err
	}
	settings, err := relevosync.ReadSettings(local)
	if err != nil {
		return f, store.Binding{}, err
	}
	f.SyncEnabled = settings.Enabled
	if key, kerr := relevosync.KeyState(local); kerr == nil {
		f.Locked = key == "locked" || key == "stale"
	}
	if f.Installations, err = installationFacts(d); err != nil {
		return f, store.Binding{}, err
	}
	b, err := rt.Store.Load(opts.Name)
	var gone *store.HandedOverError
	if errors.As(err, &gone) {
		f.HandedTo = gone.To
		return f, store.Binding{}, nil
	}
	if err != nil {
		return f, store.Binding{}, err
	}
	f.Round = b.Round
	f.Server = b.Link != nil || b.Builder.Remote()
	f.ChainErr = refuseRunningChainMemberStore(rt.Store, opts.Name)
	if entries, rerr := rt.Store.ReadLog(opts.Name); rerr == nil {
		f.RoundOpen = store.RoundOpen(entries, b.Round)
	}
	bf := branchFacts(ctx, rt, b, opts.Remote)
	f.Dirty, f.Unpushable = bf.dirty, bf.unpushable
	f.Remote, f.RemoteURL, f.OriginURL = opts.Remote, bf.remoteURL, bf.originURL
	return f, b, nil
}

func installationFacts(d *db.DB) ([]InstallationFact, error) {
	list, err := d.InstallationList()
	if err != nil {
		return nil, err
	}
	schemas, err := d.OriginSchemas()
	if err != nil {
		return nil, err
	}
	out := make([]InstallationFact, 0, len(list))
	for _, in := range list {
		out = append(out, InstallationFact{ID: in.ID, Label: in.Label, NewestSchema: schemas[in.ID]})
	}
	return out, nil
}

type branchState struct {
	dirty      bool
	unpushable string
	// remoteURL and originURL stay empty when the remote is origin or a read fails.
	remoteURL string
	originURL string
}

// branchFacts says whether the tree is dirty and why the branch could not be
// pushed. A worktree that is already gone is clean.
func branchFacts(ctx context.Context, rt Runtime, b store.Binding, remote string) branchState {
	if rt.Git == nil {
		return branchState{unpushable: "git is unavailable"}
	}
	dir := b.Worktree
	if dir == "" {
		dir = b.CWD
	}
	var st branchState
	if _, err := os.Stat(dir); err == nil {
		if st.dirty, err = rt.Git.Dirty(ctx, dir); err != nil {
			return branchState{unpushable: "the dirty check failed: " + text.Brief(err)}
		}
	}
	if b.Branch == "" {
		st.unpushable = "the binding records no branch"
		return st
	}
	remotes, err := rt.Git.Remotes(ctx, dir)
	if err != nil {
		st.unpushable = "the remotes could not be listed: " + text.Brief(err)
		return st
	}
	for _, r := range remotes {
		if r == remote {
			if remote != "origin" {
				st.remoteURL, _ = rt.Git.RemoteURL(ctx, dir, remote)
				st.originURL, _ = rt.Git.RemoteURL(ctx, dir, "origin")
			}
			return st
		}
	}
	st.unpushable = "the checkout has no remote named " + remote
	return st
}
