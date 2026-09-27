package store

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/legacy"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// roundBaseRe matches the basename of a round file: the NNN- prefix every file
// relevo writes for a round carries. It is also the rule for a round's
// artifact directory, one level under the binding dir.
var roundBaseRe = regexp.MustCompile(`^\d{3}-`)

// ReadFile returns path's bytes from disk, or from the sealed round_file row
// when a seal pass already moved a round file into the database. A miss
// returns os.ReadFile's own error, so errors.Is(err, fs.ErrNotExist) keeps
// working.
func (s *Store) ReadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	d, recordID, name, ok, lerr := s.sealedLookup(path)
	if lerr != nil {
		return nil, lerr
	}
	if !ok {
		return nil, err
	}

	body, _, found, gerr := d.RoundFileGet(recordID, name)
	if gerr != nil {
		return nil, gerr
	}
	if !found {
		return nil, err
	}
	return body, nil
}

// StatFile is ReadFile for os.Stat callers: the file's size and mtime when it
// is on disk, and the sealed row's when it was sealed. A miss returns
// os.Stat's own error alongside ok == false.
func (s *Store) StatFile(path string) (size int64, mtime time.Time, ok bool, err error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.Size(), info.ModTime(), true, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return 0, time.Time{}, false, err
	}
	missErr := err

	d, recordID, name, found, lerr := s.sealedLookup(path)
	if lerr != nil {
		return 0, time.Time{}, false, lerr
	}
	if !found {
		return 0, time.Time{}, false, missErr
	}

	body, mt, ok, gerr := d.RoundFileGet(recordID, name)
	if gerr != nil {
		return 0, time.Time{}, false, gerr
	}
	if !ok {
		return 0, time.Time{}, false, missErr
	}
	return int64(len(body)), mt, true, nil
}

// sealedLookup resolves path as a round file of the binding it names under
// s.root and returns the record id whose round_file rows hold it: the name's
// live record, or its most recently archived one, where archive() put its
// round files. A flat path resolves to its base name; a path inside a
// top-level NNN-<actor>/ directory resolves to its nested "NNN-<actor>/<rel>"
// name.
//
// found is false when the path is not such a path, when the name has neither a
// live nor an archived record, and when the root has no database at all, so a
// miss costs no error and leaves the caller's own ErrNotExist in place.
func (s *Store) sealedLookup(path string) (d *db.DB, recordID, name string, found bool, err error) {
	binding, name, ok := s.bindingRelOf(path)
	if !ok {
		return nil, "", "", false, nil
	}

	d, err = s.dbForRead()
	if err != nil || d == nil {
		return nil, "", "", false, err
	}
	rec, ok, err := d.RecordGet(s.owner, binding)
	if err != nil {
		return nil, "", "", false, err
	}
	if !ok {
		rec, ok, err = d.RecordGetArchivedByName(s.owner, binding)
		if err != nil || !ok {
			return nil, "", "", false, err
		}
	}
	return d, rec.ID, name, true, nil
}

