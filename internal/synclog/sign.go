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

// keyed is the key an exporter, reconciler or importer works with. No key is a
// locked machine.
type keyed struct {
	key []byte
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
