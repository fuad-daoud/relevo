// board.go is the board verb's thin front end: the verb's work lives in
// internal/board, and this file only translates between it and the CLI frame.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/board"
	"github.com/fuad-daoud/relevo/internal/board/theme"
	"github.com/fuad-daoud/relevo/internal/db"
)

type boardFlagValues struct {
	noOpen     *bool
	board      *string
	mastermind *string
}

// boardFlagSet exists so the registry's parity test finds exactly one installer
// per verb.
func boardFlagSet(fs *flag.FlagSet) *boardFlagValues {
	v := &boardFlagValues{}
	v.noOpen = fs.Bool("no-open", false, "print the URL without opening a browser")
	v.board = fs.String("board", "", "the live scene name to open (default: the pointer, else board)")
	v.mastermind = fs.String("mastermind", "", "the MasterMind whose live board to open (id or name)")
	return v
}

type boardURLFlagValues struct {
	board      *string
	mastermind *string
}

func boardURLFlagSet(fs *flag.FlagSet) *boardURLFlagValues {
	v := &boardURLFlagValues{}
	v.board = fs.String("board", "", "the live scene name whose URL to print (default: the pointer)")
	v.mastermind = fs.String("mastermind", "", "the MasterMind whose live board URL to print (id or name)")
	return v
}

type boardCommentsFlagValues struct {
	board *string
	json  *bool
}

func boardCommentsFlagSet(fs *flag.FlagSet) *boardCommentsFlagValues {
	v := &boardCommentsFlagValues{}
	v.board = fs.String("board", "", "the live scene name to read (default: the pointer, else board)")
	v.json = fs.Bool("json", false, "print the comments as one compact JSON array")
	return v
}

type boardCommentFlagValues struct {
	board    *string
	text     *string
	selector *string
	x        *float64
	y        *float64
	by       *string
	asJSON   *bool
}

func boardCommentFlagSet(fs *flag.FlagSet) *boardCommentFlagValues {
	v := &boardCommentFlagValues{}
	v.board = fs.String("board", "", "the live board name to write (default: the pointer, else board)")
	v.text = fs.String("text", "", "the comment text (required)")
	v.selector = fs.String("selector", "", "the CSS selector the comment is about (default: a board-level note)")
	v.x = fs.Float64("x", 0, "the comment's x, a fraction of the element's box, with --y (default: below the scene)")
	v.y = fs.Float64("y", 0, "the comment's y, a fraction of the element's box, with --x (default: below the scene)")
	v.by = fs.String("by", "", "the comment's author (default: RELEVO_MASTERMIND, else human)")
	v.asJSON = fs.Bool("json", false, "print the document the write produced")
	return v
}

type boardPromoteFlagValues struct {
	board      *string
	mastermind *string
	to         *string
	force      *bool
	asJSON     *bool
}

// boardPromoteFlagSet exists so the registry's parity test finds exactly one
// installer.
func boardPromoteFlagSet(fs *flag.FlagSet) *boardPromoteFlagValues {
	v := &boardPromoteFlagValues{}
	v.board = fs.String("board", "", "the live board name to promote (default: the pointer, else board)")
	v.mastermind = fs.String("mastermind", "", "the MasterMind whose live board to promote (id or name)")
	v.to = fs.String("to", "", "the repo board name to write (default: the live board's own name)")
	v.force = fs.Bool("force", false, "overwrite an existing repo board")
	v.asJSON = fs.Bool("json", false, "print the document the write produced")
	return v
}

// boardDeps gathers the cmd-owned values the board verbs read.
func boardDeps() board.Deps {
	return board.Deps{
		Listen:           board.Listen,
		Open:             board.Open,
		ProcStart:        procStartUnix,
		RepoRoot:         board.RepoRoot,
		Now:              time.Now,
		LiveRoot:         board.LiveRoot,
		DialOwner:        dialOwnerAdHoc,
		OpenDB:           openReadOnlyDB,
		DialBudget:       verbDialBudget,
		OwnerSocket:      ownerSocket,
		MastermindLookup: mastermindLookup,
	}
}