// RoundFiles is what is in name's directory -- the flat round files and every
// file under its round artifact directories -- plus the sealed names in the
// database, sorted and de-duplicated.
func (s *Store) RoundFiles(name string) ([]string, error) {
	seen := map[string]bool{}

	onDisk, err := diskFiles(s.Dir(name))
	if err != nil {
		return nil, fmt.Errorf("read binding dir %q: %w", name, err)
	}
	for _, f := range onDisk {
		seen[f.name] = true
	}

	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d != nil {
		rec, ok, err := d.RecordGet(s.owner, name)
		if err != nil {
			return nil, err
		}
		if ok {
			names, err := d.RoundFileList(rec.ID)
			if err != nil {
				return nil, err
			}
			for _, n := range names {
				seen[n] = true
			}
		}
	}

	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// RoundsOnDisk returns the round numbers present in name's directory, from its
// flat NNN-* files and from any NNN-* artifact directory holding at least one
// file, ascending; a round already sealed has none left.
func (s *Store) RoundsOnDisk(name string) ([]int, error) {
	files, err := diskFiles(s.Dir(name))
	if err != nil {
		return nil, fmt.Errorf("read binding dir %q: %w", name, err)
	}

	seen := map[int]bool{}
	for _, f := range files {
		if f.round > 0 {
			seen[f.round] = true
		}
	}

	out := make([]int, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Ints(out)
	return out, nil
}

// Sealable reports whether round's files of b can become database rows: the
// round must be closed (round < b.Round) and nothing that still reads its
// files may be in flight. The blockers are the stream drain still owning the
// round, an unfinished consult of it, a gate run for it, and the binding's
// latest closed round (b.Round-1) while the binding is not DONE -- the planner
// was handed that round's report path, and a repair round's plan points at its
// plan and gate log.
//
// PID is deliberately not consulted for the drain: a builder that cleared its
// PID without being killed keeps flushing, and only the supervisor's exit
// trailer says it is really done.
func Sealable(b Binding, round int, streamDrained bool) bool {
	if round >= b.Round {
		return false
	}
	if round == b.Round-1 && b.State != StateDone {
		return false
	}
	if b.Builder.StreamRound == round && !streamDrained {
		return false
	}
	for _, c := range b.Consults {
		if c.Round == round && consultActive(c) {
			return false
		}
	}
	if b.GateRun != nil && b.GateRun.Round == round {
		return false
	}
	return true
}

// StreamDrained reports whether round's builder stream is fully consumed by
// the drain, so nothing more will be rendered into that round's builder log.
// Only the round the endpoint is currently draining can be undrained; every
// other round is vacuously drained.
//
// A missing stream is drained. Otherwise the stream is drained when the exit
// trailer is in its content -- the relevo spelling or the pre-rename spelling
// legacy keeps -- and the cursor has reached EOF, or every byte past the
// cursor is a trailer line, or the trailer is present and the stream has been
// quiet for staleStreamAfter.
func (s *Store) StreamDrained(b Binding, round int) bool {
	if round != b.Builder.StreamRound {
		return true
	}
	path := s.StreamPath(b.Name, round)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	text := string(body)
	if !strings.Contains(text, "\n"+spawn.ExitTrailer) && !strings.Contains(text, "\n"+legacy.ExitTrailer) {
		return false
	}
	off := b.Builder.StreamOffset
	if off < 0 {
		off = 0
	}
	if off >= info.Size() {
		return true
	}
	if trailerLinesOnly(text[off:]) {
		return true
	}
	return time.Since(info.ModTime()) >= staleStreamAfter
}

// staleStreamAfter is how long a stream that carries its exit trailer must go
// unwritten before StreamDrained stops waiting for the drain to catch up.
const staleStreamAfter = time.Hour

// trailerLinesOnly reports whether every line in s is one the supervisor's
// exit leaves behind: an empty line, or a rusage or exit trailer line in
// either the relevo or the pre-rename spelling.
func trailerLinesOnly(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, spawn.ExitTrailer) ||
			strings.HasPrefix(line, legacy.ExitTrailer) ||
			strings.HasPrefix(line, spawn.RusageTrailerPrefix) ||
			strings.HasPrefix(line, legacy.RusageTrailer) {
			continue
		}
		return false
	}
	return true
}

// consultActive reports whether a consult can still need its round's files:
// done and silent are terminal, spawning and running are not.
func consultActive(c Consult) bool {
	switch c.State {
	case ConsultDone, ConsultSilent:
		return false
	}
	return true
}

