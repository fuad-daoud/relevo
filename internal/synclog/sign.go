package synclog

import (
	"errors"
	"fmt"
)

// ErrLockedSignedOrigin refuses a keyless append where its own log already
// carries signed entries: the unsigned entry would be refused by every unlocked
// reader.
var ErrLockedSignedOrigin = errors.New("this machine's log is signed; run relevo db sync unlock")

// ErrEpochMoved reports a remote rekey past this machine's held newest epoch. It
// stops signing and holds the import whole: a stale reader must not apply entries
// current readers refuse.
var ErrEpochMoved = errors.New("sync: the remote's key moved to a newer epoch; this machine holds the old one: run relevo db sync unlock with the new passphrase")

// ErrBadSignature reports an entry an unlocked reader refuses: a bad MAC, or
// unsigned where its origin signs.
var ErrBadSignature = errors.New("synclog: an entry's signature was refused")

// ErrBoundary refuses a signed batch on an unpredicted boundary, or one naming
// none where the origin has no boundary. Nothing is written; the exporter predicts
// again next attempt.
var ErrBoundary = errors.New("synclog: the signed-from boundary was not the one predicted; it is retried on the next attempt")

// keyed is the key an exporter, reconciler or importer works with; no key is a
// locked machine.
type keyed struct {
	key []byte
	// ring is every epoch key held, keyed by epoch, for trial verification; nil
	// reads as a one-key machine, which every epoch-1 log is. key is always the
	// newest epoch's entry.
	ring map[int][]byte
	// boundaryKnown is set once the remote is known to hold this origin's
	// boundary, so later appends do not read it again.
	boundaryKnown bool
	// docs, when set, replaces the process-wide registry, so several machines run
	// in one process.
	docs map[string]SharedDoc
}

func (k *keyed) UseDocs(docs ...SharedDoc) {
	k.docs = make(map[string]SharedDoc, len(docs))
	for _, d := range docs {
		k.docs[d.Name()] = d
	}
}

func (k *keyed) docFor(tbl string) (SharedDoc, error) {
	if k.docs == nil {
		return SharedDocFor(tbl)
	}
	name, ok := DocName(tbl)
	if !ok {
		return SharedDocFor(tbl)
	}
	if doc, known := k.docs[name]; known {
		return doc, nil
	}
	return nil, fmt.Errorf("synclog: no shared doc named %q: %w", name, ErrInvalid)
}

func (k *keyed) docNamed(name string) (SharedDoc, bool) {
	if k.docs != nil {
		doc, ok := k.docs[name]
		return doc, ok
	}
	doc, ok := sharedDocs[name]
	return doc, ok
}

// SetKey sets the sync key; nil or empty is a locked machine.
func (k *keyed) SetKey(key []byte) { k.key = key }

// Keyring is every epoch key a verifier holds, handed across by the local
// keyring's reader so this package never reaches for secrets.
type Keyring struct {
	// Epoch is the newest epoch in Keys.
	Epoch int
	// Keys maps each epoch this machine holds to its key.
	Keys map[int][]byte
}

// SetKeyring sets the trial ring, carrying the newest key into the signing key.
// A ring naming no backed epoch is refused, never half-stored: such a verifier
// would verify nothing signed since the epoch moved.
func (k *keyed) SetKeyring(ring Keyring) {
	if ring.Epoch < 1 || len(ring.Keys[ring.Epoch]) == 0 {
		return
	}
	for epoch := range ring.Keys {
		if epoch > ring.Epoch {
			return
		}
	}
	k.key = ring.Keys[ring.Epoch]
	k.ring = ring.Keys
}

// sign stamps every entry with its MAC when there is a key, last, on the entries
// exactly as stored.
func (k *keyed) sign(entries []Entry) {
	if len(k.key) == 0 {
		return
	}
	for n := range entries {
		entries[n].MAC = EntryMAC(k.key, entries[n])
	}
}

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

// appendTo appends the batch, sending the predicted boundary while the remote
// holds none: the origin's head plus the first signed entry's batch index.
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
