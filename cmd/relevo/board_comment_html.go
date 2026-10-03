package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/board"
)

// boardCommentsHTMLFlagValues holds the pointers the HTML comments reader parses
// into. It is the same two flags as the Excalidraw reader, so the row in the
// registry stays true for both scopes.
type boardCommentsHTMLFlagValues struct {
	board *string
	json  *bool
}

// boardCommentsHTMLFlagSet defines the HTML board comments reader's flags on fs.
func boardCommentsHTMLFlagSet(fs *flag.FlagSet) *boardCommentsHTMLFlagValues {
	v := &boardCommentsHTMLFlagValues{}
	v.board = fs.String("board", "", "the live board name to read (default: the pointer, else board)")
	v.json = fs.Bool("json", false, "print the annotations as one compact JSON array")
	return v
}

// boardCommentHTMLFlagValues holds the pointers the HTML comment writer parses
// into. Selector is the addition over the Excalidraw verb: it names the element
// a note is about, and without it the note is about the board itself.
type boardCommentHTMLFlagValues struct {
	board    *string
	text     *string
	selector *string
	x        *float64
	y        *float64
	by       *string
}

// boardCommentHTMLFlagSet defines the HTML board comment writer's flags on fs.
func boardCommentHTMLFlagSet(fs *flag.FlagSet) *boardCommentHTMLFlagValues {
	v := &boardCommentHTMLFlagValues{}
	v.board = fs.String("board", "", "the live board name to write (default: the pointer, else board)")
	v.text = fs.String("text", "", "the comment text (required)")
	v.selector = fs.String("selector", "", "the CSS selector the comment is about (default: a board-level note)")
	v.x = fs.Float64("x", 0, "the comment's x, a fraction of the element's box, with --y (default: 0)")
	v.y = fs.Float64("y", 0, "the comment's y, a fraction of the element's box, with --x (default: 0)")
	v.by = fs.String("by", "", "the comment's author (default: RELEVO_MASTERMIND, else human)")
	return v
}

// cmdBoardCommentsHTML lists the annotations beside an HTML board, in file
// order. It is read-only: it never writes the pointer and never creates the
// board, so listing a board nobody has commented on is an empty list and exit
// zero, exactly as the Excalidraw reader is.
func cmdBoardCommentsHTML(cwd, arg, boardName string, v *boardCommentsHTMLFlagValues) error {
	res, err := resolveBoardHTML(cwd, "", boardName, arg)
	if err != nil {
		return err
	}
	entries, _, err := board.ReadAnnotations(res.Path)
	if err != nil {
		return boardRefusal(err)
	}
	if *v.json {
		if entries == nil {
			entries = []board.Annotation{}
		}
		return json.NewEncoder(os.Stdout).Encode(entries)
	}
	for _, a := range entries {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			a.ID, a.Selector, boardNumber(a.X), boardNumber(a.Y), a.Text, a.By, a.At)
	}
	return nil
}

// cmdBoardCommentHTML appends one annotation beside an HTML board and prints
// comment: <id>  <annotations path> -- the annotations path, not the board's,
// because that is the file the note landed in.
//
// A board that is not there is refused rather than created: commenting on a
// board that does not exist is a mistake worth naming, not an invitation to make
// an empty one. board.html is never written here or anywhere below it.
func cmdBoardCommentHTML(cwd, arg, boardName string, v *boardCommentHTMLFlagValues, text string) error {
	res, err := resolveBoardHTML(cwd, "", boardName, arg)
	if err != nil {
		return err
	}
	// A live board that was not read from the pointer writes it, exactly as
	// `board comment` does on the Excalidraw side, so the pointer keeps naming
	// the board the user is looking at.
	if res.Scope == board.ScopeLive && !res.FromPointer {
		if err := board.WritePointer(res.LiveDir, res.Scene); err != nil {
			return fail(codeInternal, "%v", err)
		}
	}
	if _, err := os.Stat(res.Path); err != nil {
		if os.IsNotExist(err) {
			return boardRefusal(fmt.Errorf("%w: board %s does not exist", board.ErrNotFound, res.Path))
		}
		return fail(codeInternal, "%v", err)
	}

	entry, _, err := board.AppendAnnotation(res.Path, board.AnnotationRequest{
		Selector: *v.selector,
		X:        *v.x,
		Y:        *v.y,
		Text:     text,
		By:       *v.by,
		At:       boardNow(),
	}, nil)
	if err != nil {
		return boardRefusal(err)
	}
	fmt.Printf("comment: %s  %s\n", entry.ID, board.AnnotationsPath(res.Path))
	return nil
}

// isExcalidrawArg reports whether arg names an Excalidraw scene explicitly. This
// is the D8 fork: an explicit .excalidraw path keeps the old flow byte for byte,
// and everything else -- no path, --board, or the pointer -- is an HTML board.
func isExcalidrawArg(arg string) bool {
	return strings.HasSuffix(arg, ".excalidraw")
}
