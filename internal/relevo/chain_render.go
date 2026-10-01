package relevo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainRenderSeed renders one step's seed into the prompt a runner gets. Every
// reference becomes a path through chainSeedInput, or the path-free miss clause
// when it cannot be produced; a task, a param, the chain's base and branch and
// the chain's own diff render inline. A seed that is exactly one file reference
// hands that file over as bytes, the way a plan is handed over today.
func chainRenderSeed(rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, st workflow.State, seed string) (string, error) {
	refs, err := workflow.Refs(seed)
	if err != nil {
		return "", err
	}
	if workflow.IsSingleRef(seed) && len(refs) == 1 {
		text, isFile, rerr := chainSeedRef(rt, tx, c, def, st, refs[0])
		if rerr != nil {
			return chainSeedMissing(rerr), nil
		}
		if !isFile {
			return text, nil
		}
		body, oerr := os.ReadFile(text)
		if oerr != nil {
			return chainSeedMissing(oerr), nil
		}
		return string(body), nil
	}

	var b strings.Builder
	rest := seed
	for _, ref := range refs {
		start := strings.Index(rest, "{{")
		if start < 0 {
			break
		}
		end := strings.Index(rest[start+2:], "}}")
		if end < 0 {
			break
		}
		b.WriteString(rest[:start])
		text, _, rerr := chainSeedRef(rt, tx, c, def, st, ref)
		if rerr != nil {
			b.WriteString(chainSeedMissing(rerr))
		} else {
			b.WriteString(text)
		}
		rest = rest[start+2+end+2:]
	}
	b.WriteString(rest)
	return b.String(), nil
}

// chainSeedRef resolves one reference to the text a seed names. isFile is true
// when the text is a path whose bytes a single-reference seed hands over.
func chainSeedRef(rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, st workflow.State, ref workflow.Ref) (string, bool, error) {
	switch ref.Root {
	case "task":
		body, err := rt.Store.ReadFile(rt.Store.ChainTaskPath(c.Name))
		if err != nil {
			return "", false, err
		}
		return string(body), false, nil
	case "chain":
		return chainChainRef(rt, tx, c, ref)
	}
	target, err := workflow.ResolveRef(def, st, ref)
	if err != nil {
		return "", false, err
	}
	switch {
	case target.Key != "":
		return chainSeedInput(rt, c, target.Key), true, nil
	case target.List != nil:
		path, err := chainSeedListFile(rt, c, ref, target.List)
		if err != nil {
			return "", false, err
		}
		return path, true, nil
	default:
		return target.Inline, false, nil
	}
}

// chainChainRef resolves a chain reference: the base and the branch render
// inline, and the whole-branch diff is captured and named as a copy.
func chainChainRef(rt Runtime, tx *store.Tx, c db.ChainRow, ref workflow.Ref) (string, bool, error) {
	switch ref.Attr {
	case "base":
		return c.Base, false, nil
	case "branch":
		return c.Branch, false, nil
	case "diff":
		key := chainBranchDiff(rt, tx, c, c.Builder, memberNewestClosedRound(tx, c.Builder))
		if key == "" {
			return "", false, errors.New("no chain diff captured")
		}
		return chainSeedInput(rt, c, key), true, nil
	}
	return "", false, fmt.Errorf("chain.%s is not chain state", ref.Attr)
}

// chainSeedListFile writes every path a list reference names under the chain's
// input directory and returns that file's path, so a seed names one openable
// file rather than an inline list.
func chainSeedListFile(rt Runtime, c db.ChainRow, ref workflow.Ref, items []string) (string, error) {
	dir := rt.Store.ChainInputDir(c.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, ref.Root+"-"+ref.Attr+".list")
	if err := os.WriteFile(path, []byte(strings.Join(items, "\n")+"\n"), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
