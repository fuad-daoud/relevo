package synclog

import (
	"context"
	"fmt"
)

// The in-memory log's half of the remote's meta: the passphrase material, and
// the sequence each origin started signing at. Both are the log's own records
// here so the exporter and importer can be written against the fake without a
// remote, which is what CI has.

func (m *MemTransport) KeyMeta(context.Context) (KeyMeta, error) {
	m.log.mu.Lock()
	defer m.log.mu.Unlock()

	return keyHeld(m.log), nil
}

// keyHeld is the log's material as a read answers with it: an epoch row that
// was never written reads as epoch 1, and Present is the salt's presence rather
// than a field of its own.
func keyHeld(log *memLog) KeyMeta {
	key := log.key
	key.Epoch = keyEpochOf(key)
	key.Present = len(key.Salt) > 0
	return key
}

// keyEpochOf is the epoch a key block names, where an unwritten row is epoch 1.
func keyEpochOf(key KeyMeta) int {
	if key.Epoch == 0 {
		return 1
	}
	return key.Epoch
}

// KeyInit writes the material where the log holds none and answers with what it
// holds afterwards, as the remote does: insert-if-absent, then a read rather
// than an echo, so two machines racing the first one both learn the winner's
// salt.
func (m *MemTransport) KeyInit(_ context.Context, key KeyMeta) (KeyMeta, error) {
	m.log.mu.Lock()
	defer m.log.mu.Unlock()

	held := m.log.key
	if len(held.Salt) == 0 {
		held.Salt, held.KDF, held.Check = key.Salt, key.KDF, key.Check
		m.log.key = held
	}
	return keyHeld(m.log), nil
}

// KeyRekey rotates the log's key to the epoch the spec names. The
// compare-and-set runs against the epoch the caller read, and the cuts are
// checked against the heads the log actually holds before anything is written:
// a caller whose observation is stale leaves the log as it was.
func (m *MemTransport) KeyRekey(_ context.Context, spec KeyRekeySpec) (KeyMeta, error) {
	m.log.mu.Lock()
	defer m.log.mu.Unlock()

	held := m.log.key
	from := keyEpochOf(held)
	if spec.From != from {
		return KeyMeta{}, fmt.Errorf("synclog: rekey: the log is at epoch %d, not the %d the caller read: another machine rekeyed first", from, spec.From)
	}
	if err := m.log.checkCuts(spec.Cuts); err != nil {
		return KeyMeta{}, err
	}
	held.Salt, held.KDF, held.Check = spec.Key.Salt, spec.Key.KDF, spec.Key.Check
	held.Epoch = spec.Key.Epoch
	for epoch, row := range spec.Key.Wrapped {
		if held.Wrapped == nil {
			held.Wrapped = make(map[int][]byte, len(spec.Key.Wrapped))
		}
		held.Wrapped[epoch] = row
	}
	m.log.key = held
	for origin, cut := range spec.Cuts {
		if m.log.epochCut == nil {
			m.log.epochCut = make(map[string]EpochCut, len(spec.Cuts))
		}
		m.log.epochCut[origin] = cut
	}
	return keyHeld(m.log), nil
}

// checkCuts refuses a rekey whose cuts are not the log's own heads: an origin
// whose head moved between the caller's read and this write, and an origin the
// caller named that the log does not hold, are both an observation that is no
// longer true.
func (m *memLog) checkCuts(cuts map[string]EpochCut) error {
	heads := make(map[string]int)
	for _, origin := range m.origins() {
		heads[origin] = m.latestSeq(origin)
	}
	for origin, seq := range heads {
		cut, ok := cuts[origin]
		if !ok || cut.Seq != seq {
			return fmt.Errorf("synclog: rekey: %s's head is %d, not the %d the caller observed", origin, seq, cut.Seq)
		}
	}
	for origin := range cuts {
		if _, ok := heads[origin]; !ok {
			return fmt.Errorf("synclog: rekey: %s has no entries in the log", origin)
		}
	}
	return nil
}

// firstSigned is the index of the first entry carrying a MAC, or -1.
func firstSigned(entries []Entry) int {
	for n, e := range entries {
		if len(e.MAC) > 0 {
			return n
		}
	}
	return -1
}

// checkBoundary refuses a signed batch for an origin with no boundary unless it
// names the sequence its first signed entry is about to get. It runs before
// anything is written.
func (m *memLog) checkBoundary(origin string, entries []Entry, next int, boundary Boundary) error {
	first := firstSigned(entries)
	if first < 0 {
		return nil
	}
	if _, have := m.signedFrom[origin]; have {
		return nil
	}
	if boundary.Seq == 0 || len(boundary.Tag) == 0 || boundary.Seq != next+first {
		return ErrBoundary
	}
	return nil
}

// recordSignedFrom notes where this origin started signing, at most once.
//
// It runs inside the append's own lock, so the marker and the entries it
// describes cannot be seen apart, and it is written only if there is none yet: a
// later signed batch must not move the boundary back over entries a reader has
// already been told were signed.
func (m *memLog) recordSignedFrom(entries []Entry, boundary Boundary) {
	first := firstSigned(entries)
	if first < 0 {
		return
	}
	origin := entries[first].Origin
	if m.signedFrom == nil {
		m.signedFrom = make(map[string]int)
		m.signedTag = make(map[string][]byte)
	}
	if _, ok := m.signedFrom[origin]; !ok {
		m.signedFrom[origin] = entries[first].Seq
		m.signedTag[origin] = boundary.Tag
	}
}
