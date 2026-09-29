package main

import (
	"context"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// mastermindRepoOf reads cwd's repository identity through the runtime's git
// client, in the same shape internal/ingest stores: the origin URL normalised,
// the common dir canonical. A Runtime with no git client, or a cwd outside a
// repository, yields an empty ref.
func mastermindRepoOf(ctx context.Context, rt relevo.Runtime, cwd string) db.Repo {
	if rt.Git == nil {
		return db.Repo{}
	}
	originURL, commonDir, err := rt.Git.RepoFacts(ctx, cwd)
	if err != nil {
		return db.Repo{}
	}
	out := db.Repo{}
	if origin := git.NormalizeOriginURL(originURL); origin != "" {
		out.OriginURL = &origin
	}
	if commonDir != "" {
		dir := commonDir
		out.CommonDir = &dir
	}
	return out
}

// mastermindWriteSessionConsent writes one answer for a session. Unlike the
// repository answer it needs no git repository: "this session only" is an
// answer about the session, whatever the cwd is.
func mastermindWriteSessionConsent(rt relevo.Runtime, kind, session string, c db.Consent) error {
	if kind == "" || session == "" {
		return fail(codeUsage, "relevo mastermind: needs both --kind and --session, or neither")
	}
	d, err := rt.Store.DB()
	if err != nil {
		return err
	}
	return d.SetSessionConsent(kind, session, c, rt.Now())
}

// mastermindClearSessionConsent forgets a session's own answer and its told
// baseline in one write.
func mastermindClearSessionConsent(rt relevo.Runtime, kind, session string) error {
	if kind == "" || session == "" {
		return nil
	}
	d, err := rt.Store.DB()
	if err != nil {
		return err
	}
	return d.ClearSessionConsent(kind, session)
}

// mastermindWriteConsent writes one answer for cwd's repository. A cwd outside
// a git repository has nothing to remember, so the answer commands refuse.
func mastermindWriteConsent(rt relevo.Runtime, cwd string, c db.Consent) error {
	ref := mastermindRepoOf(context.Background(), rt, cwd)
	if !mastermindRepoKnown(ref) {
		return fail(codeRefused, "relevo mastermind enable|disable|reset: %s is not inside a git repository, so there is no repository answer to change", cwd)
	}
	d, err := rt.Store.DB()
	if err != nil {
		return err
	}
	if _, err := d.SetRepoConsent(ref, c, rt.Now()); err != nil {
		return err
	}
	return nil
}
