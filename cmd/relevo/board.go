// board.go is the board verb's thin front end: flag parsing, the dependency
// wiring and the exit mapping. The verb's work lives in internal/board; this
// file only translates between it and the CLI frame.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/board"
	"github.com/fuad-daoud/relevo/internal/board/theme"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

// boardFlagValues holds the pointers board parses into.
type boardFlagValues struct {
	noOpen     *bool
	theme      *string
	board      *string
	mastermind *string
}

// boardFlagSet defines board's flags on fs and returns what it parses into, so
// the registry's parity test finds exactly one installer per verb.
func boardFlagSet(fs *flag.FlagSet) *boardFlagValues {
	v := &boardFlagValues{}
	v.noOpen = fs.Bool("no-open", false, "print the URL without opening a browser")
	v.theme = fs.String("theme", "", "the palette for new elements: read the stored names with relevo config get themes")
	v.board = fs.String("board", "", "the live scene name to open (default: the pointer, else board)")
	v.mastermind = fs.String("mastermind", "", "the MasterMind whose live board to open (id or name)")
	return v
}

// boardURLFlagValues holds the pointers `board url` parses into.
type boardURLFlagValues struct {
	board      *string
	mastermind *string
}

// boardURLFlagSet defines `board url`'s flags on fs and returns what it parses
// into.
func boardURLFlagSet(fs *flag.FlagSet) *boardURLFlagValues {
	v := &boardURLFlagValues{}
	v.board = fs.String("board", "", "the live scene name whose URL to print (default: the pointer)")
	v.mastermind = fs.String("mastermind", "", "the MasterMind whose live board URL to print (id or name)")
	return v
}

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

// boardCommentFlagValues holds the pointers `board comment` parses into.
type boardCommentFlagValues struct {
	board *string
	text  *string
	x     *float64
	y     *float64
	by    *string
}

// boardCommentFlagSet defines `board comment`'s flags on fs.
func boardCommentFlagSet(fs *flag.FlagSet) *boardCommentFlagValues {
	v := &boardCommentFlagValues{}
	v.board = fs.String("board", "", "the live scene name to write (default: the pointer, else board)")
	v.text = fs.String("text", "", "the comment text (required)")
	v.x = fs.Float64("x", 0, "the comment's x position, with --y (default: below the scene)")
	v.y = fs.Float64("y", 0, "the comment's y position, with --x (default: below the scene)")
	v.by = fs.String("by", "", "the comment's author (default: RELEVO_MASTERMIND, else human)")
	return v
}

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

// boardTextFlagValues holds the pointers board text parses into.
type boardTextFlagValues struct {
	asJSON *bool
}

// boardTextFlagSet defines board text's flags on fs and returns what it parses
// into, so the registry's parity test finds exactly one installer per verb.
func boardTextFlagSet(fs *flag.FlagSet) *boardTextFlagValues {
	v := &boardTextFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the text elements as a JSON document")
	return v
}

// boardAnnotateFlagValues holds the pointers board annotate parses into.
type boardAnnotateFlagValues struct {
	text *string
	x    *float64
	y    *float64
}

// boardAnnotateFlagSet defines board annotate's flags on fs and returns what it
// parses into.
func boardAnnotateFlagSet(fs *flag.FlagSet) *boardAnnotateFlagValues {
	v := &boardAnnotateFlagValues{}
	v.text = fs.String("text", "", "the text to append")
	v.x = fs.Float64("x", 0, "the appended element's x coordinate")
	v.y = fs.Float64("y", 0, "the appended element's y coordinate")
	return v
}

// boardDeps gathers the cmd-owned values the board verbs read: the effects
// and helpers that stay in cmd/relevo because other commands use them, and
// the board package's own default effects.
func boardDeps() board.Deps {
	return board.Deps{
		Listen:           board.Listen,
		Open:             board.Open,
		ProcStart:        procStartUnix,
		RepoRoot:         board.RepoRoot,
		GitConfig:        board.GitConfig,
		ThemeSets:        boardThemeSetsOf,
		Now:              time.Now,
		LiveRoot:         board.LiveRoot,
		DialOwner:        dialOwnerAdHoc,
		OpenDB:           openReadOnlyDB,
		DialBudget:       verbDialBudget,
		OwnerSocket:      ownerSocket,
		MastermindLookup: mastermindLookup,
	}
}

// boardThemeSetsOf adapts the staying stored-set read to the dependency the
// board package takes: the repository's board theme name and the palette set.
func boardThemeSetsOf(cwd string) (string, map[string]board.Theme, error) {
	repo, stored, err := boardThemeSets(cwd)
	if err != nil {
		return "", nil, err
	}
	return repo.BoardTheme, stored, nil
}

// cmdBoard parses the verb and runs the foreground server. The subverbs
// dispatch first.
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
	case "text":
		return cmdBoardText(args[1:])
	case "annotate":
		return cmdBoardAnnotate(args[1:])
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
	return boardRefusal(board.Run(boardDeps(), cwd, *v.mastermind, *v.theme, *v.board, arg, *v.noOpen, nil))
}

// cmdBoardURL prints the live board's URL for a shell copy when the statusline
// is not at hand (S9).
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

// cmdBoardComments prints every comment in the resolved scene in scene order.
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

// cmdBoardComment appends one comment to the resolved scene and prints
// comment: <id>  <path>.
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
	in := board.CommentInput{Text: *v.text, By: *v.by, BoardName: *v.board, Arg: arg}
	if flagGiven(fs, "x") {
		in.X = v.x
	}
	if flagGiven(fs, "y") {
		in.Y = v.y
	}
	return boardRefusal(board.WriteComment(boardDeps(), cwd, in))
}

