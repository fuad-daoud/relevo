package board

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/mastermind"
)

// htmlBoardFile is the one file name an HTML board has, in both scopes. A path
// must name the file -- never a bare slug directory -- so a path can never
// collide with a subverb name the way a bare slug could.
const htmlBoardFile = "board.html"

// htmlBoardUsage is the shape refusal both resolvers quote, so the two scopes
// name the same rule the same way.
const htmlBoardUsage = "board path must end in /%s: %s"

// ResolveHTML turns arg into the repo-scope board path. It mirrors Resolve: the
// path is resolved against cwd and must, after symlinks are resolved on its
// deepest existing ancestor, sit under repoRoot. The shape is stricter than the
// Excalidraw one -- <slug>/board.html, with the parent directory a scene slug --
// so a path always names the board file itself. A missing file is accepted: it
// is a new board, and nothing is created until Promote writes one.
func ResolveHTML(repoRoot, cwd, arg string) (Resolved, error) {
	root := filepath.Clean(repoRoot)
	if real, err := filepath.EvalSymlinks(repoRoot); err == nil {
		root = real
	}
	if arg == "" {
		return Resolved{}, usagef("a repo board needs an explicit <slug>/%s path", htmlBoardFile)
	}

	path := arg
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)

	slug, err := htmlBoardSlug(path, arg)
	if err != nil {
		return Resolved{}, err
	}

	tail, anchor, err := splitExisting(path)
	if err != nil {
		return Resolved{}, usagef("%v", err)
	}
	real, err := filepath.EvalSymlinks(anchor)
	if err != nil {
		return Resolved{}, usagef("resolve %s: %v", anchor, err)
	}
	full := filepath.Join(real, tail)
	if !underRoot(root, full) {
		return Resolved{}, usagef("%s is outside the repository root %s", path, root)
	}
	return Resolved{Scope: ScopeRepo, Scene: slug, Path: path}, nil
}

// ResolveLiveHTMLArg classifies arg as a live board path. It returns ok true
// with the resolved board when arg names <liveRoot>/<id>/<slug>/board.html, and
// ok false when arg is not under the live root at all, so the caller falls back
// to the repo scope. A path under the live root that is not live-shaped -- a
// malformed id, a bad slug, a wrong file name, nested directories, or a
// symlinked parent that escapes -- is a usage refusal.
func ResolveLiveHTMLArg(liveRoot, cwd, arg string) (Resolved, bool, error) {
	path := arg
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)

	// Classification is textual (the raw live root against the raw path), so a
	// path that is under the live root by name but escapes it through a symlink
	// is still classified live and refused below, not silently treated as a repo
	// path. Confinement below is the resolving check.
	rawRoot := filepath.Clean(liveRoot)
	rel, err := filepath.Rel(rawRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Resolved{}, false, nil
	}

	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Resolved{}, false, usagef("live board path must be <live root>/<mastermind-id>/<slug>/%s: %s", htmlBoardFile, arg)
	}
	id, slug := parts[0], parts[1]
	if err := mastermind.ValidID(id); err != nil {
		return Resolved{}, false, usagef("%v", err)
	}
	if parts[2] != htmlBoardFile {
		return Resolved{}, false, usagef(htmlBoardUsage, htmlBoardFile, arg)
	}
	if err := ValidSceneName(slug); err != nil {
		return Resolved{}, false, err
	}

	// Confinement: after symlinks are resolved on the deepest existing ancestor
	// the path must still sit under <live root>/<id>, so a symlinked parent can
	// never steer a read or a write outside the live directory.
	realRoot := evalExisting(liveRoot)
	liveDir := filepath.Join(realRoot, id)
	full := evalExisting(path)
	if !underRoot(liveDir, full) {
		return Resolved{}, false, usagef("%s is outside the live directory %s", path, liveDir)
	}
	// Both paths leave canonicalized, so callers never compare two spellings of
	// one directory.
	return Resolved{Scope: ScopeLive, Scene: slug, Path: full, LiveDir: liveDir}, true, nil
}

// ResolveLiveHTMLDir resolves name inside one MasterMind's live directory as
// <liveDir>/<name>/board.html. Like ResolveLiveDir it refuses a bad slug and two
// scopes that nest, so the scopes can never overlap.
func ResolveLiveHTMLDir(repoRoot, liveDir, name string) (Resolved, error) {
	if err := ValidSceneName(name); err != nil {
		return Resolved{}, err
	}
	if err := DisjointScopes(repoRoot, liveDir); err != nil {
		return Resolved{}, err
	}
	liveDir = filepath.Clean(liveDir)
	return Resolved{
		Scope:   ScopeLive,
		Scene:   name,
		Path:    filepath.Join(liveDir, name, htmlBoardFile),
		LiveDir: liveDir,
	}, nil
}

