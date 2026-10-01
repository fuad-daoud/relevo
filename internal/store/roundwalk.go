package store

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// diskRoundFile is one file found under a binding's directory: the path to its
// bytes, the round_file name it uses -- a flat file's base, or
// "NNN-<actor>/<rel>" with forward slashes under a round's artifact directory
// -- and its round (0 for a flat file that is not a round file).
type diskRoundFile struct {
	path  string
	name  string
	round int
}

// roundFilesOfDir returns the binding's round files whose leading NNN- is
// round: the flat files and the files under NNN-*/ artifact directories of
// that round, in the binding directory and in its out/ child, sorted by
// round_file name.
func roundFilesOfDir(bindingDir string, round int) ([]diskRoundFile, error) {
	files, err := diskFiles(bindingDir)
	if err != nil {
		return nil, err
	}
	out := files[:0]
	for _, f := range files {
		if f.round > 0 && f.round == round {
			out = append(out, f)
		}
	}
	return out, nil
}

// diskFiles walks every round file of the binding below bindingDir: the flat
// files and everything under a top-level NNN-* directory, recursively, in the
// binding directory and in its out/ child. Names are flat: a file at
// <binding>/out/NNN-report.md has the round_file name NNN-report.md, and a file
// under <binding>/out/NNN-<actor>/ has the name NNN-<actor>/<rel>. out/ wins
// when a name is present in both homes. It returns them sorted by round_file
// name. A missing directory is an empty list, not an error. A flat file whose
// name has no NNN- prefix has round 0; a file under an NNN-* directory takes
// that directory's round.
//
// Security: a reserved round-file name is skipped with one warning per path,
// because relevo authors those keys only as rows and a file with one of those
// names is a plant or a stale copy; a symlink, file or dir, is skipped with one
// warning per path; a dot-file or dot-dir is skipped; a relative path
// containing ".." is refused.
func diskFiles(bindingDir string) ([]diskRoundFile, error) {
	byName := map[string]diskRoundFile{}
	add := func(f diskRoundFile) { byName[f.name] = f }
	if err := walkRoundDir(bindingDir, add); err != nil {
		return nil, err
	}
	// The out/ home is walked second so it wins on a name present in both.
	if err := walkRoundDir(filepath.Join(bindingDir, outDirName), add); err != nil {
		return nil, err
	}

	out := make([]diskRoundFile, 0, len(byName))
	for _, f := range byName {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// walkRoundDir reads dir's flat files and descends into its top-level NNN-*
// artifact directories, one level under dir and everything below them. It is
// the one walk; diskFiles calls it once per home.
func walkRoundDir(dir string, fn func(diskRoundFile)) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read binding dir %s: %w", dir, err)
	}

	for _, e := range entries {
		base := e.Name()
		if strings.HasPrefix(base, ".") {
			continue
		}
		path := filepath.Join(dir, base)
		if e.Type()&fs.ModeSymlink != 0 {
			slog.Warn("round walk: skipping symlink", "path", path)
			continue
		}
		if e.IsDir() {
			// Only a directory whose name matches ^\d{3}- is a round artifact
			// directory; anything else under the binding -- out/ included -- is
			// not ours.
			if !roundBaseRe.MatchString(base) {
				continue
			}
			round, ok := roundOfFile(base)
			if !ok {
				continue
			}
			if err := walkArtifactDir(path, base, round, fn); err != nil {
				return err
			}
			continue
		}
		// A reserved name is row-only: relevo never writes one to disk, so a
		// file with it is a plant or a stale copy and never a round file.
		if reservedRoundFile(base) {
			slog.Warn("round walk: skipping reserved round file", "path", path)
			continue
		}
		// round is 0 for a flat file that is not a round file: it is listed,
		// but it is not sealed by any round.
		round, _ := roundOfFile(base)
		fn(diskRoundFile{path: path, name: base, round: round})
	}
	return nil
}

// walkArtifactDir walks one NNN-<actor>/ directory and everything below it,
// naming each file NNN-<actor>/<rel> with forward slashes. Symlinks are not
// followed, dot entries are skipped, and a relative path containing ".." is
// refused.
func walkArtifactDir(dir, rel string, round int, fn func(diskRoundFile)) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read artifact dir %s: %w", dir, err)
	}

	for _, e := range entries {
		base := e.Name()
		if strings.HasPrefix(base, ".") {
			continue
		}
		path := filepath.Join(dir, base)
		// A nested file's round_file name is "NNN-<actor>/<rel>". Its row goes
		// in round_file, not the cockpit spec's `artifact` table: round_file is
		// the record today, and this is a deliberate refinement of that wording.
		name := rel + "/" + base
		if e.Type()&fs.ModeSymlink != 0 {
			slog.Warn("round walk: skipping symlink", "path", path)
			continue
		}
		if e.IsDir() {
			if err := walkArtifactDir(path, name, round, fn); err != nil {
				return err
			}
			continue
		}
		if containsDotDot(name) {
			continue
		}
		fn(diskRoundFile{path: path, name: name, round: round})
	}
	return nil
}
