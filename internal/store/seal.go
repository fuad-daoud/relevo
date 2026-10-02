package store

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// roundBaseRe matches the basename of a round file: the NNN- prefix every file
// relevo writes for a round carries. It is also the rule for a round's
// artifact directory, one level under the binding dir.
var roundBaseRe = regexp.MustCompile(`^\d{3}-`)

// RoundFiles is what is in name's directory -- the flat round files and every
// file under its round artifact directories -- plus the sealed names in the
// database, sorted and de-duplicated. A reserved round-file name is never a
// disk round file, so it is listed only when a row holds it.
func (s *Store) RoundFiles(name string) ([]string, error) {
	seen := map[string]bool{}

	onDisk, err := s.regularDiskFiles(name)
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
	files, err := s.regularDiskFiles(name)
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
// latest closed round (b.Round-1) while the binding is not DONE -- the mastermind
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
// trailer is in its content and the cursor has reached EOF, or every byte past
// the cursor is a trailer line, or the trailer is present and the stream has been
// quiet for staleStreamAfter.
//
// Only the stream's end is read. The exit trailer is the supervisor's last
// write, so a bounded tail carries both it and the trailer lines the drain can
// still be waiting on; reading a builder's whole output every tick is what kept
// the daemon allocating megabytes a second. A cursor older than the tail cannot
// be spoken for by the lines the tail shows, so it falls through to the
// staleness rule rather than claiming a drained stream it cannot see.
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

	size := info.Size()
	start := size - streamTailBytes
	if start < 0 {
		start = 0
	}
	tail, err := readStreamTail(path, start, size)
	if err != nil {
		return false
	}
	if !bytes.Contains(tail, []byte("\n"+spawn.ExitTrailer)) {
		return false
	}
	off := b.Builder.StreamOffset
	if off < 0 {
		off = 0
	}
	if off >= size {
		return true
	}
	if off >= start && trailerLinesOnly(string(tail[off-start:])) {
		return true
	}
	return time.Since(info.ModTime()) >= staleStreamAfter
}

// streamTailBytes is how much of a builder stream's end StreamDrained reads.
// A trailer plus its rusage line is a few hundred bytes; the allowance covers a
// builder that flushes a burst after the supervisor's exit marker.
const streamTailBytes = 64 << 10

// readStreamTail returns path's bytes from start to size, the end of a stream
// StreamDrained reasons over.
func readStreamTail(path string, start, size int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(f, size-start))
}

// staleStreamAfter is how long a stream that carries its exit trailer must go
// unwritten before StreamDrained stops waiting for the drain to catch up.
const staleStreamAfter = time.Hour

// trailerLinesOnly reports whether every line in s is one the supervisor's
// exit leaves behind: an empty line, or a rusage or exit trailer line.
func trailerLinesOnly(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, spawn.ExitTrailer) ||
			strings.HasPrefix(line, spawn.RusageTrailerPrefix) {
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
// returned: the seal itself succeeded. Non-NNN files are never sealed, and
// neither is a reserved round-file name: relevo authors those keys only as
// rows, so a file with one of those names is skipped by the walk and left
// where it is.
func (t *Tx) SealRound(name string, round int) (int, error) {
	files, err := t.s.roundFilesOfDir(name, round)
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

	root, rerr := t.s.OutRoot(name)
	if rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		return 0, rerr
	}
	if root != nil {
		defer func() { _ = root.Close() }()
	}

	now := time.Now().UTC()
	sealed := make([]diskRoundFile, 0, len(files))
	err = d.Tx(func(dtx *db.Tx) error {
		sealed = sealed[:0]
		for _, f := range files {
			body, info, skip, rerr := t.s.sealRead(root, name, f.path)
			if rerr != nil {
				return fmt.Errorf("seal %s: %w", f.name, rerr)
			}
			if skip {
				slog.Warn("seal: skipping non-regular round file", "binding", name, "file", f.name)
				continue
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
	// leave empty, bottom up. Remove never removes a non-empty directory, so
	// anything still in one stays. A removal error is logged, never returned.
	for _, f := range sealed {
		if rerr := t.s.sealRemove(root, name, f.path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			slog.Warn("seal: could not remove sealed round file", "binding", name, "file", f.name, "err", rerr)
		}
	}
	t.s.removeEmptyRoundDirs(name, round)
	return len(sealed), nil
}

// sealRead reads a round file for sealing: through the out/ root when it lives
// under out/, raw from the binding directory (root-owned) otherwise. A plant
// the root refuses -- a symlink swapped in after the walk, a fifo -- is
// reported as skip, so an escaping link is neither sealed into a row nor
// removed. skip is false and err nil for a regular file.
func (s *Store) sealRead(root *os.Root, binding, path string) ([]byte, fs.FileInfo, bool, error) {
	if rel, ok := s.outRelOf(binding, path); ok && root != nil {
		f, info, ok, err := rootRegularFile(root, rel)
		if err != nil {
			if errors.Is(err, errNotRegular) {
				return nil, nil, true, nil
			}
			return nil, nil, false, err
		}
		if !ok {
			return nil, nil, false, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
		}
		defer func() { _ = f.Close() }()
		body, rerr := io.ReadAll(f)
		if rerr != nil {
			return nil, nil, false, rerr
		}
		return body, info, false, nil
	}
	body, rerr := os.ReadFile(path)
	if rerr != nil {
		return nil, nil, false, rerr
	}
	info, serr := os.Stat(path)
	if serr != nil {
		return nil, nil, false, serr
	}
	return body, info, false, nil
}

// sealRemove removes a sealed round file: through the out/ root when it is in
// out/, raw otherwise.
func (s *Store) sealRemove(root *os.Root, binding, path string) error {
	if rel, ok := s.outRelOf(binding, path); ok && root != nil {
		return root.Remove(rel)
	}
	return os.Remove(path)
}

// removeEmptyRoundDirs removes round's artifact directories under name once
// they are empty, bottom up, in both homes -- the binding directory and its
// out/ child -- with os.Remove: a directory that still holds an unsealed file
// -- a skipped symlink or dot-file -- is never removed.
func (s *Store) removeEmptyRoundDirs(name string, round int) {
	if entries, err := os.ReadDir(s.Dir(name)); err == nil {
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
	// The out/ home goes through its root, so a directory swapped for a symlink
	// out of out/ is refused rather than descended.
	root, err := s.OutRoot(name)
	if err != nil {
		return
	}
	defer func() { _ = root.Close() }()
	entries, err := rootReadDir(root, ".")
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
		removeEmptyDirsBelowRoot(root, e.Name())
	}
}

// removeEmptyDirsBelowRoot removes dir's empty subdirectories and then dir
// itself, bottom up, through the out/ root: a directory swapped for a symlink
// that escapes the root is refused, and a directory that still holds a file is
// never removed. It never returns an error; a removal that fails is logged.
func removeEmptyDirsBelowRoot(root *os.Root, dir string) {
	entries, err := rootReadDir(root, dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			removeEmptyDirsBelowRoot(root, joinRel(dir, e.Name()))
		}
	}
	if derr := root.Remove(dir); derr != nil && !errors.Is(derr, fs.ErrNotExist) {
		slog.Warn("seal: could not remove empty round artifact dir", "dir", filepath.Join(root.Name(), filepath.FromSlash(dir)), "err", derr)
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
