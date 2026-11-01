package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// maxPlanFileBytes bounds the size of one copied file.
const maxPlanFileBytes = 1 << 20

// chainBaseCommit resolves the chain's base branch from held refs: the
// builder's base branch, then its origin remote-tracking ref. It never
// fetches: the caller holds a store transaction, and a network call there
// would hold the database lock against every other verb.
func chainBaseCommit(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow) (string, error) {
	ref := c.Base
	if b, err := tx.Load(c.Builder); err == nil && b.BaseRef != "" {
		ref = b.BaseRef
	}
	if ref == "" {
		return "", errors.New("the chain has no base")
	}
	for _, name := range []string{ref, "origin/" + ref} {
		if sha, ok, err := rt.Git.RefSHA(ctx, c.Repo, name); err == nil && ok {
			return sha, nil
		}
	}
	return "", fmt.Errorf("base %s is not in the local repository", ref)
}

// chainSeedPlanFiles copies every repo file the chain's plan texts name from
// the base branch into the chain's inputs. A name the base branch does not
// hold is left out; an unreadable file or base renders as chain.SeedMissing's
// path-free clause. It never fails a send.
func chainSeedPlanFiles(rt Runtime, tx *store.Tx, c db.ChainRow) []workflow.SeedFile {
	paths, err := chainPlanPaths(c)
	if err != nil || rt.Git == nil || c.Repo == "" {
		return nil
	}
	var texts [][]byte
	for _, p := range paths {
		if body, err := rt.Store.ReadFile(p); err == nil {
			texts = append(texts, body)
		}
	}
	names := chain.PlanFileCandidates(texts)
	if len(names) == 0 {
		return nil
	}
	ctx := context.Background()
	commit, err := chainBaseCommit(ctx, rt, tx, c)
	if err != nil {
		return []workflow.SeedFile{{Name: "base branch", Path: chain.SeedMissing(err)}}
	}
	var files []workflow.SeedFile
	for _, name := range names {
		oid, ok, err := rt.Git.BlobAt(ctx, c.Repo, commit, name)
		if err == nil && !ok {
			continue
		}
		f := workflow.SeedFile{Name: name}
		if err != nil {
			f.Path = chain.SeedMissing(err)
		} else {
			f.Path = chainCopyPlanFile(ctx, rt, c, oid, name)
		}
		files = append(files, f)
	}
	return files
}

// chainCopyPlanFile writes one base-branch blob under the chain's inputs and
// returns the copy's path, or the path-free clause when it cannot.
func chainCopyPlanFile(ctx context.Context, rt Runtime, c db.ChainRow, oid, name string) string {
	dest, ok := rt.Store.ChainRepoFilePath(c.Name, name)
	if !ok {
		return chain.SeedMissing(errors.New("not a repo-relative path"))
	}
	body, err := rt.Git.ReadBlob(ctx, c.Repo, oid)
	if err != nil {
		return chain.SeedMissing(err)
	}
	if len(body) > maxPlanFileBytes {
		return chain.SeedMissing(errors.New("file too large"))
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return chain.SeedMissing(err)
	}
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		return chain.SeedMissing(err)
	}
	return dest
}
