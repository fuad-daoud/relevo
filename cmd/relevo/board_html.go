package main

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/board"
)

// resolveBoardHTML resolves one `relevo board` invocation to a single-file
// board. It is the sibling of resolveBoard and keeps its precedence: an explicit
// path is live-shaped or repo, otherwise --board selects a name in the resolved
// MasterMind's live directory, the pointer is used when no name is given, and
// the default is written when neither exists. The only difference is the target
// shape -- <slug>/board.html rather than <name>.excalidraw -- and that a repo
// path needs no --board, exactly as the Excalidraw path does not.
//
// The pointer is written for a live board that was not read from the pointer and
// never for a repo board, so the pointer always names the board the user is
// looking at.
func resolveBoardHTML(cwd, ref, boardName, arg string) (board.Resolved, error) {
	liveRoot, err := boardLiveRoot()
	if err != nil {
		return board.Resolved{}, fail(codeInternal, "%v", err)
	}
	// The repository root is resolved best-effort once, so the disjointness rule
	// holds whenever the invocation is inside a repository while a bare board
	// still works outside one.
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
		if res, ok, err := board.ResolveLiveHTMLArg(liveRoot, cwd, arg); err != nil {
			return board.Resolved{}, boardRefusal(err)
		} else if ok {
			res.Owner = filepath.Base(res.LiveDir)
			if ref != "" {
				id, name, err := boardMasterMind(ref)
				if err != nil {
					return board.Resolved{}, err
				}
				if id != filepath.Base(res.LiveDir) {
					return board.Resolved{}, fail(codeUsage,
						"--mastermind %s does not name the live path's MasterMind %s", ref, filepath.Base(res.LiveDir))
				}
				if name != "" {
					res.Owner = name
				}
			}
			return res, nil
		}
		if !repoKnown {
			return board.Resolved{}, fail(codeUsage, "not inside a git repository")
		}
		res, err := board.ResolveHTML(repoRoot, cwd, arg)
		if err != nil {
			return board.Resolved{}, boardRefusal(err)
		}
		return res, nil
	}

	id, owner, err := boardMasterMind(ref)
	if err != nil {
		return board.Resolved{}, err
	}
	liveDir := filepath.Join(liveRoot, id)
	name, fromPointer, err := board.SelectScene(liveDir, boardName)
	if err != nil {
		return board.Resolved{}, boardRefusal(err)
	}
	res, err := board.ResolveLiveHTMLDir(repoRoot, liveDir, name)
	if err != nil {
		return board.Resolved{}, boardRefusal(err)
	}
	res.FromPointer = fromPointer
	if owner == "" {
		owner = id
	}
	res.Owner = owner
	return res, nil
}

// runBoardHTML serves a single-file board on a loopback listener. It is
// runBoard's lifecycle with a different handler and no Excalidraw theme: the
// listener, the printed line, the server.json advertisement and the signal
// drain are shared with runBoard through serveBoard, so the two formats cannot
// drift on how they start or stop.
func runBoardHTML(opts boardOptions, shell fs.FS) error {
	// The offline check runs before the listener binds, so a board that names a
	// remote resource is named on stderr once rather than discovered in the
	// browser.
	if external := boardHTMLExternalRefs(opts.scene); len(external) > 0 {
		fmt.Fprintf(os.Stderr,
			"relevo: board: %s references %d external resource(s), which its content security policy blocks; first: %s\n",
			opts.scene, len(external), external[0])
	}
	return serveBoard(opts, func(host, token string) http.Handler {
		srv := &board.HTMLServer{
			Token:     token,
			BoardPath: opts.scene,
			Scope:     opts.scope,
			Name:      opts.slug,
			Host:      host,
			Shell:     shell,
		}
		return srv.Handler()
	})
}

// boardHTMLExternalRefs lists a board's external references for the startup
// warning. A board that cannot be read, or does not exist yet, is not a warning
// here: the server reports that itself, and saying it twice would say the same
// thing twice.
func boardHTMLExternalRefs(path string) []string {
	data, _, isNew, err := board.LoadHTML(path)
	if err != nil || isNew {
		return nil
	}
	return board.ExternalRefs(data)
}
