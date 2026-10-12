package synclog

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
)

// signatureError carries the sentence a refusal reads as. It names the origin
// and seq and never key material.
type signatureError struct{ msg string }

func (e *signatureError) Error() string { return e.msg }

func (e *signatureError) Unwrap() error { return ErrBadSignature }

// holdsOrigin reports whether err holds only its own origin for the run: a body
// that could not be fetched, or a signature that was refused.
func holdsOrigin(err error) bool {
	var stalled *stallError
	var refused *signatureError
	return errors.As(err, &stalled) || errors.As(err, &refused)
}

// loadSigned computes, per origin, the sequence from which entries must be
// signed: the lowest of this machine's own record and every remote boundary
// that verifies under its key. A boundary that does not verify holds its origin
// for the run, and one that does is recorded locally at once, so deleting the
// remote row afterwards changes nothing.
func (i *Importer) loadSigned(state MarkState) error {
	if len(i.key) == 0 {
		return nil
	}
	local, err := i.db.SignedFroms()
	if err != nil {
		return fmt.Errorf("synclog: import: %w", err)
	}
	i.localSigned = local
	i.signedFrom = make(map[string]int, len(state.SignedFrom)+len(local))
	for origin, seq := range local {
		i.signedFrom[origin] = seq
	}
	i.badBoundary = make(map[string]error)
	for origin, seq := range state.SignedFrom {
		if origin == i.db.Origin() {
			continue
		}
		if seq < 1 || !VerifyBoundary(i.key, origin, seq, state.SignedTags[origin]) {
			i.badBoundary[origin] = &signatureError{fmt.Sprintf("the signed-from boundary of %q does not verify under this machine's key", origin)}
			continue
		}
		if have, ok := local[origin]; ok && have <= seq {
			continue
		}
		if err := i.db.LowerSignedFrom(origin, seq); err != nil {
			return fmt.Errorf("synclog: import: %w", err)
		}
		i.localSigned[origin] = seq
		i.signedFrom[origin] = seq
	}
	return nil
}

// verify checks one origin's entries as pulled, before any ref is resolved. A
// locked machine verifies nothing.
func (i *Importer) verify(origin string, entries []Entry) error {
	if len(i.key) == 0 {
		return nil
	}
	if err := i.badBoundary[origin]; err != nil {
		return err
	}
	from, signs := i.signedFrom[origin]
	for _, e := range entries {
		if len(e.MAC) == 0 {
			continue
		}
		if !VerifyMAC(i.key, e) {
			return &signatureError{fmt.Sprintf("entry %d from %q has a bad signature", e.Seq, origin)}
		}
		if !signs || e.Seq < from {
			from, signs = e.Seq, true
		}
	}
	for _, e := range entries {
		if len(e.MAC) == 0 && signs && e.Seq >= from {
			return &signatureError{fmt.Sprintf("entry %d from %q is unsigned but %q signs from %d", e.Seq, origin, origin, from)}
		}
	}
	return nil
}

// recordSigned lowers this machine's signed-from record for an origin to the
// lowest verified entry, inside the caller's transaction.
func (i *Importer) recordSigned(tx *db.Tx, origin string, entries []Entry) (int, bool, error) {
	if len(i.key) == 0 {
		return 0, false, nil
	}
	first, found := 0, false
	for _, e := range entries {
		if len(e.MAC) > 0 && (!found || e.Seq < first) {
			first, found = e.Seq, true
		}
	}
	if !found {
		return 0, false, nil
	}
	if have, ok := i.localSigned[origin]; ok && have <= first {
		return 0, false, nil
	}
	if err := tx.LowerSignedFrom(origin, first); err != nil {
		return 0, false, err
	}
	return first, true, nil
}

// noteSigned remembers a record the committed transaction wrote.
func (i *Importer) noteSigned(origin string, seq int, wrote bool) {
	if !wrote {
		return
	}
	i.localSigned[origin] = seq
	if have, ok := i.signedFrom[origin]; !ok || seq < have {
		i.signedFrom[origin] = seq
	}
}
