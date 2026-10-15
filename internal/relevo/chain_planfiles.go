package relevo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// planFileToken matches a word that looks like a repo file: dotted name with an
// extension, optionally under directories. Most matches are not files; the base
// branch decides which are.
var planFileToken = regexp.MustCompile(`[A-Za-z0-9_@-][A-Za-z0-9_.@-]*(?:/[A-Za-z0-9_.@-]+)*\.[A-Za-z0-9]+`)

// maxPlanFiles bounds the tokens looked up on the base branch, and
// maxPlanFileBytes the size of one copied file.
const (
	maxPlanFiles     = 64
	maxPlanFileBytes = 1 << 20
)

// planFileCandidates lists the distinct file-like names the plan texts carry,
// in order of first appearance.
func planFileCandidates(texts [][]byte) []string {
	seen := map[string]bool{}
	var out []string
	for _, text := range texts {
		for _, tok := range planFileToken.FindAllString(string(text), -1) {
			if seen[tok] || len(out) >= maxPlanFiles || strings.Contains(tok, "..") {
				continue
			}
			seen[tok] = true
			out = append(out, tok)
		}
	}
	return out
}

// chainBaseCommit resolves the chain's base branch to a commit: the builder's
// base branch, fetched from origin when only the remote has it, else the
// commit the chain was cut from.
func chainBaseCommit(ctx context.Context, rt Runtime, tx *store.Tx, c db.ChainRow) (string, error) {
	ref := c.Base
	if b, err := tx.Load(c.Builder); err == nil && b.BaseRef != "" {
		ref = b.BaseRef
	}
	if ref == "" {
		return "", errors.New("the chain has no base")
	}
	if sha, ok, err := rt.Git.RefSHA(ctx, c.Repo, ref); err == nil && ok {
		return sha, nil
	}
	if err := rt.Git.Fetch(ctx, c.Repo, "origin", ref); err != nil {
		return "", fmt.Errorf("fetch %s: %w", ref, err)
	}
	sha, ok, err := rt.Git.RefSHA(ctx, c.Repo, "origin/"+ref)
	if err != nil || !ok {
		return "", fmt.Errorf("base %s not found", ref)
	}
	return sha, nil
}

// chainSeedPlanFiles copies every repo file the chain's plan texts name from
// the chain's base branch into the chain's inputs directory. A name the base
// branch does not hold is not a file and is left out; a file or a base that
// cannot be read renders as chainSeedMissing's path-free clause. It never fails
// a send.
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
	names := planFileCandidates(texts)
	if len(names) == 0 {
		return nil
	}
	ctx := context.Background()
	commit, err := chainBaseCommit(ctx, rt, tx, c)
	if err != nil {
		return []workflow.SeedFile{{Name: "base branch", Path: chainSeedMissing(err)}}
	}
	var files []workflow.SeedFile
	for _, name := range names {
		oid, ok, err := rt.Git.BlobAt(ctx, c.Repo, commit, name)
		if err == nil && !ok {
			continue
		}
		f := workflow.SeedFile{Name: name}
		if err != nil {
			f.Path = chainSeedMissing(err)
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
		return chainSeedMissing(errors.New("not a repo-relative path"))
	}
	body, err := rt.Git.ReadBlob(ctx, c.Repo, oid)
	if err != nil {
		return chainSeedMissing(err)
	}
	if len(body) > maxPlanFileBytes {
		return chainSeedMissing(errors.New("file too large"))
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return chainSeedMissing(err)
	}
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		return chainSeedMissing(err)
	}
	return dest
}