// htmlBoardSlug reads the scene slug off a resolved board.html path and refuses
// the two ways the shape can be wrong: a file name that is not board.html, and a
// parent directory that is not a scene slug.
func htmlBoardSlug(path, arg string) (string, error) {
	if filepath.Base(path) != htmlBoardFile {
		return "", usagef(htmlBoardUsage, htmlBoardFile, arg)
	}
	slug := filepath.Base(filepath.Dir(path))
	if err := ValidSceneName(slug); err != nil {
		return "", err
	}
	return slug, nil
}

// LoadHTML reads the board at path under the same cap as a scene write, so an
// oversized board is refused whole rather than read in part. A missing file is
// not an error: it is a new board with an empty etag.
func LoadHTML(path string) (data []byte, etag string, isNew bool, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", true, nil
		}
		return nil, "", false, err
	}
	if fi.Size() > maxBody {
		return nil, "", false, fmt.Errorf("board %s is %d bytes, over the %d byte cap", path, fi.Size(), maxBody)
	}
	return Load(path)
}

// Promote copies a live board to a repo board atomically, refusing the same
// refusals it always has. It reports only the error, so the callers that do not
// care whether notes came along keep one-line call sites; the CLI uses
// PromoteWithAnnotations to learn whether to print a second line.
func Promote(src, dst string, force bool) error {
	_, err := PromoteWithAnnotations(src, dst, force)
	return err
}

// PromoteWithAnnotations copies a live board to a repo board atomically:
// board.html, and the annotations.json beside it when the source has one. A
// missing source is ErrNotFound and an existing target without force is
// ErrInvalid, so both refusals surface as the CLI's refused code rather than a
// crash. The destination's own shape is the caller's to resolve: this only
// moves bytes.
//
// Both targets are checked before either file is written, so a promotion that
// cannot finish leaves nothing half-done on the way out.
func PromoteWithAnnotations(src, dst string, force bool) (copiedAnnotations bool, err error) {
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return false, fmt.Errorf("%w: board %s does not exist", ErrNotFound, src)
		}
		return false, err
	}
	if err := checkPromoteTarget(dst, force); err != nil {
		return false, err
	}

	srcAnn := AnnotationsPath(src)
	dstAnn := AnnotationsPath(dst)
	srcAnnInfo, srcAnnErr := os.Lstat(srcAnn)
	srcHasAnn := srcAnnErr == nil
	if srcAnnErr != nil && !os.IsNotExist(srcAnnErr) {
		return false, srcAnnErr
	}
	// A symlinked source annotations file is refused here, before the board is
	// written: promoting a link would give the repo board notes that live
	// somewhere else entirely, and finding that out after the copy is too late.
	if srcHasAnn && srcAnnInfo.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%w: %s is a symlink", ErrInvalid, srcAnn)
	}
	if srcHasAnn {
		if err := checkPromoteTarget(dstAnn, force); err != nil {
			return false, err
		}
	}

	data, _, _, err := LoadHTML(src)
	if err != nil {
		return false, err
	}
	if err := writeAtomic(dst, data); err != nil {
		return false, err
	}
	if !srcHasAnn {
		// No source annotations: a stale target one is removed under force, so a
		// promoted board never inherits another board's pins.
		if force {
			if err := os.Remove(dstAnn); err != nil && !os.IsNotExist(err) {
				return false, err
			}
		}
		return false, nil
	}
	ann, err := loadAnnotationsForCopy(srcAnn)
	if err != nil {
		return false, err
	}
	if err := writeAtomic(dstAnn, ann); err != nil {
		return false, err
	}
	return true, nil
}

// checkPromoteTarget refuses an existing target without force, naming both the
// path and the flag, the same way the board's own rule does.
func checkPromoteTarget(path string, force bool) error {
	if _, err := os.Stat(path); err == nil {
		if !force {
			return fmt.Errorf("%w: %s already exists; pass --force to overwrite it", ErrInvalid, path)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

// loadAnnotationsForCopy reads an annotations file whole, under the same cap as
// a board, so an oversized file is refused rather than copied in part. The
// symlink refusal is done by the caller, before anything is written, so that
// path stays a single rule rather than two.
func loadAnnotationsForCopy(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %s is a symlink", ErrInvalid, path)
	}
	if fi.Size() > maxBody {
		return nil, fmt.Errorf("%s is %d bytes, over the %d byte cap",
			path, fi.Size(), maxBody)
	}
	return os.ReadFile(path)
}