// cmdBoard parses the verb and runs the foreground server; the subverbs dispatch
// first.
func cmdBoard(args []string) error {
	var sub string
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "url":
		return cmdBoardURL(args[1:])
	case "promote":
		return cmdBoardPromote(args[1:])
	case "comments":
		return cmdBoardComments(args[1:])
	case "comment":
		return cmdBoardComment(args[1:])
	}
	fs := flag.NewFlagSet("relevo board", flag.ContinueOnError)
	v := boardFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) > 1 {
		return fail(codeUsage, "relevo board takes at most one scene path, got %d", len(fs.Args()))
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}

	arg := ""
	if len(fs.Args()) == 1 {
		arg = fs.Args()[0]
	}
	return boardRefusal(board.Run(boardDeps(), cwd, *v.mastermind, *v.board, arg, *v.noOpen, nil))
}

// cmdBoardURL prints the live board's URL for a shell copy.
func cmdBoardURL(args []string) error {
	fs := flag.NewFlagSet("relevo board url", flag.ContinueOnError)
	v := boardURLFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fail(codeUsage, "relevo board url takes no scene path, got %q", fs.Args()[0])
	}
	return boardRefusal(board.URL(boardDeps(), *v.mastermind, *v.board))
}

// cmdBoardComments prints every comment in the resolved scene.
func cmdBoardComments(args []string) error {
	fs := flag.NewFlagSet("relevo board comments", flag.ContinueOnError)
	v := boardCommentsFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) > 1 {
		return fail(codeUsage, "relevo board comments takes at most one scene path, got %d", len(fs.Args()))
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	arg := ""
	if len(fs.Args()) == 1 {
		arg = fs.Args()[0]
	}
	return boardRefusal(board.Comments(boardDeps(), cwd, "", *v.board, arg, *v.json))
}

// cmdBoardComment appends one comment to the resolved scene.
func cmdBoardComment(args []string) error {
	fs := flag.NewFlagSet("relevo board comment", flag.ContinueOnError)
	v := boardCommentFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) > 1 {
		return fail(codeUsage, "relevo board comment takes at most one scene path, got %d", len(fs.Args()))
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	arg := ""
	if len(fs.Args()) == 1 {
		arg = fs.Args()[0]
	}
	in := board.CommentInput{Text: *v.text, By: *v.by, BoardName: *v.board, Arg: arg, Selector: *v.selector}
	if flagGiven(fs, "x") {
		in.X = v.x
	}
	if flagGiven(fs, "y") {
		in.Y = v.y
	}
	w, err := board.WriteComment(boardDeps(), cwd, in)
	if err != nil {
		return boardRefusal(err)
	}
	if *v.asJSON {
		return printDoc(commentDocOf(w))
	}
	fmt.Printf("comment: %s  %s\n", w.ID, w.Path)
	return nil
}

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
	p, err := board.PromoteToRepo(boardDeps(), cwd, *v.mastermind, *v.board, *v.to, *v.force)
	if err != nil {
		return boardRefusal(err)
	}
	if *v.asJSON {
		return printDoc(promoteDocOf(p))
	}
	fmt.Printf("board: promoted %s -> %s\n", p.Live, p.Dst)
	// A second line only when there was an annotations file to copy, so a
	// forced promotion of a board without notes still names no file that does
	// not exist.
	if p.CopiedAnnotations {
		fmt.Printf("board: promoted %s -> %s\n",
			board.AnnotationsPath(p.Live), board.AnnotationsPath(p.Dst))
	}
	return nil
}

// boardRefusal maps a board package refusal to the catalog code it earns. An
// error the helpers already classified reaches the user as it is.
func boardRefusal(err error) error {
	var ce *cliError
	var he *board.HeldError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ce):
		return ce
	case errors.Is(err, board.ErrUsage):
		return fail(codeUsage, "%s", err)
	case errors.Is(err, theme.ErrUsage):
		// The palette refusals live beside the stored sets; the board carries
		// them out with the same usage code.
		return fail(codeUsage, "%s", err)
	case errors.Is(err, board.ErrNotFound):
		return fail(codeRefused, "%s", err)
	case errors.Is(err, board.ErrInvalid):
		return fail(codeRefused, "%s", boardInvalidText(err))
	case errors.As(err, &he):
		return failWrap(codeConflict, err, "%s", err)
	case errors.Is(err, board.ErrNotAvailable):
		return fail(codeNotAvailable, "%s", err)
	case errors.Is(err, db.ErrNotConverted):
		return failWrap(codeRefused, err, "%s", err)
	default:
		return fail(codeInternal, "%s", err)
	}
}

// boardInvalidText is the board.ErrInvalid message without its sentinel word.
func boardInvalidText(err error) string {
	return strings.TrimPrefix(err.Error(), board.ErrInvalid.Error()+": ")
}
