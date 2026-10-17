package synclog

import (
	"errors"
	"fmt"
	"sort"
	"strings"

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
		// The boundary was tagged under the key its origin signed with, which
		// can be an older epoch's than the newest this machine holds -- the
		// remote's history is old-key material the moment it rekeyed. It
		// verifies by trial like any other signed row.
		if seq < 1 || !i.anyKeyVerifies(func(key []byte) bool {
			return VerifyBoundary(key, origin, seq, state.SignedTags[origin])
		}) {
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
	return i.loadCuts(state)
}

// loadCuts computes, per origin, the highest cut this machine acts on: the
// highest of its own record and every remote cut whose tag verifies under the
// cut's epoch key. A remote cut is acted on only when it names the reader's
// newest held epoch -- a row at a retired epoch, whatever its key verifies,
// is never recorded and holds its origin for the run, so the default fence of
// the reader's newest key stands and a leaked old key cannot re-bless history
// the reader has never verified. A cut that does not verify holds its origin
// for the run, and one that does is recorded locally at once and never
// regresses, so deleting, lowering or replaying the remote row afterwards
// changes nothing.
func (i *Importer) loadCuts(state MarkState) error {
	if len(i.ring) == 0 {
		return nil
	}
	newest := i.epochs()[0]
	local, err := i.db.EpochCuts()
	if err != nil {
		return fmt.Errorf("synclog: import: %w", err)
	}
	i.localCut = local
	i.cuts = make(map[string]EpochCut, len(state.EpochCuts)+len(local))
	for origin, cut := range local {
		i.cuts[origin] = EpochCut{Epoch: cut.Epoch, Seq: cut.Seq}
	}
	i.badCut = make(map[string]error)
	for origin, cut := range state.EpochCuts {
		if cut.Epoch != newest {
			i.badCut[origin] = &signatureError{fmt.Sprintf("the epoch cut of %q names a retired epoch (%d, newest held is %d), so it is held for the run", origin, cut.Epoch, newest)}
			continue
		}
		key := i.ring[cut.Epoch]
		if len(key) == 0 || !VerifyEpochCut(key, origin, cut.Epoch, cut.Seq, cut.Tag) {
			i.badCut[origin] = &signatureError{fmt.Sprintf("the epoch cut of %q does not verify under this machine's keys", origin)}
			continue
		}
		if have, ok := local[origin]; ok && (cut.Epoch < have.Epoch || (cut.Epoch == have.Epoch && cut.Seq <= have.Seq)) {
			continue
		}
		if err := i.db.RaiseEpochCut(origin, cut.Epoch, cut.Seq); err != nil {
			return fmt.Errorf("synclog: import: %w", err)
		}
		i.localCut[origin] = db.EpochCut{Epoch: cut.Epoch, Seq: cut.Seq}
		i.cuts[origin] = EpochCut{Epoch: cut.Epoch, Seq: cut.Seq}
	}
	return nil
}

// requiresSigned reports whether an entry must carry a valid signature under rule 6:
// rules 3 and 5, any After-bearing entry, or any ctl: entry.
func requiresSigned(e Entry) bool {
	if e.After != nil || strings.HasPrefix(e.Table, "ctl:") {
		return true
	}
	if (e.Table == "binding" || e.Table == "binding_record") && e.Op == OpUpsert {
		if body, err := DecodeBody(e.Body); err == nil {
			if w, ok := body["writer"].(string); ok && w != "" && w != e.Origin {
				return true
			}
		}
	}
	return false
}

// verify checks one origin's entries as pulled, before any ref is resolved. A
// locked machine verifies nothing, unless entries require signatures (rule 6).
func (i *Importer) verify(origin string, entries []Entry) error {
	if len(i.key) == 0 {
		for _, e := range entries {
			if requiresSigned(e) {
				return &HoldError{
					Reason: fmt.Sprintf("origin %q requires a key to apply hand-over or cross-log entries", origin),
				}
			}
		}
		return nil
	}
	if err := i.badBoundary[origin]; err != nil {
		return err
	}
	if err := i.badCut[origin]; err != nil {
		return err
	}
	for _, e := range entries {
		if IsDocTable(e.Table) && len(e.MAC) == 0 {
			return &signatureError{fmt.Sprintf("doc entry %d from %q is unsigned; a document is only taken from a signed entry", e.Seq, origin)}
		}
		if requiresSigned(e) && len(e.MAC) == 0 {
			return &signatureError{fmt.Sprintf("entry %d from %q requires a signature but is unsigned", e.Seq, origin)}
		}
	}
	from, signs := i.signedFrom[origin]
	for _, e := range entries {
		if len(e.MAC) == 0 {
			continue
		}
		if err := i.verifyMAC(origin, e); err != nil {
			return err
		}
		if !signs || e.Seq < from {
			from, signs = e.Seq, true
		}
	}
	for _, e := range entries {
		if len(e.MAC) == 0 && signs && e.Seq >= from {
			return &signatureError{fmt.Sprintf("entry %d from %q is unsigned but %q signs from %d", e.Seq, origin, origin, from)}
		}
		if requiresSigned(e) && signs && e.Seq < from {
			return &signatureError{fmt.Sprintf("entry %d from %q is in unsigned region before %d", e.Seq, origin, from)}
		}
	}
	return nil
}

// verifyMAC checks one entry's MAC against the keys the origin's cut allows:
// an entry at or below the cut verifies under any key this machine holds, and
// one above it under some epoch at least the cut's. An entry that verifies
// under none of the ones allowed is refused, not skipped -- a refusal holds
// the origin for the run and the next attempt re-refuses it rather than
// applying what could be forgery.
func (i *Importer) verifyMAC(origin string, e Entry) error {
	for _, key := range i.allowedKeys(origin, e.Seq) {
		if VerifyMAC(key, e) {
			return nil
		}
	}
	return &signatureError{fmt.Sprintf("entry %d from %q has a bad signature", e.Seq, origin)}
}

// anyKeyVerifies reports whether a key of this machine's ring verifies under
// the check given. It is the trial a confirmed-at-or-below-the-cut row runs:
// every held epoch is on the table, newest first.
func (i *Importer) anyKeyVerifies(check func([]byte) bool) bool {
	for _, key := range i.allowedKeys("", -1) {
		if check(key) {
			return true
		}
	}
	return false
}

// allowedKeys is the set of held keys, newest epoch first, the entry at seq
// may verify under. An origin without a cut row is fenced by the default: its
// entries must verify under the reader's newest key, which at an epoch-1
// fleet is exactly the single-key verification of today and at a rekeyed one
// is what keeps an old-key write attempting a log no reader has heard of a
// cut for from re-blessing the dead key. The cut narrows the set from the top
// only: an entry above an epoch-2 cut may not verify under key 1, however
// honestly signed it looks, because the cut is exactly what says key 1 is
// dead above that point.
func (i *Importer) allowedKeys(origin string, seq int) [][]byte {
	all := i.epochs()
	cut, found := i.cuts[origin]
	if !found {
		if len(all) == 0 {
			return nil
		}
		cut, found = EpochCut{Epoch: all[0], Seq: 0}, true
	}
	if found && seq > cut.Seq {
		narrowed := all[:0]
		for _, epoch := range all {
			if epoch >= cut.Epoch {
				narrowed = append(narrowed, epoch)
			}
		}
		all = narrowed
	}
	keys := make([][]byte, 0, len(all))
	for _, epoch := range all {
		if key := i.keyOf(epoch); len(key) > 0 {
			keys = append(keys, key)
		}
	}
	return keys
}

// epochs is the epochs this machine holds keys for, highest first, so a trial
// answers with the newest key's before the older ones. A machine set by
// SetKey alone is a one-key machine at epoch 1, which every epoch-1 log is.
func (i *Importer) epochs() []int {
	if len(i.ring) == 0 {
		if len(i.key) == 0 {
			return nil
		}
		return []int{1}
	}
	var out []int
	for epoch := range i.ring {
		out = append(out, epoch)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(out)))
	return out
}

// keyOf is the key this machine holds for epoch, or nil.
func (i *Importer) keyOf(epoch int) []byte {
	if len(i.ring) == 0 {
		if epoch == 1 {
			return i.key
		}
		return nil
	}
	return i.ring[epoch]
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
