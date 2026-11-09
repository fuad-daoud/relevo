package board

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// CommentInput is what one `board comment` asks for: Ref is the --mastermind
// value, BoardName the --board name, Arg an optional board path, and the
// comment text, author, selector and position. A nil X/Y places the comment
// below the board; Selector names the element the note is about, and empty
// means a board-level note.
type CommentInput struct {
	Ref       string
	BoardName string
	Arg       string
	Text      string
	By        string
	Selector  string
	X, Y      *float64
}

// Comments prints every comment in the resolved HTML board in file order. It
// is read-only: a missing board is no comments, exit 0, nothing created, and
// the pointer is never written.
func Comments(deps Deps, cwd, ref, boardName, arg string, asJSON bool) error {
	res, err := ResolveSceneHTML(deps, cwd, ref, boardName, arg)
	if err != nil {
		return err
	}
	entries, _, err := ReadAnnotations(res.Path)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(entries)
	}
	for _, a := range entries {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			a.ID, a.Selector, number(a.X), number(a.Y), a.Text, a.By, a.At)
	}
	return nil
}

// CommentWrite is what a comment write appended: the comment's id and the
// resolved scene path it lives in. The caller prints both, so the human line
// and the JSON document can never disagree about the same write.
type CommentWrite struct {
	ID   string
	Path string
}

// WriteComment appends one comment to the resolved HTML board and returns
// what it wrote; printing is the caller's. A live board whose name did not
// come from the pointer writes the pointer; a repo board never does.
func WriteComment(deps Deps, cwd string, in CommentInput) (CommentWrite, error) {
	if in.Text == "" {
		return CommentWrite{}, usagef("relevo board comment: --text is required and must not be empty")
	}
	if (in.X == nil) != (in.Y == nil) {
		return CommentWrite{}, usagef("relevo board comment: --x and --y go together")
	}
	return writeBoardAnnotation(deps, cwd, in)
}

// writeBoardAnnotation is the HTML branch of one comment write: the note lands
// in the annotations file beside the board with the board's bytes untouched,
// and the printed path names that file. A position is a fraction of a chosen
// element's box, so --x and --y without --selector are a mistake rather than a
// silent default. A board that is not there is refused, never created.
func writeBoardAnnotation(deps Deps, cwd string, in CommentInput) (CommentWrite, error) {
	// A position needs an element to be a fraction of. --selector alone places
	// the note at the origin, which is a usable default; --x without one is
	// not.
	if (in.X != nil || in.Y != nil) && in.Selector == "" {
		return CommentWrite{}, usagef("relevo board comment: --x and --y need --selector on an HTML board")
	}
	res, err := ResolveSceneHTML(deps, cwd, in.Ref, in.BoardName, in.Arg)
	if err != nil {
		return CommentWrite{}, err
	}
	if res.Scope == ScopeLive && !res.FromPointer {
		if err := WritePointer(res.LiveDir, res.Scene); err != nil {
			return CommentWrite{}, err
		}
	}
	if _, err := os.Stat(res.Path); err != nil {
		if os.IsNotExist(err) {
			return CommentWrite{}, fmt.Errorf("%w: board %s does not exist", ErrNotFound, res.Path)
		}
		return CommentWrite{}, err
	}
	by := in.By
	if by == "" {
		by = DefaultBy(os.Getenv("RELEVO_MASTERMIND"))
	}
	entry, _, err := AppendAnnotation(res.Path, AnnotationRequest{
		Selector: in.Selector,
		X:        derefOr(in.X),
		Y:        derefOr(in.Y),
		Text:     in.Text,
		By:       by,
		At:       deps.Now(),
	}, nil)
	if err != nil {
		return CommentWrite{}, err
	}
	return CommentWrite{ID: entry.ID, Path: AnnotationsPath(res.Path)}, nil
}

// humanBy is the author a comment gets when neither --by nor the
// RELEVO_MASTERMIND environment names one.
const humanBy = "human"

// DefaultBy resolves the comment author: the explicit value, else the
// environment's MasterMind, else human.
func DefaultBy(env string) string {
	if env != "" {
		return env
	}
	return humanBy
}

// derefOr is the position of a flag the writer was not given: the store
// forces the origin for a board-level note anyway, so the zero value only
// needs to survive validation.
func derefOr(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func number(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
