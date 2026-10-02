package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// roundOfFile parses the leading NNN- of a binding directory entry and returns
// the round number. It accepts every suffix the store writes without
// enumerating them, so a future round artifact is copied by forks unchanged.
func roundOfFile(base string) (int, bool) {
	idx := strings.IndexByte(base, '-')
	if idx <= 0 || idx == len(base)-1 {
		return 0, false
	}
	for i := 0; i < idx; i++ {
		if base[i] < '0' || base[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(base[:idx])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// ForkState copies src's history into a NEW binding for dst under the state
// lock.
func (s *Store) ForkState(src string, dst Binding, throughRound int) error {
	return s.WithLock(func(tx *Tx) error {
		return tx.ForkState(src, dst, throughRound)
	})
}

// ForkState copies src's history into a NEW binding for dst: every log entry
// with Round <= throughRound and every round file whose leading number is
// <= throughRound. Copied entries are all marked Confirmed, so a fork begins
// with nothing pending, and an entry whose Path pointed into src's directory
// is rewritten into dst's. dst's record is NOT written here -- the caller owns
// the new Binding.
//
// The history goes straight into the database: the record Save writes, the
// copied entries as that record's events, and one round_file row per copied
// file. Save creates dst's directory and the fork leaves it empty; src is
// never modified.
//
// The lock must be held, src must exist, and dst.Name must have neither a
// record nor a directory. On error dst's record is deleted, which cascades its
// events and round files, so a failed fork leaves nothing List could pick up.
func (t *Tx) ForkState(src string, dst Binding, throughRound int) error {
	if err := ValidName(dst.Name); err != nil {
		return err
	}
	if throughRound < 1 {
		return fmt.Errorf("through round must be >= 1, got %d", throughRound)
	}

	if _, err := t.s.load(src); err != nil {
		return err
	}

	dstDir := t.s.Dir(dst.Name)
	if _, err := os.Lstat(dstDir); err == nil {
		return fmt.Errorf("binding directory %q already exists", dst.Name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat destination dir %s: %w", dstDir, err)
	}

	// A record with no directory of its own still owns the name: Save would
	// refuse it, but only after the copy had begun.
	if d, err := t.s.dbForRead(); err != nil {
		return err
	} else if d != nil {
		if _, ok, err := d.RecordGet(t.s.owner, dst.Name); err != nil {
			return fmt.Errorf("read binding %q: %w", dst.Name, err)
		} else if ok {
			return fmt.Errorf("binding %q already exists", dst.Name)
		}
	}

	if err := t.Save(dst); err != nil {
		return err
	}

	var success bool
	defer func() {
		if !success {
			_ = t.Delete(dst.Name)
		}
	}()

	entries, err := t.s.readLog(src)
	if err != nil {
		return fmt.Errorf("read log %q: %w", src, err)
	}

	// Every entry through the cut, confirmed and undelivered (a fork begins
	// with nothing pending), with a Path that pointed into src's directory --
	// the binding directory or its out/ home -- rewritten into dst's.
	srcDir := t.s.Dir(src)
	srcOut := t.s.OutDir(src)
	dstOut := t.s.OutDir(dst.Name)
	copied := make([]LogEntry, 0, len(entries))
	lines := make([][]byte, 0, len(entries))
	for _, e := range entries {
		if e.Round > throughRound {
			continue
		}
		e.Confirmed = true
		e.DeliveredAt = nil
		e.Path = rewriteForkPath(e.Path, srcDir, srcOut, dstDir, dstOut)
		raw, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("encode log entry for %q: %w", dst.Name, err)
		}
		copied = append(copied, e)
		lines = append(lines, raw)
	}

	if err := t.forkRoundFiles(dst.Name, dstDir, src, srcDir, throughRound, copied, lines); err != nil {
		return err
	}

	success = true
	return nil
}

// rewriteForkPath rewrites a log entry Path that pointed into src's directory
// -- the binding directory or its out/ runner-output home -- into dst's. Any
// other path is returned unchanged.
func rewriteForkPath(path, srcDir, srcOut, dstDir, dstOut string) string {
	if path == "" {
		return path
	}
	switch filepath.Dir(path) {
	case srcDir:
		return filepath.Join(dstDir, filepath.Base(path))
	case srcOut:
		return filepath.Join(dstOut, filepath.Base(path))
	}
	return path
}

// forkRoundFiles writes the copied entries as dst's events and every selected
// round file as a round_file row, in one transaction. RoundFiles unions what
// is in src's directory with what a seal pass already moved into the database,
// so a fork cut from a closed round copies its files either way.
func (t *Tx) forkRoundFiles(dstName, dstDir, src, srcDir string, throughRound int, copied []LogEntry, lines [][]byte) error {
	d, err := t.s.dbForWrite()
	if err != nil {
		return err
	}
	rec, ok, err := d.RecordGet(t.s.owner, dstName)
	if err != nil {
		return fmt.Errorf("read binding %q: %w", dstName, err)
	}
	if !ok {
		return fmt.Errorf("%s: %w", dstName, ErrNotFound)
	}

	// The copied entries become the record's events through the same
	// conversion a decoded log.jsonl takes: the medium moved, the conversion
	// did not.
	evs, err := recordEventsOf(copied, lines)
	if err != nil {
		return fmt.Errorf("encode log for %q: %w", dstName, err)
	}
	if err := d.EventReplaceAll(rec.ID, evs); err != nil {
		return fmt.Errorf("write log for %q: %w", dstName, err)
	}

	names, err := t.s.RoundFiles(src)
	if err != nil {
		return fmt.Errorf("list round files %q: %w", src, err)
	}

	now := time.Now().UTC()
	return d.Tx(func(dtx *db.Tx) error {
		for _, base := range names {
			r, ok := roundOfFile(base)
			if !ok || r > throughRound {
				continue
			}
			// Runner-output files live in src's out/ home; every other round
			// file -- a prompt, a stream, a gate log -- stays in the binding
			// directory. ReadFile resolves a runner-output name's two homes
			// anyway, so a pre-migration source still answers.
			home := srcDir
			if runnerOutputName(base) {
				home = t.s.OutDir(src)
			}
			srcPath := filepath.Join(home, filepath.FromSlash(base))
			body, err := t.s.ReadFile(srcPath)
			if err != nil {
				return fmt.Errorf("copy %s: %w", base, err)
			}
			// StatFile exposes the source's own mtime, on disk or sealed;
			// without one the seal's stamp stands in.
			mtime := now
			if _, mt, ok, err := t.s.StatFile(srcPath); err == nil && ok {
				mtime = mt
			}
			if err := dtx.RoundFilePut(rec.ID, base, r, body, mtime, now); err != nil {
				return fmt.Errorf("copy %s for %q: %w", base, dstName, err)
			}
		}
		return nil
	})
}
