package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/db"
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
// cursor never saw those either, because it only hashes a member's head. A
// caller that must not miss an in-place flag reads TickState, whose Rev is
// exactly this digest.
//
// ErrNotFound when the record is gone, which the caller treats as nothing to
// mirror rather than a failure.
func (s *Store) Revision(name string) (string, error) {
	rev, _, err := s.revisionParts(name)
	return rev, err
}

// TickState is one live binding as the daemon's tick must see it before
// skipping that binding's pass: the Revision digest, and the two states that
// settle or retry without ever moving the digest.
type TickState struct {
	// Rev is Revision's digest of the binding, so a value recorded from
	// either function compares against the other.
	Rev string
	// AdmitsOpen is the count of mastermind entries a push route admitted
	// and has not confirmed. The settle of an admitted entry is the done
	// binding's own tick, its read-back polls outside state, and its expiry a
	// clock, so while one stands the pass cannot be skipped -- and neither
	// the admit nor the confirm moves Rev: both patch the entry in place.
	AdmitsOpen int
	// RoundsOnDisk is how many rounds still have files in the binding
	// directory. Their seal can wait on the stream going quiet or on the
	// artifact cap being raised -- a clock and a policy, neither of which is
	// a store write -- so while one stands the directory may change without
	// anything else moving, and the pass that retries the seal must run.
	RoundsOnDisk int
}

// TickState reads the record row and walks the binding directory; a missing
// record is ErrNotFound, which the caller treats as nothing to guard.
func (s *Store) TickState(name string) (TickState, error) {
	rev, rec, err := s.revisionParts(name)
	if err != nil {
		return TickState{}, err
	}
	rounds, err := s.RoundsOnDisk(name)
	if err != nil {
		return TickState{}, err
	}
	return TickState{Rev: rev, AdmitsOpen: rec.AdmitsOpen, RoundsOnDisk: len(rounds)}, nil
}

// revisionParts reads the one row RecordRevision answers for and folds it and
// the binding directory's stat into the digest. The formula lives here so
// Revision and TickState can never hand their callers values that do not
// compare.
func (s *Store) revisionParts(name string) (string, db.RecordRevision, error) {
	d, err := s.dbForRead()
	if err != nil {
		return "", db.RecordRevision{}, err
	}
	if d == nil {
		return "", db.RecordRevision{}, ErrNotFound
	}

	rec, ok, err := d.RecordRevision(s.owner, name)
	if err != nil {
		return "", db.RecordRevision{}, err
	}
	if !ok {
		return "", db.RecordRevision{}, ErrNotFound
	}

	var buf []byte
	buf = append(buf, rec.JSON...)
	buf = fmt.Appendf(buf, "\x00log %d/%d files %d/%s\x00", rec.EventCount, rec.EventMaxSeq, rec.FileCount, rec.FileMaxName)
	if fi, serr := os.Stat(s.Dir(name)); serr == nil {
		buf = fmt.Appendf(buf, "\x00dir %d/%d", fi.Size(), fi.ModTime().UnixNano())
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), rec, nil
}
