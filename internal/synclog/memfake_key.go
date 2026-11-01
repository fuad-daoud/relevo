package synclog

import (
	"context"
	"fmt"
)

// The in-memory log's half of the remote's meta: passphrase material and each
// origin's signing start, held here so exporter and importer run against the fake
// without a remote.

func (m *MemTransport) KeyMeta(context.Context) (KeyMeta, error) {
	m.log.mu.Lock()
	defer m.log.mu.Unlock()

	return keyHeld(m.log), nil
}

// keyHeld is the log's material as a read answers: a never-written row reads as
// epoch 1, and Present is the salt's presence.
func keyHeld(log *memLog) KeyMeta {
	key := log.key
	key.Epoch = keyEpochOf(key)
	key.Present = len(key.Salt) > 0
	return key
}

func keyEpochOf(key KeyMeta) int {
	if key.Epoch == 0 {
		return 1
	}
	return key.Epoch
}

// KeyInit writes the material where the log holds none, answering with a read
// rather than an echo, so two racers both learn the winner's salt.
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

// KeyRekey rotates the log's key to the spec's epoch. Epoch and cuts are checked
// against what the log holds before writing, so a stale caller leaves the log as
// it was.
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

// checkCuts refuses cuts that are not the log's own heads: a moved head or an
// unknown origin is an observation no longer true.
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

// PublishEpochCut extends this handle's own cut, forward only and within the log:
// beyond head vouches for unappended history, and a backward extension is refused
// rather than ignored so the caller learns it did not land.
func (m *MemTransport) PublishEpochCut(_ context.Context, cut EpochCut) error {
	m.log.mu.Lock()
	defer m.log.mu.Unlock()

	head := m.log.latestSeq(m.origin)
	if cut.Seq < 0 || cut.Seq > head {
		return fmt.Errorf("synclog: cut extension: the log's head is %d, not the %d named", head, cut.Seq)
	}
	if have, ok := m.log.epochCut[m.origin]; ok {
		if cut.Epoch == have.Epoch && cut.Seq == have.Seq {
			return nil
		}
		if cut.Epoch < have.Epoch || (cut.Epoch == have.Epoch && cut.Seq < have.Seq) {
			return fmt.Errorf("synclog: cut extension: the stored cut is (%d, %d), which the named (%d, %d) does not raise",
				have.Epoch, have.Seq, cut.Epoch, cut.Seq)
		}
	}
	if m.log.epochCut == nil {
		m.log.epochCut = make(map[string]EpochCut)
	}
	m.log.epochCut[m.origin] = cut
	return nil
}

// firstSigned is the first MAC-carrying entry's index, -1 when none.
func firstSigned(entries []Entry) int {
	for n, e := range entries {
		if len(e.MAC) > 0 {
			return n
		}
	}
	return -1
}

// checkBoundary refuses a signed batch for a boundaryless origin unless it names
// its first signed entry's sequence, before anything is written.
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

// recordSignedFrom notes where this origin started signing, at most once, inside
// the append's lock so marker and entries are never seen apart. A later batch must
// not move the boundary over entries readers were told were signed.
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
