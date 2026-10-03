package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/board"
)

// boardPromoteFlagValues holds the pointers `board promote` parses into.
type boardPromoteFlagValues struct {
	board      *string
	mastermind *string
	to         *string
	force      *bool
}

// boardPromoteFlagSet defines `board promote`'s flags on fs and returns what it
// parses into, so the registry's parity test finds exactly one installer.
func boardPromoteFlagSet(fs *flag.FlagSet) *boardPromoteFlagValues {
	v := &boardPromoteFlagValues{}
	v.board = fs.String("board", "", "the live board name to promote (default: the pointer, else board)")
	v.mastermind = fs.String("mastermind", "", "the MasterMind whose live board to promote (id or name)")
	v.to = fs.String("to", "", "the repo board name to write (default: the live board's own name)")
	v.force = fs.Bool("force", false, "overwrite an existing repo board")
	return v
}

// cmdBoardPromote copies a live board into the repo. It is a peek verb: the only
// database contact is the read-only registry lookup boardMasterMind already
// does, and there is no daemon, no listener, no server.json and no pointer
// write -- promoting is not opening the board, so the pointer still names the
// live board the user is looking at.
//
// The destination is <repo>/docs/boards/<name>/board.html and the copy is
// atomic. A missing source is refused (exit 2), an existing destination without
// --force is refused naming both the target and the flag, a bad slug and a run
// outside a repository are usage.
func cmdBoardPromote(args []string) error {
	fs := flag.NewFlagSet("relevo board promote", flag.ContinueOnError)
	v := boardPromoteFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fail(codeUsage, "relevo board promote takes no scene path, got %q", fs.Args()[0])
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	repoRoot, err := boardRepoRootFn(cwd)
	if err != nil {
		return fail(codeUsage, "not inside a git repository: %v", err)
	}

	id, _, err := boardMasterMind(*v.mastermind)
	if err != nil {
		return err
	}
	liveRoot, err := boardLiveRoot()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	liveDir := filepath.Join(liveRoot, id)

	// The source is resolved as a name, not as a path argument, so promoting
	// never takes a path that could point anywhere but one MasterMind's live
	// directory. The pointer is read and never written.
	name, _, err := board.SelectScene(liveDir, *v.board)
	if err != nil {
		return boardRefusal(err)
	}
	slug := name
	if *v.to != "" {
		slug = *v.to
	}
	if err := board.ValidSceneName(slug); err != nil {
		return boardRefusal(err)
	}

	live := filepath.Join(liveDir, name, "board.html")
	dst := filepath.Join(repoRoot, "docs", "boards", slug, "board.html")
	copiedAnnotations, err := board.PromoteWithAnnotations(live, dst, *v.force)
	if err != nil {
		return boardRefusal(err)
	}
	fmt.Printf("board: promoted %s -> %s\n", live, dst)
	// A second line only when there was an annotations file to copy, so the
	// common case stays one line and the notes are never silently dropped.
	if copiedAnnotations {
		fmt.Printf("board: promoted %s -> %s\n",
			board.AnnotationsPath(live), board.AnnotationsPath(dst))
	}
	return nil
}