// SealRound writes every round file of name's round into the database in one
// transaction, and, only after it commits, removes them from the directory.
//
// A file's round_file name is its flat base, or "NNN-<actor>/<rel>" for a file
// under a round's artifact directory. Those rows go in round_file, not the
// cockpit spec's `artifact` table: round_file is the record today, and this is
// a deliberate refinement of that wording.
//
// A read or write error leaves every file in place and is returned, so the
// next pass retries; RoundFilePut is an upsert, so a removal that failed after
// the commit re-puts the same bytes. A removal error is logged, never
// returned: the seal itself succeeded. Non-NNN files are never sealed.
func (t *Tx) SealRound(name string, round int) (int, error) {
	files, err := roundFilesOfDir(t.s.Dir(name), round)
	if err != nil || len(files) == 0 {
		return 0, err
	}

	d, err := t.s.dbForWrite()
	if err != nil {
		return 0, err
	}
	rec, ok, err := d.RecordGet(t.s.owner, name)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}

	now := time.Now().UTC()
	sealed := make([]diskRoundFile, 0, len(files))
	err = d.Tx(func(dtx *db.Tx) error {
		sealed = sealed[:0]
		for _, f := range files {
			body, rerr := os.ReadFile(f.path)
			if rerr != nil {
				return fmt.Errorf("seal %s: %w", f.name, rerr)
			}
			info, serr := os.Stat(f.path)
			if serr != nil {
				return fmt.Errorf("seal %s: %w", f.name, serr)
			}
			if perr := dtx.RoundFilePut(rec.ID, f.name, round, body, info.ModTime(), now); perr != nil {
				return perr
			}
			sealed = append(sealed, f)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	// The seal committed: remove the sealed files, then the directories they
	// leave empty, bottom up. os.Remove never removes a non-empty directory,
	// so anything still in one stays. A removal error is logged, never
	// returned.
	for _, f := range sealed {
		if rerr := os.Remove(f.path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			slog.Warn("seal: could not remove sealed round file", "binding", name, "file", f.name, "err", rerr)
		}
	}
	t.s.removeEmptyRoundDirs(name, round)
	return len(sealed), nil
}

// diskRoundFile is one file found under a binding's directory: the path to its
// bytes, the round_file name it uses -- a flat file's base, or
// "NNN-<actor>/<rel>" with forward slashes under a round's artifact directory
// -- and its round (0 for a flat file that is not a round file).
type diskRoundFile struct {
	path  string
	name  string
	round int
}

// roundFilesOfDir returns dir's round files whose leading NNN- is round: the
// flat files and the files under NNN-*/ artifact directories of that round,
// sorted by round_file name.
func roundFilesOfDir(dir string, round int) ([]diskRoundFile, error) {
	files, err := diskFiles(dir)
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

// diskFiles walks every file below dir: dir's flat files, whatever their name,
// and everything under a top-level NNN-* directory, recursively, which is
// where a round's artifact directory and its subdirectories live. It returns
// them sorted by round_file name. A missing dir is an empty list, not an
// error. A flat file whose name has no NNN- prefix has round 0; a file under an
// NNN-* directory takes that directory's round.
//
// Security: a symlink, file or dir, is skipped with one warning per path; a
// dot-file or dot-dir is skipped; a relative path containing ".." is refused.
func diskFiles(dir string) ([]diskRoundFile, error) {
	var out []diskRoundFile
	if err := walkRoundDir(dir, func(f diskRoundFile) { out = append(out, f) }); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// walkRoundDir reads dir's flat files and descends into its top-level NNN-*
// artifact directories, one level under dir and everything below them.
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
			// directory; anything else under the binding is not ours.
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

// removeEmptyRoundDirs removes round's artifact directories under name once
// they are empty, bottom up, with os.Remove: a directory that still holds an
// unsealed file -- a skipped symlink or dot-file -- is never removed.
func (s *Store) removeEmptyRoundDirs(name string, round int) {
	entries, err := os.ReadDir(s.Dir(name))
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || !roundBaseRe.MatchString(e.Name()) {
			continue
		}
		if r, ok := roundOfFile(e.Name()); !ok || r != round {
			continue
		}
		removeEmptyDirsBelow(filepath.Join(s.Dir(name), e.Name()))
	}
}

// removeEmptyDirsBelow removes dir's empty subdirectories and then dir itself,
// bottom up, never following a symlink and never returning an error: a removal
// that fails is logged.
func removeEmptyDirsBelow(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		if e.IsDir() {
			removeEmptyDirsBelow(filepath.Join(dir, e.Name()))
		}
	}
	if derr := os.Remove(dir); derr != nil && !errors.Is(derr, fs.ErrNotExist) {
		slog.Warn("seal: could not remove empty round artifact dir", "dir", dir, "err", derr)
	}
}
