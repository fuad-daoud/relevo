package config

import "github.com/fuad-daoud/relevo/internal/remote"

// NormalizeClientKey rewrites a stored client.key whose PEM label predates the
// relevo rename, so every later read sees the current label. It reports whether
// it wrote. An absent key, or one already carrying the current label, is a no-op
// that records no revision.
//
// The write is unstamped, because it is the program re-marshalling a value
// rather than a person choosing one. A stamp here would claim this machine
// deliberately set the fleet's client key at the moment the daemon happened to
// start, which is how one machine's generated identity would replace another's
// on every server they share.
func (s *Store) NormalizeClientKey() (bool, error) {
	raw, ok, err := s.db.SecretGet(SecretClientKey)
	if err != nil {
		return false, err
	}
	if !ok || !remote.IsLegacyPrivatePEM(raw) {
		return false, nil
	}
	if err := s.As("daemon", "client key PEM label updated").PutSecretUnstamped(SecretClientKey, raw); err != nil {
		return false, err
	}
	return true, nil
}
