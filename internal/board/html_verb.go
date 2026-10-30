package board

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
)

// ResolveSceneHTML resolves one `relevo board` invocation to a single-file
// board. It is the sibling of ResolveScene and keeps its precedence: an explicit
// path is live-shaped or repo, otherwise --board selects a name in the resolved
// MasterMind's live directory, the pointer is used when no name is given, and
// the default is written when neither exists. The only difference is the target
// shape -- <slug>/board.html rather than <name>.excalidraw -- and that a repo
// path needs no --board, exactly as the Excalidraw path does not.
//
// The pointer is written for a live board that was not read from the pointer and
// never for a repo board, so the pointer always names the board the user is
// looking at.
func ResolveSceneHTML(deps Deps, cwd, ref, boardName, arg string) (Resolved, error) {
	liveRoot, err := deps.LiveRoot()
	if err != nil {
		return Resolved{}, err
	}
	// The repository root is resolved best-effort once, so the disjointness rule
	// holds whenever the invocation is inside a repository while a bare board
	// still works outside one.
	repoRoot, repoKnown := repoRootBestEffort(deps.RepoRoot, cwd)

	if arg != "" {
		if boardName != "" {
			return Resolved{}, usagef("relevo board: a scene path and --board are exclusive")
		}
		return resolveSceneHTMLPath(deps, liveRoot, repoRoot, repoKnown, cwd, ref, arg)
	}
	return resolveSceneHTMLDefault(deps, liveRoot, repoRoot, ref, boardName)
}

// resolveSceneHTMLPath resolves an explicit single-file board path:
// live-shaped when the path names a live board, else a repo board under the
// git top level.
func resolveSceneHTMLPath(deps Deps, liveRoot, repoRoot string, repoKnown bool, cwd, ref, arg string) (Resolved, error) {
	if repoKnown {
		if err := DisjointScopes(repoRoot, liveRoot); err != nil {
			return Resolved{}, err
		}
	}
	if res, ok, err := ResolveLiveHTMLArg(liveRoot, cwd, arg); err != nil {
		return Resolved{}, err
	} else if ok {
		return checkLiveOwner(deps, res, ref)
	}
	if !repoKnown {
		return Resolved{}, usagef("not inside a git repository")
	}
	res, err := ResolveHTML(repoRoot, cwd, arg)
	if err != nil {
		return Resolved{}, err
	}
	return res, nil
}

// resolveSceneHTMLDefault resolves a bare board or a --board name to the
// resolved MasterMind's live directory, reading the pointer when no name is
// given.
func resolveSceneHTMLDefault(deps Deps, liveRoot, repoRoot, ref, boardName string) (Resolved, error) {
	id, owner, err := Mastermind(deps, ref)
	if err != nil {
		return Resolved{}, err
	}
	liveDir := filepath.Join(liveRoot, id)
	name, fromPointer, err := SelectScene(liveDir, boardName)
	if err != nil {
		return Resolved{}, err
	}
	res, err := ResolveLiveHTMLDir(repoRoot, liveDir, name)
	if err != nil {
		return Resolved{}, err
	}
	res.FromPointer = fromPointer
	if owner == "" {
		owner = id
	}
	res.Owner = owner
	return res, nil
}

// ServeHTML serves a single-file board on a loopback listener. It is
// Serve's lifecycle with a different handler and no Excalidraw theme: the
// listener, the printed line, the server.json advertisement and the signal
// drain are shared with Serve through serve, so the two formats cannot
// drift on how they start or stop.
func ServeHTML(deps Deps, opts Options, shell fs.FS) error {
	// The offline check runs before the listener binds, so a board that names a
	// remote resource is named on stderr once rather than discovered in the
	// browser.
	if external := HTMLExternalRefs(opts.Scene); len(external) > 0 {
		fmt.Fprintf(os.Stderr,
			"relevo: board: %s references %d external resource(s), which its content security policy blocks; first: %s\n",
			opts.Scene, len(external), external[0])
	}
	return serve(deps, opts, func(host, token string) http.Handler {
		srv := &HTMLServer{
			Token:     token,
			BoardPath: opts.Scene,
			Scope:     opts.Scope,
			Name:      opts.Slug,
			Host:      host,
			Shell:     shell,
		}
		return srv.Handler()
	})
}

// HTMLExternalRefs lists a board's external references for the startup
// warning. A board that cannot be read, or does not exist yet, is not a warning
// here: the server reports that itself, and saying it twice would say the same
// thing twice.
func HTMLExternalRefs(path string) []string {
	data, _, isNew, err := LoadHTML(path)
	if err != nil || isNew {
		return nil
	}
	return ExternalRefs(data)
}
