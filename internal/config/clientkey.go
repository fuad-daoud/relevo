package config

import "github.com/fuad-daoud/relevo/internal/remote"

// NormalizeClientKey rewrites a stored client.key carrying the old PEM label,
// so every later read sees the current label. It reports whether it wrote.
// An absent key, or one already carrying the current label, is a no-op
// recording no revision.
//
// The write is unstamped: the program re-marshals a value rather than a
// person choosing one. A stamp here would claim this machine set the fleet's
// client key when the daemon happened to start, which is how one machine's
// generated identity would replace another's on every server they share.
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