// cmdBoardText lists a scene's text elements: one JSON document under --json,
// one tabwriter row each otherwise.
func cmdBoardText(args []string) error {
	fs := flag.NewFlagSet("relevo board text", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := boardTextFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	positional := fs.Args()
	if len(positional) != 1 {
		return fail(codeUsage, "relevo board text needs one scene path")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	return boardRefusal(board.Text(boardDeps(), cwd, positional[0], *v.asJSON))
}

// cmdBoardAnnotate appends one text element to a scene and prints its id. The
// presence of --text/--x/--y is read here, where the flags live; the values
// are checked in the board package before any repository, theme or file work.
func cmdBoardAnnotate(args []string) error {
	fs := flag.NewFlagSet("relevo board annotate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := boardAnnotateFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	positional := fs.Args()
	if len(positional) != 1 {
		return fail(codeUsage, "relevo board annotate needs one scene path")
	}

	var textSet, xSet, ySet bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "text":
			textSet = true
		case "x":
			xSet = true
		case "y":
			ySet = true
		}
	})

	cwd, err := os.Getwd()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	req := board.AnnotateRequest{Text: *v.text, HasText: textSet, HasX: xSet, HasY: ySet, X: *v.x, Y: *v.y}
	return boardRefusal(board.AnnotateScene(boardDeps(), cwd, positional[0], req))
}

// cmdBoardPromote copies a live board into the repo.
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
	return boardRefusal(board.PromoteToRepo(boardDeps(), cwd, *v.mastermind, *v.board, *v.to, *v.force))
}

// boardRefusal maps a board package refusal to the catalog code it earns: a
// usage error exits 2 with the usage hint, a missing or invalid scene is a
// refused error with no next command, a held file whose owner did not answer
// keeps its conflict code, and anything else is internal. An error the staying
// helpers already classified (a cliError from the registry lookup or the
// stored-set read) reaches the user as it is.
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

// boardInvalidText is the board.ErrInvalid message without its sentinel word,
// so the refusal names the scene's fault rather than the sentinel.
func boardInvalidText(err error) string {
	return strings.TrimPrefix(err.Error(), board.ErrInvalid.Error()+": ")
}

// boardThemeSets is where the theme resolution reads this machine's stored
// sets: the repository's settings (its board_theme) through the repository
// identity, and the palettes of the `themes` section. It reads the machine's
// own database when one exists and never writes: a board verb keeps creating
// nothing. A held database (a daemon has the file) is read through the owner
// an ad-hoc dial reaches; the owner is never started, and when it does not
// answer the read refuses with the registry's conflict rather than resolving
// against the seed. The seed file is read only where the resolution keeps a
// fallback: no database exists yet, or the section was never seeded.
func boardThemeSets(cwd string) (relevo.RepoSettings, map[string]board.Theme, error) {
	repo := relevo.RepoSettings{}
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return repo, nil, err
	}
	path := filepath.Join(root, "relevo.db")
	if !fileExists(path) {
		return repo, theme.Seed(), nil
	}
	d, err := openReadOnlyDB(path, db.Options{})
	if errors.Is(err, db.ErrNotConverted) {
		return repo, nil, failWrap(codeRefused, err, "open %s", path)
	}
	if errors.Is(err, db.ErrLocked) {
		// The daemon holds the file, so the section and the repository's
		// settings live behind the owner: read through an ad-hoc dial, which
		// never starts the owner. A dial that gets no answer refuses with the
		// registry's conflict rather than substituting the seed, which would
		// paint a section that exists wrong.
		d, err = dialOwnerAdHoc(context.Background(), root, verbDialBudget)
		if err != nil {
			return repo, nil, boardHeldRefusal(root, err)
		}
	} else if err != nil {
		return repo, nil, failWrap(codeInternal, err, "open %s", path)
	}
	defer func() { _ = d.Close() }()

	rt := relevo.Runtime{
		Git:    git.NewClient("git", 10*time.Second, git.DefaultMaxPatchBytes),
		Config: config.Open(d),
	}
	repo, err = relevo.RepoSettingsFor(context.Background(), rt, cwd)
	if err != nil {
		return repo, nil, err
	}
	stored, err := boardThemeStored(rt)
	if err != nil {
		return repo, nil, err
	}
	return repo, stored, nil
}

// boardHeldRefusal is the refusal a stored-set read raises when the machine
// file is held and no owner answers, carrying the registry's own refusal out:
// the held file and the unanswered socket, classified conflict, with the lock
// error as its cause so a caller probing db.ErrLocked still finds it. The
// stored sets are never resolved against the seed here, so a held database
// with a section the owner serves paints that section or refuses, never the
// seed.
func boardHeldRefusal(root string, lockErr error) error {
	path := filepath.Join(root, "relevo.db")
	sock, sockErr := ownerSocket(root)
	if sockErr != nil {
		return failWrap(codeConflict, lockErr, "relevo.db is locked (%s) and no owner socket is available: %v", path+".lock", sockErr)
	}
	return failWrap(codeConflict, lockErr, "relevo.db is locked (%s) and the owner socket at %s did not answer", path+".lock", sock)
}

// boardThemeStored is the palette set a lookup resolves against: the seeded
// `themes` section when the database carries one, else the embedded seed
// file, which is the only fallback a board verb reads. A runtime with no
// database -- or one that has never been seeded -- resolves against the seed
// file the same way.
func boardThemeStored(rt relevo.Runtime) (map[string]board.Theme, error) {
	if rt.Config != nil {
		body, present, err := rt.Config.Body(config.Themes)
		if err != nil {
			return nil, err
		}
		if present && len(bytes.TrimSpace(body)) > 0 {
			return config.ParseThemes(body)
		}
	}
	return theme.Seed(), nil
}
