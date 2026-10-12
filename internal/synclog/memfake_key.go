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

// recordSignedFrom notes where this origin started signing, at most once.
//
// It runs inside the append's own lock, so the marker and the entries it
// describes cannot be seen apart, and it is written only if there is none yet: a
// later signed batch must not move the boundary back over entries a reader has
// already been told were signed.
func (m *memLog) recordSignedFrom(entries []Entry) {
	for _, e := range entries {
		if len(e.MAC) == 0 {
			continue
		}
		if m.signedFrom == nil {
			m.signedFrom = make(map[string]int)
		}
		if _, ok := m.signedFrom[e.Origin]; !ok {
			m.signedFrom[e.Origin] = e.Seq
		}
		return
	}
}
