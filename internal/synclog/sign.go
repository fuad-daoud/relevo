package synclog

import (
	"errors"
	"fmt"
)

// ErrLockedSignedOrigin refuses an append from a machine with no key whose own
// log already carries signed entries: an unsigned entry it added would be
// refused by every unlocked reader.
var ErrLockedSignedOrigin = errors.New("this machine's log is signed; run relevo db sync unlock")

// ErrBadSignature reports an entry an unlocked reader refuses: its MAC does not
// hold, or it is unsigned where its origin signs.
var ErrBadSignature = errors.New("synclog: an entry's signature was refused")

// ErrBoundary refuses a signed batch whose first signed sequence is not the one
// the exporter predicted, or that names none where the origin has no boundary.
// Nothing is written, and the exporter predicts again on its next attempt.
var ErrBoundary = errors.New("synclog: the signed-from boundary was not the one predicted; it is retried on the next attempt")

// keyed is the key an exporter, reconciler or importer works with. No key is a
// locked machine.
type keyed struct {
	key []byte
	// boundaryKnown is set once the remote is known to hold this origin's
	// boundary, so later appends do not read it again.
	boundaryKnown bool
}

// SetKey sets the sync key; nil or empty is a locked machine.
func (k *keyed) SetKey(key []byte) { k.key = key }

// sign stamps every entry with its MAC when there is a key. It runs last, on
// the entries exactly as they will be stored.
func (k *keyed) sign(entries []Entry) {
	if len(k.key) == 0 {
		return
	}
	for n := range entries {
		entries[n].MAC = EntryMAC(k.key, entries[n])
	}
}

// guard refuses an append from a locked machine whose own origin is signed.
func (k *keyed) guard(t LogTransport, origin string) error {
	if len(k.key) > 0 {
		return nil
	}
	state, err := t.ReadMarks()
	if err != nil {
		return fmt.Errorf("read the signed-from marks: %w", err)
	}
	if _, signed := state.SignedFrom[origin]; signed {
		return ErrLockedSignedOrigin
	}
	return nil
}

// appendTo appends the batch, and while the remote holds no boundary for the
// origin it also sends the one it predicts: the origin's head plus the batch
// index of the first signed entry.
func (k *keyed) appendTo(t LogTransport, origin string, entries []Entry) ([]Entry, error) {
	ba, canBound := t.(BoundaryAppender)
	if len(k.key) == 0 || k.boundaryKnown || !canBound {
		return t.Append(entries)
	}
	state, err := t.ReadMarks()
	if err != nil {
		return nil, fmt.Errorf("read the signed-from marks: %w", err)
	}
	if _, has := state.SignedFrom[origin]; has {
		k.boundaryKnown = true
		return t.Append(entries)
	}
	first := -1
	for n, e := range entries {
		if len(e.MAC) > 0 {
			first = n
			break
		}
	}
	if first < 0 {
		return t.Append(entries)
	}
	seq := state.Heads[origin] + 1 + first
	out, err := ba.AppendSigned(entries, Boundary{Seq: seq, Tag: BoundaryTag(k.key, origin, seq)})
	if err == nil {
		k.boundaryKnown = true
	}
	return out, err
}
