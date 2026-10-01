package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/board"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
)

// boardLiveRoot is <state root>/boards, the live scope's root (S1).
func boardLiveRoot() (string, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "boards"), nil
}

// openBoardRegistry opens the state root's registry read-only, the minimum
// database contact R2 allows. It follows the exact shape of openDBQuery: when
// <state root>/relevo.db exists it dials an already-running owner within the
// verb's dial budget (start wait 0 -- "short" means no start wait, not a
// smaller dial cap), else it opens the file directly read-only, when the file
// is held it makes one more dial, and otherwise it refuses. It never starts the
// owner, and it never calls mastermind.Resolve (whose hit writes seen_at).
func openBoardRegistry() (*mastermind.DBRegistry, *db.DB, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, nil, fail(codeInternal, "%v", err)
	}
	path := filepath.Join(root, "relevo.db")
	if !fileExists(path) {
		return nil, nil, fail(codeRefused, "no relevo.db at %s; name a MasterMind with --mastermind <id|name> or RELEVO_MASTERMIND", path)
	}

	ctx := context.Background()
	if d, derr := dialOwnerAdHoc(ctx, root, verbDialBudget); derr == nil {
		return &mastermind.DBRegistry{KV: db.TxKV{DB: d}}, d, nil
	}
	d, err := openReadOnlyDB(path, db.Options{})
	if err == nil {
		return &mastermind.DBRegistry{KV: db.TxKV{DB: d}}, d, nil
	}
	if errors.Is(err, db.ErrNotConverted) {
		return nil, nil, failWrap(codeRefused, err, "open %s", path)
	}
	if !errors.Is(err, db.ErrLocked) {
		return nil, nil, failWrap(codeInternal, err, "open %s", path)
	}
	// The file is held, so the owner is the only reader. One more dial covers a
	// daemon that bound its socket between the first attempt and the open.
	if d, derr := dialOwnerAdHoc(ctx, root, verbDialBudget); derr == nil {
		return &mastermind.DBRegistry{KV: db.TxKV{DB: d}}, d, nil
	}
	if sock, sockErr := ownerSocket(root); sockErr != nil {
		return nil, nil, failWrap(codeConflict, err, "relevo.db is locked (%s) and no owner socket is available: %v", path+".lock", sockErr)
	} else {
		return nil, nil, failWrap(codeConflict, err, "relevo.db is locked (%s) and the owner socket at %s did not answer", path+".lock", sock)
	}
}

// boardMasterMind resolves the MasterMind whose live board the verb acts on:
// the --mastermind value, else RELEVO_MASTERMIND, else the single registered
// MasterMind (R2, S2). An mm_/pl_/ULID id ref needs no database at all; a name
// ref or the single-registered fallback opens the registry read-only.
func boardMasterMind(ref string) (string, error) {
	if ref == "" {
		ref = os.Getenv("RELEVO_MASTERMIND")
	}
	if ref != "" && mastermind.ValidID(ref) == nil {
		return ref, nil
	}

	reg, d, err := openBoardRegistry()
	if err != nil {
		return "", err
	}
	defer func() { _ = d.Close() }()

	if ref != "" {
		rec, err := mastermindLookup(reg, ref)
		if err != nil {
			return "", err
		}
		return rec.ID, nil
	}

	recs, err := reg.List()
	if err != nil {
		return "", fail(codeInternal, "%v", err)
	}
	switch len(recs) {
	case 0:
		return "", fail(codeUsage, "no MasterMind records; run relevo mastermind init")
	case 1:
		return recs[0].ID, nil
	default:
		names := make([]string, 0, len(recs))
		for _, rec := range recs {
			names = append(names, rec.ID+" ("+rec.Name+")")
		}
		return "", fail(codeUsage, "several MasterMinds are registered; name one with --mastermind: %s", strings.Join(names, ", "))
	}
}

// boardRepoRootBestEffort resolves cwd's repository root without failing: any
// git error -- including "not a repository" -- is ("", false), so a bare live
// board still resolves outside a repository (R6/S2).
func boardRepoRootBestEffort(cwd string) (string, bool) {
	root, err := boardRepoRootFn(cwd)
	if err != nil {
		return "", false
	}
	return root, true
}

// resolveBoard resolves one `relevo board` invocation to a scene (S2). An
// explicit path is live-shaped or repo; otherwise --board selects a scene in
// the resolved MasterMind's live directory, the pointer is used when no name is
// given, and the default is written when neither exists. ref is the
// --mastermind value alone: an explicit path ignores RELEVO_MASTERMIND, and a
// repo path ignores --mastermind.
func resolveBoard(cwd, ref, boardName, arg string) (board.Resolved, error) {
	liveRoot, err := boardLiveRoot()
	if err != nil {
		return board.Resolved{}, fail(codeInternal, "%v", err)
	}
	// The repository root is resolved best-effort once, so the disjointness
	// rule (S5) is real whenever the invocation is inside a repository while a
	// bare board still works outside one (R6).
	repoRoot, repoKnown := boardRepoRootBestEffort(cwd)

	if arg != "" {
		if boardName != "" {
			return board.Resolved{}, fail(codeUsage, "relevo board: a scene path and --board are exclusive")
		}
		if repoKnown {
			if err := board.DisjointScopes(repoRoot, liveRoot); err != nil {
				return board.Resolved{}, boardRefusal(err)
			}
		}
		if res, ok, err := board.ResolveLiveArg(liveRoot, cwd, arg); err != nil {
			return board.Resolved{}, boardRefusal(err)
		} else if ok {
			if ref != "" {
				id, err := boardMasterMind(ref)
				if err != nil {
					return board.Resolved{}, err
				}
				if id != filepath.Base(res.LiveDir) {
					return board.Resolved{}, fail(codeUsage,
						"--mastermind %s does not name the live path's MasterMind %s", ref, filepath.Base(res.LiveDir))
				}
			}
			return res, nil
		}
		if !repoKnown {
			return board.Resolved{}, fail(codeUsage, "not inside a git repository")
		}
		scenePath, err := board.Resolve(repoRoot, cwd, arg)
		if err != nil {
			return board.Resolved{}, boardRefusal(err)
		}
		return board.Resolved{
			Scope: board.ScopeRepo,
			Scene: strings.TrimSuffix(filepath.Base(scenePath), ".excalidraw"),
			Path:  scenePath,
		}, nil
	}

	id, err := boardMasterMind(ref)
	if err != nil {
		return board.Resolved{}, err
	}
	liveDir := filepath.Join(liveRoot, id)
	name, fromPointer, err := board.SelectScene(liveDir, boardName)
	if err != nil {
		return board.Resolved{}, boardRefusal(err)
	}
	res, err := board.ResolveLiveDir(repoRoot, liveDir, name)
	if err != nil {
		return board.Resolved{}, boardRefusal(err)
	}
	res.FromPointer = fromPointer
	return res, nil
}
