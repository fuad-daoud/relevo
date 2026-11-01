package board

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// CommentInput is what one `board comment` asks for: Ref is the --mastermind
// value, BoardName the --board name, Arg an optional scene path, and the
// comment text, author and position. A nil X/Y places the comment below the
// scene.
type CommentInput struct {
	Ref       string
	BoardName string
	Arg       string
	Text      string
	By        string
	X, Y      *float64
}

// Comments prints every comment in the resolved scene in scene order. It is
// read-only: a missing scene is no comments, exit 0, nothing created, and the
// pointer is never written.
func Comments(deps Deps, cwd, ref, boardName, arg string, asJSON bool) error {
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

// WriteComment appends one comment to the resolved scene and prints
// comment: <id>  <path>. A live scene whose name did not come from the
// pointer writes the pointer; a repo scene never does.
func WriteComment(deps Deps, cwd string, in CommentInput) error {
	if in.Text == "" {
		return usagef("relevo board comment: --text is required and must not be empty")
	}
	if (in.X == nil) != (in.Y == nil) {
		return usagef("relevo board comment: --x and --y go together")
	}

	res, err := ResolveScene(deps, cwd, in.Ref, in.BoardName, in.Arg)
	if err != nil {
		return err
	}
	if res.Scope == ScopeLive && !res.FromPointer {
		if err := WritePointer(res.LiveDir, res.Scene); err != nil {
			return err
		}
	}

	theme, err := ResolveTheme(deps, cwd, "")
	if err != nil {
		return err
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
		return err
	}
	fmt.Printf("comment: %s  %s\n", c.ID, res.Path)
	return nil
}

func number(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
