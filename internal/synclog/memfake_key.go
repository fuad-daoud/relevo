package synclog

import "context"

// The in-memory log's half of the remote's meta: the passphrase material, and
// the sequence each origin started signing at. Both are the log's own records
// here so the exporter and importer can be written against the fake without a
// remote, which is what CI has.

func (m *MemTransport) KeyMeta(context.Context) (KeyMeta, error) {
	m.log.mu.Lock()
	defer m.log.mu.Unlock()

	key := m.log.key
	key.Present = len(key.Salt) > 0
	return key, nil
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
	return KeyMeta{
		Salt:    held.Salt,
		KDF:     held.KDF,
		Check:   held.Check,
		Present: len(held.Salt) > 0,
	}, nil
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
