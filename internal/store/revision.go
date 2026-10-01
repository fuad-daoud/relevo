package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
)

// Revision is a cheap digest of everything a mirror run reads from this store
// for a live binding: the binding record, its log's extent and its sealed round
// files. Two equal revisions mean a fresh mirror run would write nothing, so a
// caller that keeps the last revision it mirrored may skip a run.
//
// It reads one row and a stat -- the log is measured by event count and highest
// seq, sealed files by count and highest name -- so it is far cheaper than the
// run it guards. The binding directory's own mtime stands for its flat round
// files, the ones an outcome reads; a file under a round artifact directory only
// names the round, which the log names too.
//
// It is deliberately conservative in one direction only: a signal it omits can
// delay a mirror until the next signal changes, so every write that changes
// what the mirror reads must change one of these. An event confirmed in place
// (the flag changes, not the count) is the known omission; the mirror's own
// cursor never saw those either, because it only hashes a member's head.
//
// ErrNotFound when the record is gone, which the caller treats as nothing to
// mirror rather than a failure.
func (s *Store) Revision(name string) (string, error) {
	d, err := s.dbForRead()
	if err != nil {
		return "", err
	}
	if d == nil {
		return "", ErrNotFound
	}

	rec, ok, err := d.RecordRevision(s.owner, name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrNotFound
	}

	var buf []byte
	buf = append(buf, rec.JSON...)
	buf = fmt.Appendf(buf, "\x00log %d/%d files %d/%s\x00", rec.EventCount, rec.EventMaxSeq, rec.FileCount, rec.FileMaxName)
	if fi, serr := os.Stat(s.Dir(name)); serr == nil {
		buf = fmt.Appendf(buf, "\x00dir %d/%d", fi.Size(), fi.ModTime().UnixNano())
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}
