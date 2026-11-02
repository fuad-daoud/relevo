package board

import "path/filepath"

// Promotion is what a promote copied: the live board it read and the repo
// board it wrote. The caller prints both, so the human line and the JSON
// document can never disagree about the same copy.
type Promotion struct {
	Live string
	Dst  string
}

// PromoteToRepo copies a live board into the repo and returns what it copied;
// printing is the caller's. It is a peek verb: the only database contact is
// the read-only registry lookup Mastermind already does, and there is no
// daemon, no listener, no server.json and no pointer write -- promoting is
// not opening the board, so the pointer still names the live board the user
// is looking at.
//
// The destination is <repo>/docs/boards/<name>/board.html and the copy is
// atomic. A missing source is refused, an existing destination without
// --force is refused, and a bad slug or a run outside a repository is usage.
func PromoteToRepo(deps Deps, cwd, ref, boardName, to string, force bool) (Promotion, error) {
	repoRoot, err := deps.RepoRoot(cwd)
	if err != nil {
		return Promotion{}, usagef("not inside a git repository: %v", err)
	}

	id, _, err := Mastermind(deps, ref)
	if err != nil {
		return Promotion{}, err
	}
	liveRoot, err := deps.LiveRoot()
	if err != nil {
		return Promotion{}, err
	}
	liveDir := filepath.Join(liveRoot, id)

	// The source is resolved as a name, not as a path argument, so promoting
	// never takes a path that could point anywhere but one MasterMind's live
	// directory. The pointer is read and never written.
	name, _, err := SelectScene(liveDir, boardName)
	if err != nil {
		return Promotion{}, err
	}
	slug := name
	if to != "" {
		slug = to
	}
	if err := ValidSceneName(slug); err != nil {
		return Promotion{}, err
	}

	live := filepath.Join(liveDir, name, "board.html")
	dst := filepath.Join(repoRoot, "docs", "boards", slug, "board.html")
	if err := Promote(live, dst, force); err != nil {
		return Promotion{}, err
	}
	return Promotion{Live: live, Dst: dst}, nil
}
