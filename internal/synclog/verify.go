package synclog

import (
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
)

// signatureError carries the sentence a refusal reads as. It names the origin
// and seq and never key material.
type signatureError struct{ msg string }

func (e *signatureError) Error() string { return e.msg }

func (e *signatureError) Unwrap() error { return ErrBadSignature }

// loadSigned computes, per origin, the sequence from which entries must be
// signed: the lower of the remote's value and this machine's own record.
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
	for origin, seq := range state.SignedFrom {
		i.signedFrom[origin] = seq
	}
	for origin, seq := range local {
		if have, ok := i.signedFrom[origin]; !ok || seq < have {
			i.signedFrom[origin] = seq
		}
	}
	return nil
}

// verify checks one origin's entries as pulled, before any ref is resolved. A
// locked machine verifies nothing.
func (i *Importer) verify(origin string, entries []Entry) error {
	if len(i.key) == 0 {
		return nil
	}
	from, signs := i.signedFrom[origin]
	for _, e := range entries {
		if len(e.MAC) == 0 {
			continue
		}
		if !VerifyMAC(i.key, e) {
			return &signatureError{fmt.Sprintf("entry %d from %s has a bad signature", e.Seq, origin)}
		}
		if !signs || e.Seq < from {
			from, signs = e.Seq, true
		}
	}
	for _, e := range entries {
		if len(e.MAC) == 0 && signs && e.Seq >= from {
			return &signatureError{fmt.Sprintf("entry %d from %s is unsigned but %s signs from %d", e.Seq, origin, origin, from)}
		}
	}
	return nil
}

// recordSigned writes this machine's signed-from record for an origin it has
// none for yet, at the lowest verified entry, inside the caller's transaction.
func (i *Importer) recordSigned(tx *db.Tx, origin string, entries []Entry) (int, bool, error) {
	if len(i.key) == 0 {
		return 0, false, nil
	}
	if _, have := i.localSigned[origin]; have {
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
