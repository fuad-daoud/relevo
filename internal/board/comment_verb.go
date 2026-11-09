package board

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// CommentInput is what one `board comment` asks for: Ref is the --mastermind
// value, BoardName the --board name, Arg an optional scene path, and the
// comment text, author, selector and position. A nil X/Y places the comment
// below the scene; Selector is read only on the HTML branch, where it names
// the element the note is about and empty means a board-level note.
type CommentInput struct {
	Ref       string
	BoardName string
	Arg       string
	Text      string
	By        string
	Selector  string
	X, Y      *float64
}

// Comments prints every comment in the resolved board in file order. An
// explicit .excalidraw path keeps the Excalidraw flow byte for byte, and every
// other shape -- no path, --board, or the pointer -- is an HTML board. Both
// branches are read-only: a missing board is no comments, exit 0, nothing
// created, and the pointer is never written.
func Comments(deps Deps, cwd, ref, boardName, arg string, asJSON bool) error {
	if !excalidrawArg(arg) {
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
	res, err := ResolveScene(deps, cwd, ref, boardName, arg)
	if err != nil {
		return err
	}

	comments, err := ReadComments(res.Path)
	if err != nil {
		return err
	}
	if asJSON {
		if comments == nil {
			comments = []Comment{}
		}
		return json.NewEncoder(os.Stdout).Encode(comments)
	}
	for _, c := range comments {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n",
			c.ID, number(c.X), number(c.Y), c.Text, c.By, c.At)
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

// WriteComment appends one comment to the resolved board and returns what it
// wrote; printing is the caller's. An explicit .excalidraw path keeps the
// scene flow byte for byte and refuses --selector, which an Excalidraw scene
// has no elements to satisfy. A live board whose name did not come from the
// pointer writes the pointer; a repo board never does.
func WriteComment(deps Deps, cwd string, in CommentInput) (CommentWrite, error) {
	if in.Text == "" {
		return CommentWrite{}, usagef("relevo board comment: --text is required and must not be empty")
	}
	if (in.X == nil) != (in.Y == nil) {
		return CommentWrite{}, usagef("relevo board comment: --x and --y go together")
	}
	if !excalidrawArg(in.Arg) {
		return writeBoardAnnotation(deps, cwd, in)
	}
	// --selector names an HTML element, and a scene has no elements selectors
	// name, so it is refused rather than silently dropped.
	if in.Selector != "" {
		return CommentWrite{}, usagef("relevo board comment: --selector needs an HTML board, not %s", in.Arg)
	}
	res, err := ResolveScene(deps, cwd, in.Ref, in.BoardName, in.Arg)
	if err != nil {
		return CommentWrite{}, err
	}
	if res.Scope == ScopeLive && !res.FromPointer {
		if err := WritePointer(res.LiveDir, res.Scene); err != nil {
			return CommentWrite{}, err
		}
	}

	theme, err := ResolveTheme(deps, cwd, "")
	if err != nil {
		return CommentWrite{}, err
	}
	by := in.By
	if by == "" {
		by = DefaultBy(os.Getenv("RELEVO_MASTERMIND"))
	}
	req := CommentRequest{Text: in.Text, By: by, At: deps.Now(), Theme: theme}
	if in.X != nil {
		req.X, req.Y = in.X, in.Y
	}

	c, err := AddComment(res.Path, req)
	if err != nil {
		return CommentWrite{}, err
	}
	return CommentWrite{ID: c.ID, Path: res.Path}, nil
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
