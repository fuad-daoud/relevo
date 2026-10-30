package board

import (
	"fmt"
	"path/filepath"
)

// PromoteToRepo copies a live board into the repo. It is a peek verb: the only
// database contact is the read-only registry lookup Mastermind already
// does, and there is no daemon, no listener, no server.json and no pointer
// write -- promoting is not opening the board, so the pointer still names the
// live board the user is looking at.
//
// The destination is <repo>/docs/boards/<name>/board.html and the copy is
// atomic. A missing source is refused (exit 2), an existing destination without
// --force is refused naming both the target and the flag, a bad slug and a run
// outside a repository are usage.
func PromoteToRepo(deps Deps, cwd, ref, boardName, to string, force bool) error {
	repoRoot, err := deps.RepoRoot(cwd)
	if err != nil {
		return usagef("not inside a git repository: %v", err)
	}

	id, _, err := Mastermind(deps, ref)
	if err != nil {
		return err
	}
	liveRoot, err := deps.LiveRoot()
	if err != nil {
		return err
	}
	liveDir := filepath.Join(liveRoot, id)

	// The source is resolved as a name, not as a path argument, so promoting
	// never takes a path that could point anywhere but one MasterMind's live
	// directory. The pointer is read and never written.
	name, _, err := SelectScene(liveDir, boardName)
	if err != nil {
		return err
	}
	slug := name
	if to != "" {
		slug = to
	}
	if err := ValidSceneName(slug); err != nil {
		return err
	}

	live := filepath.Join(liveDir, name, "board.html")
	dst := filepath.Join(repoRoot, "docs", "boards", slug, "board.html")
	if err := Promote(live, dst, force); err != nil {
		return err
	}
	fmt.Printf("board: promoted %s -> %s\n", live, dst)
	return nil
}
