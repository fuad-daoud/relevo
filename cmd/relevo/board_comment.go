package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/fuad-daoud/relevo/internal/board"
)

// boardNow is the clock the comment verbs stamp an at from, a seam so a test
// is not at the mercy of the wall clock.
var boardNow = time.Now

// boardCommentsFlagValues holds the pointers `board comments` parses into.
type boardCommentsFlagValues struct {
	board *string
	json  *bool
}

// boardCommentsFlagSet defines `board comments`'s flags on fs.
func boardCommentsFlagSet(fs *flag.FlagSet) *boardCommentsFlagValues {
	v := &boardCommentsFlagValues{}
	v.board = fs.String("board", "", "the live scene name to read (default: the pointer, else board)")
	v.json = fs.Bool("json", false, "print the comments as one compact JSON array")
	return v
}

// boardCommentFlagValues holds the pointers `board comment` parses into. The
// selector is defined by the HTML flag set, which is the branch that uses it;
// both sets are installed here so the registry's parity test finds one
// installer per verb on either branch.
type boardCommentFlagValues struct {
	board    *string
	text     *string
	selector *string
	x        *float64
	y        *float64
	by       *string
}

// boardCommentFlagSet defines `board comment`'s flags on fs. The two sets carry
// the same names, so which one owns a flag is decided by the branch below rather
// than by the registry.
func boardCommentFlagSet(fs *flag.FlagSet) *boardCommentFlagValues {
	v := &boardCommentFlagValues{}
	v.board = fs.String("board", "", "the live board name to write (default: the pointer, else board)")
	v.text = fs.String("text", "", "the comment text (required)")
	v.selector = fs.String("selector", "", "the CSS selector the comment is about (HTML boards only)")
	v.x = fs.Float64("x", 0, "the comment's x, a fraction of the element's box or a scene x, with --y")
	v.y = fs.Float64("y", 0, "the comment's y, a fraction of the element's box or a scene y, with --x")
	v.by = fs.String("by", "", "the comment's author (default: RELEVO_MASTERMIND, else human)")
	return v
}

// cmdBoardComments prints every comment in the resolved scene in scene order:
// one compact JSON array with --json, otherwise one tab-separated row each. It
// is read-only: a missing scene is no comments, exit 0, nothing created, and
// the pointer is never written.
// cmdBoardComments prints every comment in the resolved scene in scene order:
// one compact JSON array with --json, otherwise one tab-separated row each. It
// is read-only: a missing scene is no comments, exit 0, nothing created, and
// the pointer is never written.
//
// An explicit .excalidraw path keeps the Excalidraw flow byte for byte. Every
// other shape -- no path, --board, or the pointer -- is an HTML board, so a bare
// call reads annotations.json rather than creating a scene.
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
	if !isExcalidrawArg(arg) {
		hv := &boardCommentsHTMLFlagValues{board: v.board, json: v.json}
		return cmdBoardCommentsHTML(cwd, arg, *v.board, hv)
	}
	res, err := resolveBoard(cwd, "", *v.board, arg)
	if err != nil {
		return err
	}

	comments, err := board.ReadComments(res.Path)
	if err != nil {
		return boardRefusal(err)
	}
	if *v.json {
		if comments == nil {
			comments = []board.Comment{}
		}
		return json.NewEncoder(os.Stdout).Encode(comments)
	}
	for _, c := range comments {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n",
			c.ID, boardNumber(c.X), boardNumber(c.Y), c.Text, c.By, c.At)
	}
	return nil
}

// cmdBoardComment appends one comment to the resolved scene and prints
// comment: <id>  <path>. It selects like `board`: a live scene whose name did
// not come from the pointer writes the pointer; a repo scene never does.
func cmdBoardComment(args []string) error {
	fs := flag.NewFlagSet("relevo board comment", flag.ContinueOnError)
	v := boardCommentFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) > 1 {
		return fail(codeUsage, "relevo board comment takes at most one scene path, got %d", len(fs.Args()))
	}
	if *v.text == "" {
		return fail(codeUsage, "relevo board comment: --text is required and must not be empty")
	}
	if flagGiven(fs, "x") != flagGiven(fs, "y") {
		return fail(codeUsage, "relevo board comment: --x and --y go together")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	arg := ""
	if len(fs.Args()) == 1 {
		arg = fs.Args()[0]
	}
	if !isExcalidrawArg(arg) {
		// A position needs an element to be a fraction of. --selector on its own
		// defaults to 0,0; --x without --selector is a position with nothing to
		// position against, which is a mistake rather than a silent default.
		if (flagGiven(fs, "x") || flagGiven(fs, "y")) && *v.selector == "" {
			return fail(codeUsage, "relevo board comment: --x and --y need --selector on an HTML board")
		}
		by := *v.by
		if by == "" {
			by = board.DefaultBy(os.Getenv("RELEVO_MASTERMIND"))
		}
		hv := &boardCommentHTMLFlagValues{
			board:    v.board,
			text:     v.text,
			selector: v.selector,
			x:        v.x,
			y:        v.y,
			by:       &by,
		}
		return cmdBoardCommentHTML(cwd, arg, *v.board, hv, *v.text)
	}
	// --selector names an element, and an Excalidraw scene has no elements to
	// select, so the flag is refused rather than ignored.
	if *v.selector != "" {
		return fail(codeUsage, "relevo board comment: --selector needs an HTML board, not %s", arg)
	}
	res, err := resolveBoard(cwd, "", *v.board, arg)
	if err != nil {
		return err
	}
	if res.Scope == board.ScopeLive && !res.FromPointer {
		if err := board.WritePointer(res.LiveDir, res.Scene); err != nil {
			return fail(codeInternal, "%v", err)
		}
	}

	theme, err := boardResolveTheme(cwd, "")
	if err != nil {
		return boardRefusal(err)
	}
	by := *v.by
	if by == "" {
		by = board.DefaultBy(os.Getenv("RELEVO_MASTERMIND"))
	}
	req := board.CommentRequest{Text: *v.text, By: by, At: boardNow(), Theme: theme}
	if flagGiven(fs, "x") {
		req.X, req.Y = v.x, v.y
	}

	c, err := board.AddComment(res.Path, req)
	if err != nil {
		return boardRefusal(err)
	}
	fmt.Printf("comment: %s  %s\n", c.ID, res.Path)
	return nil
}

// boardNumber renders one position without a trailing zero, for the text row.
func boardNumber(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
