package board

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

// LiveRoot is <state root>/boards, the live scope's root.
func LiveRoot() (string, error) {
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "boards"), nil
}

// OpenRegistry opens the state root's registry read-only, the minimum
// database contact, following the exact shape of openDBQuery. When
// <state root>/relevo.db exists it dials an already-running owner, else it
// opens the file directly read-only, when the file is held it makes one more
// dial, and otherwise it refuses. It never starts the owner, and it never
// calls mastermind.Resolve (whose hit writes seen_at).
func OpenRegistry(deps Deps) (*mastermind.DBRegistry, *db.DB, error) {
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(root, "relevo.db")
	if _, err := os.Stat(path); err != nil {
		return nil, nil, &notFoundError{msg: fmt.Sprintf("no relevo.db at %s; name a MasterMind with --mastermind <id|name> or RELEVO_MASTERMIND", path)}
	}

	// The registry binds the machine-local file because a record names a pid
	// and a cwd on this machine. The board reads through a dialled handle
	// when the owner holds the file and through a read-only one otherwise,
	// so the binding goes on the handle rather than on either path.
	registry := func(d *db.DB) *mastermind.DBRegistry {
		return &mastermind.DBRegistry{KV: db.TxKV{DB: d.LocalOrSelf()}}
	}

	ctx := context.Background()
	if d, derr := deps.DialOwner(ctx, root, deps.DialBudget); derr == nil {
		return registry(d), d, nil
	}
	d, err := deps.OpenDB(path, db.Options{})
	if err == nil {
		return registry(d), d, nil
	}
	if errors.Is(err, db.ErrNotConverted) {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	if !errors.Is(err, db.ErrLocked) {
		return nil, nil, err
	}
	// The file is held, so the owner is the only reader. One more dial covers a
	// daemon that bound its socket between the first attempt and the open.
	if d, derr := deps.DialOwner(ctx, root, deps.DialBudget); derr == nil {
		return registry(d), d, nil
	}
	if sock, sockErr := deps.OwnerSocket(root); sockErr != nil {
		return nil, nil, &HeldError{
			Msg:   fmt.Sprintf("relevo.db is locked (%s) and no owner socket is available: %v: %v", path+".lock", sockErr, err),
			Cause: err,
		}
	} else {
		return nil, nil, &HeldError{
			Msg:   fmt.Sprintf("relevo.db is locked (%s) and the owner socket at %s did not answer: %v", path+".lock", sock, err),
			Cause: err,
		}
	}
}

// Mastermind resolves the MasterMind whose live board the verb acts on:
// the --mastermind value, else RELEVO_MASTERMIND, else the single registered
// MasterMind. It returns the record's id and, when the registry was
// read, its name (empty for a bare id ref). An mm_/pl_/ULID id ref needs no
// database at all.
func Mastermind(deps Deps, ref string) (id, name string, err error) {
	if ref == "" {
		ref = os.Getenv("RELEVO_MASTERMIND")
	}
	if ref != "" && mastermind.ValidID(ref) == nil {
		return ref, "", nil
	}

	reg, d, err := OpenRegistry(deps)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = d.Close() }()

	if ref != "" {
		rec, err := deps.MastermindLookup(reg, ref)
		if err != nil {
			return "", "", err
		}
		return rec.ID, rec.Name, nil
	}

	recs, err := reg.List()
	if err != nil {
		return "", "", err
	}
	switch len(recs) {
	case 0:
		return "", "", usagef("no MasterMind records; run relevo mastermind init")
	case 1:
		return recs[0].ID, recs[0].Name, nil
	default:
		names := make([]string, 0, len(recs))
		for _, rec := range recs {
			names = append(names, rec.ID+" ("+rec.Name+")")
		}
		return "", "", usagef("several MasterMinds are registered; name one with --mastermind: %s", strings.Join(names, ", "))
	}
}

// repoRootBestEffort resolves cwd's repository root without failing: any
// git error -- including "not a repository" -- is ("", false), so a bare live
// board still resolves outside a repository.
func repoRootBestEffort(resolve func(string) (string, error), cwd string) (string, bool) {
	root, err := resolve(cwd)
	if err != nil {
		return "", false
	}
	return root, true
}

// checkLiveOwner names the owner of a live-shaped path and reconciles it with
// an explicit --mastermind ref: a ref that does not name the path's MasterMind
// is usage.
func checkLiveOwner(deps Deps, res Resolved, ref string) (Resolved, error) {
	res.Owner = filepath.Base(res.LiveDir)
	if ref == "" {
		return res, nil
	}
	id, name, err := Mastermind(deps, ref)
	if err != nil {
		return Resolved{}, err
	}
	if id != filepath.Base(res.LiveDir) {
		return Resolved{}, usagef(
			"--mastermind %s does not name the live path's MasterMind %s", ref, filepath.Base(res.LiveDir))
	}
	if name != "" {
		res.Owner = name
	}
	return res, nil
}
